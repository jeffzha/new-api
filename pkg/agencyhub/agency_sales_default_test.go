package agencyhub

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateAgencySalesDefaultPreservesExplicitPolicyAndExistingAgency(t *testing.T) {
	for _, test := range []struct {
		name       string
		pricing    string
		wantStatus int
		wantSales  int
	}{
		{"omitted_sales", `{"default_settlement_bps":7500}`, http.StatusCreated, 10000},
		{"explicit_discount", `{"default_settlement_bps":7500,"default_sales_bps":9000}`, http.StatusCreated, 9000},
		{"explicit_zero_valid", `{"default_settlement_bps":0,"default_sales_bps":0,"min_spread_bps":0}`, http.StatusCreated, 0},
		{"explicit_zero_below_settlement", `{"default_settlement_bps":7500,"default_sales_bps":0}`, http.StatusUnprocessableEntity, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newFinanceRootClient(t)
			t.Setenv("AGENCY_HUB_DELIVERY_KEY", strings.Repeat("01", 32))
			t.Setenv("AGENCY_HUB_DELIVERY_KEY_FILE", "")
			existing, _, err := client.app.CreateAgency(client.rootID, "Existing agency", "existing_operator", agencycontract.Policy{
				DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000,
			})
			require.NoError(t, err)
			var originalPolicy model.AgencyPricePolicyVersion
			require.NoError(t, client.app.db.First(&originalPolicy, existing.CurrentPolicyVersionID).Error)

			body := fmt.Sprintf(`{"display_name":"New agency","operator_username":"new_operator","pricing":%s}`, test.pricing)
			proof := client.proof(t, body, "agency.create", "agency:new", "create-sales-default")
			response := client.post("/agency/api/v1/root/agencies", body, "create-sales-default", proof)
			require.Equal(t, test.wantStatus, response.Code, response.Body.String())
			if test.wantStatus == http.StatusCreated {
				var created model.Agency
				require.NoError(t, client.app.db.Where("display_name = ?", "New agency").First(&created).Error)
				_, policy, err := client.app.loadAgencyPolicy(created.ID)
				require.NoError(t, err)
				assert.Equal(t, test.wantSales, policy.DefaultSalesBPS)
			} else {
				assert.Contains(t, response.Body.String(), "invalid_pricing")
				var newAgencies int64
				require.NoError(t, client.app.db.Model(&model.Agency{}).Where("display_name = ?", "New agency").Count(&newAgencies).Error)
				assert.Zero(t, newAgencies)
			}

			var persistedExisting model.Agency
			var persistedPolicy model.AgencyPricePolicyVersion
			require.NoError(t, client.app.db.First(&persistedExisting, existing.ID).Error)
			require.NoError(t, client.app.db.First(&persistedPolicy, originalPolicy.ID).Error)
			assert.Equal(t, existing.CurrentPolicyVersionID, persistedExisting.CurrentPolicyVersionID)
			assert.Equal(t, existing.PriceRevision, persistedExisting.PriceRevision)
			assert.Equal(t, originalPolicy, persistedPolicy, "new-agency defaults cannot rewrite existing published prices")
		})
	}
}
