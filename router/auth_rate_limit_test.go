package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

var authRateLimitFixtureSequence atomic.Int64

func TestPasswordResetRouteLimitsUnknownEmailWithoutGeneralLimits(t *testing.T) {
	f := newAuthRateLimitFixture(t, true)
	require.NoError(t, i18n.Init())
	common.CriticalRateLimitEnable = false
	for i := range 2 {
		r := httptest.NewRequest(http.MethodGet, "/api/reset_password?email=unregistered%40example.com&turnstile=", nil)
		r.RemoteAddr = f.remoteAddr
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, r)
		if i == 0 {
			assert.Equal(t, http.StatusOK, w.Code)
			assert.Contains(t, w.Body.String(), `"success":true`)
		} else {
			assert.Equal(t, http.StatusTooManyRequests, w.Code)
			assert.Equal(t, "60", w.Header().Get("Retry-After"))
		}
		assert.Contains(t, w.Header().Get("Cache-Control"), "no-store")
	}
}

type authRateLimitFixture struct {
	router     *gin.Engine
	db         *gorm.DB
	remoteAddr string
	nextUserID int
}

func newAuthRateLimitFixture(t *testing.T, useRedis bool) *authRateLimitFixture {
	t.Helper()
	previousDB := model.DB
	previousDatabaseType := common.MainDatabaseType()
	previousRedisEnabled, previousRDB := common.RedisEnabled, common.RDB
	previousGlobalAPILimit := common.GlobalApiRateLimitEnable
	previousCriticalEnabled := common.CriticalRateLimitEnable
	previousCriticalNum, previousCriticalDuration := common.CriticalRateLimitNum, common.CriticalRateLimitDuration
	previousSessionNum, previousSessionDuration := common.AuthSessionRateLimitNum, common.AuthSessionRateLimitDuration
	previousPasswordLogin, previousTurnstile := common.PasswordLoginEnabled, common.TurnstileCheckEnabled
	previousCookieSecure, previousCookieURLs := common.SessionCookieSecure, common.SessionCookieTrustedURLs
	previousSessionSecret := common.SessionSecret
	previousActiveLimit, previousIssuanceLimit := common.UserSessionActiveLimit, common.UserSessionIssuanceLimit
	previousIssuanceWindow := common.UserSessionIssuanceWindowSeconds
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousDatabaseType)
		common.RedisEnabled, common.RDB = previousRedisEnabled, previousRDB
		common.GlobalApiRateLimitEnable = previousGlobalAPILimit
		common.CriticalRateLimitEnable = previousCriticalEnabled
		common.CriticalRateLimitNum, common.CriticalRateLimitDuration = previousCriticalNum, previousCriticalDuration
		common.AuthSessionRateLimitNum, common.AuthSessionRateLimitDuration = previousSessionNum, previousSessionDuration
		common.PasswordLoginEnabled, common.TurnstileCheckEnabled = previousPasswordLogin, previousTurnstile
		common.SessionCookieSecure, common.SessionCookieTrustedURLs = previousCookieSecure, previousCookieURLs
		common.SessionSecret = previousSessionSecret
		common.UserSessionActiveLimit, common.UserSessionIssuanceLimit = previousActiveLimit, previousIssuanceLimit
		common.UserSessionIssuanceWindowSeconds = previousIssuanceWindow
	})
	common.RedisEnabled = useRedis
	common.RDB = nil
	if useRedis {
		redisServer := miniredis.RunT(t)
		client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
		common.RDB = client
		t.Cleanup(func() { require.NoError(t, client.Close()) })
	}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}))
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.GlobalApiRateLimitEnable = false
	common.CriticalRateLimitEnable = true
	common.CriticalRateLimitNum, common.CriticalRateLimitDuration = 2, 1200
	common.AuthSessionRateLimitNum, common.AuthSessionRateLimitDuration = 3, 60
	common.PasswordLoginEnabled, common.TurnstileCheckEnabled = true, false
	common.SessionCookieSecure, common.SessionCookieTrustedURLs = true, nil
	common.SessionSecret = "auth-rate-limit-route-test-secret"
	common.UserSessionActiveLimit, common.UserSessionIssuanceLimit = 5, 10
	common.UserSessionIssuanceWindowSeconds = 3600

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	require.NoError(t, engine.SetTrustedProxies(nil))
	engine.Use(func(c *gin.Context) {
		// Audit persistence is outside this routing contract; avoid asynchronous
		// writes outliving the isolated authentication database.
		common.SetContextKey(c, constant.ContextKeyAuditLogged, true)
	})
	SetApiRouter(engine)
	// Unique fixture identities keep the process-wide memory limiter isolated
	// even when the tests run repeatedly in the same process with -count.
	sequence := authRateLimitFixtureSequence.Add(1)
	return &authRateLimitFixture{
		router: engine, db: db,
		remoteAddr: fmt.Sprintf("198.18.%d.%d:12345", sequence/250, sequence%250+1),
		nextUserID: int(sequence) * 10,
	}
}

