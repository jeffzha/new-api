package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgencyEffectivePricingIsPrivateAndDoesNotDowngradeMissingPolicy(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		mode       string
		bound      bool
		pending    bool
		wantSales  int
		wantStatus int
	}{
		{name: "ordinary", wantStatus: http.StatusOK},
		{name: "durable_missing", mode: model.AgencyDurableBillingMode, wantStatus: http.StatusServiceUnavailable},
		{name: "provisioning", mode: model.AgencyProvisioningBillingMode, wantStatus: http.StatusServiceUnavailable},
		{name: "bound_customer", mode: model.AgencyDurableBillingMode, bound: true, wantSales: 12500, wantStatus: http.StatusOK},
		{name: "bound_pending_model", mode: model.AgencyDurableBillingMode, bound: true, pending: true, wantSales: 10000, wantStatus: http.StatusOK},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db := agencyPricingCatalogFixture(t)
			if scenario.bound {
				bindAgencyPricingCustomer(t, db)
			} else {
				require.NoError(t, db.Model(&model.User{}).Where("id = ?", 1).Update("billing_mode", scenario.mode).Error)
			}
			if scenario.pending {
				// An unconfirmed platform model must not inherit distributor discounts.
				require.NoError(t, db.Delete(&model.AgencyPlatformPriceState{}, "id = ?", 1).Error)
			}
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/api/agency/effective-pricing?model=hy3", nil)
			ctx.Set("id", 1)
			GetAgencyEffectivePricing(ctx)
			assert.Equal(t, scenario.wantStatus, recorder.Code, recorder.Body.String())
			assert.Equal(t, "private, no-store", recorder.Header().Get("Cache-Control"))
			assert.Contains(t, recorder.Header().Get("Vary"), "Authorization")
			if scenario.bound {
				var response struct {
					Data struct {
						SalesBPS int `json:"sales_bps"`
					} `json:"data"`
				}
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
				assert.Equal(t, scenario.wantSales, response.Data.SalesBPS)
				assert.NotContains(t, recorder.Body.String(), "settlement_bps")
				assert.NotContains(t, recorder.Body.String(), "commission")
			} else if scenario.wantStatus == http.StatusOK {
				assert.Contains(t, recorder.Body.String(), `"managed":false`)
			} else {
				assert.NotContains(t, recorder.Body.String(), `"managed":false`)
				assert.NotContains(t, recorder.Body.String(), `"data"`)
			}
		})
	}
}
