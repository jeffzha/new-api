package httpapi_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/access"
	"github.com/QuantumNous/new-api/claw-control/internal/adminquery"
	"github.com/QuantumNous/new-api/claw-control/internal/app"
	"github.com/QuantumNous/new-api/claw-control/internal/appmigration"
	"github.com/QuantumNous/new-api/claw-control/internal/auditexport"
	"github.com/QuantumNous/new-api/claw-control/internal/credential"
	"github.com/QuantumNous/new-api/claw-control/internal/customer"
	"github.com/QuantumNous/new-api/claw-control/internal/httpapi"
	"github.com/QuantumNous/new-api/claw-control/internal/identity"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/resourcebinding"
	"github.com/QuantumNous/new-api/claw-control/internal/secretintegrity"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const (
	testContractVersionHeader = "X-Workbench-Contract-Version"
	testContractVersion       = "1"
)

type acceptingVerifier struct{}

func (acceptingVerifier) Verify(_ context.Context, _ int64, _ string) error      { return nil }
func (acceptingVerifier) VerifyFresh(_ context.Context, _ int64, _ string) error { return nil }
func (acceptingVerifier) VerifyAdmin(_ context.Context, _ int64, _ string) error { return nil }

func TestAgentStoreStatusIsPublicAndReturnsOnlyFeatureState(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		server := httpapi.New(httpapi.Services{}, "", httpapi.InternalAuth{}, httpapi.PublicConfig{AgentStoreEnabled: enabled})
		request := httptest.NewRequest(http.MethodGet, "/api/workbench/agent-store/status", nil)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code)
		assert.JSONEq(t, fmt.Sprintf(`{"success":true,"data":{"enabled":%t}}`, enabled), response.Body.String())
		assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	}
}

func TestReadyFailsClosedOnProviderSecretIntegrity(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	resolver := testutil.SecretResolver{"env://WORKBENCH_PROVIDER_READY_ID": "id", "env://WORKBENCH_PROVIDER_READY_KEY": "key"}
	integrity := secretintegrity.New(db, resolver)
	server := httpapi.New(httpapi.Services{DB: db, SecretIntegrity: integrity}, "admin-token", httpapi.InternalAuth{}, httpapi.PublicConfig{})

	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)

	profile := model.CredentialProfile{
		OwnerScope: "platform", ProviderEnvironment: model.ProviderChinaTencentADP, Name: "legacy-ready",
		SecretIDRef: "env://WORKBENCH_PROVIDER_READY_ID", SecretKeyRef: "env://WORKBENCH_PROVIDER_READY_KEY",
		Fingerprint: "legacy", FingerprintVersion: 0, Status: model.CredentialStatusActive, Version: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&profile).Error)
	request = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
	assert.NotContains(t, response.Body.String(), "WORKBENCH_PROVIDER_READY")
}

func TestInternalHMACEnvelopeAndNonceReplayProtection(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	const secret = "0123456789abcdef0123456789abcdef"
	server := httpapi.New(httpapi.Services{DB: db, Identities: identity.New(db)}, "admin-token", httpapi.InternalAuth{
		ServiceKeys: map[string]string{"adp-backend": secret}, TimeSkew: time.Minute,
		NewAPIServiceName: "new-api-core", ADPServiceName: "adp-backend",
	}, httpapi.PublicConfig{ADPSSORedirectPath: "/workbench/auth/sso", AdminRedirectPath: "/workbench/admin"})
	const path = "/api/internal/workbench/identities/confirm"
	body := []byte(`{"binding_id":"","canonical_subject":"","adp_account_id":"","adp_account_version":0}`)
	timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	nonce := "nonce-one"

	first := signedRequest(t, path, body, timestamp, nonce, secret)
	firstResult := httptest.NewRecorder()
	server.Handler().ServeHTTP(firstResult, first)
	assert.Equal(t, http.StatusBadRequest, firstResult.Code)
	assert.Contains(t, firstResult.Body.String(), `"success":false`)
	assertSignedResponse(t, firstResult, path, nonce, secret)

	second := signedRequest(t, path, body, timestamp, nonce, secret)
	secondResult := httptest.NewRecorder()
	server.Handler().ServeHTTP(secondResult, second)
	assert.Equal(t, http.StatusUnauthorized, secondResult.Code)
	assert.Contains(t, secondResult.Body.String(), "replayed_request")
	assertSignedResponse(t, secondResult, path, nonce, secret)
}

func TestInternalHMACRejectsMissingOrUnsupportedContractVersion(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	const secret = "0123456789abcdef0123456789abcdef"
	server := httpapi.New(httpapi.Services{DB: db, Identities: identity.New(db)}, "admin-token", httpapi.InternalAuth{
		ServiceKeys: map[string]string{"adp-backend": secret}, TimeSkew: time.Minute,
		NewAPIServiceName: "new-api-core", ADPServiceName: "adp-backend",
	}, httpapi.PublicConfig{})
	const path = "/api/internal/workbench/identities/confirm"
	body := []byte(`{"binding_id":"","canonical_subject":"","adp_account_id":"","adp_account_version":0}`)
	timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	const nonce = "contract-version"

	missing := signedRequest(t, path, body, timestamp, nonce, secret)
	missing.Header.Del(testContractVersionHeader)
	missingResult := httptest.NewRecorder()
	server.Handler().ServeHTTP(missingResult, missing)
	assert.Equal(t, http.StatusUnauthorized, missingResult.Code)
	assert.Contains(t, missingResult.Body.String(), "valid internal service signature is required")

	unsupported := signedRequest(t, path, body, timestamp, nonce, secret)
	unsupported.Header.Set(testContractVersionHeader, "2")
	unsupportedResult := httptest.NewRecorder()
	server.Handler().ServeHTTP(unsupportedResult, unsupported)
	assert.Equal(t, http.StatusUnauthorized, unsupportedResult.Code)
	assert.Contains(t, unsupportedResult.Body.String(), "valid internal service signature is required")

	valid := signedRequest(t, path, body, timestamp, nonce, secret)
	validResult := httptest.NewRecorder()
	server.Handler().ServeHTTP(validResult, valid)
	assert.Equal(t, http.StatusBadRequest, validResult.Code, "invalid contract versions must not burn the request nonce")
	assertSignedResponse(t, validResult, path, nonce, secret)
}

