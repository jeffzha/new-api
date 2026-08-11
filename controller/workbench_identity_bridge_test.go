package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/workbenchbridge"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var (
	workbenchControlTestSecret = []byte("control-hmac-secret-0123456789abcdef")
	workbenchServiceTestSecret = []byte("service-hmac-secret-0123456789abcdef")
)

const workbenchIdentityTestNonce = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type fakeWorkbenchTicketIssuer struct {
	issued  workbenchbridge.IssuedTicket
	err     error
	request workbenchbridge.TicketIssueRequest
}

func (issuer *fakeWorkbenchTicketIssuer) Issue(_ context.Context, request workbenchbridge.TicketIssueRequest) (workbenchbridge.IssuedTicket, error) {
	issuer.request = request
	return issuer.issued, issuer.err
}

func newWorkbenchTestBridge(t *testing.T, user *model.User) (*WorkbenchIdentityBridge, *fakeWorkbenchTicketIssuer) {
	t.Helper()
	config := workbenchbridge.Config{
		Enabled:             true,
		ControlURL:          "https://claw-control.internal",
		ControlHMACSecret:   workbenchControlTestSecret,
		ControlServiceName:  "new-api-core",
		ControlTimeout:      time.Second,
		ServiceHMACSecret:   workbenchServiceTestSecret,
		InternalRequestSkew: time.Minute,
	}
	tickets := &fakeWorkbenchTicketIssuer{issued: workbenchbridge.IssuedTicket{
		Value:     "control-issued-single-use-ticket",
		ExpiresAt: time.Now().Add(time.Minute),
	}}
	lookup := func(userID int) (*model.User, error) {
		if user == nil || user.Id != userID {
			return nil, gorm.ErrRecordNotFound
		}
		copy := *user
		return &copy, nil
	}
	return NewWorkbenchIdentityBridge(config, tickets, lookup), tickets
}

func newWorkbenchSessionRouter(bridge *WorkbenchIdentityBridge, sessionStatus int) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("workbench-test-session-secret"))))
	router.GET("/login", func(c *gin.Context) {
		session := sessions.Default(c)
		session.Set("username", "alice")
		session.Set("role", common.RoleCommonUser)
		session.Set("id", 42)
		session.Set("status", sessionStatus)
		session.Set("group", "default")
		if err := session.Save(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false})
			return
		}
		c.Status(http.StatusNoContent)
	})
	router.POST("/api/workbench/session-ticket", middleware.UserAuth(), bridge.SessionTicket)
	return router
}

