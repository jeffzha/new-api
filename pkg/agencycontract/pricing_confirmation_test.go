package agencycontract

import (
	"github.com/stretchr/testify/require"
	"testing"
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
