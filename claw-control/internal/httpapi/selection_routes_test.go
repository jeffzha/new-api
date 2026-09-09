package httpapi_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/access"
	"github.com/QuantumNous/new-api/claw-control/internal/httpapi"
	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/QuantumNous/new-api/claw-control/internal/productpolicy"
	"github.com/QuantumNous/new-api/claw-control/internal/secrets"
	"github.com/QuantumNous/new-api/claw-control/internal/support"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestWorkbenchSelectionBrowserFlowRedirectsThroughTheSafeSelectorPage(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	const identityVersion = "v1.http-selector"
	accessService := access.New(
		db, secrets.EnvironmentResolver{}, acceptingVerifier{},
		time.Minute, time.Minute, time.Hour, time.Hour, time.Minute,
	)
	seedHTTPSelectableContext(t, db, 8101, identityVersion, "customer-one", "app-one")
	seedHTTPSelectableContext(t, db, 8101, identityVersion, "customer-two", "app-two")
	entryTicket, err := accessService.IssueEntryTicket(access.IssueEntryTicketCommand{
		NewAPIUserID: 8101, IdentityVersion: identityVersion,
	})
	require.NoError(t, err)
	server := httpapi.New(httpapi.Services{DB: db, Access: accessService}, "emergency-admin-token", httpapi.InternalAuth{}, httpapi.PublicConfig{
		ADPSSORedirectPath: "/workbench/auth/sso", AdminRedirectPath: "/workbench/admin",
	})

	entryRequest := httptest.NewRequest(http.MethodGet, "/api/workbench/entry?ticket="+url.QueryEscape(entryTicket.Ticket), nil)
	entryResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(entryResponse, entryRequest)

	require.Equal(t, http.StatusSeeOther, entryResponse.Code)
	assert.Equal(t, "/playground/select", entryResponse.Header().Get("Location"))
	assert.Equal(t, "no-store", entryResponse.Header().Get("Cache-Control"))
	assert.Equal(t, "no-referrer", entryResponse.Header().Get("Referrer-Policy"))
	assert.NotContains(t, entryResponse.Body.String(), "selection_token")
	controlCookie := cookieByName(entryResponse.Result().Cookies(), "claw_control_session")
	require.NotNil(t, controlCookie)
	assert.Equal(t, "/", controlCookie.Path)
	assert.True(t, controlCookie.HttpOnly)
	assert.True(t, controlCookie.Secure)
	assert.Equal(t, http.SameSiteStrictMode, controlCookie.SameSite)

	listRequest := httptest.NewRequest(http.MethodGet, "/api/workbench/selections", nil)
	listRequest.AddCookie(controlCookie)
	listResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(listResponse, listRequest)
	require.Equal(t, http.StatusOK, listResponse.Code)
	var listed struct {
		Success bool                     `json:"success"`
		Data    []access.SelectionOption `json:"data"`
	}
	require.NoError(t, jsonx.Unmarshal(listResponse.Body.Bytes(), &listed))
	assert.True(t, listed.Success)
	require.Len(t, listed.Data, 2)
	assert.NotEmpty(t, listed.Data[0].SelectionToken)
	for _, forbidden := range []string{"customer_id", "application_id", "space_id", "app_profile_id", "config_version", "secret"} {
		assert.NotContains(t, listResponse.Body.String(), forbidden)
	}

	chooseBody, err := jsonx.Marshal(map[string]string{"selection_token": listed.Data[0].SelectionToken})
	require.NoError(t, err)
	chooseRequest := httptest.NewRequest(http.MethodPost, "/api/workbench/selections/choose", bytes.NewReader(chooseBody))
	chooseRequest.AddCookie(controlCookie)
	chooseResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(chooseResponse, chooseRequest)
	require.Equal(t, http.StatusOK, chooseResponse.Code)
	var selected struct {
		Success bool `json:"success"`
		Data    struct {
			RedirectURL string `json:"redirect_url"`
		} `json:"data"`
	}
	require.NoError(t, jsonx.Unmarshal(chooseResponse.Body.Bytes(), &selected))
	assert.True(t, selected.Success)
	redirect, err := url.Parse(selected.Data.RedirectURL)
	require.NoError(t, err)
	assert.Empty(t, redirect.Scheme)
	assert.Empty(t, redirect.Host)
	assert.Equal(t, "/workbench/auth/sso", redirect.Path)
	assert.NotEmpty(t, redirect.Query().Get("ticket"))
	assert.Len(t, redirect.Query(), 1)
	browserBinding := cookieByName(chooseResponse.Result().Cookies(), "claw_sso_binding")
	require.NotNil(t, browserBinding)
	assert.Equal(t, "/workbench/auth/sso", browserBinding.Path)
	assert.True(t, browserBinding.HttpOnly)
	assert.True(t, browserBinding.Secure)
	assert.Equal(t, http.SameSiteStrictMode, browserBinding.SameSite)
}

