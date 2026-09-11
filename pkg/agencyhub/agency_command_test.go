package agencyhub

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func signedCommandRequest(t *testing.T, app *App, commandID, action string, expiry time.Time) (AgencyCommandRequest, []byte, ed25519.PrivateKey, ed25519.PrivateKey) {
	t.Helper()
	rootPublic, rootPrivate, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	servicePublic, servicePrivate, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	app.SetSSOPublicKey(rootPublic)
	app.SetCommandServicePublicKey(servicePublic)
	payload := []byte(`{"user_id":7,"reason":"test"}`)
	bodyHash, err := CommandBodyHash(payload)
	require.NoError(t, err)
	now := time.Now().Unix()
	proof, err := SignSSOTicket(rootPrivate, SSOTicketClaims{Issuer: "new-api", Audience: "agency-gateway-command", Subject: 1, SourceSID: "sid-1", JTI: "proof-" + commandID, KeyID: "root-k1", Action: action, CommandID: commandID, ObjectID: "obj-1", ExpectedVersion: 3, BodyHash: bodyHash, IssuedAt: now, NotBefore: now, ExpiresAt: expiry.Unix()})
	require.NoError(t, err)
	req := AgencyCommandRequest{CommandID: commandID, Action: action, Actor: "root:1", SourceSID: "sid-1", ObjectID: "obj-1", ExpectedVersion: 3, Payload: payload, IssuedAt: now, ExpiresAt: expiry.Unix(), BodyHash: bodyHash, RootProof: proof}
	signature, err := SignCommandEnvelope(servicePrivate, req)
	require.NoError(t, err)
	req.HubSignature = signature
	body, err := common.Marshal(req)
	require.NoError(t, err)
	return req, body, rootPrivate, servicePrivate
}

func TestRootFundingReversalEndpointEnqueuesGatewayCommand(t *testing.T) {
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.AutoMigrate(&model.User{}, &model.UserSession{}))
	root := model.User{Username: "root-funding-reversal", Password: "password", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, app.db.Create(&root).Error)
	source := model.UserSession{SID: "root-funding-reversal-session", UserID: root.Id, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, RefreshHash: "refresh-root-funding-reversal", LoginMethod: "password", LastActiveAt: 1, ExpiresAt: 4102444800}
	require.NoError(t, app.db.Create(&source).Error)
	sessionToken, csrf, err := app.CreateRootSession(int64(root.Id), source.SID, source.Version)
	require.NoError(t, err)
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	app.SetSSOPublicKey(publicKey)
	servicePublic, servicePrivate, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	app.SetCommandServicePublicKey(servicePublic)
	app.SetCommandServicePrivateKey(servicePrivate)
	payloadBytes, err := common.Marshal(fundingReversalPayload{
		OriginalOperationID: "topup-command-1",
		RefundID:            "refund-command-1",
		UserID:              1007,
		RefundQuota:         88,
		CurrencyCode:        "CNY",
		PaymentReference:    "pay-command-1",
		EvidenceRef:         "evidence-command-1",
		Reason:              "chargeback",
	})
	require.NoError(t, err)
	bodyHash, err := CommandBodyHash(payloadBytes)
	require.NoError(t, err)
	now := time.Now().Unix()
	proof, err := SignSSOTicket(privateKey, SSOTicketClaims{
		Issuer: "new-api", Audience: "agency-gateway-command", Subject: int64(root.Id),
		SourceSID: source.SID, UserAuthVersion: 1, SessionVersion: source.Version,
		JTI: "root-funding-reversal-proof", KeyID: "root-k1",
		Action: CommandActionFundingReverse, CommandID: "funding-reversal-command-1",
		ObjectID: "topup-command-1", BodyHash: bodyHash, IssuedAt: now, NotBefore: now, ExpiresAt: now + 300,
	})
	require.NoError(t, err)
	body := `{"command_id":"funding-reversal-command-1","object_id":"topup-command-1","root_proof":"` + proof + `","original_operation_id":"topup-command-1","refund_id":"refund-command-1","user_id":1007,"refund_quota":88,"currency_code":"CNY","payment_reference":"pay-command-1","evidence_ref":"evidence-command-1","reason":"chargeback"}`
	request := httptest.NewRequest(http.MethodPost, "/agency/api/v1/root/funding/reversals", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	request.Header.Set("Idempotency-Key", "funding-reversal-submit")
	request.AddCookie(&http.Cookie{Name: app.config.CookieName, Value: sessionToken})
	recorder := httptest.NewRecorder()
	app.Router().ServeHTTP(recorder, request)
	require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())

	var command model.AgencyCommand
	require.NoError(t, app.db.Where("command_id = ?", "funding-reversal-command-1").First(&command).Error)
	require.Equal(t, CommandActionFundingReverse, command.Action)
	require.Equal(t, CommandStatusQueued, command.Status)
	require.Equal(t, "root:"+stringID(int64(root.Id)), command.Actor)
	require.Equal(t, source.SID, command.SourceSID)
	require.Equal(t, bodyHash, command.BodyHash)
	require.NotEqual(t, "browser-root:"+bodyHash, command.HubSignature)
	require.NotEmpty(t, command.HubSignature)
	require.NotEmpty(t, command.RootProofJTI)
	var payload map[string]any
	require.NoError(t, common.Unmarshal([]byte(command.Payload), &payload))
	require.Equal(t, "refund-command-1", payload["refund_id"])
	require.EqualValues(t, 88, payload["refund_quota"])

	replay := httptest.NewRequest(http.MethodPost, "/agency/api/v1/root/funding/reversals", strings.NewReader(body))
	replay.Header.Set("Content-Type", "application/json")
	replay.Header.Set("X-CSRF-Token", csrf)
	replay.Header.Set("Idempotency-Key", "funding-reversal-submit")
	replay.AddCookie(&http.Cookie{Name: app.config.CookieName, Value: sessionToken})
	replayRecorder := httptest.NewRecorder()
	app.Router().ServeHTTP(replayRecorder, replay)
	require.Equal(t, http.StatusAccepted, replayRecorder.Code, replayRecorder.Body.String())
	var count int64
	require.NoError(t, app.db.Model(&model.AgencyCommand{}).Where("command_id = ?", "funding-reversal-command-1").Count(&count).Error)
	require.Equal(t, int64(1), count)

	decodedHash, err := hex.DecodeString(command.BodyHash)
	require.NoError(t, err)
	require.Len(t, decodedHash, 32)
}

