package billing_setting

import (
	"testing"

	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWangpaiProviderDefaultsRespectAdministratorPrices(t *testing.T) {
	saved := billingSetting
	ratios, prices := ratio_setting.ModelRatio2JSONString(), ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() {
		billingSetting = saved
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(ratios))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(prices))
	})
	reset := func() {
		billingSetting = BillingSetting{BillingMode: map[string]string{}, BillingExpr: map[string]string{}, PluginBillingExpr: map[string]string{}}
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
	}
	reset()
	for _, plugin := range []string{"wangpaiai", "newapi-video"} {
		for _, model := range []string{"MiniMax-H3", "MiniMax-H3-2K", "MiniMax-H3-Sensitive"} {
			expr, ok := ResolveTaskBillingExpr(plugin, model, "")
			require.True(t, ok)
			assert.Equal(t, wangpaiH3Expression, expr)
		}
	}
	_, ok := ResolveTaskBillingExpr("hailuo", "MiniMax-H3", "")
	assert.False(t, ok)
	assert.Len(t, GetPluginBillingExprCopy(), 6)
	assert.Empty(t, billingSetting.PluginBillingExpr)

	for _, testCase := range []struct {
		name  string
		setup func()
	}{
		{"explicit ratio mode", func() { billingSetting.BillingMode["MiniMax-H3"] = BillingModeRatio }},
		{"explicit model ratio", func() { require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"MiniMax-H3":1}`)) }},
		{"explicit request price", func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"MiniMax-H3":0.2}`)) }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			reset()
			testCase.setup()
			_, ok := ResolveTaskBillingExpr("wangpaiai", "MiniMax-H3", "")
			assert.False(t, ok)
			assert.NotContains(t, GetPluginBillingExprCopy(), "wangpaiai::MiniMax-H3")
		})
	}
	reset()
	billingSetting.BillingMode["MiniMax-H3"] = BillingModeTieredExpr
	billingSetting.BillingExpr["MiniMax-H3"] = `tier("custom", u("seconds") * 1)`
	expr, ok := ResolveTaskBillingExpr("wangpaiai", "MiniMax-H3", "")
	require.True(t, ok)
	assert.Equal(t, billingSetting.BillingExpr["MiniMax-H3"], expr)
	billingSetting.PluginBillingExpr["wangpaiai::MiniMax-H3"] = `tier("custom-plugin", u("seconds") * 2)`
	expr, ok = ResolveTaskBillingExpr("wangpaiai", "MiniMax-H3", "")
	require.True(t, ok)
	assert.Equal(t, billingSetting.PluginBillingExpr["wangpaiai::MiniMax-H3"], expr)
}

func TestWangpaiCreditsConvertToDomesticRMB(t *testing.T) {
	for _, testCase := range []struct {
		credits, material, cny float64
	}{
		{4, 0, 0.4},
		{7.5, 0, 0.75},
		{10, 0, 1},
		{15, 0, 1.5},
		{19, 0, 1.9},
		{7.5, 1.5, 0.9},
		{0, 0, 0},
	} {
		amount, _, err := billingexpr.RunExprWithRequest(wangpaiH3Expression, billingexpr.TokenParams{}, billingexpr.RequestInput{
			Usage: map[string]any{"output_credits": testCase.credits, "material_credits": testCase.material},
		})
		require.NoError(t, err)
		assert.InDelta(t, testCase.cny, amount*7.3, 1e-10)
	}
}
