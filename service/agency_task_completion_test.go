package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type agencyTerminalPollingAdaptor struct {
	result       relaycommon.TaskInfo
	fetchError   error
	responseBody string
}

func (a *agencyTerminalPollingAdaptor) Init(*relaycommon.RelayInfo) {}
func (a *agencyTerminalPollingAdaptor) FetchTask(string, string, *model.Task, string) (*http.Response, error) {
	if a.fetchError != nil {
		return nil, a.fetchError
	}
	body := a.responseBody
	if body == "" {
		body = `{"provider":true}`
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
}
func (a *agencyTerminalPollingAdaptor) ParseTaskResult(*model.Task, *http.Response, []byte) (*relaycommon.TaskInfo, error) {
	result := a.result
	return &result, nil
}
func (a *agencyTerminalPollingAdaptor) AdjustBillingOnComplete(*model.Task, *relaycommon.TaskInfo) int {
	return 999 // Durable task settlement must use the frozen engine, not this mutable legacy callback.
}

func TestAgencyTaskSunoPollingRequiresExplicitFailure(t *testing.T) {
	db, task, user, token, channel := agencyTerminalPollingFixture(t)
	priorMemoryCache, priorFactory := common.MemoryCacheEnabled, GetTaskAdaptorFunc
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled, GetTaskAdaptorFunc = priorMemoryCache, priorFactory })
	require.NoError(t, db.Model(&channel).Update("base_url", "https://unused.invalid").Error)
	response, err := common.Marshal(taskdto.TaskResponse[[]taskdto.SunoDataResponse]{Code: taskdto.TaskSuccessCode,
		Data: []taskdto.SunoDataResponse{{TaskID: task.GetUpstreamTaskID(), Status: model.TaskStatusInProgress, FailReason: "temporary provider warning"}}})
	require.NoError(t, err)
	adaptor := &agencyTerminalPollingAdaptor{responseBody: string(response)}
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	tasks := map[string]*model.Task{task.GetUpstreamTaskID(): task}
	ids := []string{task.GetUpstreamTaskID()}
	require.NoError(t, updateVideoSingleTask(context.Background(), adaptor, &channel, ids[0], tasks))
	require.NoError(t, db.First(task, task.ID).Error)
	assert.Equal(t, model.TaskStatus(model.TaskStatusInProgress), task.Status)
	assert.Equal(t, 100, task.Quota)
	adaptor.fetchError = errors.New("provider lookup transport interrupted")
	require.Error(t, updateVideoSingleTask(context.Background(), adaptor, &channel, ids[0], tasks))
	var journal model.AgencyBillingJournal
	require.NoError(t, db.Where("charge_id = ?", task.PrivateData.BillingContext.AgencyChargeID).First(&journal).Error)
	assert.Equal(t, "reconcile_required", journal.Status)
	assert.Equal(t, "provider_response_unknown", journal.LastError)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Zero(t, user.Quota)
	response, err = common.Marshal(taskdto.TaskResponse[[]taskdto.SunoDataResponse]{Code: taskdto.TaskSuccessCode,
		Data: []taskdto.SunoDataResponse{{TaskID: task.GetUpstreamTaskID(), Status: model.TaskStatusFailure, FailReason: "generation failed"}}})
	require.NoError(t, err)
	adaptor.fetchError, adaptor.responseBody = nil, string(response)
	require.NoError(t, updateVideoSingleTask(context.Background(), adaptor, &channel, ids[0], tasks))
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	require.NoError(t, db.First(&journal, journal.ID).Error)
	assert.Equal(t, 100, user.Quota)
	assert.Equal(t, 100, token.RemainQuota)
	assert.Equal(t, "cancelled", journal.Status)
	assert.Empty(t, journal.LastError)
}

func agencyTerminalPollingFixture(t *testing.T) (*gorm.DB, *model.Task, model.User, model.Token, model.Channel) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Task{}, &model.Channel{}, &model.Log{}, &model.TaskBillingReconciliation{}))
	require.NoError(t, model.MigrateAgency(db))
	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() { model.DB, model.LOG_DB = previousDB, previousLogDB; require.NoError(t, pool.Close()) })
	user := model.User{Username: "terminal-poll-customer", Password: "password", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, BillingMode: model.AgencyDurableBillingMode, FundingVersion: 1, Quota: 100, UsedQuota: 100, RequestCount: 1}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "terminal-poll-key", Status: common.TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&token).Error)
	channel := model.Channel{Name: "terminal-poll-channel", Type: constant.ChannelTypeOpenAI, UsedQuota: 100}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&model.AgencyFundingAccount{UserID: int64(user.Id), Version: 1}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return model.RecordAgencyTopup(tx, int64(user.Id), "payment", "terminal-poll-payment", "payment_callback", 100, 0)
	}))
	pricing := agencycontract.PricingSnapshot{AgencyID: 7, BindingID: 9, CommissionEligible: true, SalesBPS: 8000, SettlementBPS: 5000,
		CurrencyCode: "TOKENS", QuotaPerUnit: "1", ExchangeRate: "1"}
	const chargeID = "terminal-poll-charge"
	_, err = model.TryReserveAgencyWalletAndTokenWithSnapshot(user.Id, token.Id, 100, token.Key, chargeID, 100, false, &pricing)
	require.NoError(t, err)
	require.NoError(t, model.FreezeAgencyTaskChargeBasis(user.Id, chargeID, model.AgencyTaskChargeBasis{Version: model.AgencyTaskChargeBasisVersion,
		ComponentBilling: true, Mode: "tokens", QuotaPerUnit: 10, ModelRatio: 1.25, OtherMultiplier: 2}))
	require.NoError(t, model.BeginAgencyTaskSubmission(user.Id, chargeID, "terminal-public-task", "request-hash", "/v1/videos"))
	require.NoError(t, model.RecordAgencyTaskSubmission(chargeID, "terminal-public-task", "terminal-provider-task"))
	task := &model.Task{TaskID: "terminal-public-task", UserId: user.Id, ChannelId: channel.Id, Quota: 100, Group: "default",
		Status: model.TaskStatusInProgress, Progress: "50%", SubmitTime: time.Now().Add(-2 * time.Hour).Unix(),
		PrivateData: model.TaskPrivateData{TokenId: token.Id, BillingSource: model.TaskBillingSourceWallet, UpstreamTaskID: "terminal-provider-task",
			BillingContext: &model.TaskBillingContext{AgencyChargeID: chargeID, AgencyPricing: &pricing}}}
	require.NoError(t, db.Create(task).Error)
	return db, task, user, token, channel
}

