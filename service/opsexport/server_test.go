package opsexport

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type refundHTTPProvider struct{ calls int }

func (p *refundHTTPProvider) Name() string { return "http-simulated" }
func (p *refundHTTPProvider) Submit(ctx context.Context, i model.CNRefundIntent) (model.CNRefundProviderResult, error) {
	p.calls++
	return model.CNRefundProviderResult{Intent: i, Status: "unknown_reconcile"}, nil
}
func (p *refundHTTPProvider) Query(ctx context.Context, i model.CNRefundIntent) (model.CNRefundProviderResult, error) {
	return model.CNRefundProviderResult{Intent: i, Status: "unknown_reconcile"}, nil
}

func TestOpsExportRefundCredentialScopeAndReservation(t *testing.T) {
	previous, oldDB := Default, model.DB
	oldRedis, oldBatch := common.RedisEnabled, common.BatchUpdateEnabled
	oldMain, oldLog := common.MainDatabaseType(), common.LogDatabaseType()
	t.Cleanup(func() {
		Default, model.DB = previous, oldDB
		common.RedisEnabled, common.BatchUpdateEnabled = oldRedis, oldBatch
		common.SetDatabaseTypes(oldMain, oldLog)
	})
	common.RedisEnabled, common.BatchUpdateEnabled = false, false
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	db, err := gorm.Open(sqlite.Open("file:refund-http?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	model.DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
	require.NoError(t, model.MigrateAgency(db))
	exportToken, mutationToken := strings.Repeat("export-", 8), strings.Repeat("refund-", 8)
	exportHash, mutationHash := sha256.Sum256([]byte(exportToken)), sha256.Sum256([]byte(mutationToken))
	t.Setenv("OPS_EXPORT_TOKEN_SHA256", fmt.Sprintf("%x", exportHash))
	t.Setenv("OPS_GATEWAY_ID", "refund-http")
	t.Setenv("UPSTREAM_EVENT_GATEWAY_ID", "")
	t.Setenv("OPS_REFUND_TENANT_ID", "tenant-http")
	t.Setenv("OPS_REFUND_MUTATION_TOKEN_SHA256", fmt.Sprintf("%x", exportHash))
	require.Error(t, StartConfigured(db))
	t.Setenv("OPS_REFUND_MUTATION_TOKEN_SHA256", fmt.Sprintf("%x", mutationHash))
	require.NoError(t, StartConfigured(db))
	t.Setenv("OPS_CN_FINANCE_V2_ENABLED", "true")
	u := model.User{Username: "refund-http", AffCode: "refund-http", BillingMode: model.AgencyDurableBillingMode, Quota: 100}
	require.NoError(t, db.Create(&u).Error)
	require.NoError(t, db.Create(&model.AgencyFundingAccount{UserID: int64(u.Id), Version: 1}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return model.RecordAgencyTopup(tx, int64(u.Id), "payment", "http-payment", "payment_callback", 100, 0, &model.TopupFundingSnapshot{Payment: &model.TopupPaymentSnapshot{ActualMoney: "80", CurrencyCode: "CNY", PaymentReference: "simulated-payment"}})
	}))
	_, err = model.TryReserveUserQuotaAndAgency(u.Id, 25, "http-charge", 25)
	require.NoError(t, err)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return model.CaptureCNAgencyFundingTx(tx, agencycontract.BillingEvent{EventID: "http-final", EventType: "agency.billing_finalized", FinancialChargeID: "http-charge", UserID: int64(u.Id), JournalRevision: 1, OperationID: "http-final"})
	}))
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterV2(router.Group("/api/ops-export/v2"))
	call := func(method, path, token, key string, body any) *httptest.ResponseRecorder {
		raw, err := common.Marshal(body)
		require.NoError(t, err)
		req := httptest.NewRequest(method, "/api/ops-export/v2"+path, strings.NewReader(string(raw)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 404 {
			assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		}
		assert.NotContains(t, w.Body.String(), mutationToken)
		return w
	}
	payment := model.CNIdentity("agency-payment", "http-payment")
	lookup := "/refund-balances?tenantId=tenant-http&gatewayId=refund-http&originalPaymentId=" + payment
	assert.Equal(t, 401, call("GET", lookup, exportToken, "", nil).Code)
	w := call("GET", lookup, mutationToken, "", nil)
	require.Equal(t, 200, w.Code, w.Body.String())
	var balance model.CNRefundBalance
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &balance))
	assert.Equal(t, "60", balance.AvailableRefundable)
	r := model.CNRefundReserveRequest{TenantID: "tenant-http", GatewayID: "refund-http", OperationID: "http-refund", ApprovalID: "finance-approval", ApprovalHash: strings.Repeat("a", 64), OriginalPaymentID: payment, LotID: balance.LotID, UserID: balance.UserID, Amount: "60", Currency: "CNY", ExpectedFundingVersion: balance.FundingVersion, ExpectedLotVersion: balance.LotVersion}
	assert.Equal(t, 401, call("POST", "/refund-reservations", exportToken, r.OperationID, r).Code)
	assert.Equal(t, 401, call("POST", "/refund-reservations", "management-token", r.OperationID, r).Code)
	assert.Equal(t, 400, call("POST", "/refund-reservations", mutationToken, "wrong-key", r).Code)
	bad := r
	bad.TenantID = "other-tenant"
	assert.Equal(t, 403, call("POST", "/refund-reservations", mutationToken, r.OperationID, bad).Code)
	bad = r
	bad.GatewayID = "other-gateway"
	assert.Equal(t, 403, call("POST", "/refund-reservations", mutationToken, r.OperationID, bad).Code)
	bad = r
	bad.Amount = "61"
	assert.Equal(t, 409, call("POST", "/refund-reservations", mutationToken, r.OperationID, bad).Code)
	w = call("POST", "/refund-reservations", mutationToken, r.OperationID, r)
	require.Equal(t, 200, w.Code, w.Body.String())
	var receipt model.CNRefundReceipt
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &receipt))
	assert.Equal(t, "reserved", receipt.Status)
	require.Equal(t, w.Body.String(), call("POST", "/refund-reservations", mutationToken, r.OperationID, r).Body.String())
	c := model.CNRefundCommand{AttemptID: "attempt-1", TenantID: r.TenantID, GatewayID: r.GatewayID, OperationID: r.OperationID, ApprovalID: r.ApprovalID, ApprovalHash: r.ApprovalHash, ExpectedVersion: receipt.Version}
	path := "/refund-reservations/" + r.OperationID + "/execution-intents"
	assert.Equal(t, 503, call("POST", path, mutationToken, r.OperationID, c).Code)
	provider := &refundHTTPProvider{}
	Default.Refund.Provider = provider
	c.ApprovalHash = strings.Repeat("b", 64)
	assert.Equal(t, 409, call("POST", path, mutationToken, r.OperationID, c).Code)
	assert.Zero(t, provider.calls)
	c.ApprovalHash = r.ApprovalHash
	raw, err := common.Marshal(c)
	require.NoError(t, err)
	var injected map[string]any
	require.NoError(t, common.Unmarshal(raw, &injected))
	injected["result"] = "succeeded"
	assert.Equal(t, 400, call("POST", path, mutationToken, r.OperationID, injected).Code)
	assert.Zero(t, provider.calls)
	w = call("POST", path, mutationToken, r.OperationID, c)
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "unknown_reconcile")
	assert.Equal(t, 1, provider.calls)
	assert.Equal(t, w.Body.String(), call("POST", path, mutationToken, r.OperationID, c).Body.String())
	assert.Equal(t, 1, provider.calls)
	assert.Equal(t, 404, call("POST", "/refund-reservations/"+r.OperationID+"/release", mutationToken, r.OperationID, c).Code)
	common.RedisEnabled = true
	bad = r
	bad.OperationID = "unsupported-redis"
	assert.Equal(t, 409, call("POST", "/refund-reservations", mutationToken, bad.OperationID, bad).Code)
}

