package billing_setting

// Prices below are the supplied DeepSeek CNY tariff converted at the
// repository's standard 7.3 CNY/USD rate. The same expression is shared by
// reasoning variants; existing administrator overrides still take precedence.
const deepSeekPeakCondition = `!is_holiday("Asia/Shanghai") && weekday("Asia/Shanghai") >= 1 && weekday("Asia/Shanghai") <= 5 && ((hour("Asia/Shanghai") >= 9 && hour("Asia/Shanghai") < 12) || (hour("Asia/Shanghai") >= 14 && hour("Asia/Shanghai") < 18))`
const deepSeekFlashExpression = deepSeekPeakCondition + ` ? tier("peak", p * 0.273972602739726 + cr * 0.00547945205479452 + c * 1.095890410958904) : tier("off_peak", p * 0.136986301369863 + cr * 0.00273972602739726 + c * 0.547945205479452)`
const deepSeekProExpression = deepSeekPeakCondition + ` ? tier("peak", p * 1.232876712328767 + cr * 0.0410958904109589 + c * 3.698630136986301) : tier("off_peak", p * 0.6164383561643836 + cr * 0.02054794520547945 + c * 1.849315068493151)`
const happyHorseExpression = `u("kind") == "edit" && u("resolution") == "720P" ? tier("720P-edit", u("seconds") * 0.1232876712328767) : u("kind") == "edit" && u("resolution") == "1080P" ? tier("1080P-edit", u("seconds") * 0.2191780821917808) : u("resolution") == "480P" ? tier("480P", u("seconds") * 0.06164383561643836) : u("resolution") == "1080P" ? tier("1080P", u("seconds") * 0.1643835616438356) : tier("720P", u("seconds") * 0.1232876712328767)`

// Built-in token prices use actual USD per million tokens. Keep new model
// defaults here instead of splitting them across the legacy ratio tables.
var builtinBillingExpr = map[string]string{
	"deepseek-flash":               deepSeekFlashExpression,
	"deepseek-v4-flash":            deepSeekFlashExpression,
	"deepseek-v4-flash-none":       deepSeekFlashExpression,
	"deepseek-v4-flash-max":        deepSeekFlashExpression,
	"deepseek-v4-flash-vision-exp": deepSeekFlashExpression,
	"deepseek-v4-pro":              deepSeekProExpression,
	"deepseek-v4-pro-none":         deepSeekProExpression,
	"deepseek-v4-pro-max":          deepSeekProExpression,
	// Yunwoke/Alibaba video task prices are CNY per output second. Billing
	// expressions use USD units, so divide the published CNY prices by the
	// gateway's USD exchange rate (7.3) before display conversion.
	"happyhorse-1.1": happyHorseExpression,
	"HappyHorse1.1":  happyHorseExpression,
	// https://developers.openai.com/api/docs/pricing (Standard, 2026-09-09).
	// The Images API reports image output in output_tokens, normalized to c.
	"gpt-image-2":            `tier("standard", p * 5 + cr * 1.25 + img * 8 + img_cr * 2 + c * 30)`,
	"gpt-image-2.5-sunburst": `tier("standard", p * 5 + cr * 1.25 + img * 8 + img_cr * 2 + c * 30)`,
	"gpt-image-2.5-flare":    `tier("standard", p * 5 + cr * 1.25 + img * 8 + img_cr * 2 + c * 30)`,
	// https://developers.openai.com/api/docs/models/gpt-6-astra
	// Standard pricing; the long-context rates apply to the whole request.
	// Do not infer service-tier discounts from incoming request parameters:
	// channels filter service_tier by default, so it may not reach the upstream.
	"gpt-6-astra": `len <= 272000 ? tier("standard", p * 10 + c * 50 + cr * 1 + cc * 12.5) : tier("long_context", p * 20 + c * 75 + cr * 2 + cc * 25)`,
	// qwen-image-3.0: base per generated image 0.18 CNY + 0.02 CNY per reference
	// image (image-to-image). Converted to USD at the gateway's 7.3 rate:
	// 0.18/7.3 = 0.0246575…, 0.02/7.3 = 0.0027397… . reference_image_count is 0
	// for text-to-image, so the default stays exactly 0.18 CNY per image.
	"qwen-image-3.0": `tier("image", fixed(0.0246575342465753)) * image_count + tier("reference", fixed(0.0027397260273972603)) * reference_image_count`,
}
