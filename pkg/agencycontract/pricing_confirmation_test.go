package agencycontract

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPendingPlatformModelDoesNotInheritAgencyOverrides(t *testing.T) {
	pending := false
	sales := 8000
	platform := PlatformPolicy{Revision: 1, ModelPrices: []PlatformModelPrice{{OriginModelName: "pending", PricingConfirmed: &pending}}}
	policy := Policy{DefaultSalesBPS: 10000, SalesCapBPS: 30000, ModelOverrides: []ModelOverride{{OriginModelName: "pending", SalesBPS: &sales}}}
	effective, err := ApplyPlatformPolicy(policy, platform)
	require.NoError(t, err)
	require.Empty(t, effective.ModelOverrides)
	require.False(t, ModelPricingConfirmed(platform, "pending"))
	require.False(t, ModelPricingConfirmed(platform, "new-model"))
}

func TestConfirmedPlatformCostAndBlankAgencyDownstreamAreSeparateBoundaries(t *testing.T) {
	platform := PlatformPolicy{Revision: 1, ModelPrices: []PlatformModelPrice{{OriginModelName: "model", PlatformCostBPS: 4000, AgencyCostBPS: 6000, DefaultSalesBPS: 10000}}}
	existing := 10000
	policy := Policy{DefaultSalesBPS: 10000, MinSpreadBPS: 100, SalesCapBPS: 30000}
	effective, err := ApplyPlatformPolicy(policy, platform)
	require.NoError(t, err)
	require.Equal(t, 6000, *effective.ModelOverrides[0].SettlementBPS)
	require.Equal(t, 6500, *effective.ModelOverrides[0].ChildCostBPS)
	require.Equal(t, 4000, platform.ModelPrices[0].PlatformCostBPS)
	require.Equal(t, 6000, platform.ModelPrices[0].AgencyCostBPS)
	policy.ModelOverrides = []ModelOverride{{OriginModelName: "model", ChildCostBPS: &existing, SalesBPS: &existing, SalesSource: PriceSourceManual}}
	effective, err = ApplyPlatformPolicy(policy, platform)
	require.NoError(t, err)
	require.Equal(t, 10000, *effective.ModelOverrides[0].ChildCostBPS)
	require.Equal(t, 10000, *effective.ModelOverrides[0].SalesBPS)
}

func TestConfirmedPlatformCostReplacesLegacyManualRootCost(t *testing.T) {
	legacyCost := 7200
	manualChild := 9000
	manualSales := 10000
	platform := PlatformPolicy{Revision: 2, ModelPrices: []PlatformModelPrice{{OriginModelName: "model", AgencyCostBPS: 6500, DefaultSalesBPS: 10000}}}
	policy := Policy{DefaultSalesBPS: 10000, MinSpreadBPS: 100, SalesCapBPS: 30000, ModelOverrides: []ModelOverride{{
		OriginModelName:  "model",
		SettlementBPS:    &legacyCost,
		SettlementSource: PriceSourceManual,
		ChildCostBPS:     &manualChild,
		ChildCostSource:  PriceSourceManual,
		SalesBPS:         &manualSales,
		SalesSource:      PriceSourceManual,
	}}}

	effective, err := ApplyPlatformPolicy(policy, platform)
	require.NoError(t, err)
	require.Len(t, effective.ModelOverrides, 1)
	require.Equal(t, 6500, *effective.ModelOverrides[0].SettlementBPS)
	require.Equal(t, PriceSourceInherited, effective.ModelOverrides[0].SettlementSource)
	require.Equal(t, manualChild, *effective.ModelOverrides[0].ChildCostBPS)
	require.Equal(t, manualSales, *effective.ModelOverrides[0].SalesBPS)
}

func TestIncompleteConfirmationDoesNotUnlockPricing(t *testing.T) {
	no := false
	price := PlatformModelPrice{OriginModelName: "model", AgencyCostConfirmed: &no}
	require.False(t, price.IsPricingConfirmed())
	require.True(t, (PlatformModelPrice{OriginModelName: "legacy"}).IsPricingConfirmed())
}

func TestTenantAndExclusiveRootCostsPreserveProcurementAndManualSales(t *testing.T) {
	specificCost, specificSales := 6500, 9500
	platform := PlatformPolicy{
		ModelPrices: []PlatformModelPrice{{OriginModelName: "model", AgencyCostBPS: 6000, DefaultSalesBPS: 10000,
			ChannelCosts: []PlatformChannelCost{{ChannelID: 1, PlatformCostBPS: 2000}, {ChannelID: 2, PlatformCostBPS: 3000}}}},
		TenantPolicy: &Policy{DefaultSettlementBPS: 5000, DefaultChildCostBPS: 6000, DefaultSalesBPS: 9000,
			MinSpreadBPS: 100, SalesCapBPS: 30000,
			ModelOverrides: []ModelOverride{{OriginModelName: "model", ChildCostBPS: &specificCost, SalesBPS: &specificSales}}},
		RootAgencyCosts: []RootAgencyCost{{AgencyID: 7, OriginModelName: "*", SettlementBPS: 6200},
			{AgencyID: 7, OriginModelName: "model", SettlementBPS: 6400}},
	}
	require.NoError(t, ValidatePlatformPolicy(platform))
	policy := Policy{DefaultSettlementBPS: 4000, DefaultChildCostBPS: 4500, DefaultSalesBPS: 10000,
		ChildCostSource: PriceSourceDefault, SalesSource: PriceSourceDefault, SalesCapBPS: 30000}
	result, err := ApplyPlatformPolicyForAgency(policy, platform, 7)
	require.NoError(t, err)
	assert.Equal(t, 6200, result.DefaultSettlementBPS)
	assert.Equal(t, 6700, result.DefaultChildCostBPS)
	require.Len(t, result.ModelOverrides, 1)
	assert.Equal(t, 6400, *result.ModelOverrides[0].SettlementBPS)
	assert.Equal(t, 9500, *result.ModelOverrides[0].SalesBPS)

	manualSale, manualChild := 11000, 10500
	policy.ModelOverrides = []ModelOverride{{OriginModelName: "model", SalesBPS: &manualSale, ChildCostBPS: &manualChild}}
	result, err = ApplyPlatformPolicyForAgency(policy, platform, 8)
	require.NoError(t, err)
	assert.Equal(t, 6500, *result.ModelOverrides[0].SettlementBPS)
	assert.Equal(t, manualSale, *result.ModelOverrides[0].SalesBPS)
	assert.Equal(t, manualChild, *result.ModelOverrides[0].ChildCostBPS)
	assert.Equal(t, 2000, platform.ModelPrices[0].ChannelCosts[0].PlatformCostBPS)
	assert.Equal(t, 4000, policy.DefaultSettlementBPS)
}
