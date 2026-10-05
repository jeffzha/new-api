package controller

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestModelMockSSOPageRequiresAllowlistedOriginAndValidState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/model-mock/sso", ModelMockSSOPage)
	stateHash := strings.Repeat("a", 64)

	t.Setenv("MODEL_MOCK_SSO_ALLOWED_ORIGIN", "")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/model-mock/sso?state_hash="+stateHash+"&origin=https://model-mock.example", nil))
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)

	t.Setenv("MODEL_MOCK_SSO_ALLOWED_ORIGIN", "https://model-mock.example,https://mocktest.example")
	for _, test := range []struct {
		name, target string
		status       int
	}{
		{name: "invalid state", target: "?state_hash=bad&origin=https://model-mock.example", status: http.StatusBadRequest},
		{name: "untrusted origin", target: "?state_hash=" + stateHash + "&origin=https://evil.example", status: http.StatusForbidden},
		{name: "invalid mode", target: "?state_hash=" + stateHash + "&origin=https://model-mock.example&mode=popup", status: http.StatusBadRequest},
		{name: "allowlisted origin", target: "?state_hash=" + stateHash + "&origin=https://model-mock.example", status: http.StatusOK},
		{name: "redirect mode", target: "?state_hash=" + stateHash + "&origin=https://model-mock.example&mode=redirect", status: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/model-mock/sso"+test.target, nil))
			require.Equal(t, test.status, recorder.Code)
			if test.status == http.StatusOK {
				require.Contains(t, recorder.Body.String(), "/api/model-mock/sso-ticket")
				require.Contains(t, recorder.Body.String(), "new-api-model-mock-sso")
				require.NotContains(t, recorder.Body.String(), "localStorage")
				if strings.Contains(test.target, "mode=redirect") {
					require.Contains(t, recorder.Body.String(), "location.replace")
					require.Contains(t, recorder.Body.String(), "model_mock_ticket")
				}
				contentSecurityPolicy := recorder.Header().Get("Content-Security-Policy")
				require.Contains(t, contentSecurityPolicy, "frame-ancestors")
				require.Contains(t, contentSecurityPolicy, "https://model-mock.example")
				require.Contains(t, contentSecurityPolicy, "https://mocktest.example")
			}
		})
	}

	t.Setenv("MODEL_MOCK_SSO_RETURN_PATH", "/model-mock/")
	for _, test := range []struct {
		path   string
		status int
	}{
		{path: "/model-mock/", status: http.StatusOK},
		{path: "/", status: http.StatusForbidden},
		{path: "//evil.example/", status: http.StatusForbidden},
	} {
		recorder := httptest.NewRecorder()
		target := "/api/model-mock/sso?state_hash=" + stateHash + "&origin=https://model-mock.example&return_path=" + url.QueryEscape(test.path) + "&mode=redirect"
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		require.Equal(t, test.status, recorder.Code)
	}
}

func TestAllowedModelMockRolesDefaultsAndOverrides(t *testing.T) {
	t.Setenv("MODEL_MOCK_SSO_ALLOWED_ROLES", "")
	require.True(t, modelMockRoleAllowed(10))
	require.True(t, modelMockRoleAllowed(100))
	require.False(t, modelMockRoleAllowed(1))

	t.Setenv("MODEL_MOCK_SSO_ALLOWED_ROLES", "20, invalid, 30")
	require.False(t, modelMockRoleAllowed(10))
	require.True(t, modelMockRoleAllowed(20))
	require.True(t, modelMockRoleAllowed(30))
}

func TestModelMockSessionStatusRejectsUnsignedAndInvalidRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const secret = "model-mock-test-secret"
	t.Setenv("MODEL_MOCK_INTERNAL_SECRET", secret)
	router := gin.New()
	router.POST("/api/internal/model-mock/session-status", GetModelMockSessionStatus)
	body := "{\"user_id\":42,\"source_sid\":\"sid\",\"user_auth_version\":1,\"session_version\":1}"

	tests := []struct {
		name, timestamp, signature string
		status                     int
	}{
		{name: "missing signature", timestamp: strconv.FormatInt(time.Now().Unix(), 10), status: http.StatusUnauthorized},
		{name: "expired timestamp", timestamp: strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10), signature: "invalid", status: http.StatusUnauthorized},
		{name: "invalid signature", timestamp: strconv.FormatInt(time.Now().Unix(), 10), signature: "invalid", status: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/internal/model-mock/session-status", strings.NewReader(body))
			request.Header.Set(modelMockInternalTimestampHeader, test.timestamp)
			request.Header.Set(modelMockInternalSignatureHeader, test.signature)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			require.Equal(t, test.status, recorder.Code)
		})
	}

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	request := httptest.NewRequest(http.MethodPost, "/api/internal/model-mock/session-status", strings.NewReader("{}"))
	request.Header.Set(modelMockInternalTimestampHeader, timestamp)
	request.Header.Set(modelMockInternalSignatureHeader, modelMockInternalMAC(secret, timestamp+"\n{}"))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Empty(t, recorder.Header().Get(modelMockInternalResponseSignatureHeader))
}
