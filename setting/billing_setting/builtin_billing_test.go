package billing_setting_test

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocoPalDeepSeek261017BuiltinBilling(t *testing.T) {
	settings := config.GlobalConfig.Get("billing_setting").(*billing_setting.BillingSetting)
	saved := *settings
	savedRatios, savedPrices := ratio_setting.ModelRatio2JSONString(), ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() {
		*settings = saved
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
	})
	*settings = billing_setting.BillingSetting{BillingMode: map[string]string{}, BillingExpr: map[string]string{}}
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))

	const modelName = "deepseek-v4-1-flash-1017"
	expression, ok := billing_setting.GetBillingExpr(modelName)
	require.True(t, ok)
	assert.Equal(t, billing_setting.BillingModeTieredExpr, billing_setting.GetBillingMode(modelName))

	usage := &dto.Usage{
		PromptTokens: 1_100_000, CompletionTokens: 1_000_000,
		PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 100_000},
	}
	params := service.BuildTieredTokenParams(usage, false, billingexpr.UsedVars(expression))
	shanghai := time.FixedZone("Asia/Shanghai", 8*60*60)

	for _, tc := range []struct {
		name  string
		at    time.Time
		quota int
	}{
		{"flat tariff before morning window", time.Date(2026, 10, 18, 8, 59, 0, 0, shanghai), 685_205},
		{"flat tariff on Sunday morning", time.Date(2026, 10, 18, 9, 0, 0, 0, shanghai), 685_205},
		{"flat tariff between windows", time.Date(2026, 10, 18, 12, 0, 0, 0, shanghai), 685_205},
		{"flat tariff in afternoon window", time.Date(2026, 10, 18, 14, 0, 0, 0, shanghai), 685_205},
		{"flat tariff after afternoon window", time.Date(2026, 10, 18, 18, 0, 0, 0, shanghai), 685_205},
		{"flat tariff with UTC clock", time.Date(2026, 10, 19, 1, 0, 0, 0, time.UTC), 685_205},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := billingexpr.ComputeTieredQuotaWithRequest(&billingexpr.BillingSnapshot{
				ExprString: expression, ExprHash: billingexpr.ExprHashString(expression),
				GroupRatio: 1, QuotaPerUnit: 500_000, EvaluationTime: tc.at,
			}, params, billingexpr.RequestInput{})
			require.NoError(t, err)
			assert.Equal(t, "base", result.MatchedTier)
			assert.Equal(t, tc.quota, result.ActualQuotaAfterGroup)
			assert.Empty(t, result.RequestRules)
		})
	}
}