func (f *authRateLimitFixture) loginSession(t *testing.T, role int) (*model.User, *service.AuthBundle) {
	t.Helper()
	f.nextUserID++
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	require.NoError(t, err)
	user := &model.User{
		Id: f.nextUserID, Username: fmt.Sprintf("rate-limit-user-%d", f.nextUserID), Password: string(hash),
		Role: role, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
		AffCode: fmt.Sprintf("rl%d", f.nextUserID),
	}
	require.NoError(t, f.db.Create(user).Error)
	bundle, err := service.CreateLoginSession(user.Id, "password", f.remoteAddr, "rate-limit-test")
	require.NoError(t, err)
	return user, bundle
}

func TestModelHealthAdminReadAndRestrictedActions(t *testing.T) {
	f := newAuthRateLimitFixture(t, false)
	_, user := f.loginSession(t, common.RoleCommonUser)
	_, admin := f.loginSession(t, common.RoleAdminUser)
	for _, test := range []struct {
		path   string
		token  string
		status int
	}{
		{"/api/model-health/capabilities", "", http.StatusUnauthorized},
		{"/api/model-health/capabilities", user.AccessToken, http.StatusForbidden},
		{"/api/model-health/capabilities", admin.AccessToken, http.StatusOK},
		{"/api/model-health/budget", admin.AccessToken, http.StatusForbidden},
  {"/api/model-health/cycle-budget?scope=global", admin.AccessToken, http.StatusForbidden},
		{"/api/model-health/evidence/1", admin.AccessToken, http.StatusForbidden},
	} {
		request := httptest.NewRequest(http.MethodGet, "https://gateway.example"+test.path, nil)
		request.RemoteAddr = f.remoteAddr
		if test.token != "" {
			request.Header.Set("Authorization", "Bearer "+test.token)
		}
		recorder := httptest.NewRecorder()
		f.router.ServeHTTP(recorder, request)
		assert.Equal(t, test.status, recorder.Code, test.path)
	}
}

func TestModelHealthReadsHaveIndependentRateLimit(t *testing.T) {
	for _, useRedis := range []bool{false, true} {
		t.Run(fmt.Sprintf("redis=%t", useRedis), func(t *testing.T) {
			previousEnabled := common.SearchRateLimitEnable
			previousNum, previousDuration := common.SearchRateLimitNum, common.SearchRateLimitDuration
			t.Cleanup(func() {
				common.SearchRateLimitEnable = previousEnabled
				common.SearchRateLimitNum, common.SearchRateLimitDuration = previousNum, previousDuration
			})
			common.SearchRateLimitEnable = true
			common.SearchRateLimitNum, common.SearchRateLimitDuration = 2, 60
			f := newAuthRateLimitFixture(t, useRedis)
			require.NoError(t, f.db.AutoMigrate(&model.Token{}, &model.Channel{}, &model.ModelHealthConfig{}, &model.ModelHealthRun{}))
			channel := model.Channel{Name: "Health fixture", Type: constant.ChannelTypeOpenAI, Models: "MiniMax-H3"}
			require.NoError(t, f.db.Create(&channel).Error)
			_, admin := f.loginSession(t, common.RoleRootUser)
			get := func(path string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodGet, "https://gateway.example"+path, nil)
				r.RemoteAddr = f.remoteAddr
				r.Header.Set("Authorization", "Bearer "+admin.AccessToken)
				w := httptest.NewRecorder()
				f.router.ServeHTTP(w, r)
				return w
			}
			const search = "/api/token/search?keyword=fixture"
			const health = "/api/model-health/capabilities"
			template := fmt.Sprintf("/api/model-health/template?channel_id=%d&model=MiniMax-H3&endpoint=openai-video", channel.Id)
			for range 2 {
				w := get(search)
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			}
			assert.Equal(t, http.StatusTooManyRequests, get(search).Code)
			w := get(template)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), `"success":true`)
			for range 89 {
				w := get(health)
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			}
			w = get(template)
			assert.Equal(t, http.StatusTooManyRequests, w.Code)
			assert.NotEmpty(t, w.Header().Get("Retry-After"))
			// An invalid evidence ID reaches its handler even after the read limit.
			assert.Equal(t, http.StatusNotFound, get("/api/model-health/evidence/0").Code)
		})
	}
}