func TestAgencyTaskPollingTerminalCommitsFrozenFinancialFactsOnce(t *testing.T) {
	db, task, user, token, channel := agencyTerminalPollingFixture(t)
	adaptor := &agencyTerminalPollingAdaptor{result: relaycommon.TaskInfo{Status: model.TaskStatusSuccess, TotalTokens: 40, Url: "https://example.test/result.mp4"}}
	initial := *task
	ctx := context.Background()
	previousUnits := common.QuotaPerUnit
	common.QuotaPerUnit = 999_999
	t.Cleanup(func() { common.QuotaPerUnit = previousUnits })
	require.NoError(t, updateVideoSingleTask(ctx, adaptor, &channel, task.GetUpstreamTaskID(), map[string]*model.Task{task.GetUpstreamTaskID(): task}))
	require.NoError(t, updateVideoSingleTask(ctx, adaptor, &channel, initial.GetUpstreamTaskID(), map[string]*model.Task{initial.GetUpstreamTaskID(): &initial}))
	var persisted model.Task
	require.NoError(t, db.First(&persisted, task.ID).Error)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	require.NoError(t, db.First(&channel, channel.Id).Error)
	assert.EqualValues(t, model.TaskStatusSuccess, persisted.Status)
	assert.Equal(t, "https://example.test/result.mp4", persisted.PrivateData.ResultURL)
	assert.Equal(t, 80, persisted.Quota)
	assert.Equal(t, 20, user.Quota)
	assert.Equal(t, 80, user.UsedQuota)
	assert.Equal(t, int64(80), channel.UsedQuota)
	assert.Equal(t, 20, token.RemainQuota)
	assert.Equal(t, 80, token.UsedQuota)
	var operations, logs int64
	require.NoError(t, db.Model(&model.AgencyBillingOperation{}).Where("charge_id = ? AND operation = ?", task.PrivateData.BillingContext.AgencyChargeID, "finalize").Count(&operations).Error)
	require.NoError(t, db.Model(&model.Log{}).Where("user_id = ?", user.Id).Count(&logs).Error)
	assert.Equal(t, int64(1), operations)
	assert.Equal(t, int64(1), logs)
}

func TestAgencyTaskPollingUnknownAndTimeoutRetainReservation(t *testing.T) {
	db, task, user, token, channel := agencyTerminalPollingFixture(t)
	previousTimeout := constant.TaskTimeoutMinutes
	constant.TaskTimeoutMinutes = 1
	t.Cleanup(func() { constant.TaskTimeoutMinutes = previousTimeout })
	sweepTimedOutTasks(context.Background())
	var journal model.AgencyBillingJournal
	require.NoError(t, db.Where("charge_id = ?", task.PrivateData.BillingContext.AgencyChargeID).First(&journal).Error)
	assert.Equal(t, "reconcile_required", journal.Status)
	assert.Equal(t, "task_timeout", journal.LastError)
	adaptor := &agencyTerminalPollingAdaptor{fetchError: errors.New("upstream connection unavailable")}
	err := updateVideoSingleTask(context.Background(), adaptor, &channel, task.GetUpstreamTaskID(), map[string]*model.Task{task.GetUpstreamTaskID(): task})
	require.Error(t, err)
	require.NoError(t, db.First(&task, task.ID).Error)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	require.NoError(t, db.First(&journal, journal.ID).Error)
	assert.EqualValues(t, model.TaskStatusInProgress, task.Status)
	assert.Equal(t, 100, task.Quota)
	assert.Zero(t, user.Quota)
	assert.Equal(t, 100, token.UsedQuota)
	assert.Equal(t, "provider_response_unknown", journal.LastError)
	adaptor.fetchError = nil
	adaptor.result = relaycommon.TaskInfo{Status: model.TaskStatusFailure, Reason: "confirmed upstream failure"}
	require.NoError(t, updateVideoSingleTask(context.Background(), adaptor, &channel, task.GetUpstreamTaskID(), map[string]*model.Task{task.GetUpstreamTaskID(): task}))
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&journal, journal.ID).Error)
	assert.Equal(t, 100, user.Quota)
	assert.Equal(t, "cancelled", journal.Status)
}
