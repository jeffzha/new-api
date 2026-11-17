package opsexport

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOpsExportCapabilitiesAreExplicitPerDomain(t *testing.T) {
	previous := Default
	t.Cleanup(func() { Default = previous })
	db, err := gorm.Open(sqlite.Open("file:capabilities-http?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(
		&model.Agency{}, &model.AgencyActiveUserBinding{}, &model.AgencyOperatorAccount{}, &model.User{},
		&model.AgencyPricePolicyVersion{}, &model.AgencyPlatformPriceVersion{}, &model.AgencyCustomerSalesOverride{},
		&model.AgencyFundingAccount{}, &model.AgencyFundingLot{}, &model.AgencyFundingAllocation{}, &model.AgencyFundingLedger{}, &model.AgencyTopupFact{}, &model.AgencyFundingReversal{},
		&model.AgencyFundingDebt{}, &model.AgencyDebtRepayment{},
		&model.AgencyCommissionLedger{}, &model.AgencyCommissionBalance{},
		&model.AgencyWithdrawalAccount{}, &model.AgencyWithdrawal{}, &model.AgencyWithdrawalTransition{},
		&model.AgencyAuditLog{},
	))
	token := strings.Repeat("capability-", 8)
	Default = &Server{DB: db, GatewayID: "cap-g1", TokenHash: sha256.Sum256([]byte(token))}
	router := gin.New()
	Register(router.Group("/api/ops-export/v1"))
	request := httptest.NewRequest(http.MethodGet, "/api/ops-export/v1/capabilities", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var payload struct {
		GatewayID string `json:"gatewayId"`
		Domains   []struct {
			Name               string `json:"name"`
			Supported          bool   `json:"supported"`
			Endpoint           string `json:"endpoint"`
			ConsistentSnapshot bool   `json:"consistentSnapshot"`
		} `json:"domains"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	require.Equal(t, "cap-g1", payload.GatewayID)
	byName := map[string]struct {
		supported bool
		endpoint  string
		snapshot  bool
	}{}
	for _, domain := range payload.Domains {
		byName[domain.Name] = struct {
			supported bool
			endpoint  string
			snapshot  bool
		}{domain.Supported, domain.Endpoint, domain.ConsistentSnapshot}
	}
	require.True(t, byName["distributor_directory"].supported)
	require.Equal(t, "/api/ops-export/v1/distributors", byName["distributor_directory"].endpoint)
	require.True(t, byName["agency_identity_metadata"].supported)
	require.Equal(t, "/api/ops-export/v1/agency-identities", byName["agency_identity_metadata"].endpoint)
	require.True(t, byName["customer_identity"].supported)
	require.True(t, byName["price_versions"].supported)
	require.Equal(t, "/api/ops-export/v1/price-versions", byName["price_versions"].endpoint)
	require.False(t, byName["price_versions"].snapshot)
	require.True(t, byName["wallet_history"].supported)
	require.Equal(t, "/api/ops-export/v1/wallet-history", byName["wallet_history"].endpoint)
	require.False(t, byName["wallet_history"].snapshot)
	require.True(t, byName["debt_history"].supported)
	require.Equal(t, "/api/ops-export/v1/debt-history", byName["debt_history"].endpoint)
	require.False(t, byName["debt_history"].snapshot)
	require.True(t, byName["commission_history"].supported)
	require.Equal(t, "/api/ops-export/v1/commission-history", byName["commission_history"].endpoint)
	require.True(t, byName["withdrawals"].supported)
	require.Equal(t, "/api/ops-export/v1/withdrawals", byName["withdrawals"].endpoint)
	require.True(t, byName["audit_history"].supported)
	require.Equal(t, "/api/ops-export/v1/audit-history", byName["audit_history"].endpoint)
	require.True(t, byName["audit_history"].snapshot)
	require.False(t, byName["sensitive_identity"].supported)
	require.False(t, byName["writer_fence"].supported)
	require.Equal(t, "/api/ops-export/v1/writer-fence", byName["writer_fence"].endpoint)
	for _, domain := range payload.Domains {
		if domain.Supported && domain.Endpoint == "" {
			t.Fatalf("supported domain %q has no endpoint", domain.Name)
		}
	}
}

func TestOpsExportWriterFenceStateAndReceipt(t *testing.T) {
	previous := Default
	t.Cleanup(func() { Default = previous })
	db, err := gorm.Open(sqlite.Open("file:writer-fence-http?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	token := strings.Repeat("fence-export-", 4)
	Default = &Server{DB: db, GatewayID: "fence-g1", TokenHash: sha256.Sum256([]byte(token))}
	router := gin.New()
	Register(router.Group("/api/ops-export/v1"))
	call := func(credential, path string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/api/ops-export/v1"+path, nil)
		if credential != "" {
			request.Header.Set("Authorization", "Bearer "+credential)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	require.Equal(t, http.StatusUnauthorized, call("", "/writer-fence").Code)
	require.Equal(t, http.StatusServiceUnavailable, call(token, "/writer-fence").Code)
	require.NoError(t, db.AutoMigrate(&model.AgencyOpsWriterState{}, &model.AgencyOpsWriterTransition{}))
	require.NoError(t, db.Create(&model.AgencyOpsWriterState{ID: 1, Mode: model.AgencyWriterShadow,
		FenceVersion: 2, ManifestVersion: 3, LastCommandID: "command-1", Reason: "private reason",
		EvidenceJSON: `{"private":"evidence"}`, UpdatedAtMS: 123}).Error)
	require.NoError(t, db.Create(&model.AgencyOpsWriterTransition{CommandID: "command-1", TargetMode: model.AgencyWriterShadow,
		ExpectedFenceVersion: 1, ResultingFenceVersion: 2, ManifestVersion: 3,
		Reason: "private reason", EvidenceJSON: `{"private":"evidence"}`, ResultJSON: `{}`, CreatedAtMS: 122}).Error)
	require.Equal(t, http.StatusBadRequest, call(token, "/writer-fence?extra=1").Code)
	require.Equal(t, http.StatusBadRequest, call(token, "/writer-fence?command_id=").Code)
	require.Equal(t, http.StatusNotFound, call(token, "/writer-fence?command_id=unknown").Code)
	response := call(token, "/writer-fence")
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.NotContains(t, response.Body.String(), "private reason")
	require.NotContains(t, response.Body.String(), "evidence")
	var payload struct {
		GatewayID    string `json:"gatewayId"`
		WriterMode   string `json:"writerMode"`
		FenceVersion int64  `json:"fenceVersion"`
		LastReceipt  struct {
			CommandID             string `json:"commandId"`
			ResultingFenceVersion int64  `json:"resultingFenceVersion"`
		} `json:"lastReceipt"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	require.Equal(t, "fence-g1", payload.GatewayID)
	require.Equal(t, model.AgencyWriterShadow, payload.WriterMode)
	require.EqualValues(t, 2, payload.FenceVersion)
	require.Equal(t, "command-1", payload.LastReceipt.CommandID)
	require.EqualValues(t, 2, payload.LastReceipt.ResultingFenceVersion)
	require.Equal(t, http.StatusOK, call(token, "/writer-fence?command_id=command-1").Code)
}
