package httpapi_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/billingimport"
	"github.com/QuantumNous/new-api/claw-control/internal/evidence"
	"github.com/QuantumNous/new-api/claw-control/internal/httpapi"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/QuantumNous/new-api/claw-control/internal/usageaudit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failingBillingClient struct{}

func (failingBillingClient) DescribeBillDetail(context.Context, billingimport.DetailRequest) (*billingimport.DetailPage, error) {
	return nil, &billingimport.ProviderError{Code: "AuthFailure.SecretIdNotFound", RequestID: "provider-query-id", Action: "DescribeBillDetail"}
}

func (failingBillingClient) DescribeBillAdjustInfo(context.Context, string) (*billingimport.AdjustmentResult, error) {
	return nil, &billingimport.ProviderError{Code: "unexpected_call"}
}

func TestTencentBillingImportAdminEndpointsExposeOnlySafeAsyncState(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	evidenceService, err := evidence.New(db, t.TempDir(), bytes.Repeat([]byte{0x51}, 32), 1<<20,
		evidence.ScannerFunc(func(context.Context, []byte) error { return nil }))
	require.NoError(t, err)
	imports, err := billingimport.New(db, failingBillingClient{}, evidenceService, usageaudit.New(db), billingimport.Config{
		Enabled: true, PayerUIN: "10001", PageSize: 300, MaxPages: 2, MaxRecords: 600,
		MaxAttempts: 1, LeaseDuration: 5 * time.Minute, MaxEvidenceBytes: 1 << 20,
	})
	require.NoError(t, err)
	const adminToken = "emergency-admin-token"
	accessService, sessionCookie, csrfToken := issueAdminSession(t, db, secrets.EnvironmentResolver{}, 79)
	server := httpapi.New(httpapi.Services{DB: db, Access: accessService, BillingImports: imports}, adminToken, httpapi.InternalAuth{}, httpapi.PublicConfig{})
	month := time.Now().UTC().Format("2006-01")
	body, err := jsonx.Marshal(map[string]string{"month": month, "business_code": "p_adp"})
	require.NoError(t, err)
	create := httptest.NewRequest(http.MethodPost, "/api/admin/workbench/tencent-billing-imports", bytes.NewReader(body))
	authorizeAdminRequest(create, sessionCookie, csrfToken)
	createResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(createResponse, create)
	require.Equal(t, http.StatusAccepted, createResponse.Code)
	var created struct {
		Success bool               `json:"success"`
		Data    billingimport.View `json:"data"`
	}
	require.NoError(t, jsonx.Unmarshal(createResponse.Body.Bytes(), &created))
	assert.True(t, created.Success)
	assert.NotEmpty(t, created.Data.ImportID)
	assert.True(t, created.Data.AccountScoped)
	assert.False(t, created.Data.InvoiceMutation)
	assert.NotContains(t, createResponse.Body.String(), "10001")

	processed, err := imports.ProcessOne(context.Background())
	require.NoError(t, err)
	assert.True(t, processed)
	get := httptest.NewRequest(http.MethodGet, "/api/admin/workbench/tencent-billing-imports/"+created.Data.ImportID, nil)
	get.AddCookie(sessionCookie)
	getResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(getResponse, get)
	assert.Equal(t, http.StatusOK, getResponse.Code)
	assert.Contains(t, getResponse.Body.String(), "AuthFailure.SecretIdNotFound")
	assert.Contains(t, getResponse.Body.String(), `"bill_query_request_ids":["provider-query-id"]`)
	assert.NotContains(t, getResponse.Body.String(), "10001")

	retry := httptest.NewRequest(http.MethodPost, "/api/admin/workbench/tencent-billing-imports/"+created.Data.ImportID+"/retry", nil)
	authorizeAdminRequest(retry, sessionCookie, csrfToken)
	retryResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(retryResponse, retry)
	assert.Equal(t, http.StatusAccepted, retryResponse.Code)
	assert.Contains(t, retryResponse.Body.String(), `"status":"pending"`)

	list := httptest.NewRequest(http.MethodGet, "/api/admin/workbench/tencent-billing-imports?limit=10", nil)
	list.AddCookie(sessionCookie)
	listResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(listResponse, list)
	assert.Equal(t, http.StatusOK, listResponse.Code)
	assert.Contains(t, listResponse.Body.String(), created.Data.ImportID)
}
