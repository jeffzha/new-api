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
