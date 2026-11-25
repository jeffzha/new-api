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
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

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
	require.NoError(t, db.AutoMigrate(&model.Token{}, &model.OPSHoldIntent{}))
	oldDB, oldRedis := model.DB, common.RedisEnabled
	model.DB, common.RedisEnabled = db, false
	t.Cleanup(func() { model.DB, common.RedisEnabled = oldDB, oldRedis })
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
	transport := opsclient.New(base, os.Getenv("OPS_HOLD_E2E_KEY"), os.Getenv("OPS_HOLD_E2E_SECRET"), 3*time.Second)
	body, err := common.Marshal(map[string]any{"schemaVersion": "1", "batchId": "gateway-e2e-batch", "events": []any{map[string]any{
		"requestId": info.RequestId, "occurredAt": "2026-07-01T00:00:00Z", "userId": "1", "username": "alice", "model": "gpt-4o", "promptTokens": 1000000, "completionTokens": 0, "gatewayQuota": 500000, "status": "success", "isStream": false,
		"extra": map[string]any{"billing_scope": os.Getenv("OPS_HOLD_E2E_SCOPE")},
	}}})
	require.NoError(t, err)
	for range 2 {
		_, err = transport.Post(context.Background(), "/api/v1/ingest/v1/usage-events", body, time.Now())
		require.NoError(t, err)
	}
	var token model.Token
	require.NoError(t, db.First(&token, key.Id).Error)
	require.Equal(t, 500000, token.RemainQuota)
	require.Equal(t, 500000, token.UsedQuota)
	client := session.(*opsReservationSession).client
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
