package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service/opsbilling"
	"github.com/QuantumNous/new-api/service/opsbridge"
	"github.com/QuantumNous/new-api/service/opswallet"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOPSAuthorityBillingFactoryNeverFallsBackToLocalWallet(t *testing.T) {
	t.Setenv("OPS_WALLET_AUTHORITY", "ops")
	t.Setenv("OPS_PROJECT_SCOPE", "overseas-factory-test")
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.OPSWalletAdmission{}, &model.OPSWalletHold{}, &model.OPSWalletUsage{}))
	previousDB, previousReader, previousUnit := model.DB, opsbilling.Default, common.QuotaPerUnit
	model.DB, opsbilling.Default, common.QuotaPerUnit = db, nil, 500_000
	t.Cleanup(func() { model.DB, opsbilling.Default, common.QuotaPerUnit = previousDB, previousReader, previousUnit })
	user := model.User{Username: "factory-test", Quota: 54321, BillingMode: model.AgencyDurableBillingMode}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "test-only-factory-token", RemainQuota: 1000, Status: common.TokenStatusEnabled}
	require.NoError(t, db.Create(&token).Error)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{RequestId: "factory-request", UserId: user.Id, TokenId: token.Id, TokenKey: token.Key, OriginModelName: "model-test", StartTime: time.Now()}
	apiErr := PreConsumeBilling(c, 75, info)
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
	assert.Nil(t, info.Billing)
	assert.Error(t, SettleBilling(c, info, 60))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		assert.Equal(t, fmt.Sprintf("/api/v1/catalog/v1/balances/%d", user.Id), req.URL.Path)
		_, _ = fmt.Fprintf(w, `{"data":{"currency":"USD","userId":%q,"balance":"0.01","balanceQuota":10000,"negative":false,"lastEntryId":20,"updatedAt":"2026-11-20T01:00:00Z","station":{"quotaPerUnit":1000000,"usdExchangeRate":"1"}}}`, fmt.Sprint(user.Id))
	}))
	t.Cleanup(server.Close)
	opsbilling.Default = opsbilling.New(opsbilling.Config{BaseURL: server.URL, KeyID: "test-usd", Secret: "test-only-factory-secret", Currency: "USD", Timeout: time.Second})
	require.Nil(t, PreConsumeBilling(c, 75, info))
	require.IsType(t, &opswallet.Session{}, info.Billing)
	assert.Equal(t, "ops", info.BillingSource)
	usage := &dto.Usage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30}
	require.Error(t, SettleBilling(c, info, 60), "missing usage must not fabricate an OPS debit")
	require.NoError(t, SettleBilling(c, info, 60, usage))
	require.NoError(t, SettleBilling(c, info, 60, usage))
	require.Error(t, SettleBilling(c, info, 61, usage))
	var debit model.OPSWalletUsage
	require.NoError(t, db.First(&debit).Error)
	assert.Contains(t, debit.Payload, `"promptTokens":20`)
	assert.Contains(t, debit.Payload, `"amount":"0.00012"`)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 54321, user.Quota)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 940, token.RemainQuota)
	info.RequestId = "factory-second"
	session, apiErr := NewBillingSession(c, info, 10)
	require.Nil(t, apiErr)
	require.IsType(t, &opswallet.Session{}, session)
	session.Refund(c)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 54321, user.Quota)

	info.RequestId = "factory-realtime"
	require.Nil(t, PreConsumeBilling(c, 10, info))
	info.TieredBillingSnapshot = &billingexpr.BillingSnapshot{BillingMode: "tiered_expr", ExprString: `len <= 100 ? tier("short", p * 2 + c * 4 + ai * 10 + ao * 20) : tier("long", p * 4 + c * 8 + ai * 20 + ao * 40)`, QuotaPerUnit: 500_000, GroupRatio: 1}
	info.TieredBillingSnapshot.ExprHash = billingexpr.ExprHashString(info.TieredBillingSnapshot.ExprString)
	frame := &dto.RealtimeUsage{TotalTokens: 110, InputTokens: 100, OutputTokens: 10, InputTokenDetails: dto.InputTokenDetails{TextTokens: 80, AudioTokens: 20}, OutputTokenDetails: dto.OutputTokenDetails{TextTokens: 10}}
	q, paid, err := PreWssConsumeQuotaWithResult(c, info, frame)
	require.NoError(t, err)
	assert.Equal(t, 200, q)
	assert.Zero(t, paid)
	q, _, err = PreWssConsumeQuotaWithResult(c, info, frame)
	require.NoError(t, err)
	assert.Equal(t, 600, q, "the complete cumulative context must cross the price tier")
	assert.Equal(t, 800, info.RealtimePreConsumedQuota)
	assert.Equal(t, 220, info.RealtimeReservedUsage.TotalTokens)
	invalid := *frame
	invalid.InputTokenDetails.CachedTokens = -1
	_, _, err = PreWssConsumeQuotaWithResult(c, info, &invalid)
	require.Error(t, err)
	assert.Equal(t, 800, info.Billing.GetPreConsumedQuota())
	assert.Equal(t, 220, info.RealtimeReservedUsage.TotalTokens)
	total := info.RealtimeReservedUsage
	quota, err := opsAuthorityRealtimeQuota(info, total)
	require.NoError(t, err)
	assert.Equal(t, 800, quota)
	info.RequestURLPath = "/v1/realtime?token=private-query"
	c.Request = httptest.NewRequest(http.MethodGet, info.RequestURLPath, nil)
	require.NoError(t, SettleBilling(c, info, quota, &dto.Usage{PromptTokens: total.InputTokens, CompletionTokens: total.OutputTokens, TotalTokens: total.TotalTokens, PromptTokensDetails: total.InputTokenDetails, CompletionTokenDetails: total.OutputTokenDetails}))
	debit = model.OPSWalletUsage{}
	require.NoError(t, db.Where("request_id = ?", info.RequestId).First(&debit).Error)
	assert.Contains(t, debit.Payload, `"amount":"0.0016"`)
	assert.Contains(t, debit.Payload, `"audio_input":40`)
	assert.NotContains(t, debit.Payload, "private-query")
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 54321, user.Quota)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 140, token.RemainQuota)
}

