package agencyhub

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadConfigDefaultsMinimumSpreadToZero(t *testing.T) {
	t.Setenv("AGENCY_HUB_MIN_SPREAD_BPS", "")

	config := LoadConfig()

	require.Zero(t, config.MinSpreadBPS)
}
