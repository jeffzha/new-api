package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSSOBrowserBindingCookieIsStrictAndRouteScoped(t *testing.T) {
	expiresAt := time.Now().UTC().Add(time.Minute)
	response := httptest.NewRecorder()

	setSSOBrowserBindingCookie(response, "browser-binding", expiresAt, 60)

	cookies := response.Result().Cookies()
	require.Len(t, cookies, 1)
	cookie := cookies[0]
	assert.Equal(t, "claw_sso_binding", cookie.Name)
	assert.Equal(t, "browser-binding", cookie.Value)
	assert.Equal(t, "/workbench/auth/sso", cookie.Path)
	assert.Equal(t, 60, cookie.MaxAge)
	assert.True(t, cookie.HttpOnly)
	assert.True(t, cookie.Secure)
	assert.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
}
