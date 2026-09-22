package agencycontract

import (
	"strings"
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

func TestModelKeyIsCaseAndUTF8ExactAcrossDatabases(t *testing.T) {
	// ModelKey is a pure SHA-256 over the exact origin model name, so the same
	// model string maps to the same key on every supported database (SQLite /
	// MySQL / PostgreSQL) regardless of collation, while case and UTF-8
	// variants stay distinct (acceptance §22.2「模型大小写与UTF8精确匹配跨DB一致」).
	stable := func(name string) string {
		first, err := ModelKey(name)
		require.NoError(t, err)
		second, err := ModelKey(name)
		require.NoError(t, err)
		require.Equal(t, first, second)
		require.Len(t, first, 64)
		return first
	}
	base := stable("Hunyuan/hy3")
	require.NotEqual(t, base, stable("hunyuan/hy3"), "case must not be folded")
	require.NotEqual(t, base, stable("Hunyuan/HY3"))
	utf8Key := stable("智谱模型/GLM-4")
	require.NotEqual(t, utf8Key, stable("智谱模型/glm-4"), "UTF-8 case must stay distinct")
	require.NotEqual(t, utf8Key, stable("智谱模型 /GLM-4"))

	_, err := ModelKey(" model")
	require.ErrorIs(t, err, ErrInvalidModelName)
	_, err = ModelKey("model ")
	require.ErrorIs(t, err, ErrInvalidModelName)
	_, err = ModelKey("")
	require.ErrorIs(t, err, ErrInvalidModelName)
	_, err = ModelKey(strings.Repeat("a", 191))
	require.NoError(t, err)
	_, err = ModelKey(strings.Repeat("a", 192))
	require.ErrorIs(t, err, ErrInvalidModelName)
	_, err = ModelKey(strings.Repeat("\U0001D49C", 191)) // 4-byte runes: 764 bytes, at the byte limit
	require.NoError(t, err)
	_, err = ModelKey(strings.Repeat("\U0001D49C", 192))
	require.ErrorIs(t, err, ErrInvalidModelName)
}

func TestPlatformPolicyControlsCostWhileAgencySalesOverrideWins(t *testing.T) {
	agencySales := 7000
	base := Policy{
		DefaultSettlementBPS: 5000, DefaultSalesBPS: 6500, MinSpreadBPS: 500, SalesCapBPS: 30000,
		ModelOverrides: []ModelOverride{{OriginModelName: "glm-5.3", SalesBPS: &agencySales}},
	}
	platform := PlatformPolicy{ModelPrices: []PlatformModelPrice{{
		OriginModelName: "glm-5.3", PlatformCostBPS: 5000, AgencyCostBPS: 5500, DefaultSalesBPS: 6000,
	}}}
	effective, err := ApplyPlatformPolicy(base, platform)
	require.NoError(t, err)
	resolved, err := Resolve(effective, "glm-5.3")
	require.NoError(t, err)
	require.Equal(t, 5500, resolved.SettlementBPS)
	require.Equal(t, 7000, resolved.SalesBPS)

	agencySales = 5400
	base.ModelOverrides[0].SalesBPS = &agencySales
	_, err = ApplyPlatformPolicy(base, platform)
	require.Error(t, err, "an agency sale below platform-owned agency cost must fail closed")
}

func TestPlatformPolicyAllowsPerChannelCostsWithoutChangingAgencySales(t *testing.T) {
	agencySales := 7000
	base := Policy{
		DefaultSettlementBPS: 5000, DefaultSalesBPS: 6500, MinSpreadBPS: 500, SalesCapBPS: 30000,
		ModelOverrides: []ModelOverride{{OriginModelName: "doubao-seedance-2-0-260128", SalesBPS: &agencySales}},
	}
	platform := PlatformPolicy{ModelPrices: []PlatformModelPrice{{
		OriginModelName: "doubao-seedance-2-0-260128",
		ChannelCosts: []PlatformChannelCost{
			{ChannelID: 12, PlatformCostBPS: 5000},
			{ChannelID: 14, PlatformCostBPS: 5500},
		},
		AgencyCostBPS: 5500, DefaultSalesBPS: 6000,
	}}}
	require.NoError(t, ValidatePlatformPolicy(platform))

	effective, err := ApplyPlatformPolicy(base, platform)
	require.NoError(t, err)
	resolved, err := Resolve(effective, "doubao-seedance-2-0-260128")
	require.NoError(t, err)
	require.Equal(t, 5500, resolved.SettlementBPS)
	require.Equal(t, agencySales, resolved.SalesBPS, "channel routing must not change customer sales pricing")

	platform.ModelPrices[0].AgencyCostBPS = 4000
	require.NoError(t, ValidatePlatformPolicy(platform), "platform channel cost is internal and must not force an agency price change")
	platform.ModelPrices[0].ChannelCosts = append(platform.ModelPrices[0].ChannelCosts, PlatformChannelCost{ChannelID: 14, PlatformCostBPS: 5200})
	require.ErrorContains(t, ValidatePlatformPolicy(platform), "duplicate platform channel cost")
}

func TestPlatformPolicyRaisesInheritedSalesToMinimumSpread(t *testing.T) {
	policy := Policy{DefaultSalesBPS: 10000, MinSpreadBPS: 500, SalesCapBPS: 30000}
	platform := PlatformPolicy{ModelPrices: []PlatformModelPrice{{
		OriginModelName: "doubao-seedance-2.0", AgencyCostBPS: 8800, DefaultSalesBPS: 9000,
	}}}

	effective, err := ApplyPlatformPolicy(policy, platform)

	require.NoError(t, err)
	require.Len(t, effective.ModelOverrides, 1)
	require.Equal(t, 8800, *effective.ModelOverrides[0].SettlementBPS)
	require.Equal(t, 9300, *effective.ModelOverrides[0].SalesBPS)
}
