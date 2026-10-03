package service

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPreConsumeBillingRejectsDurableUserWithoutQuote(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:agency-billing-guard-test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, db.AutoMigrate(&model.User{}))

	user := &model.User{
		Username:    "durable-without-quote",
		Password:    "not-used-password",
		Status:      common.UserStatusEnabled,
		BillingMode: model.AgencyDurableBillingMode,
	}
	require.NoError(t, db.Create(user).Error)

	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginContext.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	apiErr := PreConsumeBilling(ginContext, 10, &relaycommon.RelayInfo{UserId: user.Id})
	require.Error(t, apiErr)
	require.Equal(t, "agency pricing unavailable for durable user", apiErr.Error())
}

func TestAgencyPricingHierarchyPermitsOnlyExactCustomerCostException(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:agency-customer-cost-quote-test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, db.AutoMigrate(&model.Agency{}, &model.AgencyPricePolicyVersion{}))

	policy := agencycontract.Policy{
		DefaultSettlementBPS: 8000,
		DefaultSalesBPS:      9000,
		MinSpreadBPS:         500,
		SalesCapBPS:          30000,
	}
	encoded, err := common.Marshal(policy)
	require.NoError(t, err)
	policyRow := model.AgencyPricePolicyVersion{AgencyID: 1, Revision: 1, PolicyJSON: string(encoded), PolicyHash: "customer-cost-test"}
	require.NoError(t, db.Create(&policyRow).Error)
	agency := model.Agency{ID: 1, Code: "customer-cost", DisplayName: "Customer cost", Status: "active", InviteCode: "COSTTEST", Depth: 1, CurrentPolicyVersionID: policyRow.ID, PriceRevision: 1, StateRevision: 1, Version: 1}
	require.NoError(t, db.Create(&agency).Error)

	nodes, eligible, reason, err := agencyPricingHierarchy(agency, "model-a", 8000, true, "", agencycontract.PlatformPolicy{})
	require.NoError(t, err)
	require.True(t, eligible)
	require.Empty(t, reason)
	require.Equal(t, 8000, *nodes[0].SalesBPS)

	_, _, _, err = agencyPricingHierarchy(agency, "model-a", 8000, false, "", agencycontract.PlatformPolicy{})
	require.ErrorContains(t, err, "below the permitted minimum")
	_, _, _, err = agencyPricingHierarchy(agency, "model-a", 8200, true, "", agencycontract.PlatformPolicy{})
	require.ErrorContains(t, err, "below the permitted minimum")
}
