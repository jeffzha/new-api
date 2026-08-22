package ratio_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSeedance25UsesTokenRatioInsteadOfPerCallPrice(t *testing.T) {
	ratio, ok := defaultModelRatio["dreamina-seedance-2-5-filter-off"]
	require.True(t, ok)
	assert.InDelta(t, 10.70/2, ratio, 0.000001)

	_, usesPerCallPrice := defaultModelPrice["dreamina-seedance-2-5-filter-off"]
	assert.False(t, usesPerCallPrice)
}