func TestInternalCommandAcceptsAndIdempotentlyReplays(t *testing.T) {
	app := newAgencyTestApp(t)
	req, body, _, servicePrivate := signedCommandRequest(t, app, "cmd-1", CommandActionProvisioningStart, time.Now().Add(2*time.Minute))
	first := httptest.NewRecorder()
	app.Router().ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/internal/agency/v1/commands", bytes.NewReader(body)))
	require.Equal(t, http.StatusAccepted, first.Code)
	second := httptest.NewRecorder()
	app.Router().ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/internal/agency/v1/commands", bytes.NewReader(body)))
	require.Equal(t, http.StatusAccepted, second.Code)
	var stored model.AgencyCommand
	require.NoError(t, app.db.Where("command_id = ?", req.CommandID).First(&stored).Error)
	require.Equal(t, CommandStatusQueued, stored.Status)
	// GET requires a fresh service signature over the stored envelope.
	getSignature, err := SignCommandEnvelope(servicePrivate, req)
	require.NoError(t, err)
	get := httptest.NewRequest(http.MethodGet, "/internal/agency/v1/commands/"+req.CommandID, nil)
	get.Header.Set(CommandServiceSignatureHeader, getSignature)
	result := httptest.NewRecorder()
	app.Router().ServeHTTP(result, get)
	require.Equal(t, http.StatusOK, result.Code)
}

func TestInternalCommandRejectsProofAndPayloadConflicts(t *testing.T) {
	app := newAgencyTestApp(t)
	request, body, _, servicePrivate := signedCommandRequest(t, app, "cmd-2", CommandActionProvisioningStart, time.Now().Add(2*time.Minute))
	require.NoError(t, common.Unmarshal(body, &request))
	request.Payload = []byte(`{"user_id":7,"unknown":true}`)
	request.HubSignature, _ = SignCommandEnvelope(servicePrivate, request)
	rewritten, err := common.Marshal(request)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	app.Router().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/internal/agency/v1/commands", bytes.NewReader(rewritten)))
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code)

	// Expiry is checked before cryptographic acceptance, so an old command is
	// never persisted even when its envelope is otherwise well formed.
	_, expired, _, _ := signedCommandRequest(t, app, "cmd-expired", CommandActionProvisioningStart, time.Now().Add(-time.Minute))
	recorder = httptest.NewRecorder()
	app.Router().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/internal/agency/v1/commands", bytes.NewReader(expired)))
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code)
}

