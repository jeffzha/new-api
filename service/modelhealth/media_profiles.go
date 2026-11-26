package modelhealth

import (
	"math"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/shopspring/decimal"
)

// Profiles describe the existing relay contract; they never change its pricing
// or route around the platform key. Unknown providers retain editable drafts.
func confirmVideoProfile(t Target, p *Template) {
	// These plugin contracts were reviewed against the installed source, not
	// merely its mutable display name. A new version needs protocol review.
	if t.pluginKey == "cn-locopal-video" && t.pluginHash != "80062690d040212074f39b605d925ae8663cca8f2d8ca658ce408c7fbbf7a1f6" ||
		t.pluginKey == "oiioii" && t.pluginHash != "9f6af9cbaa58075d7bfbaa6c6628b8fc782069fd956de2b6fe3c15353099f224" {
		return
	}
	switch {
	case t.channelType == constant.ChannelTypeAli && slices.Contains([]string{"wan3.0-video", "wan3.0-video-prime"}, t.Upstream):
		p.Body["seconds"], p.Body["resolution"], p.Body["size"] = "2", "480P", "480P"
		confirmMediaPrice(t, p, map[string]any{"seconds": float64(2), "resolution": "480P"}, true, false)
	case t.channelType == constant.ChannelTypeHappyHorse && slices.Contains([]string{"HappyHorse1.1", "happyhorse-1.1"}, t.Upstream):
		delete(p.Body, "seconds")
		p.Body["duration"], p.Body["resolution"] = float64(3), "480P"
		confirmMediaPrice(t, p, map[string]any{"seconds": float64(3), "resolution": "480P", "kind": "text"}, true, false)
	case t.channelType == constant.ChannelTypeTaskPlugin && t.pluginKey == "cn-locopal-video" && t.Model == "MiniMax-H3" && t.Upstream == "h3-video":
		p.Body["resolution"], p.Body["size"], p.Body["ratio"] = "768P", "768P", "16:9"
		confirmMediaPrice(t, p, map[string]any{"seconds": float64(4), "resolution": "768P", "input_images": float64(0), "input_video_seconds": float64(0)}, true, false)
	case t.channelType == constant.ChannelTypeTaskPlugin && t.pluginKey == "oiioii" && strings.HasPrefix(t.Upstream, "doubao-seedance-"):
		p.Body["resolution"], p.Body["ratio"], p.Body["generate_audio"] = "480p", "16:9", false
		confirmMediaPrice(t, p, map[string]any{"tokens": float64(38430), "resolution": "480p", "video_input": "none"}, true, false)
	}
	if p.MinimumConfirmed {
		p.ResultField = ""
	}
}

func confirmImageProfile(t Target, p *Template) {
	switch {
	case t.channelType == constant.ChannelTypeAli && slices.Contains([]string{"qwen-image-2.0", "qwen-image-3.0"}, t.Upstream):
		p.Body["size"] = "1328x1328"
	case (t.channelType == constant.ChannelTypeOpenAI || t.channelType == constant.ChannelTypeSeedreamArkOpenAI) && slices.Contains([]string{"doubao-seedream-5-0-260128", "doubao-seedream-5-0-pro-260628", "doubao-seedream-5-0-flash-260915"}, t.Upstream):
		// All these channels expose the existing OpenAI Images contract.
		p.Body["size"] = "1024x1024"
	case t.channelType == constant.ChannelTypeOpenAISeedance && t.Model == "doubao-seedream-5-0-lite" && t.Upstream == "Doubao-Seedream-5.0-lite":
		p.Body["size"] = "2K"
	default:
		return
	}
	p.Body["response_format"] = "url"
	confirmMediaPrice(t, p, nil, false, true)
}

// Reserve the configured expression/legacy price with headroom. This is an
// internal admission estimate only; displayed actual fees still come from OPS.
// Unknown, invalid or non-positive prices cannot confirm a template.
func confirmMediaPrice(t Target, p *Template, usage map[string]any, task, image bool) {
	expression, hasExpression := billing_setting.ResolveTaskBillingExpr(t.pluginKey, t.Model, t.Upstream)
	var usd float64
	if hasExpression {
		body, err := common.Marshal(p.Body)
		if err != nil {
			return
		}
		tokens := billingexpr.TokenParams{}
		if image && t.Model == "doubao-seedream-5-0-lite" {
			// The integrated provider reports 16384 output tokens for one 2K
			// image. Include a conservative short prompt before price headroom.
			tokens = billingexpr.TokenParams{P: 128, C: 16384, Len: 128}
		}
		cost, _, err := billingexpr.RunExprWithRequest(expression, tokens, billingexpr.RequestInput{Body: body, Usage: usage, ImageCount: common.GetPointer(1), ReferenceImageCount: common.GetPointer(0)})
		if err != nil {
			return
		}
		usd = cost
		// Image relay expressions (including fixed()) return a value scaled
		// by one million; task usage expressions already return direct USD.
		if image {
			usd /= 1000000
		}
	} else {
		price, ok := ratio_setting.GetModelPrice(t.Model, false)
		if !ok {
			return
		}
		usd = price
		if task {
			// Wan3 legacy prices use the 720P per-second baseline.
			if t.channelType != constant.ChannelTypeAli || !slices.Contains([]string{"wan3.0-video", "wan3.0-video-prime"}, t.Upstream) {
				return
			}
			seconds, ok := usage["seconds"].(float64)
			if !ok {
				return
			}
			usd *= seconds * 0.5
		} else if !image {
			return
		}
	}
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd <= 0 {
		return
	}
	rate := operation_setting.USDExchangeRate
	if math.IsNaN(rate) || math.IsInf(rate, 0) || rate <= 0 || rate > 1000 {
		return
	}
	group := 1.0
	for _, name := range t.Groups {
		v := ratio_setting.GetGroupRatio(name)
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return
		}
		group = max(group, v)
	}
	ceiling := decimal.NewFromFloat(usd).Mul(decimal.NewFromFloat(rate)).Mul(decimal.NewFromFloat(group)).Mul(decimal.RequireFromString("1.25")).Mul(decimal.NewFromInt(1000000)).Ceil()
	if !ceiling.IsPositive() || ceiling.GreaterThan(decimal.NewFromInt(100000000)) {
		return
	}
	p.MaxMicro = max(p.MaxMicro, ceiling.IntPart())
	p.MinimumConfirmed = true
}
