package agencyhub

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadinessRequiresDebtSourceMigration(t *testing.T) {
	for _, missing := range []struct {
		name   string
		item   any
		column string
		index  string
	}{
		{name: "allocation_identity", item: &model.AgencyFundingDebt{}, column: "allocation_id"},
		{name: "allocation_index", item: &model.AgencyFundingDebt{}, index: "idx_agency_debt_allocation"},
		{name: "repayment_class", item: &model.AgencyDebtRepayment{}, column: "source_kind"},
		{name: "bonus_repaid", item: &model.AgencyFundingLot{}, column: "bonus_debt_repaid"},
	} {
		t.Run(missing.name, func(t *testing.T) {
			app := newAgencyTestApp(t)
			app.SetReady(true)
			if missing.index != "" {
				require.NoError(t, app.db.Migrator().DropIndex(missing.item, missing.index))
			} else {
				if missing.column == "allocation_id" {
					require.NoError(t, app.db.Migrator().DropIndex(missing.item, "idx_agency_debt_allocation"))
				}
				require.NoError(t, app.db.Migrator().DropColumn(missing.item, missing.column))
			}
			response := httptest.NewRecorder()
			app.Router().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/agency/readyz", nil))
			assert.Equal(t, http.StatusServiceUnavailable, response.Code)
			assert.Contains(t, response.Body.String(), missing.index+missing.column)
			require.NoError(t, model.MigrateAgency(app.db))
			response = httptest.NewRecorder()
			app.Router().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/agency/readyz", nil))
			assert.Equal(t, http.StatusOK, response.Code, response.Body.String())
		})
	}
}
