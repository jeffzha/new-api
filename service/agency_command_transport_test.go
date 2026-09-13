package service

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencyhub"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type agencyCommandTransportFixture struct {
	db         *gorm.DB
	hub        *agencyhub.App
	server     *AgencyCommandServer
	config     agencyhub.Config
	rootKey    ed25519.PrivateKey
	serviceKey ed25519.PrivateKey
}

func agencyCommandTLSClientConfig(t *testing.T, fixture agencyCommandTLSFixture) agencyhub.Config {
	t.Helper()
	identity, err := url.Parse(fixture.config.ClientIdentity)
	require.NoError(t, err)
	certificate := fixture.certificate(t, &x509.Certificate{
		URIs: []*url.URL{identity}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	key, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	require.NoError(t, err)
	directory := t.TempDir()
	config := agencyhub.Config{
		CommandGatewayURL:     "https://127.0.0.1:1",
		CommandClientCAFile:   fixture.config.ClientCAFile,
		CommandClientCertFile: filepath.Join(directory, "hub-client.pem"),
		CommandClientKeyFile:  filepath.Join(directory, "hub-client-key.pem"),
	}
	require.NoError(t, os.WriteFile(config.CommandClientCertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]}), 0600))
	require.NoError(t, os.WriteFile(config.CommandClientKeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600))
	return config
}

func newAgencyCommandTransportFixture(t *testing.T) agencyCommandTransportFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:agency-command-transport-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { assert.NoError(t, pool.Close()) })
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}))
	require.NoError(t, model.MigrateAgency(db))
	require.NoError(t, db.Create(&model.User{
		Id: 1, Username: "transport-root", AffCode: "transport-root", Role: common.RoleRootUser,
		Status: common.UserStatusEnabled, AuthVersion: 1,
	}).Error)
	require.NoError(t, db.Create(&model.UserSession{
		SID: "transport-root-session", UserID: 1, Version: 1, UserAuthVersion: 1,
		Status: model.UserSessionStatusActive, RefreshHash: "transport-test-refresh",
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}).Error)
	rootPublic, rootKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	servicePublic, serviceKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	gateway := agencyhub.New(db, db, agencyhub.Config{CommandRequireTLS: true})
	gateway.SetSSOPublicKey(rootPublic)
	gateway.SetCommandServicePublicKey(servicePublic)
	tlsFixture := newAgencyCommandTLSFixture(t)
	server := startAgencyCommandTLSTestServer(t, tlsFixture.config, gateway.CommandRouter())
	config := agencyCommandTLSClientConfig(t, tlsFixture)
	config.CommandGatewayURL = "https://" + server.Addr().String()
	// The submitting Hub has no database handle: all acceptance and reads must
	// cross the real mTLS listener into gateway-owned persistence.
	hub := agencyhub.New(nil, nil, config)
	require.NoError(t, hub.InitializeCommandTransport())
	t.Cleanup(hub.CloseCommandTransport)
	return agencyCommandTransportFixture{db: db, hub: hub, server: server, config: config, rootKey: rootKey, serviceKey: serviceKey}
}

func (fixture agencyCommandTransportFixture) signedRequest(t *testing.T, commandID string, issuedAt time.Time) agencyhub.AgencyCommandRequest {
	t.Helper()
	payload := []byte(`{"user_id":2,"reason":"operator cancelled"}`)
	bodyHash, err := agencyhub.CommandBodyHash(payload)
	require.NoError(t, err)
	request := agencyhub.AgencyCommandRequest{
		CommandID: commandID, Action: agencyhub.CommandActionProvisioningCancel,
		Actor: "root:1", SourceSID: "transport-root-session", ObjectID: "7", ExpectedVersion: 1,
		Payload: payload, BodyHash: bodyHash, IssuedAt: issuedAt.Unix(), ExpiresAt: issuedAt.Add(2 * time.Minute).Unix(),
	}
	request.RootProof, err = agencyhub.SignSSOTicket(fixture.rootKey, agencyhub.SSOTicketClaims{
		Issuer: "new-api", Audience: "agency-gateway-command", Subject: 1,
		SourceSID: request.SourceSID, UserAuthVersion: 1, SessionVersion: 1,
		JTI: "proof-" + commandID, KeyID: "transport-test-root", Action: request.Action,
		CommandID: commandID, ObjectID: request.ObjectID, ExpectedVersion: request.ExpectedVersion,
		BodyHash: bodyHash, IssuedAt: request.IssuedAt, NotBefore: request.IssuedAt, ExpiresAt: request.ExpiresAt,
	})
	require.NoError(t, err)
	request.HubSignature, err = agencyhub.SignCommandEnvelope(fixture.serviceKey, request)
	require.NoError(t, err)
	return request
}

