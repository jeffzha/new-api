package agencyhub

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadinessRejectsReconciliationSchemaUntilUniqueActiveIndexIsRestored(t *testing.T) {
	app := newAgencyTestApp(t)
	app.SetReady(true)
	require.NoError(t, app.db.Migrator().DropIndex(&model.AgencyReconciliationIssue{}, "uidx_agency_reconcile_active"))
	response := httptest.NewRecorder()
	app.Router().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/agency/readyz", nil))
	assert.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "uidx_agency_reconcile_active")
	require.NoError(t, model.MigrateAgency(app.db))
	response = httptest.NewRecorder()
	app.Router().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/agency/readyz", nil))
	assert.Equal(t, http.StatusOK, response.Code, response.Body.String())
}