func (f *authRateLimitFixture) post(path, body, token, remoteAddr string, refreshCookie *http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "https://gateway.example"+path, strings.NewReader(body))
	request.RemoteAddr = remoteAddr
	request.Header.Set("Origin", "https://gateway.example")
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if refreshCookie != nil {
		request.AddCookie(refreshCookie)
	}
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	return response
}

func findRefreshCookie(t *testing.T, cookies []*http.Cookie) *http.Cookie {
	t.Helper()
	for _, cookie := range cookies {
		if cookie.Name == service.RefreshCookieName {
			return cookie
		}
	}
	require.FailNow(t, "refresh cookie was not returned")
	return nil
}

func TestAuthSessionRoutesHaveIndependentLimitsFromCredentialAttempts(t *testing.T) {
	for _, useRedis := range []bool{false, true} {
		t.Run(fmt.Sprintf("redis=%t", useRedis), func(t *testing.T) {
			fixture := newAuthRateLimitFixture(t, useRedis)
			user, bundle := fixture.loginSession(t, common.RoleCommonUser)
			refreshCookie := &http.Cookie{Name: service.RefreshCookieName, Value: bundle.RefreshToken}
			const refreshPath = "/api/user/auth/refresh"
			const logoutPath = "/api/user/auth/logout"

			for _, path := range []string{refreshPath, logoutPath} {
				request := httptest.NewRequest(http.MethodPost, "https://gateway.example"+path, nil)
				request.RemoteAddr = fixture.remoteAddr
				request.Header.Set("Origin", "https://untrusted.example")
				response := httptest.NewRecorder()
				fixture.router.ServeHTTP(response, request)
				assert.Equal(t, http.StatusForbidden, response.Code, path)
				assert.Contains(t, response.Body.String(), "AUTH_ORIGIN_FORBIDDEN")
			}

			response := fixture.post(refreshPath, "", "", fixture.remoteAddr, refreshCookie)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			refreshCookie = findRefreshCookie(t, response.Result().Cookies())
			assert.Equal(t, http.StatusOK, fixture.post(logoutPath, "", "", fixture.remoteAddr, nil).Code)

			loginBody := fmt.Sprintf(`{"username":%q,"password":"incorrect-password"}`, user.Username)
			for range 2 {
				response = fixture.post("/api/user/login", loginBody, "", fixture.remoteAddr, nil)
				assert.Equal(t, http.StatusOK, response.Code)
				assert.Contains(t, response.Body.String(), `"success":false`)
			}
			for _, path := range []string{"/api/user/login", "/api/user/register", "/api/user/login/2fa", "/api/user/passkey/login/begin", "/api/user/passkey/login/finish"} {
				response = fixture.post(path, "{}", "", fixture.remoteAddr, nil)
				assert.Equal(t, http.StatusTooManyRequests, response.Code, path)
				assert.Equal(t, "1200", response.Header().Get("Retry-After"), path)
			}

			// A blocked credential bucket still permits valid session rotation.
			for range 2 {
				response = fixture.post(refreshPath, "", "", fixture.remoteAddr, refreshCookie)
				require.Equal(t, http.StatusOK, response.Code, response.Body.String())
				refreshCookie = findRefreshCookie(t, response.Result().Cookies())
			}
			response = fixture.post(refreshPath, "", "", fixture.remoteAddr, refreshCookie)
			assert.Equal(t, http.StatusTooManyRequests, response.Code)
			assert.Equal(t, "60", response.Header().Get("Retry-After"))

			// Exhausting refresh cannot prevent revoking the real login session.
			response = fixture.post(logoutPath, "", "", fixture.remoteAddr, refreshCookie)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			stored, err := model.GetUserSessionBySID(bundle.Session.SID)
			require.NoError(t, err)
			assert.Equal(t, model.UserSessionStatusRevoked, stored.Status)
			assert.Equal(t, http.StatusOK, fixture.post(logoutPath, "", "", fixture.remoteAddr, nil).Code)
			response = fixture.post(logoutPath, "", "", fixture.remoteAddr, nil)
			assert.Equal(t, http.StatusTooManyRequests, response.Code)
			assert.Equal(t, "60", response.Header().Get("Retry-After"))
		})
	}
}

