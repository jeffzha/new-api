package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
		wantStatus int
	}{
		{name: "ordinary", wantStatus: http.StatusOK},
		{name: "durable_missing", mode: model.AgencyDurableBillingMode, wantStatus: http.StatusServiceUnavailable},
		{name: "provisioning", mode: model.AgencyProvisioningBillingMode, wantStatus: http.StatusServiceUnavailable},
		{name: "bound_customer", mode: model.AgencyDurableBillingMode, bound: true, wantStatus: http.StatusOK},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db := agencyPricingCatalogFixture(t)
			if scenario.bound {
				bindAgencyPricingCustomer(t, db)
			} else {
				require.NoError(t, db.Model(&model.User{}).Where("id = ?", 1).Update("billing_mode", scenario.mode).Error)
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
				assert.Contains(t, recorder.Body.String(), `"sales_bps":12500`)
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
