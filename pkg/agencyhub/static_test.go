package agencyhub

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIndexEmbedsPlatformBaseURLAndSSOWiring(t *testing.T) {
	app := newAgencyTestApp(t)
	app.config.PlatformBaseURL = "http://127.0.0.1:3000"
	recorder := httptest.NewRecorder()
	app.Router().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/agency", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()
	require.Contains(t, body, `"platform_base_url":"http://127.0.0.1:3000"`)
	require.Contains(t, body, "/sso/start")
	require.Contains(t, body, "/sso/callback")
	require.Contains(t, body, "new-api-agency-sso")

	app2 := newAgencyTestApp(t)
	app2.config.PlatformBaseURL = ""
	recorder2 := httptest.NewRecorder()
	app2.Router().ServeHTTP(recorder2, httptest.NewRequest(http.MethodGet, "/agency/", nil))
	require.Equal(t, http.StatusOK, recorder2.Code)
	require.Contains(t, recorder2.Body.String(), `"platform_base_url":""`)
}
