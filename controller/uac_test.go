package controller

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/uac"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

type uacGrant struct {
	params   url.Values
	mutation string
}

func TestUACInstanceConfigurationKeepsProductLoginAsDefault(t *testing.T) {
	previous, secure, origins := uac.Default, common.SessionCookieSecure, common.SessionCookieTrustedURLs
	t.Cleanup(func() {
		uac.Default, common.SessionCookieSecure, common.SessionCookieTrustedURLs = previous, secure, origins
	})
	uac.Default = nil
	t.Setenv("UAC_OIDC_ENABLED", "")
	t.Setenv("UAC_OIDC_REQUIRED", "")
	require.NoError(t, uac.ConfigureFromEnv())
	require.False(t, uac.Enabled())
	require.Equal(t, "new_api_refresh", uac.RefreshCookie())
	t.Setenv("UAC_OIDC_ENABLED", "true")
	t.Setenv("UAC_OIDC_REQUIRED", "true")
	t.Setenv("UAC_PUBLIC_URL", "https://gateway.example/test")
	t.Setenv("UAC_OIDC_ISSUER", "https://login.example/oidc")
	t.Setenv("UAC_OIDC_CLIENT_ID", "test-client")
	t.Setenv("UAC_OIDC_CLIENT_SECRET", "test-only-secret")
	t.Setenv("UAC_INITIAL_ROLE", "10")
	t.Setenv("UAC_OIDC_CA_FILE", "")
	common.SessionCookieSecure = false
	require.Error(t, uac.ConfigureFromEnv())
	common.SessionCookieSecure = true
	common.SessionCookieTrustedURLs = []string{"https://other.example"}
	require.Error(t, uac.ConfigureFromEnv())
	common.SessionCookieTrustedURLs = []string{"https://gateway.example"}
	require.NoError(t, uac.ConfigureFromEnv())
	require.True(t, uac.Required())
	require.Equal(t, "/test", uac.Prefix())
}

type uacFixture struct {
	client    *uac.Client
	mu        sync.Mutex
	grants    map[string]uacGrant
	deny      bool
	refreshes int
	lastID    string
}

func setupUACTest(t *testing.T) *uacFixture {
	t.Helper()
	setupSecurityEnrollmentTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.ExternalIdentityClaim{}))
	previous := uac.Default
	t.Cleanup(func() { uac.Default = previous })
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	fixture := &uacFixture{grants: map[string]uacGrant{}}
	issuer := ""
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		var data any
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			data = map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "userinfo_endpoint": issuer + "/userinfo", "jwks_uri": issuer + "/jwks", "end_session_endpoint": issuer + "/logout"}
		case "/jwks":
			data = map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "uac-test", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}}
		case "/token":
			id, secret, ok := r.BasicAuth()
			if !ok || id != "test-client" || secret != "test-secret" {
				w.WriteHeader(401)
				return
			}
			require.NoError(t, r.ParseForm())
			if fixture.deny {
				w.WriteHeader(400)
				return
			}
			if r.Form.Get("grant_type") == "refresh_token" {
				fixture.refreshes++
				data = map[string]any{"access_token": "opaque-access", "id_token": fixture.lastID, "refresh_token": "rotated-refresh", "token_type": "Bearer", "expires_in": 300}
				break
			}
			grant, ok := fixture.grants[r.Form.Get("code")]
			delete(fixture.grants, r.Form.Get("code"))
			if !ok || oauth2.S256ChallengeFromVerifier(r.Form.Get("code_verifier")) != grant.params.Get("code_challenge") || r.Form.Get("redirect_uri") != grant.params.Get("redirect_uri") {
				w.WriteHeader(400)
				return
			}
			access := "opaque-access"
			hash := sha256.Sum256([]byte(access))
			claims := jwt.MapClaims{"iss": issuer, "sub": "operator-42", "aud": "test-client", "iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix(), "nonce": grant.params.Get("nonce"), "at_hash": base64.RawURLEncoding.EncodeToString(hash[:16])}
			switch grant.mutation {
			case "nonce":
				claims["nonce"] = "wrong"
			case "audience":
				claims["aud"] = "other-client"
			case "issuer":
				claims["iss"] = "https://other.example"
			case "expired":
				claims["exp"] = time.Now().Add(-time.Minute).Unix()
			case "access_hash":
				claims["at_hash"] = "wrong"
			case "subject":
				claims["sub"] = "other-user"
			}
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
			token.Header["kid"] = "uac-test"
			signed, err := token.SignedString(key)
			require.NoError(t, err)
			if grant.mutation == "signature" {
				signed = signed[:len(signed)-8] + "tampered"
			}
			data = map[string]any{"access_token": access, "id_token": signed, "refresh_token": "initial-refresh", "token_type": "Bearer", "expires_in": 300}
			fixture.lastID = signed
		case "/userinfo":
			if fixture.deny || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				w.WriteHeader(401)
				return
			}
			data = map[string]any{"sub": "operator-42", "name": "运维测试人员", "email": "operator@example.com", "email_verified": true}
		default:
			w.WriteHeader(404)
			return
		}
		body, err := common.Marshal(data)
		require.NoError(t, err)
		_, _ = w.Write(body)
	}))
	issuer = server.URL
	t.Cleanup(server.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600))
	fixture.client, err = uac.New(uac.Config{Enabled: true, Required: true, Issuer: issuer, ClientID: "test-client", ClientSecret: "test-secret", PublicURL: "https://gateway.example/test", InitialRole: 100, CAFile: ca})
	require.NoError(t, err)
	uac.Default = fixture.client
	return fixture
}

