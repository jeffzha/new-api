package agencyhub

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublicHubDoesNotExposeInternalCommandTransport(t *testing.T) {
	app := newAgencyTestApp(t)
	_, body, _, _ := signedCommandRequest(t, app, "public-must-not-enqueue", CommandActionProvisioningStart, time.Now().Add(time.Minute))
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		path := "/internal/agency/v1/commands"
		if method == http.MethodGet {
			path += "/public-must-not-enqueue"
		}
		recorder := httptest.NewRecorder()
		app.Router().ServeHTTP(recorder, httptest.NewRequest(method, path, bytes.NewReader(body)))
		assert.Equal(t, http.StatusNotFound, recorder.Code)
	}
	var count int64
	require.NoError(t, app.db.Model(&model.AgencyCommand{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestInternalCommandRevalidatesRootAtAcceptance(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model any
		where string
		field string
		value any
	}{
		{"root_disabled", &model.User{}, "id = 1", "status", common.UserStatusDisabled},
		{"root_downgraded", &model.User{}, "id = 1", "role", common.RoleCommonUser},
		{"root_credentials_changed", &model.User{}, "id = 1", "auth_version", 2},
		{"session_revoked", &model.UserSession{}, "sid = 'sid-1'", "revoked_at", 1},
		{"session_expired", &model.UserSession{}, "sid = 'sid-1'", "expires_at", 1},
		{"session_rotated", &model.UserSession{}, "sid = 'sid-1'", "version", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newAgencyTestApp(t)
			_, body, _, _ := signedCommandRequest(t, app, "revoked-command", CommandActionProvisioningStart, time.Now().Add(time.Minute))
			require.NoError(t, app.db.Model(tc.model).Where(tc.where).Update(tc.field, tc.value).Error)
			recorder := httptest.NewRecorder()
			app.CommandRouter().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/internal/agency/v1/commands", bytes.NewReader(body)))
			assert.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
			assert.Contains(t, recorder.Body.String(), "root_authorization_revoked")
			var count int64
			require.NoError(t, app.db.Model(&model.AgencyCommand{}).Count(&count).Error)
			assert.Zero(t, count, "rejected proof must remain unconsumed")
		})
	}
}

func TestRootCommandLocalSQLiteRequiresExplicitDevelopmentOptIn(t *testing.T) {
	app := newAgencyTestApp(t)
	req, _, _, _ := signedCommandRequest(t, app, "explicit-local-only", CommandActionProvisioningStart, time.Now().Add(time.Minute))
	_, err := app.SubmitRootCommand(context.Background(), req)
	require.ErrorContains(t, err, "mTLS transport is not configured")
	var count int64
	require.NoError(t, app.db.Model(&model.AgencyCommand{}).Count(&count).Error)
	assert.Zero(t, count)
	app.config.CommandAllowLocalSQLite = true
	receipt, err := app.SubmitRootCommand(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, req.CommandID, receipt["command_id"])
	_, err = app.SubmitRootCommand(context.Background(), req)
	require.NoError(t, err)
	require.NoError(t, app.db.Model(&model.AgencyCommand{}).Count(&count).Error)
	assert.EqualValues(t, 1, count)
	app.config.CommandGatewayURL = "https://127.0.0.1:1"
	_, err = app.SubmitRootCommand(context.Background(), req)
	require.Error(t, err, "configured production transport must never fall back to local SQL")
}

func TestRootCommandTransportRejectsInsecureOrPartialConfiguration(t *testing.T) {
	for _, cfg := range []Config{
		{CommandGatewayURL: "http://127.0.0.1:9444"},
		{CommandGatewayURL: "https://user:password@localhost:9444"},
		{CommandGatewayURL: "https://localhost:9444/extra"},
		{CommandGatewayURL: "https://localhost:9444?secret=x"},
		{CommandGatewayURL: "https://localhost:9444"},
		{CommandClientCAFile: "missing-ca"},
		{CommandAllowLocalSQLite: true},
	} {
		app := New(nil, nil, cfg)
		require.Error(t, app.InitializeCommandTransport())
		assert.Nil(t, app.commandClient)
	}
}