func TestResourceBindingRouteRequiresADPHMACAndValidatesPayload(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	const secret = "0123456789abcdef0123456789abcdef"
	server := httpapi.New(httpapi.Services{DB: db, Resources: resourcebinding.New(db)}, "admin-token", httpapi.InternalAuth{
		ServiceKeys: map[string]string{"adp-backend": secret}, TimeSkew: time.Minute,
		NewAPIServiceName: "new-api-core", ADPServiceName: "adp-backend",
	}, httpapi.PublicConfig{})
	const path = "/api/internal/workbench/resources/bind"
	body := []byte(`{"binding_id":"","resource_type":"account","resource_id":""}`)
	request := signedRequest(t, path, body, strconv.FormatInt(time.Now().UTC().Unix(), 10), "resource-route", secret)
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	assert.Equal(t, http.StatusBadRequest, response.Code)
	assert.Contains(t, response.Body.String(), `"code":"invalid_request"`)
	assertSignedResponse(t, response, path, "resource-route", secret)
}

func TestInternalAdminStepUpProofRejectsReplayAndExpiry(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	const secret = "0123456789abcdef0123456789abcdef"
	accessService := access.New(db, secrets.EnvironmentResolver{}, acceptingVerifier{}, time.Minute, time.Minute, time.Hour, time.Hour, time.Minute)
	server := httpapi.New(httpapi.Services{DB: db, Access: accessService}, "admin-token", httpapi.InternalAuth{
		ServiceKeys: map[string]string{"new-api-core": secret}, TimeSkew: time.Minute,
		NewAPIServiceName: "new-api-core", ADPServiceName: "adp-backend",
	}, httpapi.PublicConfig{})
	const path = "/api/internal/workbench/entry-tickets/issue"
	proofNonce := sha256.Sum256([]byte("http-step-up-proof"))
	body := []byte(fmt.Sprintf(`{"new_api_user_id":99,"identity_version":"v1.admin","surface":"admin","is_super_admin":true,"authenticated_at":%d,"amr":["pwd"],"reauth_nonce":"%s"}`,
		time.Now().UTC().Unix(), base64.RawURLEncoding.EncodeToString(proofNonce[:])))

	first := signedRequest(t, path, body, strconv.FormatInt(time.Now().UTC().Unix(), 10), "step-up-first", secret)
	first.Header.Set("X-Workbench-Service", "new-api-core")
	firstResult := httptest.NewRecorder()
	server.Handler().ServeHTTP(firstResult, first)
	require.Equal(t, http.StatusCreated, firstResult.Code, firstResult.Body.String())

	replayed := signedRequest(t, path, body, strconv.FormatInt(time.Now().UTC().Unix(), 10), "step-up-replayed", secret)
	replayed.Header.Set("X-Workbench-Service", "new-api-core")
	replayedResult := httptest.NewRecorder()
	server.Handler().ServeHTTP(replayedResult, replayed)
	assert.Equal(t, http.StatusConflict, replayedResult.Code)
	assert.Contains(t, replayedResult.Body.String(), "already been used")

	expiredNonce := sha256.Sum256([]byte("http-expired-step-up-proof"))
	expiredBody := []byte(fmt.Sprintf(`{"new_api_user_id":99,"identity_version":"v1.admin","surface":"admin","is_super_admin":true,"authenticated_at":%d,"amr":["otp"],"reauth_nonce":"%s"}`,
		time.Now().UTC().Add(-6*time.Minute).Unix(), base64.RawURLEncoding.EncodeToString(expiredNonce[:])))
	expired := signedRequest(t, path, expiredBody, strconv.FormatInt(time.Now().UTC().Unix(), 10), "step-up-expired", secret)
	expired.Header.Set("X-Workbench-Service", "new-api-core")
	expiredResult := httptest.NewRecorder()
	server.Handler().ServeHTTP(expiredResult, expired)
	assert.Equal(t, http.StatusForbidden, expiredResult.Code)
	assert.Contains(t, expiredResult.Body.String(), "expired or invalid")
}

