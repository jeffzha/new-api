package controller

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShouldRetryHonorsPinRetryMode(t *testing.T) {
	openaiErr := types.NewOpenAIError(errors.New("upstream"), types.ErrorCodeBadResponseStatusCode, http.StatusInternalServerError)

	c := newPinRetryContext()
	assert.True(t, shouldRetry(c, openaiErr, 1))

	origin := newPinRetryContext()
	service.GetChannelConstraints(origin).AddPin(dto.ChannelPin{
		ChannelId: 2,
		Source:    dto.PinSourceOriginTask,
		Rank:      dto.PinRankOriginTask,
		RetryMode: dto.PinRetrySameChannel,
	})
	assert.True(t, shouldRetry(origin, openaiErr, 1), "origin pin retries on the same channel")

	token := newPinRetryContext()
	service.GetChannelConstraints(token).AddPin(dto.ChannelPin{
		ChannelId: 1,
		Source:    dto.PinSourceToken,
		Rank:      dto.PinRankToken,
		RetryMode: dto.PinRetrySingleAttempt,
	})
	assert.False(t, shouldRetry(token, openaiErr, 1), "token pin suppresses retry")
}

func TestShouldRetryTaskRelayHonorsPinRetryMode(t *testing.T) {
	taskErr := &dto.TaskError{StatusCode: http.StatusInternalServerError}

	c := newPinRetryContext()
	assert.True(t, shouldRetryTaskRelay(c, 1, taskErr, 1))

	origin := newPinRetryContext()
	service.GetChannelConstraints(origin).AddPin(dto.ChannelPin{
		ChannelId: 2,
		Source:    dto.PinSourceOriginTask,
		Rank:      dto.PinRankOriginTask,
		RetryMode: dto.PinRetrySameChannel,
	})
	assert.True(t, shouldRetryTaskRelay(origin, 2, taskErr, 1))

	token := newPinRetryContext()
	service.GetChannelConstraints(token).AddPin(dto.ChannelPin{
		ChannelId: 1,
		Source:    dto.PinSourceToken,
		Rank:      dto.PinRankToken,
		RetryMode: dto.PinRetrySingleAttempt,
	})
	assert.False(t, shouldRetryTaskRelay(token, 1, taskErr, 1))
}

func TestSameChannelPinsMergeToStricterRetryMode(t *testing.T) {
	c := newPinRetryContext()
	constraints := service.GetChannelConstraints(c)
	constraints.AddPin(dto.ChannelPin{
		ChannelId: 7,
		Source:    dto.PinSourceOriginTask,
		Rank:      dto.PinRankOriginTask,
		RetryMode: dto.PinRetrySameChannel,
	})
	constraints.AddPin(dto.ChannelPin{
		ChannelId: 7,
		Source:    dto.PinSourceToken,
		Rank:      dto.PinRankToken,
		RetryMode: dto.PinRetrySingleAttempt,
	})
	pin, found, overridden := constraints.ResolvedPin()
	require.True(t, found)
	assert.Equal(t, 7, pin.ChannelId)
	assert.Equal(t, dto.PinRetrySingleAttempt, pin.RetryMode)
	assert.Empty(t, overridden)
	assert.False(t, shouldRetry(c, types.NewOpenAIError(errors.New("upstream"), types.ErrorCodeBadResponseStatusCode, http.StatusInternalServerError), 1))
}

func newPinRetryContext() *gin.Context {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return c
}

