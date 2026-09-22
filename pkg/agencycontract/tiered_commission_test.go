package agencycontract

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCalculateTieredCommissionConservesCustomerChargeLessPlatformCost(t *testing.T) {
	sales := 9000
	result, err := CalculateTieredCommission(10000, []TierNode{{AgencyID: 1, CostBPS: 7000}, {AgencyID: 2, CostBPS: 8000, SalesBPS: &sales}}, 6000, 500, 900, true)
	require.NoError(t, err)
	require.Equal(t, int64(9000), result.CustomerChargedQuota)
	require.Equal(t, int64(6000), result.PlatformCostQuota)
	require.Equal(t, int64(1000), result.PlatformMarginQuota)
	require.Len(t, result.Segments, 2)
	require.Equal(t, int64(1000), result.Segments[0].TheoreticalQuota)
	require.Equal(t, int64(1000), result.Segments[1].TheoreticalQuota)
	require.Equal(t, int64(2000), result.TotalCommissionQuota)
}

func TestCalculateTieredCommissionRejectsInvalidSpread(t *testing.T) {
	sales := 9000
	_, err := CalculateTieredCommission(10000, []TierNode{{AgencyID: 1, CostBPS: 7000}, {AgencyID: 2, CostBPS: 7200, SalesBPS: &sales}}, 6000, 500, 0, true)
	require.ErrorContains(t, err, "spread")
}

func TestCalculateTieredCommissionUsesStrictestNodeSpread(t *testing.T) {
	sales := 9500
	_, err := CalculateTieredCommission(10000, []TierNode{{AgencyID: 1, CostBPS: 7000, MinSpreadBPS: 500}, {AgencyID: 2, CostBPS: 7800, MinSpreadBPS: 1000, SalesBPS: &sales}}, 6000, 0, 0, true)
	require.ErrorContains(t, err, "spread")
}

func TestValidateCommissionSplitsRequiresEnvelopeTotals(t *testing.T) {
	event := BillingEvent{
		SchemaVersion:              ComponentSchemaVersion,
		EventType:                  "agency.billing_finalized",
		CommissionEligible:         true,
		TheoreticalCommissionQuota: 300,
		CommissionQuota:            270,
		CommissionAmountMicros:     270,
		CommissionSplits: []CommissionSplit{
			{AgencyID: 1, Depth: 1, CostBPS: 7000, TheoreticalQuota: 100, CommissionQuota: 90, CommissionAmountMicros: 90},
			{AgencyID: 2, Depth: 2, CostBPS: 8000, TheoreticalQuota: 200, CommissionQuota: 180, CommissionAmountMicros: 180},
		},
	}
	require.NoError(t, ValidateCommissionSplits(event))
	event.CommissionSplits[1].CommissionQuota++
	require.ErrorContains(t, ValidateCommissionSplits(event), "totals mismatch")
}
