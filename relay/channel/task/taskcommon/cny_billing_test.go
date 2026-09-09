package taskcommon

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQuotaFromCNYPerMillionTokensAppliesFrozenGroupRatio(t *testing.T) {
	oldQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500_000
	t.Cleanup(func() {
		common.QuotaPerUnit = oldQuotaPerUnit
	})
	snapshot := &model.TaskProviderBillingSnapshot{
		Provider:                  model.TaskBillingProviderDoubaoVideoCNY,
		Currency:                  "CNY",
		UnitPricePerMillionTokens: "46",
		CNYPerUSD:                 "7.3",
		GroupRatio:                1.2,
	}

	quota, clamp, err := QuotaFromCNYPerMillionTokens(1_000_000, snapshot)

	require.NoError(t, err)
	assert.Nil(t, clamp)
	expected := decimal.NewFromInt(46).
		Div(decimal.NewFromFloat(7.3)).
		Mul(decimal.NewFromInt(500_000)).
		Mul(decimal.NewFromFloat(1.2))
	assert.Equal(t, common.QuotaFromDecimal(expected), quota)
}

func TestQuotaFromCNYPerMillionTokensReportsOverflow(t *testing.T) {
	oldQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500_000
	t.Cleanup(func() {
		common.QuotaPerUnit = oldQuotaPerUnit
	})
	snapshot := &model.TaskProviderBillingSnapshot{
		Provider:                  model.TaskBillingProviderDoubaoVideoCNY,
		Currency:                  "CNY",
		UnitPricePerMillionTokens: "1000000",
		CNYPerUSD:                 "1",
		GroupRatio:                1,
	}

	quota, clamp, err := QuotaFromCNYPerMillionTokens(1_000_000, snapshot)

	require.NoError(t, err)
	require.NotNil(t, clamp)
	assert.Equal(t, common.QuotaClampOverflow, clamp.Kind)
	assert.Equal(t, common.MaxQuota, quota)
}
