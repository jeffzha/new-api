package agencycontract

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCommissionGoldenCase(t *testing.T) {
	policy := Policy{DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000}
	resolved, err := Resolve(policy, "Hunyuan/hy3")
	require.NoError(t, err)
	result, err := Calculate(1000, resolved, 600, true)
	require.NoError(t, err)
	require.Equal(t, int64(900), result.ChargedQuota)
	require.Equal(t, int64(750), result.SettlementCostQuota)
	require.Equal(t, int64(150), result.TheoreticalCommissionQuota)
	require.Equal(t, int64(100), result.CommissionQuota)
}

func TestExplicitZeroSettlementDoesNotInherit(t *testing.T) {
	zero := 0
	policy := Policy{DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 0, SalesCapBPS: 30000, ModelOverrides: []ModelOverride{{OriginModelName: "free-model", SettlementBPS: &zero}}}
	resolved, err := Resolve(policy, "free-model")
	require.NoError(t, err)
	require.Equal(t, 0, resolved.SettlementBPS)
	result, err := Calculate(1000, resolved, 900, true)
	require.NoError(t, err)
	require.Equal(t, int64(0), result.SettlementCostQuota)
}

func TestInvalidModelNameAndNegativeChargeRejected(t *testing.T) {
	_, err := ModelKey(" model")
	require.ErrorIs(t, err, ErrInvalidModelName)
	policy := Policy{DefaultSettlementBPS: 7500, DefaultSalesBPS: 9000, MinSpreadBPS: 500, SalesCapBPS: 30000}
	resolved, err := Resolve(policy, "m")
	require.NoError(t, err)
	_, err = Calculate(-1, resolved, 0, true)
	require.Error(t, err)
}

func TestCommissionForPaidUsesFinalChargedQuota(t *testing.T) {
	// Final provider usage B can differ from the estimate; K must use B as
	// denominator and round half away from zero.
	value, err := CommissionForPaid(150, 600, 900, true)
	require.NoError(t, err)
	require.Equal(t, int64(100), value)
	value, err = CommissionForPaid(150, 450, 700, true)
	require.NoError(t, err)
	require.Equal(t, int64(96), value)
}

func TestCommissionForPaidZeroAndInvalidRatios(t *testing.T) {
	value, err := CommissionForPaid(10, 0, 0, true)
	require.NoError(t, err)
	require.Zero(t, value)
	_, err = CommissionForPaid(10, 11, 10, true)
	require.Error(t, err)
}