func TestOpsTaskAuthorityFreezesFinalUSDAndRecoversWithoutProvider(t *testing.T) {
	truncate(t)
	t.Setenv("OPS_WALLET_AUTHORITY", "ops")
	t.Setenv("OPS_PROJECT_SCOPE", "overseas-terminal-test")
	previous := common.QuotaPerUnit
	common.QuotaPerUnit = 500_000
	t.Cleanup(func() { common.QuotaPerUnit = previous })
	require.NoError(t, model.DB.AutoMigrate(&model.OPSWalletAdmission{}, &model.OPSWalletHold{}, &model.TaskBillingReconciliation{}))
	user := model.User{Username: "ops-terminal", Quota: 9999}
	require.NoError(t, model.DB.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "test-only-ops-terminal", RemainQuota: 500_000, Status: common.TokenStatusEnabled}
	require.NoError(t, model.DB.Create(&token).Error)
	request := "terminal-request"
	_, err := model.ReserveOPSWallet(model.DB, model.OPSWalletHoldIdentity{RequestID: request, ChargeRequestID: opsauthority.TaskRequestID(request), UserID: user.Id, TokenID: token.Id, Model: "video-test"}, 250_000, model.OPSWalletPosition{UserID: user.Id, EntryID: 1, Quota: 500_000})
	require.NoError(t, err)
	require.NoError(t, model.BeginOPSTaskDispatch(model.DB, request, user.Id, "public-ops-terminal", fmt.Sprintf("%064x", 1), 8, "video"))
	require.NoError(t, model.ResolveOPSTaskDispatch(model.DB, request, user.Id, "upstream-ops-terminal", false))
	task := &model.Task{TaskID: "public-ops-terminal", ChannelId: 8, Platform: "video", UserId: user.Id, Status: model.TaskStatusInProgress, Quota: 250_000, SubmitTime: time.Now().Unix(), Properties: model.Properties{OriginModelName: "video-test"}, PrivateData: model.TaskPrivateData{UpstreamTaskID: "upstream-ops-terminal", BillingSource: "ops", BillingScope: opsauthority.Scope(), TokenId: token.Id, Execution: &model.TaskExecutionSnapshot{RequestID: request}, BillingContext: &model.TaskBillingContext{PerCallBilling: true}}}
	require.NoError(t, model.DB.Create(task).Error)
	task.Status = model.TaskStatusSuccess
	task.PrivateData.BillingContext.TieredSnapshot = &billingexpr.BillingSnapshot{ExprString: `tier("base", u("seconds") * 0.1)`, UsageFacts: map[string]any{"seconds": float64(5)}, QuotaPerUnit: 500_000, GroupRatio: 1, TaskUsageBilling: true}
	task.PrivateData.BillingContext.TieredSnapshot.ExprHash = billingexpr.ExprHashString(task.PrivateData.BillingContext.TieredSnapshot.ExprString)
	result := &relaycommon.TaskInfo{Status: model.TaskStatusSuccess, UsageFacts: map[string]any{"seconds": float64(2)}}
	PrepareOPSAuthorityTask(nil, task, result)
	require.NotNil(t, task.PrivateData.OPSTerminal)
	assert.Equal(t, 100_000, task.PrivateData.OPSTerminal.Quota)
	assert.Empty(t, task.PrivateData.OpsUsage, "the debit is not deliverable before atomic token finalization")
	assert.Contains(t, string(task.PrivateData.OPSTerminal.Event), `"amount":"0.2"`)
	assert.NotContains(t, string(task.PrivateData.OPSTerminal.Event), "test-only")
	won, err := task.UpdateWithStatus(model.TaskStatusInProgress)
	require.NoError(t, err)
	require.True(t, won)
	var restored model.Task
	require.NoError(t, model.DB.First(&restored, task.ID).Error)
	require.NoError(t, finalizeOPSAuthorityTask(&restored))
	require.NoError(t, finalizeOPSAuthorityTask(&restored))
	assert.Equal(t, 100_000, restored.Quota)
	assert.True(t, restored.OpsUsagePending)
	require.NoError(t, model.DB.First(&user, user.Id).Error)
	assert.Equal(t, 9999, user.Quota)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	assert.Equal(t, 400_000, token.RemainQuota)
	for i, tc := range []struct {
		status model.TaskStatus
		async  bool
		result *relaycommon.TaskInfo
		want   bool
	}{
		{model.TaskStatusSuccess, true, result, false},
		{model.TaskStatusSuccess, false, nil, false},
		{model.TaskStatusFailure, false, nil, true},
	} {
		copy := model.Task{TaskID: fmt.Sprint(i), UserId: user.Id, Status: tc.status, Quota: 250_000, Properties: model.Properties{OriginModelName: "video-test"}, PrivateData: model.TaskPrivateData{BillingSource: "ops", BillingScope: opsauthority.Scope(), Execution: &model.TaskExecutionSnapshot{RequestID: request}, BillingContext: &model.TaskBillingContext{PerCallBilling: true, ProviderBilling: &model.TaskProviderBillingSnapshot{AsyncReconciliationRequired: tc.async}}}}
		PrepareOPSAuthorityTask(nil, &copy, tc.result)
		assert.Equal(t, tc.want, copy.PrivateData.OPSTerminal != nil)
		assert.True(t, copy.OpsUsagePending)
		if tc.status == model.TaskStatusFailure {
			assert.Zero(t, copy.PrivateData.OPSTerminal.Quota)
		}
	}
}