func TestOctober30YellowRowTariffs(t *testing.T) {
	for _, tc := range []struct {
		name, model, tier string
		params            billingexpr.TokenParams
		cny               float64
	}{
		{"mini short boundary", "doubao-seed-2-0-mini-260428", "32K", billingexpr.TokenParams{Len: 32000, P: 1000, C: 1000, CR: 1000}, 0.00224},
		{"mini above short boundary", "doubao-seed-2-0-mini-260428", "128K", billingexpr.TokenParams{Len: 32001, P: 1000, C: 1000, CR: 1000}, 0.00448},
		{"mini middle boundary", "doubao-seed-2-0-mini-260428", "128K", billingexpr.TokenParams{Len: 128000, P: 1000, C: 1000, CR: 1000}, 0.00448},
		{"mini long context", "doubao-seed-2-0-mini-260428", "256K", billingexpr.TokenParams{Len: 128001, P: 1000, C: 1000, CR: 1000}, 0.00896},
		{"lite standard", "doubao-seed-2.1-lite", "standard", billingexpr.TokenParams{P: 1000, C: 1000, CR: 1000}, 0.00366},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expression, ok := billing_setting.GetBuiltinBillingExpr(tc.model)
			require.True(t, ok)
			require.NoError(t, billing_setting.SmokeTestExpr(expression))
			result, err := billingexpr.ComputeTieredQuota(&billingexpr.BillingSnapshot{
				ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, QuotaPerUnit: 500000,
			}, tc.params)
			require.NoError(t, err)
			assert.Equal(t, tc.tier, result.MatchedTier)
			assert.Equal(t, common.QuotaRound(tc.cny/7.3*500000), result.ActualQuotaAfterGroup)
		})
	}
	shanghai := time.FixedZone("Asia/Shanghai", 8*60*60)
	for _, tc := range []struct {
		model         string
		peak, offPeak float64
	}{
		{"deepseek-v4.1-flash", 10.04, 5.02},
		{"deepseek-v4-pro", 36.3, 18.15},
	} {
		for _, hour := range []int{8, 9, 12, 14, 18} {
			expression, ok := billing_setting.GetBuiltinBillingExpr(tc.model)
			require.True(t, ok)
			rate := tc.offPeak
			if hour == 9 || hour == 14 {
				rate = tc.peak
			}
			result, err := billingexpr.ComputeTieredQuotaWithRequest(&billingexpr.BillingSnapshot{
				ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, QuotaPerUnit: 500000,
				EvaluationTime: time.Date(2026, 10, 30, hour, 0, 0, 0, shanghai),
			}, billingexpr.TokenParams{P: 1e6, C: 1e6, CR: 1e6}, billingexpr.RequestInput{})
			require.NoError(t, err)
			assert.Equal(t, common.QuotaRound(rate/7.3*500000), result.ActualQuotaAfterGroup, "%s hour %d", tc.model, hour)
		}
	}
	expression, ok := billing_setting.GetBuiltinBillingExpr("HappyHorse1.1")
	require.True(t, ok)
	for resolution, cny := range map[string]float64{"480P": 0.45, "720P": 0.9, "1080P": 1.2} {
		result, err := billingexpr.ComputeTieredQuotaWithRequest(&billingexpr.BillingSnapshot{
			ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, QuotaPerUnit: 500000, UsageFactsBilling: true,
		}, billingexpr.TokenParams{}, billingexpr.RequestInput{Usage: map[string]any{"kind": "video", "resolution": resolution, "seconds": 1.0}})
		require.NoError(t, err)
		assert.Equal(t, common.QuotaRound(cny/7.3*500000), result.ActualQuotaAfterGroup)
	}
}

func TestFunASRFlashBuiltinBilling(t *testing.T) {
	expression, ok := billing_setting.GetBuiltinBillingExpr("fun-asr-flash-2026-06-15")
	require.True(t, ok)
	result, err := billingexpr.ComputeTieredQuotaWithRequest(&billingexpr.BillingSnapshot{
		ExprString: expression, ExprHash: billingexpr.ExprHashString(expression),
		GroupRatio: 1, QuotaPerUnit: 500_000, ExprVersion: 1, UsageFactsBilling: true,
	}, billingexpr.TokenParams{}, billingexpr.RequestInput{Usage: map[string]any{"seconds": 36_000.0}})
	require.NoError(t, err)
	assert.InDelta(t, 542_465.7534246575, result.ActualQuotaBeforeGroup, 1e-7)
	assert.Equal(t, 542_466, result.ActualQuotaAfterGroup)
}