func TestAppMigrationTaskRoutesRequireADPHMACAndNeverCache(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	const secret = "0123456789abcdef0123456789abcdef"
	server := httpapi.New(httpapi.Services{
		DB: db, AppMigrations: appmigration.New(db, testutil.SecretResolver{}),
	}, "admin-token", httpapi.InternalAuth{
		ServiceKeys: map[string]string{"adp-backend": secret}, TimeSkew: time.Minute,
		NewAPIServiceName: "new-api-core", ADPServiceName: "adp-backend",
	}, httpapi.PublicConfig{})
	const claimPath = "/api/internal/workbench/app-migrations/tasks/claim"
	claimBody := []byte(`{"worker_id":"adp-blue","lease_seconds":60}`)
	claim := signedRequest(t, claimPath, claimBody, strconv.FormatInt(time.Now().UTC().Unix(), 10), "migration-claim", secret)
	claimResult := httptest.NewRecorder()
	server.Handler().ServeHTTP(claimResult, claim)
	require.Equal(t, http.StatusOK, claimResult.Code)
	assert.Equal(t, "no-store", claimResult.Header().Get("Cache-Control"))
	assert.Contains(t, claimResult.Body.String(), `"task":null`)
	assertSignedResponse(t, claimResult, claimPath, "migration-claim", secret)

	const reportPath = "/api/internal/workbench/app-migrations/tasks/report"
	reportBody := []byte(`{"migration_member_id":"","attempt_id":"","lease_token":"","status":""}`)
	report := signedRequest(t, reportPath, reportBody, strconv.FormatInt(time.Now().UTC().Unix(), 10), "migration-report", secret)
	reportResult := httptest.NewRecorder()
	server.Handler().ServeHTTP(reportResult, report)
	assert.Equal(t, http.StatusBadRequest, reportResult.Code)
	assert.Equal(t, "no-store", reportResult.Header().Get("Cache-Control"))
	assertSignedResponse(t, reportResult, reportPath, "migration-report", secret)
}

func TestAdminEntryCookiesAndCSRFProtectMutations(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	accessService := access.New(
		db, secrets.EnvironmentResolver{}, acceptingVerifier{},
		time.Minute, time.Minute, time.Hour, time.Hour, time.Minute,
	)
	nonce := sha256.Sum256([]byte("admin-entry-csrf-step-up"))
	entry, err := accessService.IssueEntryTicket(access.IssueEntryTicketCommand{
		NewAPIUserID: 99, IdentityVersion: "v1.admin", Surface: "admin", IsSuperAdmin: true,
		AuthenticatedAt: time.Now().UTC(), AMR: []string{"webauthn"}, ReauthNonce: base64.RawURLEncoding.EncodeToString(nonce[:]),
	})
	require.NoError(t, err)
	server := httpapi.New(httpapi.Services{
		DB: db, Access: accessService, Customers: customer.New(db, "prod", testutil.NewIdentityVerifier("v1.test")),
		AuditExport: auditexport.New(db),
	}, "emergency-admin-token", httpapi.InternalAuth{}, httpapi.PublicConfig{
		ADPSSORedirectPath: "/workbench/auth/sso", AdminRedirectPath: "/workbench/admin",
	})
	entryRequest := httptest.NewRequest(http.MethodGet, "/api/workbench/entry?ticket="+entry.Ticket, nil)
	entryResult := httptest.NewRecorder()
	server.Handler().ServeHTTP(entryResult, entryRequest)
	require.Equal(t, http.StatusFound, entryResult.Code)
	assert.Equal(t, "/workbench/admin", entryResult.Header().Get("Location"))

	var sessionCookie, csrfCookie *http.Cookie
	for _, cookie := range entryResult.Result().Cookies() {
		switch cookie.Name {
		case "claw_admin_session":
			sessionCookie = cookie
		case "claw_admin_csrf":
			csrfCookie = cookie
		}
	}
	require.NotNil(t, sessionCookie)
	require.NotNil(t, csrfCookie)
	assert.Equal(t, "/api/admin/workbench", sessionCookie.Path)
	assert.True(t, sessionCookie.HttpOnly)
	assert.True(t, sessionCookie.Secure)
	assert.Equal(t, http.SameSiteStrictMode, sessionCookie.SameSite)
	assert.Equal(t, "/workbench/admin", csrfCookie.Path, "only the admin SPA must be able to read the CSRF cookie")
	assert.False(t, csrfCookie.HttpOnly)
	assert.True(t, csrfCookie.Secure)
	assert.Equal(t, http.SameSiteStrictMode, csrfCookie.SameSite)

	now := time.Now().UTC()
	exportPath := "/api/admin/workbench/audits/export?format=csv&limit=10&start=" + now.Add(-time.Hour).Format(time.RFC3339) + "&end=" + now.Add(time.Hour).Format(time.RFC3339)
	exportRequest := httptest.NewRequest(http.MethodGet, exportPath, nil)
	exportRequest.AddCookie(sessionCookie)
	exportResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(exportResponse, exportRequest)
	assert.Equal(t, http.StatusOK, exportResponse.Code)
	assert.Equal(t, "no-store", exportResponse.Header().Get("Cache-Control"))
	assert.Contains(t, exportResponse.Header().Get("Content-Disposition"), "attachment")

	emergencyExport := httptest.NewRequest(http.MethodGet, exportPath, nil)
	emergencyExport.Header.Set("Authorization", "Bearer emergency-admin-token")
	emergencyExport.Header.Set("X-Claw-Actor", "bootstrap-admin")
	emergencyResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(emergencyResponse, emergencyExport)
	assert.Equal(t, http.StatusForbidden, emergencyResponse.Code, "audit export requires a revocable admin session, not emergency bootstrap access")

	body := []byte(`{"customer_code":"admin-created","display_name":"Admin Created"}`)
	mutation := httptest.NewRequest(http.MethodPost, "/api/admin/workbench/customers", bytes.NewReader(body))
	mutation.AddCookie(sessionCookie)
	mutation.Header.Set("X-CSRF-Token", csrfCookie.Value)
	mutation.Header.Set("X-Claw-Actor", "forged-admin-actor")
	mutationResult := httptest.NewRecorder()
	server.Handler().ServeHTTP(mutationResult, mutation)
	assert.Equal(t, http.StatusCreated, mutationResult.Code)
	var sessionAudit model.AdminAudit
	require.NoError(t, db.Where("action = ?", "customer.create").Order("id desc").First(&sessionAudit).Error)
	assert.Equal(t, "admin-session:99", sessionAudit.Actor)
}

