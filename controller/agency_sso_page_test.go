package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAgencySSOPageOriginAllowlistAndPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/agency/sso", AgencySSOPage)

	// Disabled when AGENCY_SSO_ALLOWED_ORIGIN is empty.
	t.Setenv("AGENCY_SSO_ALLOWED_ORIGIN", "")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agency/sso?state_hash=aa&origin=http://127.0.0.1:3201", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	t.Setenv("AGENCY_SSO_ALLOWED_ORIGIN", "http://127.0.0.1:3201")

	// Missing state_hash is rejected.
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agency/sso?origin=http://127.0.0.1:3201", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// A non-allowlisted target origin is refused.
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agency/sso?state_hash=aa&origin=http://evil.example", nil))
	require.Equal(t, http.StatusForbidden, rec.Code)

	// Allowlisted origin gets the bridge page that actively POSTs the ticket.
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agency/sso?state_hash=aa&origin=http://127.0.0.1:3201", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, `"state_hash":"aa"`)
	require.Contains(t, body, `"origin":"http://127.0.0.1:3201"`)
	require.Contains(t, body, "/api/agency/sso-ticket")
	require.Contains(t, body, "new-api-agency-sso")
	require.Contains(t, body, "new_api_access_token")
	require.Contains(t, body, "'Authorization':'Bearer '+token")
	require.Equal(t, "", rec.Header().Get("X-Frame-Options"))
	require.Contains(t, rec.Header().Get("Content-Security-Policy"), "frame-ancestors http://127.0.0.1:3201")
}
