package billing_setting

// Built-in token prices use actual USD per million tokens. Keep new model
// defaults here instead of splitting them across the legacy ratio tables.
var builtinBillingExpr = map[string]string{
	// Yunwoke/Alibaba video task prices are CNY per output second. Billing
	// expressions use USD units, so divide the published CNY prices by the
	// gateway's USD exchange rate (7.3) before display conversion.
	"HappyHorse1.1": `u("kind") == "edit" && u("resolution") == "720P" ? tier("720P-edit", u("seconds") * 0.1232876712328767) : u("kind") == "edit" && u("resolution") == "1080P" ? tier("1080P-edit", u("seconds") * 0.2191780821917808) : u("resolution") == "480P" ? tier("480P", u("seconds") * 0.06164383561643836) : u("resolution") == "1080P" ? tier("1080P", u("seconds") * 0.1643835616438356) : tier("720P", u("seconds") * 0.1232876712328767)`,
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
}