func TestEmergencyBootstrapCannotAccessPublicAdminAPI(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	server := httpapi.New(httpapi.Services{
		DB: db, Customers: customer.New(db, "prod", testutil.NewIdentityVerifier("v1.test")),
	}, "emergency-admin-token", httpapi.InternalAuth{}, httpapi.PublicConfig{})
	create := httptest.NewRequest(http.MethodPost, "/api/admin/workbench/customers", bytes.NewReader([]byte(`{"customer_code":"bootstrap-actor","display_name":"Bootstrap"}`)))
	create.Header.Set("Authorization", "Bearer emergency-admin-token")
	create.Header.Set("X-Claw-Actor", "forged-super-admin")
	created := httptest.NewRecorder()
	server.Handler().ServeHTTP(created, create)
	require.Equal(t, http.StatusForbidden, created.Code, created.Body.String())
	assert.Contains(t, created.Body.String(), "restricted to loopback maintenance")

	for _, endpoint := range []string{
		"/api/admin/workbench/approvals",
		"/api/admin/workbench/approvals/apr_test/approve",
		"/api/admin/workbench/approvals/apr_test/reject",
		"/api/admin/workbench/approvals/apr_test/execute",
	} {
		approvalRequest := httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader([]byte(`{}`)))
		approvalRequest.Header.Set("Authorization", "Bearer emergency-admin-token")
		approvalRequest.Header.Set("X-Claw-Actor", "forged-approver")
		approvalResponse := httptest.NewRecorder()
		server.Handler().ServeHTTP(approvalResponse, approvalRequest)
		assert.Equal(t, http.StatusForbidden, approvalResponse.Code, endpoint)
		assert.Contains(t, approvalResponse.Body.String(), "restricted to loopback maintenance", endpoint)
	}
}

func TestFingerprintReenrollmentIsLoopbackBootstrapOnlyAndUsesFixedActor(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	resolver := testutil.SecretResolver{
		"env://WORKBENCH_PROVIDER_MAINTENANCE_ID":  "maintenance-id",
		"env://WORKBENCH_PROVIDER_MAINTENANCE_KEY": "maintenance-key",
	}
	profile := model.CredentialProfile{
		OwnerScope: "platform", ProviderEnvironment: model.ProviderChinaTencentADP, Name: "maintenance",
		SecretIDRef: "env://WORKBENCH_PROVIDER_MAINTENANCE_ID", SecretKeyRef: "env://WORKBENCH_PROVIDER_MAINTENANCE_KEY",
		Fingerprint: "legacy", FingerprintVersion: 0, Status: model.CredentialStatusActive, Version: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&profile).Error)
	accessService := access.New(db, resolver, acceptingVerifier{}, time.Minute, time.Minute, time.Hour, time.Hour, time.Minute)
	entry, err := accessService.IssueEntryTicket(access.IssueEntryTicketCommand{
		NewAPIUserID: 101, IdentityVersion: "v1.maintenance", Surface: "admin", IsSuperAdmin: true,
	})
	require.NoError(t, err)
	adminSession, err := accessService.Enter(context.Background(), access.EnterCommand{Ticket: entry.Ticket})
	require.NoError(t, err)
	integrity := secretintegrity.New(db, resolver)
	server := httpapi.New(httpapi.Services{DB: db, Access: accessService, SecretIntegrity: integrity}, "emergency-admin-token", httpapi.InternalAuth{}, httpapi.PublicConfig{})
	const endpoint = "/api/admin/workbench/secret-fingerprints/re-enroll"
	const body = `{"confirmation":"rebind-current-runtime-secrets"}`

	remote := httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader([]byte(body)))
	remote.RemoteAddr = "172.18.0.5:42000"
	remote.Header.Set("Authorization", "Bearer emergency-admin-token")
	remoteResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(remoteResponse, remote)
	assert.Equal(t, http.StatusForbidden, remoteResponse.Code)

	sessionOnly := httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader([]byte(body)))
	sessionOnly.RemoteAddr = "127.0.0.1:42001"
	sessionOnly.AddCookie(&http.Cookie{Name: "claw_admin_session", Value: adminSession.AdminSessionToken})
	sessionOnly.Header.Set("X-CSRF-Token", adminSession.AdminCSRFToken)
	sessionResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(sessionResponse, sessionOnly)
	assert.Equal(t, http.StatusUnauthorized, sessionResponse.Code, "even a valid admin session cannot invoke emergency maintenance")

	loopback := httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader([]byte(body)))
	loopback.RemoteAddr = "127.0.0.1:42002"
	loopback.Header.Set("Authorization", "Bearer emergency-admin-token")
	loopback.Header.Set("X-Claw-Actor", "forged-maintenance-actor")
	loopbackResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(loopbackResponse, loopback)
	require.Equal(t, http.StatusOK, loopbackResponse.Code, loopbackResponse.Body.String())
	var audit model.AdminAudit
	require.NoError(t, db.Where("action = ?", "credential.fingerprint.reenroll").First(&audit).Error)
	assert.Equal(t, "emergency-bootstrap:provider-fingerprint-reenroll", audit.Actor)
}