func TestAgencyRoutesLimitAuthenticatedUsersWithoutConsumingLoginAttempts(t *testing.T) {
	for _, useRedis := range []bool{false, true} {
		t.Run(fmt.Sprintf("redis=%t", useRedis), func(t *testing.T) {
			fixture := newAuthRateLimitFixture(t, useRedis)
			root, rootSession := fixture.loginSession(t, common.RoleRootUser)
			_, otherRootSession := fixture.loginSession(t, common.RoleRootUser)
			_, userSession := fixture.loginSession(t, common.RoleCommonUser)
			const ssoPath = "/api/agency/sso-ticket"
			const verifyPath = "/api/agency/verify"
			const commandPath = "/api/agency/command-proof"
			for _, path := range []string{ssoPath, verifyPath, commandPath} {
				assert.Equal(t, http.StatusUnauthorized, fixture.post(path, "{}", "", fixture.remoteAddr, nil).Code)
				wantStatus := http.StatusForbidden
				if path == ssoPath {
					// Linked distributor operators may use browser SSO; an empty
					// state is rejected before looking up their operator binding.
					wantStatus = http.StatusBadRequest
				}
				assert.Equal(t, wantStatus, fixture.post(path, "{}", userSession.AccessToken, fixture.remoteAddr, nil).Code)
			}

			proofBody := `{"password":"incorrect-password","command_id":"command-1","action":"agency.update","object_id":"agency-1","body_hash":"` + strings.Repeat("a", 64) + `"}`
			for _, request := range []struct {
				path, remoteAddr string
			}{
				{verifyPath, fixture.remoteAddr},
				{commandPath, "203.0.113.41:12345"},
			} {
				response := fixture.post(request.path, proofBody, rootSession.AccessToken, request.remoteAddr, nil)
				assert.Equal(t, http.StatusUnauthorized, response.Code)
				assert.Contains(t, response.Body.String(), "verification failed")
			}
			// Alternating proof endpoints or rotating IPs does not create more
			// password guesses for the same authenticated Root account.
			for _, path := range []string{verifyPath, commandPath} {
				response := fixture.post(path, proofBody, rootSession.AccessToken, "203.0.113.42:12345", nil)
				assert.Equal(t, http.StatusTooManyRequests, response.Code, path)
				assert.Equal(t, "1200", response.Header().Get("Retry-After"))
			}

			// SSO issuance has a separate allowance from password verification.
			for range 2 {
				response := fixture.post(ssoPath, "{}", rootSession.AccessToken, fixture.remoteAddr, nil)
				assert.Equal(t, http.StatusBadRequest, response.Code)
				assert.Contains(t, response.Body.String(), "invalid state hash")
			}
			assert.Equal(t, http.StatusTooManyRequests, fixture.post(ssoPath, "{}", rootSession.AccessToken, fixture.remoteAddr, nil).Code)
			// Another authenticated Root on the same IP retains both allowances.
			assert.Equal(t, http.StatusBadRequest, fixture.post(ssoPath, "{}", otherRootSession.AccessToken, fixture.remoteAddr, nil).Code)
			assert.Equal(t, http.StatusUnauthorized, fixture.post(verifyPath, proofBody, otherRootSession.AccessToken, fixture.remoteAddr, nil).Code)
			// Authentication remains ahead of the limiter, including after a
			// valid user's allowance has been exhausted on this IP.
			assert.Equal(t, http.StatusUnauthorized, fixture.post(verifyPath, proofBody, "", fixture.remoteAddr, nil).Code)

			loginBody := fmt.Sprintf(`{"username":%q,"password":"incorrect-password"}`, root.Username)
			for range 2 {
				response := fixture.post("/api/user/login", loginBody, "", fixture.remoteAddr, nil)
				assert.Equal(t, http.StatusOK, response.Code)
				assert.Contains(t, response.Body.String(), `"success":false`)
			}
			assert.Equal(t, http.StatusTooManyRequests, fixture.post("/api/user/login", loginBody, "", fixture.remoteAddr, nil).Code)
		})
	}
}