func TestCommandPayloadMustBeJSONObject(t *testing.T) {
	_, err := CanonicalPayload([]byte(`[{"user_id":7}]`))
	require.Error(t, err)
	_, err = CommandBodyHash([]byte(`"not-an-object"`))
	require.Error(t, err)
	canonical, err := CanonicalPayload([]byte(`{"user_id":7}`))
	require.NoError(t, err)
	require.Equal(t, `{"user_id":7}`, string(canonical))
}

func TestCommandPayloadRejectsDuplicateKeys(t *testing.T) {
	_, err := CanonicalPayload([]byte(`{"user_id":7,"nested":{"reason":"a","reason":"b"}}`))
	require.Error(t, err)
	app := newAgencyTestApp(t)
	recorder := httptest.NewRecorder()
	app.Router().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/internal/agency/v1/commands", strings.NewReader(`{"command_id":"dup","command_id":"dup2"}`)))
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "重复字段")
}

func TestInternalCommandReplayRemainsAvailableAfterExpiry(t *testing.T) {
	app := newAgencyTestApp(t)
	req, body, _, servicePrivate := signedCommandRequest(t, app, "cmd-expiry-replay", CommandActionProvisioningCancel, time.Now().Add(2*time.Minute))
	recorder := httptest.NewRecorder()
	app.Router().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/internal/agency/v1/commands", bytes.NewReader(body)))
	require.Equal(t, http.StatusAccepted, recorder.Code)
	// Re-sign the same envelope with an expired interval. The command_id and
	// body match the durable row, so this is a replay lookup and must not need a
	// live Root proof.
	req.IssuedAt = time.Now().Add(-2 * time.Minute).Unix()
	req.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	req.HubSignature, _ = SignCommandEnvelope(servicePrivate, req)
	replay, err := common.Marshal(req)
	require.NoError(t, err)
	recorder = httptest.NewRecorder()
	app.Router().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/internal/agency/v1/commands", bytes.NewReader(replay)))
	require.Equal(t, http.StatusAccepted, recorder.Code)
}

func TestInternalCommandRejectsUnsupportedActionAndRootReplay(t *testing.T) {
	app := newAgencyTestApp(t)
	_, body, _, _ := signedCommandRequest(t, app, "cmd-3", "funding.delete", time.Now().Add(2*time.Minute))
	recorder := httptest.NewRecorder()
	app.Router().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/internal/agency/v1/commands", bytes.NewReader(body)))
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code)

	firstReq, first, rootPrivate, servicePrivate := signedCommandRequest(t, app, "cmd-4", CommandActionProvisioningCancel, time.Now().Add(2*time.Minute))
	recorder = httptest.NewRecorder()
	app.Router().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/internal/agency/v1/commands", bytes.NewReader(first)))
	require.Equal(t, http.StatusAccepted, recorder.Code)
	// Reuse the proof JTI under a different command id must be rejected.
	secondReq := firstReq
	secondReq.CommandID = "cmd-5"
	secondReq.ExpiresAt = time.Now().Add(2 * time.Minute).Unix()
	secondReq.IssuedAt = time.Now().Unix()
	now := time.Now().Unix()
	proof, err := SignSSOTicket(rootPrivate, SSOTicketClaims{Issuer: "new-api", Audience: "agency-gateway-command", Subject: 1, SourceSID: secondReq.SourceSID, JTI: "proof-cmd-4", KeyID: "root-k1", Action: secondReq.Action, CommandID: secondReq.CommandID, ObjectID: secondReq.ObjectID, ExpectedVersion: secondReq.ExpectedVersion, BodyHash: secondReq.BodyHash, IssuedAt: now, NotBefore: now, ExpiresAt: secondReq.ExpiresAt})
	require.NoError(t, err)
	secondReq.RootProof = proof
	secondReq.HubSignature = ""
	secondReq.HubSignature, err = SignCommandEnvelope(servicePrivate, secondReq)
	require.NoError(t, err)
	second, err := common.Marshal(secondReq)
	require.NoError(t, err)
	recorder = httptest.NewRecorder()
	app.Router().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/internal/agency/v1/commands", bytes.NewReader(second)))
	require.Equal(t, http.StatusConflict, recorder.Code)
}
