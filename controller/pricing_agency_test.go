package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func agencyPricingCatalogFixture(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB, previousRedis := model.DB, common.RedisEnabled
	previousGroups, previousRatios := setting.UserUsableGroups2JSONString(), ratio_setting.GroupRatio2JSONString()
	previousModels, previousPrices := ratio_setting.ModelRatio2JSONString(), ratio_setting.ModelPrice2JSONString()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB, common.RedisEnabled = db, false
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Channel{}, &model.Ability{}, &model.Model{}, &model.Vendor{}, &model.Agency{}, &model.AgencyActiveUserBinding{}, &model.AgencyUserBinding{}, &model.AgencyPricePolicyVersion{}))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":2}`))
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"hy3":1,"Hy3":1,"free-model":1}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"per-call":2}`))
	for id := 1; id <= 2; id++ {
		require.NoError(t, db.Create(&model.User{Id: id, Username: fmt.Sprintf("pricing-user-%d", id), AffCode: fmt.Sprintf("prc%d", id), Password: "unused", Group: "default", Status: common.UserStatusEnabled}).Error)
	}
	require.NoError(t, db.Create(&model.Channel{Id: 1, Name: "pricing", Type: constant.ChannelTypeOpenAI, Key: "unused", Status: common.ChannelStatusEnabled}).Error)
	for _, name := range []string{"hy3", "Hy3", "free-model", "per-call"} {
		require.NoError(t, db.Create(&model.Ability{Group: "default", Model: name, ChannelId: 1, Enabled: true}).Error)
	}
	model.InvalidatePricingCache()
	t.Cleanup(func() {
		model.InvalidatePricingCache()
		model.DB, common.RedisEnabled = previousDB, previousRedis
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(previousGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousRatios))
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousModels))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(previousPrices))
		sqlDB, closeErr := db.DB()
		require.NoError(t, closeErr)
		require.NoError(t, sqlDB.Close())
	})
	return db
}

func bindAgencyPricingCustomer(t *testing.T, db *gorm.DB) {
	t.Helper()
	zero, override := 0, 12500
	policy, err := common.Marshal(agencycontract.Policy{DefaultSettlementBPS: 0, DefaultSalesBPS: 9000, SalesCapBPS: 30000, ModelOverrides: []agencycontract.ModelOverride{{OriginModelName: "hy3", SalesBPS: &override}, {OriginModelName: "free-model", SalesBPS: &zero}}})
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.Agency{ID: 1, Code: "agency-pricing", InviteCode: "pricing-invite", Status: "active", CurrentPolicyVersionID: 1}).Error)
	require.NoError(t, db.Create(&model.AgencyPricePolicyVersion{ID: 1, AgencyID: 1, Revision: 1, PolicyJSON: string(policy)}).Error)
	require.NoError(t, db.Create(&model.AgencyUserBinding{ID: 1, UserID: 1, AgencyID: 1, Revision: 1}).Error)
	require.NoError(t, db.Create(&model.AgencyActiveUserBinding{UserID: 1, BindingID: 1, AgencyID: 1, Revision: 1}).Error)
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", 1).Update("billing_mode", model.AgencyDurableBillingMode).Error)
}

func requestAgencyPricingCatalog(t *testing.T, userID int) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/pricing", nil)
	if userID > 0 {
		context.Set("id", userID)
	}
	GetPricing(context)
	var response map[string]any
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	return recorder, response
}

func TestGetPricingUsesCustomerSalesWithoutMutatingStandardCatalog(t *testing.T) {
	db := agencyPricingCatalogFixture(t)
	bindAgencyPricingCustomer(t, db)
	standard, err := common.Marshal(model.GetPricing())
	require.NoError(t, err)
	for _, state := range []string{"active", "disabled"} {
		t.Run(state, func(t *testing.T) {
			require.NoError(t, db.Model(&model.Agency{}).Where("id = ?", 1).Update("status", state).Error)
			recorder, response := requestAgencyPricingCatalog(t, 1)
			require.Equal(t, http.StatusOK, recorder.Code)
			assert.Equal(t, "private, no-store", recorder.Header().Get("Cache-Control"))
			assert.Equal(t, "agency", response["pricing_scope"])
			rows, ok := response["data"].([]any)
			require.True(t, ok)
			require.Len(t, rows, 4)
			want := map[string]int{"hy3": 12500, "Hy3": 9000, "free-model": 0, "per-call": 9000}
			for _, raw := range rows {
				row := raw.(map[string]any)
				name := row["model_name"].(string)
				assert.Equal(t, float64(want[name]), row["sales_bps"])
				quote, quoteErr := service.AgencyQuoteForUser(1, 0, name, 1)
				require.NoError(t, quoteErr)
				assert.Equal(t, float64(quote.SalesBPS), row["sales_bps"], "catalog must match actual gateway policy")
			}
			assert.NotContains(t, recorder.Body.String(), "settlement_bps")
			assert.NotContains(t, recorder.Body.String(), "commission")
			assert.NotContains(t, recorder.Body.String(), "policy_json")
		})
	}
	after, err := common.Marshal(model.GetPricing())
	require.NoError(t, err)
	assert.JSONEq(t, string(standard), string(after))
	for _, userID := range []int{0, 2} {
		recorder, response := requestAgencyPricingCatalog(t, userID)
		require.Equal(t, http.StatusOK, recorder.Code)
		assert.Equal(t, "standard", response["pricing_scope"])
		assert.NotContains(t, recorder.Body.String(), "sales_bps")
		assert.Equal(t, float64(2), response["group_ratio"].(map[string]any)["default"])
	}
	// A committed policy publication must affect the next catalog response;
	// the public model cache must not cache customer policy versions.
	nextPolicy, err := common.Marshal(agencycontract.Policy{DefaultSalesBPS: 11000, SalesCapBPS: 30000})
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.AgencyPricePolicyVersion{ID: 2, AgencyID: 1, Revision: 2, PolicyJSON: string(nextPolicy)}).Error)
	require.NoError(t, db.Model(&model.Agency{}).Where("id = ?", 1).Update("current_policy_version_id", 2).Error)
	recorder, response := requestAgencyPricingCatalog(t, 1)
	require.Equal(t, http.StatusOK, recorder.Code)
	for _, raw := range response["data"].([]any) {
		assert.Equal(t, float64(11000), raw.(map[string]any)["sales_bps"])
	}
}

func TestGetPricingFailsClosedForMissingOrInvalidDurablePolicy(t *testing.T) {
	for _, invalid := range []string{"missing-binding", "missing-policy", "wrong-agency-policy", "invalid-policy", "missing-schema"} {
		t.Run(invalid, func(t *testing.T) {
			db := agencyPricingCatalogFixture(t)
			bindAgencyPricingCustomer(t, db)
			switch invalid {
			case "missing-binding":
				require.NoError(t, db.Delete(&model.AgencyActiveUserBinding{}, "user_id = ?", 1).Error)
			case "missing-policy":
				require.NoError(t, db.Delete(&model.AgencyPricePolicyVersion{}, "id = ?", 1).Error)
			case "invalid-policy":
				require.NoError(t, db.Model(&model.AgencyPricePolicyVersion{}).Where("id = ?", 1).Update("policy_json", `{"default_sales_bps":-1}`).Error)
			case "wrong-agency-policy":
				require.NoError(t, db.Model(&model.AgencyPricePolicyVersion{}).Where("id = ?", 1).Update("agency_id", 9).Error)
			case "missing-schema":
				require.NoError(t, db.Migrator().DropTable(&model.AgencyActiveUserBinding{}))
			}
			recorder, response := requestAgencyPricingCatalog(t, 1)
			assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
			assert.Equal(t, false, response["success"])
			assert.NotContains(t, response, "data")
			assert.Equal(t, "private, no-store", recorder.Header().Get("Cache-Control"))
		})
	}
}

func TestGetPricingRetainsLegacyCatalogWithoutAgencyTables(t *testing.T) {
	db := agencyPricingCatalogFixture(t)
	require.NoError(t, db.Migrator().DropTable(&model.AgencyActiveUserBinding{}))
	recorder, response := requestAgencyPricingCatalog(t, 2)
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "standard", response["pricing_scope"])
	assert.NotContains(t, recorder.Body.String(), "sales_bps")
}