func TestAgencyCommandTransportAcceptsReplaysAndQueriesWithoutHubDatabase(t *testing.T) {
	fixture := newAgencyCommandTransportFixture(t)
	request := fixture.signedRequest(t, "transport-accepted", time.Now())
	encoded, err := common.Marshal(request)
	require.NoError(t, err)
	publicRouter := fixture.hub.Router()
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		path := "/internal/agency/v1/commands"
		if method == http.MethodGet {
			path += "/" + request.CommandID
		}
		recorder := httptest.NewRecorder()
		httpRequest := httptest.NewRequest(method, path, bytes.NewReader(encoded))
		httpRequest.Header.Set(agencyhub.CommandServiceSignatureHeader, request.HubSignature)
		publicRouter.ServeHTTP(recorder, httpRequest)
		assert.Equal(t, http.StatusNotFound, recorder.Code, "the public Hub router must not expose internal commands")
	}

	accepted, err := fixture.hub.SubmitRootCommand(t.Context(), request)
	require.NoError(t, err)
	assert.Equal(t, request.CommandID, accepted["command_id"])
	assert.Equal(t, agencyhub.CommandStatusQueued, accepted["status"])
	var stored model.AgencyCommand
	require.NoError(t, fixture.db.Where("command_id = ?", request.CommandID).First(&stored).Error)
	assert.Equal(t, request.BodyHash, stored.BodyHash)
	assert.Equal(t, request.RootProof, stored.RootProof)
	assert.Equal(t, request.HubSignature, stored.HubSignature)
	assert.Equal(t, request.SourceSID, stored.SourceSID)
	assert.JSONEq(t, string(request.Payload), stored.Payload)

	replayed, err := fixture.hub.SubmitRootCommand(t.Context(), request)
	require.NoError(t, err)
	assert.Equal(t, accepted, replayed)
	queried, err := fixture.hub.QueryRootCommand(t.Context(), request)
	require.NoError(t, err)
	assert.Equal(t, accepted, queried)
	var count int64
	require.NoError(t, fixture.db.Model(&model.AgencyCommand{}).Count(&count).Error)
	assert.Equal(t, int64(1), count, "replay must reuse one durable command and proof")

	require.NoError(t, fixture.db.Model(&model.UserSession{}).Where("sid = ?", request.SourceSID).Update("revoked_at", time.Now().Unix()).Error)
	revoked := fixture.signedRequest(t, "transport-revoked", time.Now())
	_, err = fixture.hub.SubmitRootCommand(t.Context(), revoked)
	var transportError *agencyhub.CommandTransportError
	require.ErrorAs(t, err, &transportError)
	assert.Equal(t, http.StatusForbidden, transportError.Status)
	assert.Equal(t, "root_authorization_revoked", transportError.Code)
	require.NoError(t, fixture.db.Model(&model.AgencyCommand{}).Count(&count).Error)
	assert.Equal(t, int64(1), count, "revocation must reject a new command before persistence")
	queried, err = fixture.hub.QueryRootCommand(t.Context(), request)
	require.NoError(t, err)
	assert.Equal(t, accepted, queried, "revocation must not erase the receipt for an already accepted command")
}