func TestShenzhenSeedreamLiteContextBilling(t *testing.T) {
	settings := config.GlobalConfig.Get("billing_setting").(*billing_setting.BillingSetting)
	saved := *settings
	savedRatios, savedPrices := ratio_setting.ModelRatio2JSONString(), ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() {
		*settings = saved
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
	})
	*settings = billing_setting.BillingSetting{BillingMode: map[string]string{}, BillingExpr: map[string]string{}}
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
	const name = "doubao-seedream-5-0-lite"
	expression, ok := billing_setting.GetBillingExpr(name)
	require.True(t, ok)
	for _, tc := range []struct {
		name, tier                       string
		input, cached, output            int
		inputRate, cacheRate, outputRate float64
	}{
		{"observed single 2K image", "base", 0, 0, 16384, 0.6, 0.12, 3.6},
		{"short boundary with cache", "base", 32000, 16000, 100, 0.6, 0.12, 3.6},
		{"above short boundary", "medium_context", 32001, 0, 100, 0.9, 0.18, 5.4},
		{"medium boundary with cache", "medium_context", 128000, 100000, 100, 0.9, 0.18, 5.4},
		{"above medium boundary with cache", "long_context", 128001, 100000, 100, 1.8, 0.36, 10.8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := &dto.Usage{PromptTokens: tc.input, CompletionTokens: tc.output,
				PromptTokensDetails: dto.InputTokenDetails{CachedTokens: tc.cached}}
			result, err := billingexpr.ComputeTieredQuota(&billingexpr.BillingSnapshot{
				ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, QuotaPerUnit: 500000,
			}, service.BuildTieredTokenParams(usage, false, billingexpr.UsedVars(expression)))
			require.NoError(t, err)
			assert.Equal(t, tc.tier, result.MatchedTier)
			cny := (float64(tc.input-tc.cached)*tc.inputRate + float64(tc.cached)*tc.cacheRate + float64(tc.output)*tc.outputRate) / 1e6
			assert.Equal(t, common.QuotaRound(cny/7.3*500000), result.ActualQuotaAfterGroup)
		})
	}
	settings.BillingMode[name] = "tiered_expr"
	settings.BillingExpr[name] = `tier("custom", fixed(0.1)) * image_count`
	actual, ok := billing_setting.GetBillingExpr(name)
	require.True(t, ok)
	assert.Equal(t, settings.BillingExpr[name], actual)
}

func TestGPT6AstraBuiltinBilling(t *testing.T) {
	settings := config.GlobalConfig.Get("billing_setting").(*billing_setting.BillingSetting)
	saved := *settings
	savedRatios, savedPrices := ratio_setting.ModelRatio2JSONString(), ratio_setting.ModelPrice2JSONString()
	savedOptions := common.OptionMap
	t.Cleanup(func() {
		*settings, common.OptionMap = saved, savedOptions
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
	})
	common.OptionMap = map[string]string{"billing_setting.billing_mode": `{}`, "billing_setting.billing_expr": `{}`}
	require.NoError(t, config.GlobalConfig.LoadFromDB(common.OptionMap))
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
	assert.Equal(t, billing_setting.BillingModeTieredExpr, billing_setting.GetBillingMode("gpt-6-astra"))
	expression, ok := billing_setting.GetBillingExpr("gpt-6-astra")
	require.True(t, ok)

	for _, tc := range []struct {
		name                           string
		input, output, cached, written int
		request                        string
		quota                          int
	}{
		{"standard", 1000, 100, 0, 0, `{}`, 7500},
		{"client flex cannot discount standard pricing", 1000, 100, 0, 0, `{"service_tier":"flex"}`, 7500},
		{"cache at context boundary", 272000, 1000, 200000, 20000, `{}`, 510000},
		{"whole request above boundary", 272001, 1000, 200000, 20000, `{}`, 1007510},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := &dto.Usage{
				PromptTokens: tc.input, CompletionTokens: tc.output,
				PromptTokensDetails: dto.InputTokenDetails{CachedTokens: tc.cached, CacheWriteTokens: tc.written},
			}
			params := service.BuildTieredTokenParams(usage, false, billingexpr.UsedVars(expression))
			result, err := billingexpr.ComputeTieredQuotaWithRequest(&billingexpr.BillingSnapshot{
				ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, QuotaPerUnit: 500000,
			}, params, billingexpr.RequestInput{Body: []byte(tc.request)})
			require.NoError(t, err)
			assert.Equal(t, tc.quota, result.ActualQuotaAfterGroup)
		})
	}

	t.Run("admin options expose defaults without persisting them", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		controller.GetOptions(ctx)
		var response struct {
			Success bool
			Data    []model.Option
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		require.True(t, response.Success)
		found := map[string]string{}
		for _, option := range response.Data {
			if _, ok := common.OptionMap[option.Key]; ok {
				assert.NotContains(t, found, option.Key)
				var values map[string]string
				require.NoError(t, common.UnmarshalJsonStr(option.Value, &values))
				found[option.Key] = values["gpt-6-astra"]
				assert.Equal(t, `{}`, common.OptionMap[option.Key])
			}
		}
		assert.Equal(t, map[string]string{"billing_setting.billing_mode": "tiered_expr", "billing_setting.billing_expr": expression}, found)
	})

	for _, tc := range []struct {
		name, mode, expr, ratios, prices, wantMode string
	}{
		{"custom expression overrides legacy price", "tiered_expr", "p * 7", `{"gpt-6-astra":8}`, `{}`, "tiered_expr"},
		{"explicit ratio mode", "ratio", "", `{}`, `{}`, "ratio"},
		{"existing free token price", "", "", `{"gpt-6-astra":0}`, `{}`, "ratio"},
		{"existing per-call price", "", "", `{}`, `{"gpt-6-astra":0.1}`, "ratio"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			*settings = billing_setting.BillingSetting{BillingMode: map[string]string{}, BillingExpr: map[string]string{}}
			if tc.mode != "" {
				settings.BillingMode["gpt-6-astra"] = tc.mode
			}
			if tc.expr != "" {
				settings.BillingExpr["gpt-6-astra"] = tc.expr
			}
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(tc.ratios))
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(tc.prices))
			assert.Equal(t, tc.wantMode, billing_setting.GetBillingMode("gpt-6-astra"))
			actual, ok := billing_setting.GetBillingExpr("gpt-6-astra")
			assert.Equal(t, tc.expr, actual)
			assert.Equal(t, tc.expr != "", ok)
		})
	}
}

