package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service/opspush"
	"github.com/QuantumNous/new-api/service/opsreserve"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOPSReservationUsageSurvivesDisabledDisplayLog(t *testing.T) {
	t.Setenv("OPS_WALLET_AUTHORITY", "ops")
	t.Setenv("OPS_PROJECT_SCOPE", "isolated-project")
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "journal.db")+"?_txlock=immediate"), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&model.Token{}, &model.OPSHoldIntent{}, &model.User{}))
	oldDB, oldLogDB, oldLogging, oldRedis := model.DB, model.LOG_DB, common.LogConsumeEnabled, common.RedisEnabled
	model.DB, model.LOG_DB, common.LogConsumeEnabled, common.RedisEnabled = db, nil, false, false
	t.Cleanup(func() {
		model.DB, model.LOG_DB, common.LogConsumeEnabled, common.RedisEnabled = oldDB, oldLogDB, oldLogging, oldRedis
	})
	key := model.Token{UserId: 1, Key: "journal-fixture", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 1000}
	require.NoError(t, db.Create(&key).Error)
	var delivery []byte
	deliveries := 0
	reject := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == opsreserve.Path {
			_, _ = w.Write([]byte(`{"data":{"requestId":"journal-request","state":"reserved"}}`))
			return
		}
		var payload map[string]any
		require.NoError(t, common.DecodeJson(r.Body, &payload))
		delivery, err = common.Marshal(payload)
		require.NoError(t, err)
		deliveries++
		if reject {
			_, _ = w.Write([]byte(`{"data":{"accepted":0,"duplicates":0,"rejected":[{}]}}`))
			return
		}
		count := len(payload["events"].([]any))
		ack, marshalErr := common.Marshal(map[string]any{"data": map[string]any{"accepted": count, "duplicates": 0, "rejected": []any{}}})
		require.NoError(t, marshalErr)
		_, _ = w.Write(ack)
	}))
	defer server.Close()
	t.Setenv("OPS_BILLING_URL", server.URL)
	t.Setenv("OPS_BILLING_KEY_ID", "journal-gateway")
	t.Setenv("OPS_BILLING_SECRET", "isolated-journal-signing-fixture")
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	ctx.Set(common.RequestIdKey, "journal-request")
	info := &relaycommon.RelayInfo{RequestId: "journal-request", UserId: 1, TokenId: key.Id, OriginModelName: "gpt-4o", StartTime: time.Now()}
	bill, apiErr := newOPSReservationSession(ctx, info, 100)
	require.Nil(t, apiErr)
	info.Billing = bill
	require.NoError(t, BeginOPSReservationDispatch(info))
	require.NoError(t, bill.Settle(60))
	model.RecordConsumeLog(ctx, 1, model.RecordConsumeLogParams{ModelName: "local-price-alias", TokenId: key.Id, Quota: 60, PromptTokens: 3, CompletionTokens: 2})
	var row model.OPSHoldIntent
	require.NoError(t, db.First(&row).Error)
	require.NotEmpty(t, row.UsageLog)
	require.True(t, row.Finalized)
	client, err := opsreserve.Configured()
	require.NoError(t, err)
	previous := opsreserve.RecoverUsage
	opsreserve.RecoverUsage = RecoverOPSUsage
	t.Cleanup(func() { opsreserve.RecoverUsage = previous })
	require.NoError(t, client.SendJournalUsage(context.Background(), row.Request))
	require.NoError(t, client.SendJournalUsage(context.Background(), row.Request))
	require.Equal(t, 1, deliveries, "acknowledged journals must not send twice")
	require.Contains(t, string(delivery), `"promptTokens":3`)
	require.Contains(t, string(delivery), `"completionTokens":2`)
	require.Contains(t, string(delivery), `"billing_scope":"isolated-project"`)
	require.Contains(t, string(delivery), `"model":"gpt-4o"`)
	require.NotContains(t, string(delivery), "local-price-alias")
	var token model.Token
	require.NoError(t, db.First(&token, key.Id).Error)
	require.Equal(t, 940, token.RemainQuota)
	// Rejected batches retain every immutable event for replay. One HTTP
	// request delivers several journals, rather than a request per model call.
	for _, request := range []string{"batch-a", "batch-b"} {
		row.Request, row.UsagePayload, row.UsageDelivered = request, "", false
		var facts struct {
			Log        *model.Log
			Conversion opspush.Conversion
		}
		require.NoError(t, common.UnmarshalJsonStr(row.UsageLog, &facts))
		facts.Log.RequestId = request
		raw, marshalErr := common.Marshal(facts)
		require.NoError(t, marshalErr)
		row.UsageLog = string(raw)
		require.NoError(t, db.Create(&row).Error)
	}
	reject = true
	require.Error(t, client.SendJournalBatch(context.Background(), []string{"batch-b", "batch-a"}))
	failedBody := string(delivery)
	var pending int64
	require.NoError(t, db.Model(&model.OPSHoldIntent{}).Where("usage_delivered = ?", false).Count(&pending).Error)
	require.EqualValues(t, 2, pending)
	reject = false
	require.NoError(t, client.SendJournalBatch(context.Background(), []string{"batch-a", "batch-b"}))
	require.Equal(t, failedBody, string(delivery), "retry preserves exact event bytes and batch identity")
	require.Equal(t, 3, deliveries)
}

