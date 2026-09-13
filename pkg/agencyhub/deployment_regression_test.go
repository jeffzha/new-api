package agencyhub

import (
	"bytes"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInternalCommandRejectsTLSWithoutVerifiedClientCertificate(t *testing.T) {
	app := newAgencyTestApp(t)
	app.config.CommandRequireTLS = true
	_, body, _, _ := signedCommandRequest(t, app, "no-client-cert", CommandActionProvisioningStart, time.Now().Add(time.Minute))
	request := httptest.NewRequest(http.MethodPost, "/internal/agency/v1/commands", bytes.NewReader(body))
	request.TLS = &tls.ConnectionState{HandshakeComplete: true}
	recorder := httptest.NewRecorder()
	app.CommandRouter().ServeHTTP(recorder, request)
	assert.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
	var count int64
	require.NoError(t, app.db.Model(&model.AgencyCommand{}).Where("command_id = ?", "no-client-cert").Count(&count).Error)
	assert.Zero(t, count, "a TLS connection alone must not authorize a privileged command")
}

func TestReadinessRejectsMissingFinancialColumn(t *testing.T) {
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.Migrator().DropColumn(&model.Agency{}, "PriceRevision"))
	require.False(t, app.db.Migrator().HasColumn(&model.Agency{}, "PriceRevision"))
	app.SetReady(true)
	recorder := httptest.NewRecorder()
	app.Router().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/agency/readyz", nil))
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
}

func TestReadinessRejectsOldFinancialSchemaUntilIncrementalMigration(t *testing.T) {
	for _, tc := range []struct {
		name, field, column string
		model               any
	}{
		{"export", "ErrorCode", "error_code", &model.AgencyExportJob{}},
		{"topup", "QuotaConversionSnapshot", "quota_conversion_snapshot", &model.AgencyTopupFact{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newAgencyTestApp(t)
			require.NoError(t, app.db.Migrator().DropColumn(tc.model, tc.field))
			app.SetReady(true)
			before := httptest.NewRecorder()
			app.Router().ServeHTTP(before, httptest.NewRequest(http.MethodGet, "/agency/readyz", nil))
			assert.Equal(t, http.StatusServiceUnavailable, before.Code)
			assert.Contains(t, before.Body.String(), tc.column)
			require.NoError(t, app.Migrate())
			after := httptest.NewRecorder()
			app.Router().ServeHTTP(after, httptest.NewRequest(http.MethodGet, "/agency/readyz", nil))
			assert.Equal(t, http.StatusOK, after.Code, after.Body.String())
		})
	}
}

func TestExpiredExportRecoveryRespectsActiveWorkerLease(t *testing.T) {
	for _, recovery := range []string{"worker", "cleanup"} {
		t.Run(recovery, func(t *testing.T) {
			app := newAgencyTestApp(t)
			app.config.ExportDir = t.TempDir()
			now := time.Now().Unix()
			crashed := model.AgencyExportJob{
				ActorType: "root", ActorID: 1, Kind: "usage", FilterJSON: "{}",
				Status: "processing", ExpiresAt: now - 3600, LeaseOwner: "crashed-worker", LeaseUntil: now - 60,
			}
			active := model.AgencyExportJob{
				ActorType: "root", ActorID: 1, Kind: "usage", FilterJSON: "{}",
				Status: "processing", ExpiresAt: now - 3600, LeaseOwner: "active-worker", LeaseUntil: now + 3600,
			}
			require.NoError(t, app.db.Create(&crashed).Error)
			require.NoError(t, app.db.Create(&active).Error)
			if recovery == "worker" {
				_, err := app.ProcessExportJobs(2)
				require.NoError(t, err)
			} else {
				_, err := app.CleanupExpiredExportJobs(2)
				require.NoError(t, err)
			}
			require.NoError(t, app.db.First(&crashed, crashed.ID).Error)
			require.NoError(t, app.db.First(&active, active.ID).Error)
			assert.Equal(t, "expired", crashed.Status)
			assert.Empty(t, crashed.LeaseOwner)
			assert.Zero(t, crashed.LeaseUntil)
			assert.Equal(t, "processing", active.Status, "an active worker must retain its job")
			assert.Equal(t, "active-worker", active.LeaseOwner)
		})
	}
}