func TestUnknownCapabilityReturnsBadRequest(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	accessService, sessionCookie, csrfToken := issueAdminSession(t, db, secrets.EnvironmentResolver{}, 102)
	server := httpapi.New(httpapi.Services{DB: db, Access: accessService, Apps: app.New(db, false)}, "emergency-admin-token", httpapi.InternalAuth{}, httpapi.PublicConfig{})
	body := []byte(`{
		"expected_version":0,
		"provider_environment":"china_tencent_cloud",
		"region":"ap-guangzhou",
		"space_id":"space-1",
		"app_id":"app-1",
		"template_agent_id":"agent-1",
		"credential_profile_id":1,
		"app_key_secret_ref":"env://WORKBENCH_PROVIDER_TEST_APP_KEY",
		"app_key_fingerprint":"sha256:test",
		"display_name":"Test App",
		"limits":{"customer_concurrency":2,"user_concurrency":1,"max_runtime_seconds":600,"max_reasoning_rounds":20,"max_output_tokens":8192,"web_search_per_turn":3,"max_file_bytes":52428800},
		"capabilities":["chat","arbitrary_provider_action"]
	}`)
	request := httptest.NewRequest(http.MethodPut, "/api/admin/workbench/customers/1/app", bytes.NewReader(body))
	authorizeAdminRequest(request, sessionCookie, csrfToken)
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	assert.Equal(t, http.StatusBadRequest, response.Code)
	assert.Contains(t, response.Body.String(), `"code":"invalid_request"`)
	assert.Contains(t, response.Body.String(), `unsupported capability`)
}

func TestWorkbenchSelectionRoutesRequireTheBoundControlSessionCookie(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	server := httpapi.New(httpapi.Services{DB: db, Access: access.New(
		db, secrets.EnvironmentResolver{}, acceptingVerifier{},
		time.Minute, time.Minute, time.Hour, time.Hour, time.Minute,
	)}, "emergency-admin-token", httpapi.InternalAuth{}, httpapi.PublicConfig{})
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/workbench/selections", nil),
		httptest.NewRequest(http.MethodPost, "/api/workbench/selections/choose", bytes.NewReader([]byte(`{"selection_token":"browser-cannot-submit-customer-id"}`))),
	} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		assert.Equal(t, http.StatusForbidden, response.Code)
		assert.Contains(t, response.Body.String(), "control session is required")
		assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	}
}

func TestSelfReportedProviderVerificationRouteIsAbsentAndTrustedRouteRejectsExtraFields(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	accessService, sessionCookie, csrfToken := issueAdminSession(t, db, secrets.EnvironmentResolver{}, 103)
	server := httpapi.New(httpapi.Services{Access: accessService, Apps: app.New(db, false)}, "emergency-admin-token", httpapi.InternalAuth{}, httpapi.PublicConfig{})
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/admin/workbench/customers/1/app/verification-records",
		bytes.NewReader([]byte(`{"result":"verified","app_mode":4,"release_status":"published"}`)),
	)
	authorizeAdminRequest(request, sessionCookie, csrfToken)
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	assert.Equal(t, http.StatusNotFound, response.Code)

	trustedRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/admin/workbench/customers/1/app/verify",
		bytes.NewReader([]byte(`{"expected_version":1,"config_version":1,"result":"verified"}`)),
	)
	authorizeAdminRequest(trustedRequest, sessionCookie, csrfToken)
	trustedResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(trustedResponse, trustedRequest)
	assert.Equal(t, http.StatusBadRequest, trustedResponse.Code)
	assert.Contains(t, trustedResponse.Body.String(), "unknown field")
}

func TestAdminReadProjectionsAreAvailableWithoutSecretReferences(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	customers := customer.New(db, "prod", testutil.NewIdentityVerifier("v1.test"))
	created, err := customers.Create(customer.CreateCommand{
		CustomerCode: "admin-projection", DisplayName: "Admin Projection", Actor: "fixture",
	})
	require.NoError(t, err)
	accessService, sessionCookie, _ := issueAdminSession(t, db, secrets.EnvironmentResolver{}, 106)
	server := httpapi.New(httpapi.Services{
		DB: db, Access: accessService, Customers: customers, AdminQueries: adminquery.New(db),
	}, "emergency-admin-token", httpapi.InternalAuth{}, httpapi.PublicConfig{})

	for _, target := range []string{
		"/api/admin/workbench/dashboard",
		fmt.Sprintf("/api/admin/workbench/customers/%d", created.ID),
		"/api/admin/workbench/plan-catalog?limit=100",
		"/api/admin/workbench/credential-profiles?limit=100",
		"/api/admin/workbench/audits?limit=100",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.AddCookie(sessionCookie)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		assert.Equal(t, http.StatusOK, response.Code, target)
		assert.Contains(t, response.Body.String(), `"success":true`, target)
		assert.NotContains(t, response.Body.String(), "secret_ref", target)
	}
}

