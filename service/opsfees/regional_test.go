package opsfees

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/require"
)

func TestUSDWalletCannotEnableCNYFeeContract(t *testing.T) {
	settings := operation_setting.GetGeneralSetting()
	previous := settings.QuotaDisplayType
	t.Cleanup(func() { settings.QuotaDisplayType = previous })
	settings.QuotaDisplayType = operation_setting.QuotaDisplayTypeUSD
	t.Setenv("OPS_FEES_ENABLED", "true")
	t.Setenv("OPS_FEES_URL", "https://ops.example")
	t.Setenv("OPS_FEES_KEY_ID", "overseas-test")
	t.Setenv("OPS_FEES_SECRET", "overseas-test-secret-only")
	t.Setenv("OPS_FEES_CURRENCY", "CNY")
	require.ErrorContains(t, StartConfigured(context.Background()), "verified wallet currency contract")
	require.False(t, model.OpsFeesEnabled())
}