func (f *uacFixture) grant(t *testing.T, mutation string) (state, binding, code string) {
	t.Helper()
	authorization, binding, err := f.client.Start(context.Background())
	require.NoError(t, err)
	parsed, err := url.Parse(authorization)
	require.NoError(t, err)
	params := parsed.Query()
	assert.Equal(t, "S256", params.Get("code_challenge_method"))
	assert.Equal(t, "https://gateway.example/test/api/oauth/uac/callback", params.Get("redirect_uri"))
	assert.NotEmpty(t, params.Get("nonce"))
	code, err = uac.Random()
	require.NoError(t, err)
	f.mu.Lock()
	f.grants[code] = uacGrant{params: params, mutation: mutation}
	f.mu.Unlock()
	return params.Get("state"), binding, code
}

func TestUACProtocolRejectsInvalidIdentityAndReplay(t *testing.T) {
	f := setupUACTest(t)
	for _, mutation := range []string{"nonce", "audience", "issuer", "expired", "access_hash", "subject", "signature"} {
		t.Run(mutation, func(t *testing.T) {
			state, binding, code := f.grant(t, mutation)
			_, err := f.client.Complete(context.Background(), state, binding, code)
			require.Error(t, err)
		})
	}
	state, binding, code := f.grant(t, "")
	_, err := f.client.Complete(context.Background(), state, "different-browser", code)
	require.Error(t, err)
	session, err := f.client.Complete(context.Background(), state, binding, code)
	require.NoError(t, err)
	assert.Equal(t, "operator-42", session.Identity.Subject)
	_, err = f.client.Complete(context.Background(), state, binding, code)
	require.Error(t, err)
	require.NoError(t, f.client.Attach("session", session))
	session.AccessExpires = time.Now().Add(-time.Second)
	session.Checked = time.Now().Add(-time.Minute)
	require.NoError(t, f.client.Validate(context.Background(), "session"))
	assert.Equal(t, 1, f.refreshes)
	assert.Equal(t, "rotated-refresh", session.Tokens.Refresh)
	f.mu.Lock()
	f.deny = true
	f.mu.Unlock()
	session.Checked = time.Now().Add(-time.Minute)
	require.Error(t, f.client.Validate(context.Background(), "session"))
}