func TestWorkbenchSelectionBrowserFlowDoesNotSilentlyChooseAmongOneCustomersApps(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	const identityVersion = "v1.http-multi-app"
	accessService := access.New(
		db, secrets.EnvironmentResolver{}, acceptingVerifier{},
		time.Minute, time.Minute, time.Hour, time.Hour, time.Minute,
	)
	customerID := seedHTTPSelectableContext(t, db, 8102, identityVersion, "one-customer", "default-app")
	seedHTTPAdditionalSelectableApp(t, db, customerID, "second-app")
	entryTicket, err := accessService.IssueEntryTicket(access.IssueEntryTicketCommand{
		NewAPIUserID: 8102, IdentityVersion: identityVersion,
	})
	require.NoError(t, err)
	server := httpapi.New(httpapi.Services{DB: db, Access: accessService}, "emergency-admin-token", httpapi.InternalAuth{}, httpapi.PublicConfig{
		ADPSSORedirectPath: "/workbench/auth/sso", AdminRedirectPath: "/workbench/admin",
	})

	entryRequest := httptest.NewRequest(http.MethodGet, "/api/workbench/entry?ticket="+url.QueryEscape(entryTicket.Ticket), nil)
	entryResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(entryResponse, entryRequest)
	require.Equal(t, http.StatusSeeOther, entryResponse.Code)
	assert.Equal(t, "/playground/select", entryResponse.Header().Get("Location"))
	controlCookie := cookieByName(entryResponse.Result().Cookies(), "claw_control_session")
	require.NotNil(t, controlCookie)

	listRequest := httptest.NewRequest(http.MethodGet, "/api/workbench/selections", nil)
	listRequest.AddCookie(controlCookie)
	listResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(listResponse, listRequest)
	require.Equal(t, http.StatusOK, listResponse.Code)
	var listed struct {
		Data []access.SelectionOption `json:"data"`
	}
	require.NoError(t, jsonx.Unmarshal(listResponse.Body.Bytes(), &listed))
	require.Len(t, listed.Data, 2)
	assert.NotEqual(t, listed.Data[0].AppSelector, listed.Data[1].AppSelector)
}

func TestWorkbenchSSOPreflightAllowsOnlyTheProvisioningBootstrapBoundary(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	const identityVersion = "v1.http-sso-provisioning"
	accessService := access.New(
		db, secrets.EnvironmentResolver{}, acceptingVerifier{},
		time.Minute, time.Minute, time.Hour, time.Hour, time.Minute,
	)
	seedHTTPSelectableContext(t, db, 8103, identityVersion, "provisioning-customer", "provisioning-app")
	require.NoError(t, db.Model(&model.IdentityBinding{}).
		Where("new_api_user_id = ?", 8103).
		Update("status", model.IdentityStatusProvisioning).Error)
	entryTicket, err := accessService.IssueEntryTicket(access.IssueEntryTicketCommand{
		NewAPIUserID: 8103, IdentityVersion: identityVersion,
	})
	require.NoError(t, err)
	server := httpapi.New(httpapi.Services{DB: db, Access: accessService}, "emergency-admin-token", httpapi.InternalAuth{}, httpapi.PublicConfig{
		ADPSSORedirectPath: "/workbench/auth/sso", AdminRedirectPath: "/workbench/admin",
	})

	entryRequest := httptest.NewRequest(http.MethodGet, "/api/workbench/entry?ticket="+url.QueryEscape(entryTicket.Ticket), nil)
	entryResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(entryResponse, entryRequest)
	require.Equal(t, http.StatusFound, entryResponse.Code)
	controlCookie := cookieByName(entryResponse.Result().Cookies(), "claw_control_session")
	require.NotNil(t, controlCookie)

	preflightRequest := httptest.NewRequest(http.MethodGet, "/api/workbench/sso-preflight", nil)
	preflightRequest.AddCookie(controlCookie)
	preflightResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(preflightResponse, preflightRequest)
	assert.Equal(t, http.StatusNoContent, preflightResponse.Code)
	assert.Equal(t, "no-store", preflightResponse.Header().Get("Cache-Control"))

	configRequest := httptest.NewRequest(http.MethodGet, "/api/workbench/config", nil)
	configRequest.AddCookie(controlCookie)
	configResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(configResponse, configRequest)
	assert.Equal(t, http.StatusForbidden, configResponse.Code, "ordinary browser reads must remain closed until ADP confirms the identity")

	missingCookieRequest := httptest.NewRequest(http.MethodGet, "/api/workbench/sso-preflight", nil)
	missingCookieResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(missingCookieResponse, missingCookieRequest)
	assert.Equal(t, http.StatusForbidden, missingCookieResponse.Code)
}