func TestAdminMutationResponsesNeverEchoSecretReferences(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	customers := customer.New(db, "prod", testutil.NewIdentityVerifier("v1.test"))
	created, err := customers.Create(customer.CreateCommand{
		CustomerCode: "secret-projection", DisplayName: "Secret Projection", Actor: "fixture",
	})
	require.NoError(t, err)
	environmentResolver := testutil.SecretResolver{
		"env://WORKBENCH_PROVIDER_FIXTURE_ID": "fixture-id", "env://WORKBENCH_PROVIDER_FIXTURE_KEY": "fixture-key",
		"env://WORKBENCH_PROVIDER_HTTP_ID": "http-id", "env://WORKBENCH_PROVIDER_HTTP_KEY": "http-key",
		"env://WORKBENCH_PROVIDER_ROTATE_ID": "rotate-id", "env://WORKBENCH_PROVIDER_ROTATE_KEY": "rotate-key",
		"env://WORKBENCH_PROVIDER_HTTP_APP_KEY": "http-app-key",
	}
	vault, err := secrets.NewVaultResolver(db, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	resolver := secrets.NewCompositeResolver(environmentResolver, vault)
	credentials := credential.New(db, resolver)
	profile, err := credentials.Create(credential.CreateCommand{
		ProviderEnvironment: "china_tencent_cloud", Name: "fixture",
		SecretIDRef: "env://WORKBENCH_PROVIDER_FIXTURE_ID", SecretKeyRef: "env://WORKBENCH_PROVIDER_FIXTURE_KEY",
		Fingerprint: secrets.CredentialPairFingerprint("fixture-id", "fixture-key"), Actor: "fixture",
	})
	require.NoError(t, err)
	accessService, sessionCookie, csrfToken := issueAdminSession(t, db, resolver, 104)
	server := httpapi.New(httpapi.Services{
		DB: db, Access: accessService, Customers: customers, Credentials: credentials, Apps: app.New(db, false, resolver),
		AdminQueries: adminquery.New(db),
	}, "emergency-admin-token", httpapi.InternalAuth{}, httpapi.PublicConfig{})

	credentialRequest := httptest.NewRequest(http.MethodPost, "/api/admin/workbench/credential-profiles", bytes.NewReader([]byte(fmt.Sprintf(`{
		"owner_scope":"customer:%d","customer_id":%d,
		"provider_environment":"china_tencent_cloud","name":"http-created",
		"secret_id_ref":"env://WORKBENCH_PROVIDER_HTTP_ID","secret_key_ref":"env://WORKBENCH_PROVIDER_HTTP_KEY",
		"fingerprint":"%s"
	}`, created.ID, created.ID, secrets.CredentialPairFingerprint("http-id", "http-key")))))
	authorizeAdminRequest(credentialRequest, sessionCookie, csrfToken)
	credentialResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(credentialResponse, credentialRequest)
	require.Equal(t, http.StatusCreated, credentialResponse.Code)
	assert.NotContains(t, credentialResponse.Body.String(), "WORKBENCH_PROVIDER_")
	assert.NotContains(t, credentialResponse.Body.String(), "secret_id_ref")
	assert.Contains(t, credentialResponse.Body.String(), secrets.CredentialPairFingerprint("http-id", "http-key"))
	assert.Contains(t, credentialResponse.Body.String(), fmt.Sprintf(`"owner_scope":"customer:%d"`, created.ID))

	rotationRequest := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/admin/workbench/credential-profiles/%d/rotations", profile.ID), bytes.NewReader([]byte(fmt.Sprintf(`{
		"expected_version":%d,"secret_id_ref":"env://WORKBENCH_PROVIDER_ROTATE_ID",
		"secret_key_ref":"env://WORKBENCH_PROVIDER_ROTATE_KEY","fingerprint":"%s"
	}`, profile.RowVersion, secrets.CredentialPairFingerprint("rotate-id", "rotate-key")))))
	authorizeAdminRequest(rotationRequest, sessionCookie, csrfToken)
	rotationResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(rotationResponse, rotationRequest)
	require.Equal(t, http.StatusCreated, rotationResponse.Code, rotationResponse.Body.String())
	assert.NotContains(t, rotationResponse.Body.String(), "WORKBENCH_PROVIDER_")
	assert.NotContains(t, rotationResponse.Body.String(), "secret_key_ref")
	assert.Contains(t, rotationResponse.Body.String(), `"owner_scope":"platform"`)

	listRequest := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/admin/workbench/credential-profiles?customer_id=%d", created.ID), nil)
	listRequest.AddCookie(sessionCookie)
	listResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(listResponse, listRequest)
	require.Equal(t, http.StatusOK, listResponse.Code, listResponse.Body.String())
	assert.NotContains(t, listResponse.Body.String(), "WORKBENCH_PROVIDER_")
	assert.Contains(t, listResponse.Body.String(), `"owner_scope":"platform"`)
	assert.Contains(t, listResponse.Body.String(), fmt.Sprintf(`"owner_scope":"customer:%d"`, created.ID))

	appRequest := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/admin/workbench/customers/%d/app", created.ID), bytes.NewReader([]byte(fmt.Sprintf(`{
		"expected_version":0,"provider_environment":"china_tencent_cloud","region":"ap-guangzhou",
		"space_id":"space-1","app_id":"app-1","template_agent_id":"agent-1","credential_profile_id":%d,
		"app_key":"write-only-http-app-key",
		"display_name":"Workbench","limits":{"customer_concurrency":2,"user_concurrency":1,"max_runtime_seconds":600,
		"max_reasoning_rounds":20,"max_output_tokens":4096,"web_search_per_turn":0,"max_file_bytes":1048576},"capabilities":["chat"]
	}`, profile.ID))))
	authorizeAdminRequest(appRequest, sessionCookie, csrfToken)
	appResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(appResponse, appRequest)
	require.Equal(t, http.StatusOK, appResponse.Code, appResponse.Body.String())
	assert.NotContains(t, appResponse.Body.String(), "WORKBENCH_PROVIDER_")
	assert.NotContains(t, appResponse.Body.String(), "write-only-http-app-key")
	assert.NotContains(t, appResponse.Body.String(), "app_key_fingerprint")
	var storedSecret model.ProviderSecret
	require.NoError(t, db.First(&storedSecret).Error)
	assert.NotContains(t, storedSecret.Ciphertext, "write-only-http-app-key")
}

func TestSensitiveAdminMutationsRequireRecentAuthentication(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	accessService, sessionCookie, csrfToken := issueAdminSession(t, db, secrets.EnvironmentResolver{}, 105)
	digest := sha256.Sum256([]byte(sessionCookie.Value))
	require.NoError(t, db.Model(&model.AdminSession{}).
		Where("token_hash = ?", hex.EncodeToString(digest[:])).
		Update("authenticated_at", time.Now().UTC().Add(-20*time.Minute)).Error)
	server := httpapi.New(httpapi.Services{DB: db, Access: accessService, AdminQueries: adminquery.New(db)}, "emergency-admin-token", httpapi.InternalAuth{}, httpapi.PublicConfig{})

	for _, testCase := range []struct {
		method string
		path   string
	}{
		{http.MethodPut, "/api/admin/workbench/customers/1/app"},
		{http.MethodPost, "/api/admin/workbench/credential-profiles"},
		{http.MethodPost, "/api/admin/workbench/plan-periods/1/confirm-payment"},
		{http.MethodPost, "/api/admin/workbench/retention-runs/run-1/execute"},
		{http.MethodPost, "/api/admin/workbench/approvals/apr-1/approve"},
		{http.MethodPost, "/api/admin/workbench/customers"},
		{http.MethodPost, "/api/admin/workbench/customers/1/members"},
		{http.MethodPost, "/api/admin/workbench/plan-catalog"},
		{http.MethodPost, "/api/admin/workbench/customers/1/plan-periods"},
		{http.MethodPost, "/api/admin/workbench/usage-audits"},
		{http.MethodPost, "/api/admin/workbench/evidence"},
		{http.MethodPost, "/api/admin/workbench/tencent-billing-imports"},
		{http.MethodPut, "/api/admin/workbench/customers/1/retention-policy"},
		{http.MethodPost, "/api/admin/workbench/customers/1/retention-runs"},
	} {
		request := httptest.NewRequest(testCase.method, testCase.path, bytes.NewReader([]byte(`{}`)))
		authorizeAdminRequest(request, sessionCookie, csrfToken)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		assert.Equal(t, http.StatusForbidden, response.Code, testCase.path)
		assert.Contains(t, response.Body.String(), "recent administrator authentication is required", testCase.path)
	}
	readRequest := httptest.NewRequest(http.MethodGet, "/api/admin/workbench/dashboard", nil)
	readRequest.AddCookie(sessionCookie)
	readResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(readResponse, readRequest)
	assert.Equal(t, http.StatusOK, readResponse.Code, "recent-auth expiry must not block read-only admin projections")

	bootstrap := httptest.NewRequest(http.MethodPost, "/api/admin/workbench/credential-profiles", bytes.NewReader([]byte(`{}`)))
	bootstrap.Header.Set("Authorization", "Bearer emergency-admin-token")
	bootstrapResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(bootstrapResponse, bootstrap)
	assert.Equal(t, http.StatusForbidden, bootstrapResponse.Code)
	assert.Contains(t, bootstrapResponse.Body.String(), "restricted to loopback maintenance")
}

func TestAdminListCursorIsStableScopedAndPreservesArrayDataShape(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	for index := 1; index <= 4; index++ {
		require.NoError(t, db.Create(&model.Customer{
			CustomerCode: fmt.Sprintf("cursor-%d", index), DisplayName: fmt.Sprintf("Customer %d", index),
			Status: model.CustomerStatusActive, RowVersion: 1,
		}).Error)
	}
	accessService, sessionCookie, _ := issueAdminSession(t, db, secrets.EnvironmentResolver{}, 107)
	server := httpapi.New(httpapi.Services{
		DB: db, Access: accessService, Customers: customer.New(db, "prod", testutil.NewIdentityVerifier("v1.test")),
	}, "emergency-admin-token", httpapi.InternalAuth{}, httpapi.PublicConfig{})

	request := httptest.NewRequest(http.MethodGet, "/api/admin/workbench/customers?limit=2", nil)
	request.AddCookie(sessionCookie)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var first struct {
		Success bool             `json:"success"`
		Data    []model.Customer `json:"data"`
		Meta    struct {
			NextCursor string `json:"next_cursor"`
		} `json:"meta"`
	}
	require.NoError(t, jsonx.Unmarshal(response.Body.Bytes(), &first))
	require.True(t, first.Success)
	require.Len(t, first.Data, 2)
	assert.Greater(t, first.Data[0].ID, first.Data[1].ID)
	require.NotEmpty(t, first.Meta.NextCursor)

	newest := model.Customer{CustomerCode: "cursor-newest", DisplayName: "Newest", Status: model.CustomerStatusActive, RowVersion: 1}
	require.NoError(t, db.Create(&newest).Error)
	nextRequest := httptest.NewRequest(http.MethodGet, "/api/admin/workbench/customers?limit=2&cursor="+url.QueryEscape(first.Meta.NextCursor), nil)
	nextRequest.AddCookie(sessionCookie)
	nextResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(nextResponse, nextRequest)
	require.Equal(t, http.StatusOK, nextResponse.Code, nextResponse.Body.String())
	var second struct {
		Data []model.Customer `json:"data"`
	}
	require.NoError(t, jsonx.Unmarshal(nextResponse.Body.Bytes(), &second))
	require.Len(t, second.Data, 2)
	assert.Less(t, second.Data[0].ID, first.Data[1].ID, "rows inserted after page one must not shift the keyset window")
	assert.NotEqual(t, newest.ID, second.Data[0].ID)

	wrongScope := httptest.NewRequest(http.MethodGet, "/api/admin/workbench/credential-profiles?cursor="+url.QueryEscape(first.Meta.NextCursor), nil)
	wrongScope.AddCookie(sessionCookie)
	wrongScopeResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(wrongScopeResponse, wrongScope)
	assert.Equal(t, http.StatusBadRequest, wrongScopeResponse.Code)
	assert.Contains(t, wrongScopeResponse.Body.String(), "invalid for this list")

	ambiguous := httptest.NewRequest(http.MethodGet, "/api/admin/workbench/customers?before_id=2&cursor="+url.QueryEscape(first.Meta.NextCursor), nil)
	ambiguous.AddCookie(sessionCookie)
	ambiguousResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(ambiguousResponse, ambiguous)
	assert.Equal(t, http.StatusBadRequest, ambiguousResponse.Code)
	assert.Contains(t, ambiguousResponse.Body.String(), "cannot be combined")

	unknownFieldCursor := base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"scope":"customers","before_id":2,"extra":true}`))
	unknownField := httptest.NewRequest(http.MethodGet, "/api/admin/workbench/customers?cursor="+url.QueryEscape(unknownFieldCursor), nil)
	unknownField.AddCookie(sessionCookie)
	unknownFieldResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(unknownFieldResponse, unknownField)
	assert.Equal(t, http.StatusBadRequest, unknownFieldResponse.Code)
	assert.Contains(t, unknownFieldResponse.Body.String(), "invalid for this list")
}