func TestUACCallbackCreatesSingleAccountAndLocalSession(t *testing.T) {
	f := setupUACTest(t)
	state, binding, code := f.grant(t, "")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "https://gateway.example/api/oauth/uac/callback?state="+state+"&code="+code, nil)
	c.Request.AddCookie(&http.Cookie{Name: "new_api_uac_login", Value: binding})
	UACCallback(c)
	require.Equal(t, 302, w.Code)
	assert.Equal(t, "/test/dashboard", w.Header().Get("Location"))
	var raw string
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "new_api_uac_refresh" {
			raw = cookie.Value
			assert.Equal(t, "/test/api/user/auth", cookie.Path)
			assert.True(t, cookie.HttpOnly)
		}
	}
	require.NotEmpty(t, raw)
	bundle, user, err := service.RefreshLoginSession(raw, "", "127.0.0.1", "test")
	require.NoError(t, err)
	assert.Equal(t, 100, user.Role)
	assert.Empty(t, user.Password)
	_, err = service.CreateLoginSession(user.Id, "password", "127.0.0.1", "test")
	require.Error(t, err)
	firstID := user.Id
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", firstID).Update("role", 10).Error)
	user, err = model.ResolveUACUser(f.client.Config.Issuer, "operator-42", "changed name", 100)
	require.NoError(t, err)
	assert.Equal(t, firstID, user.Id)
	assert.Equal(t, 10, user.Role)
	for range 2 {
		require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.ExternalIdentityClaim{}))
	}
	user, err = model.ResolveUACUser(f.client.Config.Issuer, "operator-42", "ignored", 100)
	require.NoError(t, err)
	assert.Equal(t, firstID, user.Id)
	other, err := model.ResolveUACUser("https://other.example", "operator-42", "other", 100)
	require.NoError(t, err)
	assert.NotEqual(t, firstID, other.Id)
	logout := httptest.NewRecorder()
	lc, _ := gin.CreateTestContext(logout)
	lc.Request = httptest.NewRequest("POST", "https://gateway.example/api/user/auth/logout", nil)
	lc.Request.AddCookie(&http.Cookie{Name: "new_api_uac_refresh", Value: bundle.RefreshToken})
	UACLogout(lc)
	assert.Equal(t, 200, logout.Code)
	assert.Contains(t, logout.Body.String(), "logout_url")
	_, _, err = service.RefreshLoginSession(bundle.RefreshToken, "", "127.0.0.1", "test")
	require.Error(t, err)
}

func TestUACTLSNeverInheritsRelaySkipVerification(t *testing.T) {
	f := setupUACTest(t)
	cfg := f.client.Config
	cfg.CAFile = ""
	client, err := uac.New(cfg)
	require.NoError(t, err)
	_, err = client.Discover(context.Background())
	require.Error(t, err)
}

func TestUACPreservesLocalMFAAndConsumesBrowserHandoffOnce(t *testing.T) {
	f := setupUACTest(t)
	user, err := model.ResolveUACUser(f.client.Config.Issuer, "operator-42", "operator", 100)
	require.NoError(t, err)
	twoFA := &model.TwoFA{UserId: user.Id, Secret: "JBSWY3DPEHPK3PXP", IsEnabled: true}
	require.NoError(t, model.DB.Create(twoFA).Error)
	state, binding, code := f.grant(t, "")
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodGet, "https://gateway.example/api/oauth/uac/callback?state="+state+"&code="+code, nil)
	c.Request.AddCookie(&http.Cookie{Name: "new_api_uac_login", Value: binding})
	UACCallback(c)
	assert.Equal(t, "/test/sign-in?uac_complete=1", response.Header().Get("Location"))
	var ticket string
	for _, cookie := range response.Result().Cookies() {
		assert.NotEqual(t, "new_api_uac_refresh", cookie.Name)
		if cookie.Name == "new_api_uac_handoff" {
			ticket = cookie.Value
		}
	}
	require.NotEmpty(t, ticket)
	handoff, err := f.client.TakeLogin(ticket)
	require.NoError(t, err)
	_, err = f.client.TakeLogin(ticket)
	require.Error(t, err)
	challenge := handoff.Data.(*service.LoginChallenge)
	_, err = service.VerifyLoginCode(challenge.FlowToken, "000000", "127.0.0.1", "test")
	require.Error(t, err)
	code, err = totp.GenerateCode(twoFA.Secret, time.Now())
	require.NoError(t, err)
	bundle, err := service.VerifyLoginCode(challenge.FlowToken, code, "127.0.0.1", "test")
	require.NoError(t, err)
	require.NoError(t, f.client.Validate(context.Background(), bundle.Session.SID))
	_, err = service.VerifyLoginCode(challenge.FlowToken, code, "127.0.0.1", "test")
	require.Error(t, err)
}