func TestImageModelBuiltinPricesAndOverrides(t *testing.T) {
	settings := config.GlobalConfig.Get("billing_setting").(*billing_setting.BillingSetting)
	saved := *settings
	savedRatios, savedPrices := ratio_setting.ModelRatio2JSONString(), ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() {
		*settings = saved
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
	})
	for _, name := range []string{"gpt-image-2", "gpt-image-2.5-sunburst", "gpt-image-2.5-flare"} {
		t.Run(name, func(t *testing.T) {
			*settings = billing_setting.BillingSetting{BillingMode: map[string]string{}, BillingExpr: map[string]string{}}
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
			expression, ok := billing_setting.GetBillingExpr(name)
			require.True(t, ok)
			usage := &dto.Usage{PromptTokens: 1000, CompletionTokens: 100, PromptTokensDetails: dto.InputTokenDetails{
				CachedTokens: 300, ImageTokens: 600, CachedTokensDetails: &dto.CachedTokenDetails{ImageTokens: common.GetPointer(200)},
			}}
			result, err := billingexpr.ComputeTieredQuota(&billingexpr.BillingSnapshot{ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, QuotaPerUnit: 500000},
				service.BuildTieredTokenParams(usage, false, billingexpr.UsedVars(expression)))
			require.NoError(t, err)
			assert.Equal(t, 4113, result.ActualQuotaAfterGroup)
			encoded, err := common.Marshal(map[string]float64{name: 0})
			require.NoError(t, err)
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(encoded)))
			assert.Equal(t, "ratio", billing_setting.GetBillingMode(name))
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(encoded)))
			assert.Equal(t, "ratio", billing_setting.GetBillingMode(name))
			settings.BillingMode[name] = "tiered_expr"
			settings.BillingExpr[name] = `tier("custom", p * 7)`
			actual, ok := billing_setting.GetBillingExpr(name)
			require.True(t, ok)
			assert.Equal(t, settings.BillingExpr[name], actual)
		})
	}
}