// Invoked by the OPS real-PostgreSQL suite with a disposable signed station.
func TestOPSReservationSignedGatewayLifecycle(t *testing.T) {
	base := os.Getenv("OPS_HOLD_E2E_URL")
	if base == "" {
		t.Skip("isolated OPS fixture required")
	}
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "gateway.db")+"?_pragma=busy_timeout(10000)&_txlock=immediate"), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&model.Token{}, &model.OPSHoldIntent{}, &model.User{}))
	oldDB, oldRedis, oldLogging := model.DB, common.RedisEnabled, common.LogConsumeEnabled
	model.DB, common.RedisEnabled, common.LogConsumeEnabled = db, false, false
	t.Cleanup(func() { model.DB, common.RedisEnabled, common.LogConsumeEnabled = oldDB, oldRedis, oldLogging })
	key := model.Token{UserId: 1, Key: "isolated-e2e", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 1000000}
	require.NoError(t, db.Create(&key).Error)
	t.Setenv("OPS_BILLING_URL", base)
	t.Setenv("OPS_BILLING_KEY_ID", os.Getenv("OPS_HOLD_E2E_KEY"))
	t.Setenv("OPS_BILLING_SECRET", os.Getenv("OPS_HOLD_E2E_SECRET"))
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{RequestId: "gateway-e2e", UserId: 1, TokenId: key.Id, OriginModelName: "gpt-4o", StartTime: time.Now()}
	session, apiErr := newOPSReservationSession(ctx, info, 100000)
	require.Nil(t, apiErr)
	info.Billing = session
	require.NoError(t, BeginOPSReservationDispatch(info))
	for range 2 {
		require.NoError(t, session.Settle(500000))
	}
	ctx.Set(common.RequestIdKey, info.RequestId)
	other := model.NewLogOther()
	if scope := os.Getenv("OPS_HOLD_E2E_SCOPE"); scope != "" {
		t.Setenv("OPS_WALLET_AUTHORITY", "ops")
		t.Setenv("OPS_PROJECT_SCOPE", scope)
	}
	model.RecordConsumeLog(ctx, 1, model.RecordConsumeLogParams{ModelName: "gpt-4o", TokenId: key.Id, Quota: 500000, PromptTokens: 1000000, Other: other})
	var journal model.OPSHoldIntent
	require.NoError(t, db.First(&journal).Error)
	var facts struct{ Log *model.Log }
	require.NoError(t, common.UnmarshalJsonStr(journal.UsageLog, &facts))
	require.NotNil(t, facts.Log)
	client := session.(*opsReservationSession).client
	previousRecover := opsreserve.RecoverUsage
	opsreserve.RecoverUsage = RecoverOPSUsage
	t.Cleanup(func() { opsreserve.RecoverUsage = previousRecover })
	for range 2 {
		require.NoError(t, client.SendJournalUsage(context.Background(), info.RequestId))
	}
	var token model.Token
	require.NoError(t, db.First(&token, key.Id).Error)
	require.Equal(t, 500000, token.RemainQuota)
	require.Equal(t, 500000, token.UsedQuota)
	require.NoError(t, db.Model(&model.OPSHoldIntent{}).Where("request = ?", info.RequestId).Update("updated", int64(1)).Error)
	require.NoError(t, client.Recover(context.Background()))
	var n int64
	require.NoError(t, db.Model(&model.OPSHoldIntent{}).Where("request = ?", info.RequestId).Count(&n).Error)
	require.Zero(t, n)
	// Definitive rejection releases both holds. A timeout retained across a
	// later rejection cannot do so without reviewed provider evidence.
	for _, uncertain := range []bool{false, true} {
		id := "gateway-e2e-rejected"
		if uncertain {
			id = "gateway-e2e-unknown"
		}
		requestInfo := &relaycommon.RelayInfo{RequestId: id, UserId: 1, TokenId: key.Id, OriginModelName: "gpt-4o", StartTime: time.Now()}
		bill, failure := newOPSReservationSession(ctx, requestInfo, 10000)
		require.Nil(t, failure)
		requestInfo.Billing = bill
		require.NoError(t, BeginOPSReservationDispatch(requestInfo))
		if uncertain {
			ObserveOPSReservationAttempt(requestInfo, types.NewError(errors.New("response lost"), types.ErrorCodeDoRequestFailed))
		}
		requestInfo.LastError = types.NewErrorWithStatusCode(errors.New("rejected"), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests)
		ObserveOPSReservationAttempt(requestInfo, requestInfo.LastError)
		bill.Refund(ctx)
		require.NoError(t, db.First(&token, key.Id).Error)
		if uncertain {
			require.Equal(t, 490000, token.RemainQuota)
			require.NoError(t, client.ResolveReviewed(context.Background(), id, "confirmed_no_charge", "isolated-provider-terminal-receipt", "test-reviewer", 0))
		} else {
			require.Equal(t, 500000, token.RemainQuota)
		}
		require.NoError(t, db.First(&token, key.Id).Error)
		require.Equal(t, 500000, token.RemainQuota)
	}
	// Process rollback must not switch an in-flight session to local billing.
	info = &relaycommon.RelayInfo{RequestId: "gateway-e2e-rollback", UserId: 1, TokenId: key.Id, OriginModelName: "gpt-4o", StartTime: time.Now()}
	rollback, failure := newOPSReservationSession(ctx, info, 10000)
	require.Nil(t, failure)
	info.Billing = rollback
	t.Setenv("OPS_WALLET_RESERVATIONS", "false")
	require.True(t, OPSReservationActive(info))
	rollback.Refund(ctx)
	require.NoError(t, db.First(&token, key.Id).Error)
	require.Equal(t, 500000, token.RemainQuota)
}

