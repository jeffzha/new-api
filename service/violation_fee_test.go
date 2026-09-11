package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCalcViolationFeeQuotaSaturatesAndReturnsClamp(t *testing.T) {
	oldQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	t.Cleanup(func() {
		common.QuotaPerUnit = oldQuotaPerUnit
	})

	quota, clamp := calcViolationFeeQuotaWithClamp(float64(common.MaxQuota), 2)

	require.NotNil(t, clamp)
	assert.Equal(t, common.MaxQuota, quota)
	assert.Equal(t, "QuotaFromDecimal", clamp.Op)
	assert.Equal(t, common.QuotaClampOverflow, clamp.Kind)
	assert.Equal(t, common.MaxQuota, clamp.Clamped)
}
