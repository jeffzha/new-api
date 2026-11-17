package agencyhub

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

// ssoStartTicket obtains a fresh SSO state from the public start endpoint.
func ssoStartTicket(t *testing.T, app *App) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	app.Router().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/agency/sso/start", nil))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var envelope struct {
		Data struct {
			State string `json:"state"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.NotEmpty(t, envelope.Data.State)
	return envelope.Data.State
}

func TestSSOTicketVerifyRejectsWrongAudienceIssuerExpiryAndKey(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Now().Unix()
	base := SSOTicketClaims{Issuer: "new-api", Audience: "agency-hub", Subject: 1, SourceSID: "sid-1", SessionVersion: 1, JTI: "unit-ticket", KeyID: "test-key", IssuedAt: now, NotBefore: now, ExpiresAt: now + 60}

	valid, err := SignSSOTicket(privateKey, base)
	require.NoError(t, err)
	claims, err := VerifySSOTicket(publicKey, valid, "new-api", "agency-hub")
	require.NoError(t, err)
	require.Equal(t, int64(1), claims.Subject)

	mutate := func(jti string, fn func(*SSOTicketClaims)) string {
		c := base
		c.JTI = jti
		fn(&c)
		ticket, signErr := SignSSOTicket(privateKey, c)
		require.NoError(t, signErr)
		return ticket
	}

	wrongAud := mutate("unit-wrong-aud", func(c *SSOTicketClaims) { c.Audience = "attacker-app" })
	_, err = VerifySSOTicket(publicKey, wrongAud, "new-api", "agency-hub")
	require.Error(t, err)

	wrongIssuer := mutate("unit-wrong-iss", func(c *SSOTicketClaims) { c.Issuer = "evil-issuer" })
	_, err = VerifySSOTicket(publicKey, wrongIssuer, "new-api", "agency-hub")
	require.Error(t, err)

	expired := mutate("unit-expired", func(c *SSOTicketClaims) { c.IssuedAt, c.NotBefore, c.ExpiresAt = now-60, now-60, now-10 })
	_, err = VerifySSOTicket(publicKey, expired, "new-api", "agency-hub")
	require.Error(t, err)

	otherPublic, otherPrivate, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_ = otherPublic
	forged := mutate("unit-forged", func(*SSOTicketClaims) {})
	_ = forged
	forgedTicket, err := SignSSOTicket(otherPrivate, base)
	require.NoError(t, err)
	_, err = VerifySSOTicket(publicKey, forgedTicket, "new-api", "agency-hub")
	require.Error(t, err)
}

func TestSSOCallbackRejectsStateMismatchAndWrongAudience(t *testing.T) {
	app := newAgencyTestApp(t)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	app.ssoPublicKey = publicKey
	state := ssoStartTicket(t, app)
	now := time.Now().Unix()

	postback := func(ticket, postedState, cookieState string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/agency/sso/callback", strings.NewReader(`{"ticket":"`+ticket+`","state":"`+postedState+`"}`))
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(&http.Cookie{Name: "agency_sso_state", Value: cookieState, Path: "/agency"})
		app.Router().ServeHTTP(recorder, request)
		return recorder
	}

	// A ticket whose state_hash does not match the cookie's state is rejected.
	wrongStateTicket, err := SignSSOTicket(privateKey, SSOTicketClaims{Issuer: "new-api", Audience: "agency-hub", Subject: 1, SourceSID: "sid-1", SessionVersion: 1, JTI: "cb-wrong-state", KeyID: "test-key", StateHash: hashState("attacker-state"), IssuedAt: now, NotBefore: now, ExpiresAt: now + 60})
	require.NoError(t, err)
	recorder := postback(wrongStateTicket, state, state)
	require.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "invalid_ticket")

	// Posting a state that is not the cookie value fails the OAuth-style check.
	validStateTicket, err := SignSSOTicket(privateKey, SSOTicketClaims{Issuer: "new-api", Audience: "agency-hub", Subject: 1, SourceSID: "sid-1", SessionVersion: 1, JTI: "cb-state-mismatch", KeyID: "test-key", StateHash: hashState(state), IssuedAt: now, NotBefore: now, ExpiresAt: now + 60})
	require.NoError(t, err)
	badRecorder := postback(validStateTicket, "not-the-cookie-state", state)
	require.Equal(t, http.StatusUnauthorized, badRecorder.Code, badRecorder.Body.String())
	require.Contains(t, badRecorder.Body.String(), "invalid_state")

	// A ticket addressed to a different audience is rejected by the callback.
	wrongAudTicket, err := SignSSOTicket(privateKey, SSOTicketClaims{Issuer: "new-api", Audience: "agency-hub-verification", Subject: 1, SourceSID: "sid-1", SessionVersion: 1, JTI: "cb-wrong-aud", KeyID: "test-key", StateHash: hashState(state), IssuedAt: now, NotBefore: now, ExpiresAt: now + 60})
	require.NoError(t, err)
	audRecorder := postback(wrongAudTicket, state, state)
	require.Equal(t, http.StatusUnauthorized, audRecorder.Code, audRecorder.Body.String())
	require.Contains(t, audRecorder.Body.String(), "invalid_ticket")

	// An expired ticket is rejected even when state matches.
	expiredTicket, err := SignSSOTicket(privateKey, SSOTicketClaims{Issuer: "new-api", Audience: "agency-hub", Subject: 1, SourceSID: "sid-1", SessionVersion: 1, JTI: "cb-expired", KeyID: "test-key", StateHash: hashState(state), IssuedAt: now - 60, NotBefore: now - 60, ExpiresAt: now - 10})
	require.NoError(t, err)
	expRecorder := postback(expiredTicket, state, state)
	require.Equal(t, http.StatusUnauthorized, expRecorder.Code, expRecorder.Body.String())
	require.Contains(t, expRecorder.Body.String(), "invalid_ticket")
}

func TestSSOCallbackTicketIsSingleUse(t *testing.T) {
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.AutoMigrate(&model.User{}, &model.UserSession{}))
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	app.ssoPublicKey = publicKey
	root := model.User{Username: "single-use-root", Password: "unused-password", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, app.db.Create(&root).Error)
	source := model.UserSession{SID: "single-use-source", UserID: root.Id, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, RefreshHash: "single-use-refresh", LoginMethod: "password", LastActiveAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()}
	require.NoError(t, app.db.Create(&source).Error)
	state := ssoStartTicket(t, app)
	now := time.Now().Unix()
	ticket, err := SignSSOTicket(privateKey, SSOTicketClaims{Issuer: "new-api", Audience: "agency-hub", Subject: int64(root.Id), SourceSID: source.SID, UserAuthVersion: 1, SessionVersion: 1, JTI: "cb-single-use", KeyID: "test-key", ActorType: ActorTypeRoot, StateHash: hashState(state), IssuedAt: now, NotBefore: now, ExpiresAt: now + 60})
	require.NoError(t, err)
	call := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/agency/sso/callback", strings.NewReader(`{"ticket":"`+ticket+`","state":"`+state+`"}`))
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(&http.Cookie{Name: "agency_sso_state", Value: state, Path: "/agency"})
		app.Router().ServeHTTP(recorder, request)
		return recorder
	}
	first := call()
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Contains(t, first.Body.String(), "actor_type")

	second := call()
	require.Equal(t, http.StatusUnauthorized, second.Code, second.Body.String())
	require.Contains(t, second.Body.String(), "replayed")
}

type operatorIdentityFixture struct {
	app           *App
	privateKey    ed25519.PrivateKey
	agency        model.Agency
	operator      model.AgencyOperatorAccount
	platformUser  model.User
	sourceSession model.UserSession
	directToken   string
	directCSRF    string
	password      string
}

func newOperatorIdentityFixture(t *testing.T) operatorIdentityFixture {
	t.Helper()
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.AutoMigrate(&model.User{}, &model.UserSession{}))
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	app.ssoPublicKey = publicKey
	now := time.Now().Unix()
	agency := model.Agency{Depth: 1, Code: "identity-agency-" + strings.ReplaceAll(t.Name(), "/", "-"), DisplayName: "Identity Agency", Status: AgencyStatusActive, InviteCode: "ID" + common.GetUUID()[:10], Version: 1, CreatedByType: ActorTypeRoot, CreatedByID: 1, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, app.db.Create(&agency).Error)
	password := "Agency-identity-2026!"
	passwordHash, err := common.Password2Hash(password)
	require.NoError(t, err)
	operator := model.AgencyOperatorAccount{AgencyID: agency.ID, NormalizedUsername: "identity-operator-" + common.GetUUID()[:8], Username: "identity-operator", PasswordHash: passwordHash, Status: OperatorStatusActive, AuthVersion: 1, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, app.db.Create(&operator).Error)
	platformUser := model.User{Username: "identity-user-" + common.GetUUID()[:8], Password: "unused-password", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, app.db.Create(&platformUser).Error)
	source := model.UserSession{SID: "identity-source-" + common.GetUUID()[:8], UserID: platformUser.Id, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, RefreshHash: "identity-refresh-" + common.GetUUID()[:8], LoginMethod: "password", LastActiveAt: now, ExpiresAt: now + 3600}
	require.NoError(t, app.db.Create(&source).Error)
	directToken, err := randomToken(32)
	require.NoError(t, err)
	directCSRF, err := randomToken(24)
	require.NoError(t, err)
	directSession := model.AgencySession{TokenHash: tokenHash(directToken), ActorType: ActorTypeOperator, ActorID: operator.ID, AgencyID: &agency.ID, AuthVersion: operator.AuthVersion, CSRFHash: tokenHash(directCSRF), LastSeenAt: now, CreatedAt: now, ExpiresAt: now + 3600}
	require.NoError(t, app.db.Create(&directSession).Error)
	return operatorIdentityFixture{app: app, privateKey: privateKey, agency: agency, operator: operator, platformUser: platformUser, sourceSession: source, directToken: directToken, directCSRF: directCSRF, password: password}
}

func (fixture operatorIdentityFixture) linkProof(t *testing.T, requestID, jti string, mutate func(*SSOTicketClaims)) string {
	t.Helper()
	now := time.Now().Unix()
	claims := SSOTicketClaims{Issuer: "new-api", Audience: "agency-hub-account-link", Subject: int64(fixture.platformUser.Id), SourceSID: fixture.sourceSession.SID, UserAuthVersion: fixture.platformUser.AuthVersion, SessionVersion: fixture.sourceSession.Version, JTI: jti, KeyID: "test-key", ActorType: ActorTypePlatformUser, ObjectID: requestID, IssuedAt: now, NotBefore: now, ExpiresAt: now + 60}
	if mutate != nil {
		mutate(&claims)
	}
	proof, err := SignSSOTicket(fixture.privateKey, claims)
	require.NoError(t, err)
	return proof
}

func (fixture operatorIdentityFixture) postOperator(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", fixture.directCSRF)
	request.AddCookie(&http.Cookie{Name: fixture.app.config.CookieName, Value: fixture.directToken})
	fixture.app.Router().ServeHTTP(recorder, request)
	return recorder
}

func (fixture operatorIdentityFixture) createLinkedSSOSession(t *testing.T, jti string, mutate func(*SSOTicketClaims)) string {
	t.Helper()
	state := ssoStartTicket(t, fixture.app)
	now := time.Now().Unix()
	claims := SSOTicketClaims{Issuer: "new-api", Audience: "agency-hub", Subject: int64(fixture.platformUser.Id), SourceSID: fixture.sourceSession.SID, UserAuthVersion: fixture.platformUser.AuthVersion, SessionVersion: fixture.sourceSession.Version, StateHash: hashState(state), JTI: jti, KeyID: "test-key", ActorType: ActorTypeOperator, ActorID: fixture.operator.ID, AgencyID: fixture.agency.ID, IssuedAt: now, NotBefore: now, ExpiresAt: now + 60}
	if mutate != nil {
		mutate(&claims)
	}
	ticket, err := SignSSOTicket(fixture.privateKey, claims)
	require.NoError(t, err)
	callback := httptest.NewRecorder()
	callbackRequest := httptest.NewRequest(http.MethodPost, "/agency/sso/callback", strings.NewReader(`{"ticket":"`+ticket+`","state":"`+state+`"}`))
	callbackRequest.Header.Set("Content-Type", "application/json")
	callbackRequest.AddCookie(&http.Cookie{Name: "agency_sso_state", Value: state, Path: "/agency"})
	fixture.app.Router().ServeHTTP(callback, callbackRequest)
	require.Equal(t, http.StatusOK, callback.Code, callback.Body.String())
	for _, cookie := range callback.Result().Cookies() {
		if cookie.Name == fixture.app.config.CookieName {
			return cookie.Value
		}
	}
	require.FailNow(t, "SSO callback did not set the agency session cookie")
	return ""
}

func (fixture operatorIdentityFixture) getMe(t *testing.T, token string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/agency/api/v1/auth/me", nil)
	request.AddCookie(&http.Cookie{Name: fixture.app.config.CookieName, Value: token})
	fixture.app.Router().ServeHTTP(recorder, request)
	return recorder
}

func TestPlatformAccountLinkRequiresBothAuthenticatedAccountsAndSingleUseProof(t *testing.T) {
	fixture := newOperatorIdentityFixture(t)
	requestID := "link-request-1"
	proof := fixture.linkProof(t, requestID, "link-proof-1", nil)
	wrongPassword := fixture.postOperator(t, "/agency/api/v1/auth/platform-link", `{"password":"wrong","proof":"`+proof+`","request_id":"`+requestID+`"}`)
	require.Equal(t, http.StatusUnauthorized, wrongPassword.Code, wrongPassword.Body.String())

	linked := fixture.postOperator(t, "/agency/api/v1/auth/platform-link", `{"password":"`+fixture.password+`","proof":"`+proof+`","request_id":"`+requestID+`"}`)
	require.Equal(t, http.StatusOK, linked.Code, linked.Body.String())
	var binding model.AgencyOperatorIdentityBinding
	require.NoError(t, fixture.app.db.Where("operator_account_id = ?", fixture.operator.ID).First(&binding).Error)
	require.Equal(t, int64(fixture.platformUser.Id), binding.PlatformUserID)

	replayed := fixture.postOperator(t, "/agency/api/v1/auth/platform-link", `{"password":"`+fixture.password+`","proof":"`+proof+`","request_id":"`+requestID+`"}`)
	require.Equal(t, http.StatusUnauthorized, replayed.Code, replayed.Body.String())
	require.Contains(t, replayed.Body.String(), "invalid_link_proof")
}

func TestPlatformAccountLinkRejectsRootMismatchedRequestAndDuplicateIdentity(t *testing.T) {
	fixture := newOperatorIdentityFixture(t)
	requestID := "link-request-scope"
	rootProof := fixture.linkProof(t, requestID, "root-link-proof", func(claims *SSOTicketClaims) {
		claims.ActorType = ActorTypeRoot
	})
	rootAttempt := fixture.postOperator(t, "/agency/api/v1/auth/platform-link", `{"password":"`+fixture.password+`","proof":"`+rootProof+`","request_id":"`+requestID+`"}`)
	require.Equal(t, http.StatusUnauthorized, rootAttempt.Code, rootAttempt.Body.String())

	mismatchProof := fixture.linkProof(t, requestID, "mismatch-link-proof", nil)
	mismatch := fixture.postOperator(t, "/agency/api/v1/auth/platform-link", `{"password":"`+fixture.password+`","proof":"`+mismatchProof+`","request_id":"other-request"}`)
	require.Equal(t, http.StatusUnauthorized, mismatch.Code, mismatch.Body.String())

	validProof := fixture.linkProof(t, requestID, "valid-link-proof", nil)
	linked := fixture.postOperator(t, "/agency/api/v1/auth/platform-link", `{"password":"`+fixture.password+`","proof":"`+validProof+`","request_id":"`+requestID+`"}`)
	require.Equal(t, http.StatusOK, linked.Code, linked.Body.String())

	now := time.Now().Unix()
	secondAgency := model.Agency{Depth: 1, Code: "identity-agency-second-" + common.GetUUID()[:8], DisplayName: "Second Identity Agency", Status: AgencyStatusActive, InviteCode: "ID" + common.GetUUID()[:10], Version: 1, CreatedByType: ActorTypeRoot, CreatedByID: 1, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, fixture.app.db.Create(&secondAgency).Error)
	passwordHash, err := common.Password2Hash(fixture.password)
	require.NoError(t, err)
	secondOperator := model.AgencyOperatorAccount{AgencyID: secondAgency.ID, NormalizedUsername: "identity-operator-second-" + common.GetUUID()[:8], Username: "identity-operator-second", PasswordHash: passwordHash, Status: OperatorStatusActive, AuthVersion: 1, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, fixture.app.db.Create(&secondOperator).Error)
	secondToken, err := randomToken(32)
	require.NoError(t, err)
	secondCSRF, err := randomToken(24)
	require.NoError(t, err)
	require.NoError(t, fixture.app.db.Create(&model.AgencySession{TokenHash: tokenHash(secondToken), ActorType: ActorTypeOperator, ActorID: secondOperator.ID, AgencyID: &secondAgency.ID, AuthVersion: secondOperator.AuthVersion, CSRFHash: tokenHash(secondCSRF), LastSeenAt: now, CreatedAt: now, ExpiresAt: now + 3600}).Error)
	second := fixture
	second.agency = secondAgency
	second.operator = secondOperator
	second.directToken = secondToken
	second.directCSRF = secondCSRF
	duplicateProof := fixture.linkProof(t, "duplicate-user", "duplicate-user-proof", nil)
	duplicate := second.postOperator(t, "/agency/api/v1/auth/platform-link", `{"password":"`+second.password+`","proof":"`+duplicateProof+`","request_id":"duplicate-user"}`)
	require.Equal(t, http.StatusConflict, duplicate.Code, duplicate.Body.String())
	require.Contains(t, duplicate.Body.String(), "link_failed")
}

func TestLinkedOperatorSSOTracksPlatformSessionAndUnlinkPreservesPasswordSession(t *testing.T) {
	fixture := newOperatorIdentityFixture(t)
	requestID := "linked-sso-request"
	proof := fixture.linkProof(t, requestID, "linked-sso-link-proof", nil)
	linked := fixture.postOperator(t, "/agency/api/v1/auth/platform-link", `{"password":"`+fixture.password+`","proof":"`+proof+`","request_id":"`+requestID+`"}`)
	require.Equal(t, http.StatusOK, linked.Code, linked.Body.String())

	linkedToken := fixture.createLinkedSSOSession(t, "linked-sso-ticket", nil)
	require.Equal(t, http.StatusOK, fixture.getMe(t, linkedToken).Code)
	unlinked := fixture.postOperator(t, "/agency/api/v1/auth/platform-link/remove", `{"password":"`+fixture.password+`"}`)
	require.Equal(t, http.StatusOK, unlinked.Code, unlinked.Body.String())
	require.Equal(t, http.StatusUnauthorized, fixture.getMe(t, linkedToken).Code)
	require.Equal(t, http.StatusOK, fixture.getMe(t, fixture.directToken).Code)
}

func TestLinkedOperatorSSORejectsMismatchedActorAndAgencyClaims(t *testing.T) {
	fixture := newOperatorIdentityFixture(t)
	proof := fixture.linkProof(t, "claim-scope-link", "claim-scope-proof", nil)
	linked := fixture.postOperator(t, "/agency/api/v1/auth/platform-link", `{"password":"`+fixture.password+`","proof":"`+proof+`","request_id":"claim-scope-link"}`)
	require.Equal(t, http.StatusOK, linked.Code, linked.Body.String())

	tests := []struct {
		name   string
		mutate func(*SSOTicketClaims)
	}{
		{name: "operator", mutate: func(claims *SSOTicketClaims) { claims.ActorID++ }},
		{name: "agency", mutate: func(claims *SSOTicketClaims) { claims.AgencyID++ }},
		{name: "platform user", mutate: func(claims *SSOTicketClaims) { claims.Subject++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := ssoStartTicket(t, fixture.app)
			now := time.Now().Unix()
			claims := SSOTicketClaims{Issuer: "new-api", Audience: "agency-hub", Subject: int64(fixture.platformUser.Id), SourceSID: fixture.sourceSession.SID, UserAuthVersion: fixture.platformUser.AuthVersion, SessionVersion: fixture.sourceSession.Version, StateHash: hashState(state), JTI: "invalid-scope-" + test.name, KeyID: "test-key", ActorType: ActorTypeOperator, ActorID: fixture.operator.ID, AgencyID: fixture.agency.ID, IssuedAt: now, NotBefore: now, ExpiresAt: now + 60}
			test.mutate(&claims)
			ticket, err := SignSSOTicket(fixture.privateKey, claims)
			require.NoError(t, err)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/agency/sso/callback", strings.NewReader(`{"ticket":"`+ticket+`","state":"`+state+`"}`))
			request.Header.Set("Content-Type", "application/json")
			request.AddCookie(&http.Cookie{Name: "agency_sso_state", Value: state, Path: "/agency"})
			fixture.app.Router().ServeHTTP(recorder, request)
			require.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
			require.Contains(t, recorder.Body.String(), "session_failed")
		})
	}
}

func TestLinkedOperatorSessionTracksAllLinkedIdentityState(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(operatorIdentityFixture)
	}{
		{name: "source logout", mutate: func(fixture operatorIdentityFixture) {
			now := time.Now().Unix()
			require.NoError(t, fixture.app.db.Model(&model.UserSession{}).Where("sid = ?", fixture.sourceSession.SID).Updates(map[string]any{"status": model.UserSessionStatusRevoked, "revoked_at": now}).Error)
		}},
		{name: "source version", mutate: func(fixture operatorIdentityFixture) {
			require.NoError(t, fixture.app.db.Model(&model.UserSession{}).Where("sid = ?", fixture.sourceSession.SID).Update("version", fixture.sourceSession.Version+1).Error)
		}},
		{name: "platform user disabled", mutate: func(fixture operatorIdentityFixture) {
			require.NoError(t, fixture.app.db.Model(&model.User{}).Where("id = ?", fixture.platformUser.Id).Update("status", common.UserStatusDisabled).Error)
		}},
		{name: "platform auth version", mutate: func(fixture operatorIdentityFixture) {
			require.NoError(t, fixture.app.db.Model(&model.User{}).Where("id = ?", fixture.platformUser.Id).Update("auth_version", fixture.platformUser.AuthVersion+1).Error)
		}},
		{name: "operator disabled", mutate: func(fixture operatorIdentityFixture) {
			require.NoError(t, fixture.app.db.Model(&model.AgencyOperatorAccount{}).Where("id = ?", fixture.operator.ID).Update("status", OperatorStatusDisabled).Error)
		}},
		{name: "operator auth version", mutate: func(fixture operatorIdentityFixture) {
			require.NoError(t, fixture.app.db.Model(&model.AgencyOperatorAccount{}).Where("id = ?", fixture.operator.ID).Update("auth_version", fixture.operator.AuthVersion+1).Error)
		}},
		{name: "agency disabled", mutate: func(fixture operatorIdentityFixture) {
			require.NoError(t, fixture.app.db.Model(&model.Agency{}).Where("id = ?", fixture.agency.ID).Update("status", AgencyStatusDisabled).Error)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newOperatorIdentityFixture(t)
			proof := fixture.linkProof(t, "state-link-"+test.name, "state-proof-"+test.name, nil)
			linked := fixture.postOperator(t, "/agency/api/v1/auth/platform-link", `{"password":"`+fixture.password+`","proof":"`+proof+`","request_id":"state-link-`+test.name+`"}`)
			require.Equal(t, http.StatusOK, linked.Code, linked.Body.String())
			token := fixture.createLinkedSSOSession(t, "state-ticket-"+test.name, nil)
			test.mutate(fixture)
			response := fixture.getMe(t, token)
			require.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, response.Code, response.Body.String())
		})
	}
}

func TestPlatformAccountLinkPasswordFailuresTriggerLockout(t *testing.T) {
	fixture := newOperatorIdentityFixture(t)
	for attempt := range fixture.app.config.MaxLoginAttempts {
		proof := fixture.linkProof(t, "lockout-link", fmt.Sprintf("lockout-proof-%d", attempt), nil)
		response := fixture.postOperator(t, "/agency/api/v1/auth/platform-link", `{"password":"wrong","proof":"`+proof+`","request_id":"lockout-link"}`)
		require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
	}
	proof := fixture.linkProof(t, "lockout-link", "lockout-proof-final", nil)
	locked := fixture.postOperator(t, "/agency/api/v1/auth/platform-link", `{"password":"`+fixture.password+`","proof":"`+proof+`","request_id":"lockout-link"}`)
	require.Equal(t, http.StatusTooManyRequests, locked.Code, locked.Body.String())
	require.Contains(t, locked.Body.String(), "account_locked")
}

func TestSourceSessionRevocationInvalidatesRootSession(t *testing.T) {
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.AutoMigrate(&model.User{}, &model.UserSession{}))
	root := model.User{Username: "sso-revoke-root", Password: "password", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, app.db.Create(&root).Error)
	source := model.UserSession{SID: "sso-revoke-source", UserID: root.Id, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, RefreshHash: "refresh-sso-revoke", LoginMethod: "password", LastActiveAt: 1, ExpiresAt: 4102444800}
	require.NoError(t, app.db.Create(&source).Error)
	sessionToken, _, err := app.CreateRootSession(int64(root.Id), source.SID, source.Version)
	require.NoError(t, err)
	me := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/agency/api/v1/auth/me", nil)
		request.AddCookie(&http.Cookie{Name: app.config.CookieName, Value: sessionToken})
		app.Router().ServeHTTP(recorder, request)
		return recorder
	}
	require.Equal(t, http.StatusOK, me().Code, me().Body.String())

	now := time.Now().Unix()
	require.NoError(t, app.db.Model(&model.UserSession{}).Where("sid = ?", source.SID).Updates(map[string]any{"status": model.UserSessionStatusRevoked, "revoked_at": now}).Error)
	revoked := me()
	require.Equal(t, http.StatusUnauthorized, revoked.Code, revoked.Body.String())
	require.Contains(t, revoked.Body.String(), "source_session_invalid")
}

func TestSourceSessionDowngradeInvalidatesRootSession(t *testing.T) {
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.AutoMigrate(&model.User{}, &model.UserSession{}))
	root := model.User{Username: "sso-downgrade-root", Password: "password", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, app.db.Create(&root).Error)
	source := model.UserSession{SID: "sso-downgrade-source", UserID: root.Id, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, RefreshHash: "refresh-sso-downgrade", LoginMethod: "password", LastActiveAt: 1, ExpiresAt: 4102444800}
	require.NoError(t, app.db.Create(&source).Error)
	sessionToken, _, err := app.CreateRootSession(int64(root.Id), source.SID, source.Version)
	require.NoError(t, err)
	require.NoError(t, app.db.Model(&model.UserSession{}).Where("sid = ?", source.SID).Update("version", 2).Error)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/agency/api/v1/auth/me", nil)
	request.AddCookie(&http.Cookie{Name: app.config.CookieName, Value: sessionToken})
	app.Router().ServeHTTP(recorder, request)
	require.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "source_session_invalid")
}

func TestRootVerificationProofIsSingleUseAndBodyBound(t *testing.T) {
	app := newAgencyTestApp(t)
	t.Setenv("AGENCY_HUB_DELIVERY_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	publicKey, privateKey, sessionToken, csrf, rootID, sourceSID, sourceVersion := newRootEvidence(t, app)
	_ = publicKey
	now := time.Now().Unix()
	body := `{"display_name":"API Agency","operator_username":"api_operator","pricing":{"default_settlement_bps":7500,"default_sales_bps":9000,"min_spread_bps":500,"sales_cap_bps":30000}}`
	bodyHash := idempotencyHash(body)
	signProof := func(jti string, fn func(*SSOTicketClaims)) string {
		c := SSOTicketClaims{Issuer: "new-api", Audience: "agency-hub-verification", Subject: rootID, SourceSID: sourceSID, SessionVersion: sourceVersion, JTI: jti, KeyID: "test-key", Action: "agency.create", ObjectID: "agency:new", BodyHash: bodyHash, IssuedAt: now, NotBefore: now, ExpiresAt: now + 300}
		fn(&c)
		signed, signErr := SignSSOTicket(privateKey, c)
		require.NoError(t, signErr)
		return signed
	}
	create := func(key, proofValue string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/agency/api/v1/root/agencies", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", csrf)
		request.Header.Set("Idempotency-Key", key)
		request.Header.Set("X-Agency-Verification-Proof", proofValue)
		request.AddCookie(&http.Cookie{Name: app.config.CookieName, Value: sessionToken})
		app.Router().ServeHTTP(recorder, request)
		return recorder
	}

	first := create("proof-use-1", signProof("proof-jti-1", func(*SSOTicketClaims) {}))
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())

	replayed := create("proof-use-2", signProof("proof-jti-1", func(*SSOTicketClaims) {}))
	require.Equal(t, http.StatusForbidden, replayed.Code, replayed.Body.String())
	require.Contains(t, replayed.Body.String(), "invalid_verification")

	otherHash := idempotencyHash(`{"display_name":"API Agency","operator_username":"api_operator2","pricing":{"default_settlement_bps":7500,"default_sales_bps":9000,"min_spread_bps":500,"sales_cap_bps":30000}}`)
	bodySwap := create("proof-use-3", signProof("proof-jti-body", func(c *SSOTicketClaims) { c.BodyHash = otherHash }))
	require.Equal(t, http.StatusForbidden, bodySwap.Code, bodySwap.Body.String())
	require.Contains(t, bodySwap.Body.String(), "invalid_verification")
}

func TestRootVerificationProofScopeAndSessionMustMatch(t *testing.T) {
	app := newAgencyTestApp(t)
	publicKey, privateKey, sessionToken, csrf, rootID, sourceSID, sourceVersion := newRootEvidence(t, app)
	_ = publicKey
	now := time.Now().Unix()
	body := `{"display_name":"API Agency","operator_username":"api_operator","pricing":{"default_settlement_bps":7500,"default_sales_bps":9000,"min_spread_bps":500,"sales_cap_bps":30000}}`
	bodyHash := idempotencyHash(body)
	signProof := func(jti string, fn func(*SSOTicketClaims)) string {
		c := SSOTicketClaims{Issuer: "new-api", Audience: "agency-hub-verification", Subject: rootID, SourceSID: sourceSID, SessionVersion: sourceVersion, JTI: jti, KeyID: "test-key", Action: "agency.create", ObjectID: "agency:new", BodyHash: bodyHash, IssuedAt: now, NotBefore: now, ExpiresAt: now + 300}
		fn(&c)
		signed, signErr := SignSSOTicket(privateKey, c)
		require.NoError(t, signErr)
		return signed
	}
	create := func(key, proofValue string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/agency/api/v1/root/agencies", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", csrf)
		request.Header.Set("Idempotency-Key", key)
		request.Header.Set("X-Agency-Verification-Proof", proofValue)
		request.AddCookie(&http.Cookie{Name: app.config.CookieName, Value: sessionToken})
		app.Router().ServeHTTP(recorder, request)
		return recorder
	}

	wrongScope := create("proof-scope-1", signProof("proof-scope-wrong", func(c *SSOTicketClaims) { c.Action, c.ObjectID = "agency.update", "agency:8" }))
	require.Equal(t, http.StatusForbidden, wrongScope.Code, wrongScope.Body.String())
	require.Contains(t, wrongScope.Body.String(), "invalid_verification")

	staleVersion := create("proof-scope-2", signProof("proof-version-stale", func(c *SSOTicketClaims) { c.SessionVersion = sourceVersion + 1 }))
	require.Equal(t, http.StatusForbidden, staleVersion.Code, staleVersion.Body.String())
	require.Contains(t, staleVersion.Body.String(), "invalid_verification")

	wrongKey := create("proof-scope-3", signProof("proof-key-forged", func(c *SSOTicketClaims) { c.JTI = "proof-key-forged" }))
	signed, err := SignSSOTicket(privateKey, SSOTicketClaims{Issuer: "new-api", Audience: "agency-hub-verification", Subject: rootID, SourceSID: sourceSID, SessionVersion: sourceVersion, JTI: "proof-key-forged2", KeyID: "test-key", Action: "agency.create", ObjectID: "agency:new", BodyHash: bodyHash, IssuedAt: now, NotBefore: now, ExpiresAt: now + 300})
	require.NoError(t, err)
	_ = signed
	_ = wrongKey
}

func newRootEvidence(t *testing.T, app *App) (ed25519.PublicKey, ed25519.PrivateKey, string, string, int64, string, int64) {
	t.Helper()
	require.NoError(t, app.db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.Task{}))
	root := model.User{Username: "sso-proof-root-" + strings.ReplaceAll(t.Name(), "/", "-"), Password: "password", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, app.db.Create(&root).Error)
	source := model.UserSession{SID: "proof-source-" + strings.ReplaceAll(t.Name(), "/", "-"), UserID: root.Id, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, RefreshHash: "refresh-proof-" + strings.ReplaceAll(t.Name(), "/", "-"), LoginMethod: "password", LastActiveAt: 1, ExpiresAt: 4102444800}
	require.NoError(t, app.db.Create(&source).Error)
	sessionToken, csrf, err := app.CreateRootSession(int64(root.Id), source.SID, source.Version)
	require.NoError(t, err)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	app.ssoPublicKey = publicKey
	return publicKey, privateKey, sessionToken, csrf, int64(root.Id), source.SID, source.Version
}

func idempotencyHash(body string) string {
	digest := sha256.Sum256(normalizeIdempotencyBody([]byte(body)))
	return hex.EncodeToString(digest[:])
}