func TestOPSReservationRetainsUncertaintyAcrossRetries(t *testing.T) {
	session := &opsReservationSession{dispatched: true}
	info := &relaycommon.RelayInfo{Billing: session}
	rejection := types.NewErrorWithStatusCode(errors.New("rejected"), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests)
	ObserveOPSReservationAttempt(info, rejection)
	require.False(t, session.uncertain)
	require.False(t, OPSReservationRequiresReconciliation(info))
	ObserveOPSReservationAttempt(info, types.NewError(errors.New("transport outcome unknown"), types.ErrorCodeDoRequestFailed))
	require.True(t, session.uncertain)
	require.True(t, OPSReservationRequiresReconciliation(info))
	ObserveOPSReservationAttempt(info, rejection)
	require.True(t, session.uncertain)
	ObserveOPSReservationAttempt(info, nil)
	require.True(t, session.uncertain)
}

func TestPreConsumeBillingRejectsDurableUserWithoutQuote(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:agency-billing-guard-test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, db.AutoMigrate(&model.User{}))

	user := &model.User{
		Username:    "durable-without-quote",
		Password:    "not-used-password",
		Status:      common.UserStatusEnabled,
		BillingMode: model.AgencyDurableBillingMode,
	}
	require.NoError(t, db.Create(user).Error)

	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginContext.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	apiErr := PreConsumeBilling(ginContext, 10, &relaycommon.RelayInfo{UserId: user.Id})
	require.Error(t, apiErr)
	require.Equal(t, "agency pricing unavailable for durable user", apiErr.Error())
}

func TestAgencyPricingHierarchyPermitsOnlyExactCustomerCostException(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:agency-customer-cost-quote-test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, db.AutoMigrate(&model.Agency{}, &model.AgencyPricePolicyVersion{}))

	policy := agencycontract.Policy{
		DefaultSettlementBPS: 8000,
		DefaultSalesBPS:      9000,
		MinSpreadBPS:         500,
		SalesCapBPS:          30000,
	}
	encoded, err := common.Marshal(policy)
	require.NoError(t, err)
	policyRow := model.AgencyPricePolicyVersion{AgencyID: 1, Revision: 1, PolicyJSON: string(encoded), PolicyHash: "customer-cost-test"}
	require.NoError(t, db.Create(&policyRow).Error)
	agency := model.Agency{ID: 1, Code: "customer-cost", DisplayName: "Customer cost", Status: "active", InviteCode: "COSTTEST", Depth: 1, CurrentPolicyVersionID: policyRow.ID, PriceRevision: 1, StateRevision: 1, Version: 1}
	require.NoError(t, db.Create(&agency).Error)

	nodes, eligible, reason, err := agencyPricingHierarchy(agency, "model-a", 8000, true, "", agencycontract.PlatformPolicy{})
	require.NoError(t, err)
	require.True(t, eligible)
	require.Empty(t, reason)
	require.Equal(t, 8000, *nodes[0].SalesBPS)

	_, _, _, err = agencyPricingHierarchy(agency, "model-a", 8000, false, "", agencycontract.PlatformPolicy{})
	require.ErrorContains(t, err, "below the permitted minimum")
	_, _, _, err = agencyPricingHierarchy(agency, "model-a", 8200, true, "", agencycontract.PlatformPolicy{})
	require.ErrorContains(t, err, "below the permitted minimum")
}
