package agencyhub

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	app.ssoPublicKey = publicKey
	state := ssoStartTicket(t, app)
	now := time.Now().Unix()
	ticket, err := SignSSOTicket(privateKey, SSOTicketClaims{Issuer: "new-api", Audience: "agency-hub", Subject: 1, SourceSID: "sid-1", SessionVersion: 1, JTI: "cb-single-use", KeyID: "test-key", StateHash: hashState(state), IssuedAt: now, NotBefore: now, ExpiresAt: now + 60})
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