func issueAdminSession(t *testing.T, db *gorm.DB, resolver secrets.Resolver, userID int64) (*access.Service, *http.Cookie, string) {
	t.Helper()
	accessService := access.New(db, resolver, acceptingVerifier{}, time.Minute, time.Minute, time.Hour, time.Hour, time.Minute)
	nonce := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", t.Name(), userID)))
	entry, err := accessService.IssueEntryTicket(access.IssueEntryTicketCommand{
		NewAPIUserID: userID, IdentityVersion: fmt.Sprintf("v1.admin.%d", userID), Surface: "admin", IsSuperAdmin: true,
		AuthenticatedAt: time.Now().UTC(), AMR: []string{"otp"}, ReauthNonce: base64.RawURLEncoding.EncodeToString(nonce[:]),
	})
	require.NoError(t, err)
	entered, err := accessService.Enter(context.Background(), access.EnterCommand{Ticket: entry.Ticket})
	require.NoError(t, err)
	return accessService, &http.Cookie{Name: "claw_admin_session", Value: entered.AdminSessionToken}, entered.AdminCSRFToken
}

func authorizeAdminRequest(request *http.Request, sessionCookie *http.Cookie, csrfToken string) {
	request.AddCookie(sessionCookie)
	request.Header.Set("X-CSRF-Token", csrfToken)
}