func authenticatedWorkbenchRequest(t *testing.T, router *gin.Engine) *httptest.ResponseRecorder {
	t.Helper()
	loginRecorder := httptest.NewRecorder()
	loginRequest := httptest.NewRequest(http.MethodGet, "/login", nil)
	router.ServeHTTP(loginRecorder, loginRequest)
	require.Equal(t, http.StatusNoContent, loginRecorder.Code)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/workbench/session-ticket", nil)
	request.Header.Set("Origin", "http://example.com")
	request.Header.Set("New-Api-User", "42")
	for _, sessionCookie := range loginRecorder.Result().Cookies() {
		request.AddCookie(sessionCookie)
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

func authenticatedWorkbenchAdminRequest(t *testing.T, bridge *WorkbenchIdentityBridge, sessionRole int) *httptest.ResponseRecorder {
	t.Helper()
	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("workbench-test-session-secret"))))
	router.GET("/login", func(c *gin.Context) {
		session := sessions.Default(c)
		session.Set("username", "root")
		session.Set("role", sessionRole)
		session.Set("id", 42)
		session.Set("status", common.UserStatusEnabled)
		session.Set("group", "default")
		require.NoError(t, session.Save())
		c.Status(http.StatusNoContent)
	})
	router.POST("/api/admin/workbench/session-ticket", middleware.RootAuth(), bridge.AdminSessionTicket)

	loginRecorder := httptest.NewRecorder()
	router.ServeHTTP(loginRecorder, httptest.NewRequest(http.MethodGet, "/login", nil))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/admin/workbench/session-ticket", nil)
	request.Header.Set("New-Api-User", "42")
	request.Header.Set("Origin", "http://example.com")
	for _, sessionCookie := range loginRecorder.Result().Cookies() {
		request.AddCookie(sessionCookie)
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestSessionTicketUsesCurrentDatabaseIdentityAndReturnsControlTicket(t *testing.T) {
	user := &model.User{
		Id:          42,
		Username:    "alice",
		DisplayName: "Alice",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "vip",
		Quota:       123456,
		Email:       "alice@example.com",
		CreatedAt:   1_700_000_000,
	}
	bridge, tickets := newWorkbenchTestBridge(t, user)
	recorder := authenticatedWorkbenchRequest(t, newWorkbenchSessionRouter(bridge, common.UserStatusEnabled))

	require.Equal(t, http.StatusOK, recorder.Code)
	responseBody := append([]byte(nil), recorder.Body.Bytes()...)
	var payload struct {
		Success bool `json:"success"`
		Data    struct {
			Ticket    string `json:"ticket"`
			ExpiresAt int64  `json:"expires_at"`
			ExpiresIn int64  `json:"expires_in"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(responseBody, &payload))
	require.True(t, payload.Success)
	assert.NotEmpty(t, payload.Data.Ticket)
	assert.Greater(t, payload.Data.ExpiresAt, time.Now().Unix())
	assert.InDelta(t, 60, payload.Data.ExpiresIn, 1)
	assert.NotContains(t, string(responseBody), "quota")
	assert.NotContains(t, string(responseBody), "group")
	assert.NotContains(t, string(responseBody), "email")

	assert.Equal(t, user.Id, tickets.request.UserID)
	assert.Equal(t, workbenchbridge.IdentityVersion(identityFromUser(user)), tickets.request.IdentityVersion)
	assert.Equal(t, workbenchbridge.SurfaceWorkbench, tickets.request.Surface)
}

func TestSessionTicketRejectsAccessTokenAuthentication(t *testing.T) {
	bridge, _ := newWorkbenchTestBridge(t, &model.User{Id: 42, Username: "alice", Status: common.UserStatusEnabled})
	router := gin.New()
	router.POST("/api/workbench/session-ticket", func(c *gin.Context) {
		c.Set("id", 42)
		c.Set("use_access_token", true)
		c.Next()
	}, bridge.SessionTicket)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/workbench/session-ticket", nil))

	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "web session")
}

func TestAdminSessionTicketRequiresAndRecordsSuperAdministratorSurface(t *testing.T) {
	bridge, tickets := newWorkbenchTestBridge(t, &model.User{
		Id: 42, Username: "root", Role: common.RoleRootUser, Status: common.UserStatusEnabled,
	})
	recorder := authenticatedWorkbenchAdminRequest(t, bridge, common.RoleRootUser)

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, workbenchbridge.SurfaceAdmin, tickets.request.Surface)
	assert.True(t, tickets.request.IsSuperAdmin)
}

func TestAdminStepUpTicketConsumesTrustedSecureVerificationOnce(t *testing.T) {
	bridge, tickets := newWorkbenchTestBridge(t, &model.User{
		Id: 42, Username: "root", Role: common.RoleRootUser, Status: common.UserStatusEnabled,
	})
	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("workbench-test-session-secret"))))
	router.GET("/login", func(c *gin.Context) {
		session := sessions.Default(c)
		session.Set("username", "root")
		session.Set("role", common.RoleRootUser)
		session.Set("id", 42)
		session.Set("status", common.UserStatusEnabled)
		session.Set("group", "default")
		session.Set(SecureVerificationSessionKey, time.Now().Unix())
		session.Set(secureVerificationMethodSessionKey, secureVerificationMethodPasskey)
		require.NoError(t, session.Save())
		c.Status(http.StatusNoContent)
	})
	router.POST("/api/admin/workbench/step-up-ticket", func(c *gin.Context) {
		c.Set("id", 42)
		c.Next()
	}, bridge.AdminStepUpTicket)

	login := httptest.NewRecorder()
	router.ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/login", nil))
	request := httptest.NewRequest(http.MethodPost, "/api/admin/workbench/step-up-ticket", bytes.NewBufferString(`{"method":"secure_verification"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://example.com")
	for _, sessionCookie := range login.Result().Cookies() {
		request.AddCookie(sessionCookie)
	}
	first := httptest.NewRecorder()
	router.ServeHTTP(first, request)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	assert.WithinDuration(t, time.Now(), tickets.request.AuthenticatedAt, 2*time.Second)
	assert.Equal(t, []string{"webauthn"}, tickets.request.AMR)
	assert.NotEmpty(t, tickets.request.ReauthNonce)

	replay := httptest.NewRequest(http.MethodPost, "/api/admin/workbench/step-up-ticket", bytes.NewBufferString(`{"method":"secure_verification"}`))
	replay.Header.Set("Content-Type", "application/json")
	replay.Header.Set("Origin", "http://example.com")
	for _, sessionCookie := range first.Result().Cookies() {
		replay.AddCookie(sessionCookie)
	}
	second := httptest.NewRecorder()
	router.ServeHTTP(second, replay)
	assert.Equal(t, http.StatusForbidden, second.Code)
	assert.Contains(t, second.Body.String(), "recent 2FA or Passkey verification is required")
}

func TestAdminStepUpTicketRejectsExpiredSecureVerification(t *testing.T) {
	bridge, tickets := newWorkbenchTestBridge(t, &model.User{
		Id: 42, Username: "root", Role: common.RoleRootUser, Status: common.UserStatusEnabled,
	})
	recorder := httptest.NewRecorder()
	store := cookie.NewStore([]byte("workbench-test-session-secret"))
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("session", store))
	router.GET("/login", func(c *gin.Context) {
		session := sessions.Default(c)
		session.Set("username", "root")
		session.Set("role", common.RoleRootUser)
		session.Set("id", 42)
		session.Set("status", common.UserStatusEnabled)
		session.Set("group", "default")
		session.Set(SecureVerificationSessionKey, time.Now().Add(-time.Duration(SecureVerificationTimeout+1)*time.Second).Unix())
		session.Set(secureVerificationMethodSessionKey, secureVerificationMethod2FA)
		require.NoError(t, session.Save())
		c.Status(http.StatusNoContent)
	})
	router.POST("/api/admin/workbench/step-up-ticket", func(c *gin.Context) {
		c.Set("id", 42)
		c.Next()
	}, bridge.AdminStepUpTicket)
	login := httptest.NewRecorder()
	router.ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/login", nil))
	request := httptest.NewRequest(http.MethodPost, "/api/admin/workbench/step-up-ticket", bytes.NewBufferString(`{"method":"secure_verification"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://example.com")
	for _, sessionCookie := range login.Result().Cookies() {
		request.AddCookie(sessionCookie)
	}
	router.ServeHTTP(recorder, request)
	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Zero(t, tickets.request.UserID)
}

func TestAdminStepUpTicketVerifiesPasswordOnlyInsideNewAPI(t *testing.T) {
	hashedPassword, err := common.Password2Hash("RootPassword@2026")
	require.NoError(t, err)
	bridge, tickets := newWorkbenchTestBridge(t, &model.User{
		Id: 42, Username: "root", Password: hashedPassword, Role: common.RoleRootUser, Status: common.UserStatusEnabled,
	})
	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("workbench-test-session-secret"))))
	router.POST("/api/admin/workbench/step-up-ticket", func(c *gin.Context) {
		c.Set("id", 42)
		c.Next()
	}, bridge.AdminStepUpTicket)

	wrong := httptest.NewRequest(http.MethodPost, "/api/admin/workbench/step-up-ticket", bytes.NewBufferString(`{"method":"password","password":"wrong"}`))
	wrong.Header.Set("Content-Type", "application/json")
	wrong.Header.Set("Origin", "http://example.com")
	wrongResult := httptest.NewRecorder()
	router.ServeHTTP(wrongResult, wrong)
	assert.Equal(t, http.StatusForbidden, wrongResult.Code)
	assert.Zero(t, tickets.request.UserID)

	correct := httptest.NewRequest(http.MethodPost, "/api/admin/workbench/step-up-ticket", bytes.NewBufferString(`{"method":"password","password":"RootPassword@2026"}`))
	correct.Header.Set("Content-Type", "application/json")
	correct.Header.Set("Origin", "http://example.com")
	correctResult := httptest.NewRecorder()
	router.ServeHTTP(correctResult, correct)
	require.Equal(t, http.StatusOK, correctResult.Code, correctResult.Body.String())
	assert.Equal(t, []string{"pwd"}, tickets.request.AMR)
	assert.NotEmpty(t, tickets.request.ReauthNonce)
	assert.NotContains(t, fmt.Sprintf("%#v", tickets.request), "RootPassword@2026")
}

func TestSessionTicketRejectsUserDisabledAfterSessionWasCreated(t *testing.T) {
	bridge, _ := newWorkbenchTestBridge(t, &model.User{Id: 42, Username: "alice", Status: common.UserStatusDisabled})
	recorder := authenticatedWorkbenchRequest(t, newWorkbenchSessionRouter(bridge, common.UserStatusEnabled))

	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "user is disabled")
}

func TestSessionTicketRejectsCrossOriginAndFailsClosedWhenControlIsUnavailable(t *testing.T) {
	user := &model.User{Id: 42, Username: "alice", Status: common.UserStatusEnabled}

	t.Run("cross origin", func(t *testing.T) {
		bridge, tickets := newWorkbenchTestBridge(t, user)
		router := newWorkbenchSessionRouter(bridge, common.UserStatusEnabled)
		loginRecorder := httptest.NewRecorder()
		router.ServeHTTP(loginRecorder, httptest.NewRequest(http.MethodGet, "/login", nil))

		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/workbench/session-ticket", nil)
		request.Header.Set("New-Api-User", "42")
		request.Header.Set("Origin", "https://attacker.example")
		for _, sessionCookie := range loginRecorder.Result().Cookies() {
			request.AddCookie(sessionCookie)
		}
		router.ServeHTTP(recorder, request)

		assert.Equal(t, http.StatusForbidden, recorder.Code)
		assert.Zero(t, tickets.request.UserID)
	})

	t.Run("control unavailable", func(t *testing.T) {
		bridge, tickets := newWorkbenchTestBridge(t, user)
		tickets.err = workbenchbridge.ErrControlUnavailable
		recorder := authenticatedWorkbenchRequest(t, newWorkbenchSessionRouter(bridge, common.UserStatusEnabled))

		assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
		assert.Contains(t, recorder.Body.String(), "failed to issue")
	})
}

func TestSessionTicketFeatureFlagDefaultsClosed(t *testing.T) {
	bridge, _ := newWorkbenchTestBridge(t, &model.User{Id: 42, Username: "alice", Status: common.UserStatusEnabled})
	bridge.config.Enabled = false
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/workbench/session-ticket", nil)
	context.Set("id", 42)

	bridge.SessionTicket(context)

	assert.Equal(t, http.StatusNotFound, recorder.Code)
}

func signedIdentityStatusRequest(t *testing.T, bridge *WorkbenchIdentityBridge, userID int, mutateSignature bool) *httptest.ResponseRecorder {
	t.Helper()
	body := []byte(`{"user_id":` + strconv.Itoa(userID) + `}`)
	timestamp := bridge.now().Unix()
	signature := workbenchbridge.SignInternalRequest(
		workbenchServiceTestSecret,
		http.MethodPost,
		"/api/internal/workbench/identity-status",
		timestamp,
		workbenchIdentityTestNonce,
		body,
	)
	if mutateSignature {
		signature = strings.Repeat("0", len(signature))
	}

	router := gin.New()
	router.POST("/api/internal/workbench/identity-status", bridge.IdentityStatus)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/internal/workbench/identity-status", bytes.NewReader(body))
	request.Header.Set(workbenchbridge.ContractVersionHeader, workbenchbridge.ContractVersion)
	request.Header.Set(workbenchbridge.InternalTimestampHeader, strconv.FormatInt(timestamp, 10))
	request.Header.Set(workbenchbridge.InternalNonceHeader, workbenchIdentityTestNonce)
	request.Header.Set(workbenchbridge.InternalSignatureHeader, signature)
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestIdentityStatusReturnsOnlyExistenceEnabledStateAndVersion(t *testing.T) {
	user := &model.User{
		Id:          42,
		Username:    "alice",
		DisplayName: "Alice",
		Role:        common.RoleAdminUser,
		Status:      common.UserStatusEnabled,
		Group:       "vip",
		Quota:       123456,
		Email:       "alice@example.com",
		CreatedAt:   1_700_000_000,
	}
	bridge, _ := newWorkbenchTestBridge(t, user)
	recorder := signedIdentityStatusRequest(t, bridge, user.Id, false)

	require.Equal(t, http.StatusOK, recorder.Code)
	responseBody := append([]byte(nil), recorder.Body.Bytes()...)
	var payload struct {
		Success bool                        `json:"success"`
		Data    workbenchIdentityStatusData `json:"data"`
	}
	require.NoError(t, common.Unmarshal(responseBody, &payload))
	require.True(t, payload.Success)
	assert.Equal(t, user.Id, payload.Data.UserID)
	assert.True(t, payload.Data.Exists)
	assert.True(t, payload.Data.Enabled)
	assert.Equal(t, workbenchbridge.IdentityVersion(identityFromUser(user)), payload.Data.IdentityVersion)
	assert.NotContains(t, string(responseBody), "quota")
	assert.NotContains(t, string(responseBody), "group")
	assert.NotContains(t, string(responseBody), "email")
	assert.NotContains(t, string(responseBody), "role")
	responseTimestamp, err := strconv.ParseInt(recorder.Header().Get(workbenchbridge.ResponseTimestampHeader), 10, 64)
	require.NoError(t, err)
	requestNonce := recorder.Header().Get(workbenchbridge.ResponseNonceHeader)
	assert.Equal(t, workbenchIdentityTestNonce, requestNonce)
	assert.Equal(t, workbenchbridge.SignInternalResponse(
		workbenchServiceTestSecret,
		http.StatusOK,
		"/api/internal/workbench/identity-status",
		responseTimestamp,
		requestNonce,
		responseBody,
	), recorder.Header().Get(workbenchbridge.ResponseSignatureHeader))
}

func TestIdentityStatusReportsMissingAndDisabledUsers(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		bridge, _ := newWorkbenchTestBridge(t, nil)
		recorder := signedIdentityStatusRequest(t, bridge, 99, false)
		require.Equal(t, http.StatusOK, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"exists":false`)
		assert.Contains(t, recorder.Body.String(), `"enabled":false`)
	})

	t.Run("disabled", func(t *testing.T) {
		bridge, _ := newWorkbenchTestBridge(t, &model.User{Id: 42, Username: "alice", Status: common.UserStatusDisabled})
		recorder := signedIdentityStatusRequest(t, bridge, 42, false)
		require.Equal(t, http.StatusOK, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"exists":true`)
		assert.Contains(t, recorder.Body.String(), `"enabled":false`)
	})
}

func TestAdminIdentityStatusRequiresMatchingVersionAndRootRole(t *testing.T) {
	user := &model.User{
		Id: 42, Username: "root", Role: common.RoleRootUser, Status: common.UserStatusEnabled,
	}
	bridge, _ := newWorkbenchTestBridge(t, user)
	identityVersion := workbenchbridge.IdentityVersion(identityFromUser(user))
	body, err := common.Marshal(workbenchIdentityStatusRequest{UserID: user.Id, IdentityVersion: identityVersion})
	require.NoError(t, err)
	timestamp := bridge.now().Unix()
	path := "/api/internal/workbench/admin-identity-status"
	signature := workbenchbridge.SignInternalRequest(workbenchServiceTestSecret, http.MethodPost, path, timestamp, workbenchIdentityTestNonce, body)

	router := gin.New()
	router.POST(path, bridge.AdminIdentityStatus)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.Header.Set(workbenchbridge.ContractVersionHeader, workbenchbridge.ContractVersion)
	request.Header.Set(workbenchbridge.InternalTimestampHeader, strconv.FormatInt(timestamp, 10))
	request.Header.Set(workbenchbridge.InternalNonceHeader, workbenchIdentityTestNonce)
	request.Header.Set(workbenchbridge.InternalSignatureHeader, signature)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, workbenchbridge.ContractVersion, recorder.Header().Get(workbenchbridge.ContractVersionHeader))
	assert.Contains(t, recorder.Body.String(), `"is_super_admin":true`)
}

func TestIdentityStatusRejectsInvalidSignatureBeforeUserLookup(t *testing.T) {
	lookupCalled := false
	config := workbenchbridge.Config{
		Enabled:             true,
		ControlURL:          "https://claw-control.internal",
		ControlHMACSecret:   workbenchControlTestSecret,
		ControlServiceName:  "new-api-core",
		ControlTimeout:      time.Second,
		ServiceHMACSecret:   workbenchServiceTestSecret,
		InternalRequestSkew: time.Minute,
	}
	bridge := NewWorkbenchIdentityBridge(config, nil, func(_ int) (*model.User, error) {
		lookupCalled = true
		return nil, errors.New("should not be called")
	})
	recorder := signedIdentityStatusRequest(t, bridge, 42, true)

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
	assert.False(t, lookupCalled)
}

func TestIdentityStatusRejectsMissingOrTamperedNonceBeforeUserLookup(t *testing.T) {
	for name, requestNonce := range map[string]string{
		"missing":  "",
		"tampered": "1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	} {
		t.Run(name, func(t *testing.T) {
			lookupCalled := false
			config := workbenchbridge.Config{
				Enabled:             true,
				ControlURL:          "https://claw-control.internal",
				ControlHMACSecret:   workbenchControlTestSecret,
				ControlServiceName:  "new-api-core",
				ControlTimeout:      time.Second,
				ServiceHMACSecret:   workbenchServiceTestSecret,
				InternalRequestSkew: time.Minute,
			}
			bridge := NewWorkbenchIdentityBridge(config, nil, func(_ int) (*model.User, error) {
				lookupCalled = true
				return nil, errors.New("should not be called")
			})
			body := []byte(`{"user_id":42}`)
			timestamp := bridge.now().Unix()
			path := "/api/internal/workbench/identity-status"
			signature := workbenchbridge.SignInternalRequest(
				workbenchServiceTestSecret, http.MethodPost, path, timestamp, workbenchIdentityTestNonce, body,
			)
			router := gin.New()
			router.POST(path, bridge.IdentityStatus)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
			request.Header.Set(workbenchbridge.ContractVersionHeader, workbenchbridge.ContractVersion)
			request.Header.Set(workbenchbridge.InternalTimestampHeader, strconv.FormatInt(timestamp, 10))
			if requestNonce != "" {
				request.Header.Set(workbenchbridge.InternalNonceHeader, requestNonce)
			}
			request.Header.Set(workbenchbridge.InternalSignatureHeader, signature)
			router.ServeHTTP(recorder, request)

			assert.Equal(t, http.StatusUnauthorized, recorder.Code)
			assert.False(t, lookupCalled)
		})
	}
}