func seedHTTPSelectableContext(t *testing.T, db *gorm.DB, userID int64, identityVersion, customerCode, appID string) uint64 {
	t.Helper()
	now := time.Now().UTC()
	customer := model.Customer{
		CustomerCode: customerCode, DisplayName: customerCode,
		Status: model.CustomerStatusActive, RowVersion: 1,
	}
	require.NoError(t, db.Create(&customer).Error)
	require.NoError(t, db.Create(&model.CustomerMember{
		CustomerID: customer.ID, NewAPIUserID: userID, Role: "member",
		Status: model.MemberStatusActive, MembershipSlot: model.MembershipSlot(customer.ID), AuthEpoch: 1,
	}).Error)
	require.NoError(t, db.Create(&model.IdentityBinding{
		PublicID: support.PublicID("wid"), CustomerID: customer.ID, NewAPIUserID: userID,
		CanonicalSubject: fmt.Sprintf("napi:test:customer:%d:user:%d", customer.ID, userID),
		IdentityVersion:  identityVersion, Status: model.IdentityStatusActive, AuthEpoch: 1, RowVersion: 1,
	}).Error)
	application := model.CustomerApp{
		CustomerID: customer.ID, Slot: "primary", ProviderEnvironment: model.ProviderChinaTencentADP,
		AppID: appID, DisplayName: appID, Status: model.AppStatusActive, AuthEpoch: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&application).Error)
	limits := productpolicy.Limits{
		CustomerConcurrency: 2, UserConcurrency: 1, MaxRuntimeSeconds: 600,
		MaxReasoningRounds: 20, MaxOutputTokens: 4096, WebSearchPerTurn: 1, MaxFileBytes: 1024,
	}
	limitsJSON, err := jsonx.Marshal(limits)
	require.NoError(t, err)
	capabilitiesJSON, err := jsonx.Marshal([]string{"chat"})
	require.NoError(t, err)
	configuration := model.AppConfigVersion{
		CustomerAppID: application.ID, ConfigVersion: 1, Status: model.AppConfigStatusVerified,
		Region: "ap-guangzhou", SpaceID: "space-" + appID, TemplateAgentID: "agent-" + appID,
		AppKeySecretRef: "env://WORKBENCH_PROVIDER_SELECTION_APP_KEY", AppKeyFingerprint: "sha256:test",
		LimitsJSON: string(limitsJSON), CapabilitiesJSON: string(capabilitiesJSON), CreatedBy: "test", VerifiedAt: &now,
	}
	require.NoError(t, db.Create(&configuration).Error)
	require.NoError(t, db.Model(&application).Update("current_config_version_id", configuration.ID).Error)
	snapshotJSON, err := jsonx.Marshal(map[string]any{"capabilities": []string{"chat"}, "limits": limits})
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.PlanPeriod{
		CustomerID: customer.ID, PlanVersionID: 1,
		StartAt: now.Add(-time.Minute), EndAt: now.Add(time.Hour), AmountCNY: "1.00",
		PaymentMode: model.PaymentModeOfflineManual, PaymentStatus: model.PaymentStatusPaid,
		Status: model.PeriodStatusActive, SnapshotJSON: string(snapshotJSON), RowVersion: 1,
	}).Error)
	return customer.ID
}

func seedHTTPAdditionalSelectableApp(t *testing.T, db *gorm.DB, customerID uint64, appID string) {
	t.Helper()
	now := time.Now().UTC()
	application := model.CustomerApp{
		CustomerID: customerID, Slot: "app:" + support.PublicID("aps"), Alias: "second",
		ProviderEnvironment: model.ProviderChinaTencentADP, AppID: appID, DisplayName: appID,
		Status: model.AppStatusActive, AuthEpoch: 1, RowVersion: 1,
	}
	require.NoError(t, db.Create(&application).Error)
	limits := productpolicy.Limits{
		CustomerConcurrency: 2, UserConcurrency: 1, MaxRuntimeSeconds: 600,
		MaxReasoningRounds: 20, MaxOutputTokens: 4096, WebSearchPerTurn: 1, MaxFileBytes: 1024,
	}
	limitsJSON, err := jsonx.Marshal(limits)
	require.NoError(t, err)
	capabilitiesJSON, err := jsonx.Marshal([]string{"chat"})
	require.NoError(t, err)
	configuration := model.AppConfigVersion{
		CustomerAppID: application.ID, ConfigVersion: 1, Status: model.AppConfigStatusVerified,
		Region: "ap-guangzhou", SpaceID: "space-" + appID, TemplateAgentID: "agent-" + appID,
		AppKeySecretRef: "env://WORKBENCH_PROVIDER_SELECTION_APP_KEY", AppKeyFingerprint: "sha256:test",
		LimitsJSON: string(limitsJSON), CapabilitiesJSON: string(capabilitiesJSON), CreatedBy: "test", VerifiedAt: &now,
	}
	require.NoError(t, db.Create(&configuration).Error)
	require.NoError(t, db.Model(&application).Update("current_config_version_id", configuration.ID).Error)
}

func cookieByName(cookies []*http.Cookie, name string) *http.Cookie {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}
