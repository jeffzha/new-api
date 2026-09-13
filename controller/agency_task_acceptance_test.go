package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The real task controller and provider adapter must retain a reservation at
// HTTP acceptance. A successful provider submission is not financial success.
func TestAgencyTaskControllerAcceptanceRetainsReservationUntilTerminal(t *testing.T) {
	db, app := setupAgencyInviteControllerTest(t)
	t.Setenv("AGENCY_COMPONENT_BILLING_ENABLED", "true")
	require.NoError(t, db.AutoMigrate(&model.Log{}, &model.Channel{}, &model.TaskBillingReconciliation{}))
	priorQuotaPerUnit, priorBatch := common.QuotaPerUnit, common.BatchUpdateEnabled
	priorModelPrices, priorGroupRatios := ratio_setting.ModelPrice2JSONString(), ratio_setting.GroupRatio2JSONString()
	common.QuotaPerUnit, common.BatchUpdateEnabled = 100, false
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"sora-2":0.25}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":0.1}`))
	t.Cleanup(func() {
		common.QuotaPerUnit, common.BatchUpdateEnabled = priorQuotaPerUnit, priorBatch
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(priorModelPrices))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(priorGroupRatios))
	})
	agency, _, err := app.CreateAgency(1, "Task Acceptance Agency", "task_acceptance_operator", agencyInviteTestPolicy())
	require.NoError(t, err)
	registered := postAgencyInviteRegister(t, fmt.Sprintf(`{"username":"task_customer","password":"password123","invite":%q}`, agency.InviteCode))
	require.Contains(t, registered.Body.String(), `"success":true`)
	var customer model.User
	var token model.Token
	require.NoError(t, db.Where("username = ?", "task_customer").First(&customer).Error)
	require.NoError(t, db.Where("user_id = ?", customer.Id).First(&token).Error)
	require.NoError(t, model.ApplyAgencyQuotaDelta(int64(customer.Id), 200, "admin_grant"))
	require.NoError(t, db.First(&customer, customer.Id).Error)
	var submissions atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submissions.Add(1)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/videos", r.URL.Path)
		assert.Equal(t, "Bearer fixture-upstream-key", r.Header.Get("Authorization"))
		var body map[string]any
		if assert.NoError(t, common.DecodeJson(r.Body, &body)) {
			assert.Equal(t, "sora-2", body["model"])
			assert.Equal(t, "4", body["seconds"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"controller-provider-task","object":"video","model":"sora-2","status":"queued","progress":0,"seconds":"4","size":"720x1280"}`))
	}))
	t.Cleanup(upstream.Close)
	service.InitHttpClient()
	channel := model.Channel{Name: "controller-task-fixture", Type: constant.ChannelTypeOpenAI,
		Status: common.ChannelStatusEnabled, Key: "fixture-upstream-key", BaseURL: &upstream.URL}
	require.NoError(t, db.Create(&channel).Error)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"model":"sora-2","prompt":"A static blue square","seconds":"4","size":"720x1280"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(ctx, constant.ContextKeyUserId, customer.Id)
	common.SetContextKey(ctx, constant.ContextKeyUserQuota, customer.Quota)
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenId, token.Id)
	common.SetContextKey(ctx, constant.ContextKeyTokenKey, token.Key)
	common.SetContextKey(ctx, constant.ContextKeyTokenUnlimited, token.UnlimitedQuota)
	common.SetContextKey(ctx, constant.ContextKeyOriginalModel, "sora-2")
	common.SetContextKey(ctx, constant.ContextKeyUserSetting, dto.UserSetting{BillingPreference: "wallet_only"})
	common.SetContextKey(ctx, common.RequestIdKey, "controller-task-charge")
	require.Nil(t, middleware.SetupContextForSelectedChannel(ctx, &channel, "sora-2"))
	RelayTask(ctx)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, int32(1), submissions.Load())
	var task model.Task
	var journal model.AgencyBillingJournal
	require.NoError(t, db.Where("user_id = ?", customer.Id).First(&task).Error)
	require.NoError(t, db.Where("charge_id = ?", "controller-task-charge").First(&journal).Error)
	assert.Equal(t, "submitted", journal.Status)
	assert.Equal(t, "controller-provider-task", task.GetUpstreamTaskID())
	assert.Equal(t, 88, task.Quota, "truncate 0.25 * 100 * frozen sales 0.9, then multiply seconds 4")
	assert.Empty(t, task.PrivateData.BillingContext.AgencyBillingEventID)
	var finals int64
	require.NoError(t, db.Model(&model.AgencyBillingOutbox{}).Where("user_id = ? AND event_kind = ?", customer.Id, "agency.billing_finalized").Count(&finals).Error)
	assert.Zero(t, finals, "queued upstream response cannot create a finalized billing event")
	var frozen model.AgencyTaskChargeBasis
	require.NoError(t, common.UnmarshalJsonStr(journal.BillingBasis, &frozen))
	assert.Equal(t, model.AgencyTaskChargeBasisVersion, frozen.Version)
	assert.True(t, frozen.ComponentBilling)
	priorStatus := task.Status
	task.Status, task.Progress = model.TaskStatusSuccess, "100%"
	completed, err := model.CompleteAgencyTask(&task, priorStatus, 0)
	require.NoError(t, err)
	assert.True(t, completed.FinancialFinal)
	assert.True(t, completed.Changed)
	assert.Equal(t, 88, task.Quota)
	require.NoError(t, db.First(&journal, journal.ID).Error)
	assert.Equal(t, "finalized", journal.Status)
	require.NoError(t, db.Model(&model.AgencyBillingOutbox{}).Where("user_id = ? AND event_kind = ?", customer.Id, "agency.billing_finalized").Count(&finals).Error)
	assert.Equal(t, int64(1), finals)
}