func TestOpsTaskTerminalCrashRecoveryAndScope(t *testing.T) {
	truncate(t)
	previous := opsbridge.Default
	dir := filepath.Join(t.TempDir(), "bridge")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"decision":"allowed"}}`))
	}))
	defer server.Close()
	config := opsbridge.Config{URL: server.URL, GatewayID: "test-gateway", Key: "private-key", StateDir: dir, EnforceCalls: true}
	b, err := opsbridge.Open(config)
	require.NoError(t, err)
	opsbridge.Default = b
	t.Cleanup(func() { _ = opsbridge.Default.Close(); opsbridge.Default = previous })
	task := seedPollingTask(t, 1, "task_ops_final", "private-upstream-id")
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	c.Set("id", task.UserId)
	c.Set(common.RequestIdKey, "request-task-final")
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeDoubaoVideo)
	require.NoError(t, opsbridge.ReserveTask(c, task.TaskID, "seedance", "video_generation"))
	require.NoError(t, opsbridge.BeforeTaskDispatch(c))
	opsbridge.FinishTaskSubmission(c, true)
	task.Properties.OriginModelName = "seedance"
	task.PrivateData.BillingContext = &model.TaskBillingContext{ProviderBilling: &model.TaskProviderBillingSnapshot{HasVideoInput: true}}
	task.Status, task.FinishTime = model.TaskStatusSuccess, 1791936000
	body := []byte(`{"status":"succeeded","usage":{"total_tokens":100,"completion_tokens":80},"duration":1.001,"resolution":"720p","content":{"video_url":"private-media"},"prompt":"private-prompt"}`)
	prepareOpsTaskUsage(task, constant.ChannelTypeDoubaoVideo, body)
	require.True(t, task.OpsUsagePending)
	assert.NotContains(t, string(task.PrivateData.OpsUsage), "private-")
	assert.Contains(t, string(task.PrivateData.OpsUsage), `"duration_ms":1001`)
	won, err := task.UpdateWithStatus(model.TaskStatusInProgress)
	require.NoError(t, err)
	require.True(t, won)
	// Losing the terminal CAS cannot introduce a second payload.
	task.PrivateData.OpsUsage = []byte(`{"event_id":"wrong"}`)
	won, err = task.UpdateWithStatus(model.TaskStatusInProgress)
	require.NoError(t, err)
	assert.False(t, won)
	require.NoError(t, b.Close())
	b, err = opsbridge.Open(config)
	require.NoError(t, err)
	opsbridge.Default = b
	drainOpsTaskUsage(context.Background())
	var stored model.Task
	require.NoError(t, model.DB.First(&stored, task.ID).Error)
	assert.False(t, stored.OpsUsagePending)
	// Simulate a crash before the DB acknowledgement; identical replay succeeds.
	require.NoError(t, model.DB.Model(&stored).Update("ops_usage_pending", true).Error)
	drainOpsTaskUsage(context.Background())
	require.NoError(t, model.DB.First(&stored, task.ID).Error)
	assert.False(t, stored.OpsUsagePending)
	var record map[string]any
	require.NoError(t, common.Unmarshal(stored.PrivateData.OpsUsage, &record))
	record["bridge_scope"] = "another-gateway"
	encoded, err := common.Marshal(record)
	require.NoError(t, err)
	stored.PrivateData.OpsUsage, stored.OpsUsagePending = encoded, true
	require.NoError(t, model.DB.Save(&stored).Error)
	drainOpsTaskUsage(context.Background())
	require.NoError(t, model.DB.First(&stored, task.ID).Error)
	assert.True(t, stored.OpsUsagePending)
}

func TestOpsTaskMissingAndInvalidNativeUsageRemainsRepairable(t *testing.T) {
	previous := opsbridge.Default
	opsbridge.Default = &opsbridge.Bridge{}
	t.Cleanup(func() { opsbridge.Default = previous })
	for _, body := range []string{
		`{"status":"running","usage":{"total_tokens":10}}`,
		`{"status":"succeeded"}`,
		`{"status":"succeeded","usage":{"total_tokens":"10","unknown_secret":"private"}}`,
	} {
		task := &model.Task{TaskID: "task_ops_gap", Status: model.TaskStatusSuccess, FinishTime: 1791936000}
		prepareOpsTaskUsage(task, constant.ChannelTypeDoubaoVideo, []byte(body))
		require.True(t, task.OpsUsagePending)
		assert.NotContains(t, string(task.PrivateData.OpsUsage), "private")
	}
	task := &model.Task{TaskID: "task_ops_failed", Status: model.TaskStatusFailure}
	prepareOpsTaskUsage(task, constant.ChannelTypeDoubaoVideo, []byte(`{"usage":{"total_tokens":10}}`))
	assert.False(t, task.OpsUsagePending)
}
