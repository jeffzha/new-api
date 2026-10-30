package router

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service/uac"
	"github.com/gin-gonic/gin"
)

func registerUACRoutes(router *gin.Engine) {
	if !uac.Enabled() {
		return
	}
	// UAC verifies credentials upstream. Bound each protocol step separately so
	// login retries cannot exhaust the callback or logout allowance.
	group := router.Group("/api/oauth/uac", middleware.DisableCache())
	group.GET("/start", middleware.AuthSessionRateLimit("uac-start"), controller.UACStart)
	group.GET("/callback", middleware.AuthSessionRateLimit("uac-callback"), controller.UACCallback)
	group.POST("/complete", middleware.SessionCookieOriginGuard(), middleware.AuthSessionRateLimit("uac-complete"), controller.UACComplete)
	group.GET("/logout/callback", middleware.AuthSessionRateLimit("uac-logout-callback"), controller.UACLogoutCallback)
}

// ProjectHTTPHandler strips only this instance's registered prefix before Gin
// matches routes. No product route or database option is rewritten.
func ProjectHTTPHandler(next http.Handler) http.Handler {
	if !uac.Enabled() {
		return next
	}
	prefix := uac.Prefix()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if prefix != "" {
			if r.URL.Path == prefix {
				http.Redirect(w, r, prefix+"/", http.StatusTemporaryRedirect)
				return
			}
			if !strings.HasPrefix(r.URL.Path, prefix+"/") {
				http.NotFound(w, r)
				return
			}
			r = r.Clone(r.Context())
			copied := *r.URL
			r.URL = &copied
			r.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
			r.URL.RawPath = ""
			r.RequestURI = r.URL.RequestURI()
		}
		if uac.Required() {
			path := r.URL.Path
			blockedLogin := strings.HasPrefix(path, "/api/user/login") && path != "/api/user/login/verify" && !strings.HasPrefix(path, "/api/user/login/passkey/")
			if strings.HasPrefix(path, "/api/oauth/") && !strings.HasPrefix(path, "/api/oauth/uac/") || path == "/api/user/register" || blockedLogin || strings.HasPrefix(path, "/api/user/passkey/login") || strings.HasPrefix(path, "/api/user/reset") || path == "/api/reset_password" || path == "/api/setup" && r.Method != http.MethodGet {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"success":false,"code":"UAC_LOGIN_REQUIRED","message":"请通过统一认证登录本平台。"}`))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