func TestOpsExportCredentialAndCursorBoundary(t *testing.T) {
	previous := Default
	t.Cleanup(func() { Default = previous })
	Default = nil
	db, err := gorm.Open(sqlite.Open("file:ops-export-http?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&model.AgencyBillingOutbox{}, &model.AgencyBillingOperation{}))
	gin.SetMode(gin.TestMode)
	router := gin.New()
	Register(router.Group("/api/ops-export/v1"))
	RegisterV2(router.Group("/api/ops-export/v2"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/ops-export/v1/facts", nil))
	assert.Equal(t, 404, w.Code)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/ops-export/v2/facts", nil))
	assert.Equal(t, 404, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"source_unavailable"`)
	t.Setenv("OPS_EXPORT_TOKEN_SHA256", "invalid")
	require.Error(t, StartConfigured(db))
	token := strings.Repeat("fixture-", 8)
	digest := sha256.Sum256([]byte(token))
	t.Setenv("OPS_EXPORT_TOKEN_SHA256", fmt.Sprintf("%x", digest))
	t.Setenv("OPS_GATEWAY_ID", "g1")
	t.Setenv("UPSTREAM_EVENT_GATEWAY_ID", "g2")
	require.Error(t, StartConfigured(db))
	t.Setenv("UPSTREAM_EVENT_GATEWAY_ID", "g1")
	require.NoError(t, StartConfigured(db))
	require.NoError(t, db.AutoMigrate(&model.Model{}, &model.Vendor{}, &model.Channel{}, &model.Ability{}, &model.Option{}, &model.User{}, &model.Token{}))
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "inventory", DisplayName: "Inventory user", Password: "s4-password-never-export", Email: "s4-email-never-export@example.test"}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 1, UserId: 1, Key: "s4-token-secret-never-export", Name: "Inventory token"}).Error)
	require.NoError(t, db.Create(&model.Model{ModelName: "s4-hidden", Endpoints: `["openai"]`}).Error)
	require.NoError(t, db.Model(&model.Model{}).Where("model_name = ?", "s4-hidden").Update("status", 0).Error)
	require.NoError(t, db.Create(&model.Channel{Id: 1, Models: "s4-disabled", Status: 2, Key: "s4-channel-secret-never-export"}).Error)
	var fixture struct {
		Items []model.CNFact `json:"items"`
	}
	fixtureJSON, err := os.ReadFile(filepath.Join("..", "..", "model", "testdata", "cn-contract", "finance-v2-facts.json"))
	require.NoError(t, err)
	require.NoError(t, common.Unmarshal(fixtureJSON, &fixture))
	for _, fact := range fixture.Items {
		require.NoError(t, model.StageCNFactTx(db, fact))
	}
	for _, tc := range []struct {
		path, credential string
		status           int
	}{
		{"/facts", "", 401}, {"/facts", "Bearer management-token", 401},
		{"/facts", "Bearer " + token + "wrong", 401}, {"/facts", "Bearer " + token, 200},
		{"/facts?limit=1001", "Bearer " + token, 400}, {"/facts?limit=0", "Bearer " + token, 400},
		{"/facts?limit=1&limit=2", "Bearer " + token, 400}, {"/facts?after=bad", "Bearer " + token, 400},
		{"/facts?after=djI6LTE", "Bearer " + token, 400}, {"/daily?date=2026-02-30", "Bearer " + token, 400},
		{"/daily?date=2026-10-14", "Bearer " + token, 200},
		{"/catalog", "Bearer " + token, 200},
		{"/inventory", "", 401}, {"/inventory", "Bearer management-token", 401},
		{"/inventory", "Bearer " + token, 200}, {"/inventory?limit=1", "Bearer " + token, 400},
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/ops-export/v1"+tc.path, nil)
		request.Header.Set("Authorization", tc.credential)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, request)
		assert.Equal(t, tc.status, w.Code, tc.path)
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		assert.NotContains(t, w.Body.String(), token)
		assert.NotContains(t, w.Body.String(), "s4-channel-secret-never-export")
		assert.NotContains(t, w.Body.String(), "s4-password-never-export")
		assert.NotContains(t, w.Body.String(), "s4-email-never-export")
		assert.NotContains(t, w.Body.String(), "s4-token-secret-never-export")
		if directory := os.Getenv("S4_TEST_ARTIFACT_DIR"); directory != "" && (tc.path == "/catalog" || tc.path == "/inventory") && tc.status == 200 {
			require.NoError(t, os.MkdirAll(directory, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(directory, strings.TrimPrefix(tc.path, "/")+".json"), w.Body.Bytes(), 0600))
		}
		if tc.path == "/facts" && tc.status == 200 {
			var result map[string]any
			require.NoError(t, common.Unmarshal(w.Body.Bytes(), &result))
			assert.Equal(t, "djI6MA", result["nextCursor"])
			assert.Equal(t, "g1", result["gatewayId"])
		}
	}
	w = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/ops-export/v1/facts", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, request)
	assert.Equal(t, 404, w.Code)
	for _, tc := range []struct {
		path, credential, code string
		status                 int
	}{
		{"/facts", "Bearer management-token", "unauthorized", 401},
		{"/facts?after=djI6MA", "Bearer " + token, "invalid_cursor", 400},
		{"/facts?limit=1&limit=2", "Bearer " + token, "invalid_payload", 400},
		{"/facts?extra=1", "Bearer " + token, "invalid_payload", 400},
		{"/daily?date=2026-02-30", "Bearer " + token, "invalid_payload", 400},
		{"/facts", "Bearer " + token, "", 200},
		{"/refund-capabilities", "Bearer " + token, "", 200},
		{"/daily?date=2026-10-14", "Bearer " + token, "", 200},
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/ops-export/v2"+tc.path, nil)
		request.Header.Set("Authorization", tc.credential)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, request)
		require.Equal(t, tc.status, w.Code, w.Body.String())
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		assert.NotContains(t, w.Body.String(), token)
		var body map[string]any
		require.NoError(t, common.Unmarshal(w.Body.Bytes(), &body))
		if tc.code != "" {
			failure := body["error"].(map[string]any)
			assert.Equal(t, tc.code, failure["code"])
			assert.Equal(t, false, failure["retryable"])
			assert.Nil(t, failure["operationId"])
		} else if tc.path == "/refund-capabilities" {
			assert.Equal(t, false, body["reservationAvailable"])
			assert.Equal(t, false, body["executionAvailable"])
			assert.Equal(t, false, body["manualVoucherEvidenceAvailable"])
		} else {
			assert.Equal(t, model.CNFinanceSource, body["sourceSystem"])
			if dependencies, ok := body["dependencies"].([]any); ok {
				for _, value := range dependencies {
					dependency := value.(map[string]any)
					from, to := dependency["coverageFrom"].(string), dependency["coverageTo"].(string)
					for _, timestamp := range []string{from, to} {
						parsed, err := time.Parse(model.CNTimeLayout, timestamp)
						require.NoError(t, err)
						assert.Equal(t, timestamp, parsed.UTC().Format(model.CNTimeLayout))
					}
					if from == to {
						assert.NotEqual(t, "complete", dependency["status"])
					}
				}
			}
		}
		if directory := os.Getenv("S4_TEST_ARTIFACT_DIR"); directory != "" && tc.status == 200 {
			require.NoError(t, os.MkdirAll(directory, 0700))
			name := strings.Split(strings.TrimPrefix(tc.path, "/"), "?")[0]
			require.NoError(t, os.WriteFile(filepath.Join(directory, name+".json"), w.Body.Bytes(), 0600))
		}
	}
}
