package agencyhub

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadinessRequiresFundingExpirySchema(t *testing.T) {
	for _, missing := range []struct {
		name   string
		item   any
		field  string
		column string
		index  string
	}{
		{name: "topup_expiry", item: &model.AgencyTopupFact{}, field: "ExpiresAt", column: "expires_at"},
		{name: "topup_expired_quota", item: &model.AgencyTopupFact{}, field: "ExpiredQuota", column: "expired_quota"},
		{name: "lot_expired_balance", item: &model.AgencyFundingLot{}, field: "BonusExpired", column: "bonus_expired"},
		{name: "lot_expiry_index", item: &model.AgencyFundingLot{}, index: "idx_agency_funding_lot_expiry"},
	} {
		t.Run(missing.name, func(t *testing.T) {
			app := newAgencyTestApp(t)
			app.SetReady(true)
			if missing.index != "" {
				require.NoError(t, app.db.Migrator().DropIndex(missing.item, missing.index))
			} else {
				require.NoError(t, app.db.Migrator().DropColumn(missing.item, missing.field))
			}

			response := httptest.NewRecorder()
			app.Router().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/agency/readyz", nil))
			assert.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
			if missing.column != "" {
				assert.Contains(t, response.Body.String(), missing.column)
			}
			if missing.index != "" {
				assert.Contains(t, response.Body.String(), missing.index)
			}

			require.NoError(t, app.Migrate())
			response = httptest.NewRecorder()
			app.Router().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/agency/readyz", nil))
			assert.Equal(t, http.StatusOK, response.Code, response.Body.String())
		})
	}
}