func TestAgencyCommandTransportQueriesCompletedReceiptAfterProofExpiry(t *testing.T) {
	fixture := newAgencyCommandTransportFixture(t)
	request := fixture.signedRequest(t, "transport-expired-receipt", time.Now().Add(-3*time.Minute))
	completedAt := request.IssuedAt + 30
	// Seed a previously accepted and completed command with its original signed
	// interval already elapsed, without sleeps or changing the process clock.
	require.NoError(t, fixture.db.Create(&model.AgencyCommand{
		CommandID: request.CommandID, Action: request.Action, Actor: request.Actor, SourceSID: request.SourceSID,
		ObjectID: request.ObjectID, ExpectedVersion: request.ExpectedVersion, Payload: string(request.Payload),
		BodyHash: request.BodyHash, RootProof: request.RootProof, RootProofJTI: "proof-" + request.CommandID,
		HubSignature: request.HubSignature, IssuedAt: request.IssuedAt, ExpiresAt: request.ExpiresAt,
		Status: agencyhub.CommandStatusSucceeded, ResultCode: http.StatusOK, ResultJSON: `{"cancelled":true}`,
		CreatedAt: request.IssuedAt, UpdatedAt: completedAt, CompletedAt: &completedAt,
	}).Error)
	queried, err := fixture.hub.QueryRootCommand(t.Context(), request)
	require.NoError(t, err)
	assert.Equal(t, request.CommandID, queried["command_id"])
	assert.Equal(t, agencyhub.CommandStatusSucceeded, queried["status"])
	assert.Equal(t, float64(http.StatusOK), queried["result_code"])
	assert.Equal(t, map[string]any{"cancelled": true}, queried["result"])
	replayed, err := fixture.hub.SubmitRootCommand(t.Context(), request)
	require.NoError(t, err)
	assert.Equal(t, queried, replayed)

	expiredNew := fixture.signedRequest(t, "transport-expired-new", time.Now().Add(-3*time.Minute))
	_, err = fixture.hub.SubmitRootCommand(t.Context(), expiredNew)
	var transportError *agencyhub.CommandTransportError
	require.ErrorAs(t, err, &transportError)
	assert.Equal(t, http.StatusUnprocessableEntity, transportError.Status)
	assert.Equal(t, "invalid_command", transportError.Code)
	var count int64
	require.NoError(t, fixture.db.Model(&model.AgencyCommand{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestAgencyCommandTransportExecutesAndReturnsPermanentBusinessReceipt(t *testing.T) {
	fixture := newAgencyCommandTransportFixture(t)
	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = fixture.db, fixture.db
	t.Cleanup(func() { model.DB, model.LOG_DB = previousDB, previousLogDB })
	for name, private := range map[string]ed25519.PrivateKey{
		"AGENCY_SSO_PUBLIC_KEY_FILE":                 fixture.rootKey,
		"AGENCY_HUB_COMMAND_SERVICE_PUBLIC_KEY_FILE": fixture.serviceKey,
	} {
		der, err := x509.MarshalPKIXPublicKey(private.Public())
		require.NoError(t, err)
		path := filepath.Join(t.TempDir(), "verification.pem")
		require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0600))
		t.Setenv(name, path)
	}
	user := model.User{Id: 2, Username: "transport-customer", AffCode: "transport-customer", Status: common.UserStatusEnabled,
		Role: common.RoleCommonUser, BillingMode: model.AgencyProvisioningBillingMode, AuthVersion: 1, Quota: 100}
	require.NoError(t, fixture.db.Create(&user).Error)
	job := model.AgencyProvisioningJob{ID: 7, UserID: 2, InviteCode: "transport-invite", RootActorID: 1, ExpectedUserVersion: 1, Status: "queued", FencingToken: 1}
	require.NoError(t, fixture.db.Create(&job).Error)
	request := fixture.signedRequest(t, "transport-business-receipt", time.Now())
	_, err := fixture.hub.SubmitRootCommand(t.Context(), request)
	require.NoError(t, err)
	summary, err := RunAgencyCommandWorkerOnce(t.Context(), 1)
	require.NoError(t, err)
	assert.Equal(t, 1, summary.Succeeded)
	assert.Zero(t, summary.Failed)
	receipt, err := fixture.hub.QueryRootCommand(t.Context(), request)
	require.NoError(t, err)
	assert.Equal(t, agencyhub.CommandStatusCancelled, receipt["status"])
	assert.Equal(t, float64(http.StatusOK), receipt["result_code"])
	assert.Equal(t, map[string]any{"job_id": float64(7), "status": "cancelled"}, receipt["result"])
	require.NoError(t, fixture.db.First(&job, job.ID).Error)
	require.NoError(t, fixture.db.First(&user, user.Id).Error)
	assert.Equal(t, "cancelled", job.Status)
	assert.Equal(t, "legacy", user.BillingMode)
	assert.Equal(t, 100, user.Quota)
	replayed, err := fixture.hub.SubmitRootCommand(t.Context(), request)
	require.NoError(t, err)
	assert.Equal(t, receipt, replayed)
	summary, err = RunAgencyCommandWorkerOnce(t.Context(), 1)
	require.NoError(t, err)
	assert.Zero(t, summary.Claimed)
	var audits int64
	require.NoError(t, fixture.db.Model(&model.AgencyAuditLog{}).Where("action = ?", "provisioning.cancel").Count(&audits).Error)
	assert.Equal(t, int64(1), audits)
}

func TestAgencyCommandTransportRejectsInvalidServiceAndRootSignatures(t *testing.T) {
	fixture := newAgencyCommandTransportFixture(t)
	_, wrongKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	for _, signature := range []string{"service", "root"} {
		t.Run(signature, func(t *testing.T) {
			signer := fixture
			expectedStatus, expectedCode := http.StatusUnauthorized, "invalid_service_signature"
			if signature == "root" {
				signer.rootKey = wrongKey
				expectedStatus, expectedCode = http.StatusForbidden, "invalid_root_proof"
			} else {
				signer.serviceKey = wrongKey
			}
			request := signer.signedRequest(t, "transport-wrong-"+signature, time.Now())
			_, err := fixture.hub.SubmitRootCommand(t.Context(), request)
			var transportError *agencyhub.CommandTransportError
			require.ErrorAs(t, err, &transportError)
			assert.Equal(t, expectedStatus, transportError.Status)
			assert.Equal(t, expectedCode, transportError.Code)
			var count int64
			require.NoError(t, fixture.db.Model(&model.AgencyCommand{}).Count(&count).Error)
			assert.Zero(t, count, "a valid mTLS client still needs both independent command signatures")
		})
	}
}

func TestAgencyCommandTransportRejectsUnsafeClientConfiguration(t *testing.T) {
	fixture := newAgencyCommandTLSFixture(t)
	valid := agencyCommandTLSClientConfig(t, fixture)
	cases := []struct {
		name   string
		change func(*agencyhub.Config)
	}{
		{"plaintext", func(c *agencyhub.Config) { c.CommandGatewayURL = "http://127.0.0.1:1" }},
		{"missing_origin", func(c *agencyhub.Config) { c.CommandGatewayURL = "" }},
		{"origin_credentials", func(c *agencyhub.Config) { c.CommandGatewayURL = "https://user:pass@127.0.0.1:1" }},
		{"origin_path", func(c *agencyhub.Config) { c.CommandGatewayURL += "/unexpected" }},
		{"origin_query", func(c *agencyhub.Config) { c.CommandGatewayURL += "?redirect=1" }},
		{"missing_ca", func(c *agencyhub.Config) { c.CommandClientCAFile = "" }},
		{"missing_certificate", func(c *agencyhub.Config) { c.CommandClientCertFile = "" }},
		{"missing_key", func(c *agencyhub.Config) { c.CommandClientKeyFile = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := valid
			tc.change(&config)
			hub := agencyhub.New(nil, nil, config)
			t.Cleanup(hub.CloseCommandTransport)
			require.Error(t, hub.InitializeCommandTransport())
			_, err := hub.SubmitRootCommand(t.Context(), agencyhub.AgencyCommandRequest{})
			assert.Error(t, err, "failed initialization must not leave a usable command transport")
		})
	}
}

func TestAgencyCommandTransportUnavailableNeverFallsBackToHubDatabase(t *testing.T) {
	fixture := newAgencyCommandTransportFixture(t)
	config := fixture.config
	config.CommandAllowLocalSQLite = true
	// Even when a writable SQLite database and valid signing keys are present,
	// failure of a configured gateway must not enqueue a command locally.
	hub := agencyhub.New(fixture.db, fixture.db, config)
	hub.SetSSOPublicKey(fixture.rootKey.Public().(ed25519.PublicKey))
	hub.SetCommandServicePublicKey(fixture.serviceKey.Public().(ed25519.PublicKey))
	require.NoError(t, hub.InitializeCommandTransport())
	t.Cleanup(hub.CloseCommandTransport)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, fixture.server.Shutdown(ctx))
	request := fixture.signedRequest(t, "transport-unavailable", time.Now())
	_, err := hub.SubmitRootCommand(ctx, request)
	require.Error(t, err)
	_, err = hub.QueryRootCommand(ctx, request)
	require.Error(t, err)
	var count int64
	require.NoError(t, fixture.db.Model(&model.AgencyCommand{}).Count(&count).Error)
	assert.Zero(t, count, "unavailable mTLS must not fall back to the Hub's writable SQL connection")
}