func TestControlledRelayFailoverChargesOnceAndRefundsExhaustedRetries(t *testing.T) {
	db, app := setupAgencyInviteControllerTest(t)
	require.NoError(t, db.AutoMigrate(&model.Log{}, &model.Channel{}, &model.Ability{}, &model.Option{}, &model.OpsConcurrencyLimit{}))
	previousRetry, previousBatch, previousMemory := common.RetryTimes, common.BatchUpdateEnabled, common.MemoryCacheEnabled
	previousDisable, previousLog := common.AutomaticDisableChannelEnabled, common.LogConsumeEnabled
	previousPrice, previousGroup := ratio_setting.ModelPrice2JSONString(), ratio_setting.GroupRatio2JSONString()
	previousQuota := common.QuotaPerUnit
	common.RetryTimes, common.BatchUpdateEnabled, common.MemoryCacheEnabled = 1, false, true
	common.AutomaticDisableChannelEnabled, common.LogConsumeEnabled = false, true
	common.QuotaPerUnit = 100
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"retry-acceptance":1}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	t.Cleanup(func() {
		common.RetryTimes, common.BatchUpdateEnabled, common.MemoryCacheEnabled = previousRetry, previousBatch, previousMemory
		common.AutomaticDisableChannelEnabled, common.LogConsumeEnabled = previousDisable, previousLog
		common.QuotaPerUnit = previousQuota
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(previousPrice))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousGroup))
	})
	agency, _, err := app.CreateAgency(1, "Retry Acceptance", "retry_acceptance_operator", agencyInviteTestPolicy())
	require.NoError(t, err)
	registered := postAgencyInviteRegister(t, fmt.Sprintf(`{"username":"retry.customer@example.com","password":"password123","invite":%q}`, agency.InviteCode))
	require.Contains(t, registered.Body.String(), `"success":true`)
	var customer model.User
	var token model.Token
	require.NoError(t, db.Where("username = ?", "retry.customer@example.com").First(&customer).Error)
	require.NoError(t, db.Where("user_id = ?", customer.Id).First(&token).Error)
	require.NoError(t, model.ApplyAgencyQuotaDelta(int64(customer.Id), 1000, "admin_grant"))
	var primaryCalls, fallbackCalls atomic.Int32
	var fallbackFails atomic.Bool
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"error":{"message":"controlled upstream outage","type":"server_error","code":"unavailable"}}`))
	}))
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if fallbackFails.Load() {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":{"message":"controlled fallback outage","type":"server_error","code":"unavailable"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"controlled-completion","object":"chat.completion","model":"retry-acceptance","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`))
	}))
	t.Cleanup(primary.Close)
	t.Cleanup(fallback.Close)
	service.InitHttpClient()
	channels := []model.Channel{
		{Name: "controlled-primary", Type: constant.ChannelTypeOpenAI, Status: 1, Models: "retry-acceptance", Group: "default", BaseURL: &primary.URL, Key: "fixture-primary", Priority: common.GetPointer(int64(10))},
		{Name: "controlled-fallback", Type: constant.ChannelTypeOpenAI, Status: 1, Models: "retry-acceptance", Group: "default", BaseURL: &fallback.URL, Key: "fixture-fallback", Priority: common.GetPointer(int64(0))},
	}
	for i := range channels {
		require.NoError(t, channels[i].Insert())
	}
	model.InitChannelCache()
	t.Cleanup(model.InitChannelCache)
	run := func(requestID string) (*httptest.ResponseRecorder, *gin.Context) {
		t.Helper()
		require.NoError(t, db.First(&customer, customer.Id).Error)
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"retry-acceptance","messages":[{"role":"user","content":"Reply OK"}],"max_tokens":4}`))
		ctx.Request.Header.Set("Content-Type", "application/json")
		common.SetContextKey(ctx, constant.ContextKeyUserId, customer.Id)
		common.SetContextKey(ctx, constant.ContextKeyUserQuota, customer.Quota)
		common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
		common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "default")
		common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "default")
		common.SetContextKey(ctx, constant.ContextKeyTokenId, token.Id)
		common.SetContextKey(ctx, constant.ContextKeyTokenKey, token.Key)
		common.SetContextKey(ctx, constant.ContextKeyTokenUnlimited, token.UnlimitedQuota)
		common.SetContextKey(ctx, constant.ContextKeyOriginalModel, "retry-acceptance")
		common.SetContextKey(ctx, constant.ContextKeyUserSetting, kitdto.UserSetting{BillingPreference: "wallet_only"})
		common.SetContextKey(ctx, common.RequestIdKey, requestID)
		selected, err := model.GetRandomSatisfiedChannel("default", "retry-acceptance", 0, nil)
		require.NoError(t, err)
		require.NotNil(t, selected)
		require.Nil(t, middleware.SetupContextForSelectedChannel(ctx, selected, "retry-acceptance"))
		Relay(ctx, types.RelayFormatOpenAI)
		return recorder, ctx
	}
	response, ctx := run("controlled-retry-success")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, []string{fmt.Sprint(channels[0].Id), fmt.Sprint(channels[1].Id)}, ctx.GetStringSlice("use_channel"))
	assert.EqualValues(t, 1, primaryCalls.Load())
	assert.EqualValues(t, 1, fallbackCalls.Load())
	var journal model.AgencyBillingJournal
	require.NoError(t, db.Where("charge_id = ?", "controlled-retry-success").First(&journal).Error)
	assert.Equal(t, "finalized", journal.Status)
	require.NoError(t, db.First(&customer, customer.Id).Error)
	assert.Equal(t, 900, customer.Quota, "one successful request at unpublished default sales 1, regardless of attempts")
	var consumeCount int64
	require.NoError(t, db.Model(&model.Log{}).Where("request_id = ? AND type = ?", "controlled-retry-success", model.LogTypeConsume).Count(&consumeCount).Error)
	assert.EqualValues(t, 1, consumeCount)
	previousCodes, previousKeywords := operation_setting.AutomaticDisableStatusCodeRanges, operation_setting.AutomaticDisableKeywords
	t.Cleanup(func() {
		operation_setting.AutomaticDisableStatusCodeRanges, operation_setting.AutomaticDisableKeywords = previousCodes, previousKeywords
	})
	common.AutomaticDisableChannelEnabled = true
	operation_setting.AutomaticDisableStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: 503, End: 503}}
	operation_setting.AutomaticDisableKeywords = []string{"controlled outage"}
	assert.True(t, service.ShouldDisableChannel(types.NewOpenAIError(errors.New("service unavailable"), types.ErrorCodeBadResponseStatusCode, 503)))
	assert.True(t, service.ShouldDisableChannel(types.NewOpenAIError(errors.New("CONTROLLED OUTAGE"), types.ErrorCodeBadResponseStatusCode, 400)))
	assert.False(t, service.ShouldDisableChannel(types.NewOpenAIError(errors.New("invalid prompt"), types.ErrorCodeBadResponseStatusCode, 400)))
	channelError := types.ChannelError{ChannelId: channels[0].Id, ChannelName: channels[0].Name, UsingKey: "fixture-primary", AutoBan: false}
	service.DisableChannel(channelError, "controlled fault")
	stillEnabled, err := model.GetChannelById(channels[0].Id, false)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusEnabled, stillEnabled.Status, "per-channel auto-ban opt-out is respected")
	channelError.AutoBan = true
	service.DisableChannel(channelError, "controlled fault")
	common.AutomaticDisableChannelEnabled = false
	response, ctx = run("controlled-disabled")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, []string{fmt.Sprint(channels[1].Id)}, ctx.GetStringSlice("use_channel"))
	assert.EqualValues(t, 1, primaryCalls.Load(), "disabled channel is never contacted")
	require.True(t, model.UpdateChannelStatus(channels[0].Id, "fixture-primary", common.ChannelStatusEnabled, "controlled recovery"))
	fallbackFails.Store(true)
	require.NoError(t, db.First(&customer, customer.Id).Error)
	before := customer.Quota
	response, ctx = run("controlled-exhausted")
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
	assert.Contains(t, response.Body.String(), `"error"`)
	assert.Contains(t, response.Body.String(), `"code":"unavailable"`)
	assert.Len(t, ctx.GetStringSlice("use_channel"), 2)
	assert.EqualValues(t, 2, primaryCalls.Load(), "restored channel participates immediately")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		var current model.User
		assert.NoError(c, db.First(&current, customer.Id).Error)
		assert.Equal(c, before, current.Quota, "all failed attempts refund the single reservation")
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, db.Model(&model.Log{}).Where("request_id = ? AND type = ?", "controlled-exhausted", model.LogTypeConsume).Count(&consumeCount).Error)
	assert.Zero(t, consumeCount)
}