func assertSignedResponse(t *testing.T, response *httptest.ResponseRecorder, path, requestNonce, secret string) {
	t.Helper()
	assert.Equal(t, testContractVersion, response.Header().Get(testContractVersionHeader))
	responseTimestamp := response.Header().Get("X-Workbench-Response-Timestamp")
	require.NotEmpty(t, responseTimestamp)
	parsedTimestamp, err := strconv.ParseInt(responseTimestamp, 10, 64)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().UTC(), time.Unix(parsedTimestamp, 0).UTC(), time.Minute)
	assert.Equal(t, requestNonce, response.Header().Get("X-Workbench-Response-Nonce"))
	bodyHash := sha256.Sum256(response.Body.Bytes())
	canonical := strings.Join([]string{
		testContractVersion, strconv.Itoa(response.Code), path, responseTimestamp, requestNonce, hex.EncodeToString(bodyHash[:]),
	}, "\n")
	mac := hmac.New(sha256.New, []byte(secret))
	_, err = mac.Write([]byte(canonical))
	require.NoError(t, err)
	expected := hex.EncodeToString(mac.Sum(nil))
	assert.Equal(t, expected, response.Header().Get("X-Workbench-Response-Signature"))

	tamperedBodyHash := sha256.Sum256(append(response.Body.Bytes(), byte('x')))
	tamperedCanonical := strings.Join([]string{
		testContractVersion, strconv.Itoa(response.Code), path, responseTimestamp, requestNonce, hex.EncodeToString(tamperedBodyHash[:]),
	}, "\n")
	tamperedMAC := hmac.New(sha256.New, []byte(secret))
	_, err = tamperedMAC.Write([]byte(tamperedCanonical))
	require.NoError(t, err)
	assert.NotEqual(t, expected, hex.EncodeToString(tamperedMAC.Sum(nil)))

	for _, tamperedCanonical := range []string{
		strings.Join([]string{testContractVersion, strconv.Itoa(response.Code + 1), path, responseTimestamp, requestNonce, hex.EncodeToString(bodyHash[:])}, "\n"),
		strings.Join([]string{testContractVersion, strconv.Itoa(response.Code), path, responseTimestamp, requestNonce + "-other", hex.EncodeToString(bodyHash[:])}, "\n"),
	} {
		otherMAC := hmac.New(sha256.New, []byte(secret))
		_, err = otherMAC.Write([]byte(tamperedCanonical))
		require.NoError(t, err)
		assert.NotEqual(t, expected, hex.EncodeToString(otherMAC.Sum(nil)))
	}
}

func signedRequest(t *testing.T, path string, body []byte, timestamp, nonce, secret string) *http.Request {
	t.Helper()
	bodyHash := sha256.Sum256(body)
	canonical := strings.Join([]string{testContractVersion, http.MethodPost, path, timestamp, nonce, hex.EncodeToString(bodyHash[:])}, "\n")
	mac := hmac.New(sha256.New, []byte(secret))
	_, err := mac.Write([]byte(canonical))
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.Header.Set(testContractVersionHeader, testContractVersion)
	request.Header.Set("X-Workbench-Service", "adp-backend")
	request.Header.Set("X-Workbench-Timestamp", timestamp)
	request.Header.Set("X-Workbench-Nonce", nonce)
	request.Header.Set("X-Workbench-Signature", hex.EncodeToString(mac.Sum(nil)))
	return request
}
