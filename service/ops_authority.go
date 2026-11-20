package service

import (
	"context"
	"encoding/base64"
	"errors"
	"maps"
	"math"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service/opsbilling"
	"github.com/QuantumNous/new-api/service/opspush"
	"github.com/QuantumNous/new-api/service/opswallet"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func projectAuthority() bool { return opsauthority.Enabled() }

func opsAuthorityRealtimeQuota(info *relaycommon.RelayInfo, usage *dto.RealtimeUsage) (int, error) {
	if info == nil || usage == nil {
		return 0, model.ErrOPSWalletHoldConflict
	}
	if _, err := realtimeUsageSum(usage, nil); err != nil {
		return 0, err
	}
	if snap := info.TieredBillingSnapshot; snap != nil && snap.BillingMode == "tiered_expr" {
		if billingexpr.UsesFixedPricingByHash(snap.ExprString, snap.ExprHash) {
			return 0, model.ErrOPSWalletHoldConflict
		}
		input := billingexpr.RequestInput{}
		if info.BillingRequestInput != nil {
			input = *info.BillingRequestInput
		}
		actual := &dto.Usage{PromptTokens: usage.InputTokens, CompletionTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens, PromptTokensDetails: usage.InputTokenDetails, CompletionTokenDetails: usage.OutputTokenDetails}
		params := BuildTieredTokenParams(actual, false, billingexpr.UsedVarsByHash(snap.ExprString, snap.ExprHash))
		result, err := billingexpr.ComputeTieredQuotaWithRequest(snap, params, input)
		if err != nil {
			return 0, err
		}
		noteQuotaClamp(info, result.Clamp)
		if result.Clamp != nil {
			return 0, result.Clamp
		}
		return result.ActualQuotaAfterGroup, nil
	}
	price := info.PriceData
	for _, value := range []float64{price.GroupRatioInfo.GroupRatio, price.ModelPrice, price.ModelRatio, price.AudioRatio, price.CompletionRatio, price.AudioCompletionRatio} {
		if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, model.ErrOPSWalletHoldConflict
		}
	}
	group := decimal.NewFromFloat(price.GroupRatioInfo.GroupRatio)
	amount := decimal.Zero
	if price.UsePrice {
		amount = decimal.NewFromFloat(price.ModelPrice).Mul(decimal.NewFromFloat(common.QuotaPerUnit)).Mul(group)
	} else {
		input := decimal.NewFromInt(int64(usage.InputTokenDetails.TextTokens)).Add(decimal.NewFromInt(int64(usage.InputTokenDetails.AudioTokens)).Mul(decimal.NewFromFloat(price.AudioRatio)))
		output := decimal.NewFromInt(int64(usage.OutputTokenDetails.TextTokens)).Mul(decimal.NewFromFloat(price.CompletionRatio)).Add(decimal.NewFromInt(int64(usage.OutputTokenDetails.AudioTokens)).Mul(decimal.NewFromFloat(price.AudioRatio)).Mul(decimal.NewFromFloat(price.AudioCompletionRatio)))
		amount = input.Add(output).Mul(decimal.NewFromFloat(price.ModelRatio)).Mul(group)
		if price.ModelRatio > 0 && group.IsPositive() && amount.LessThanOrEqual(decimal.Zero) {
			amount = decimal.NewFromInt(1)
		}
	}
	quota, clamp := common.QuotaFromDecimalChecked(amount)
	noteQuotaClamp(info, clamp)
	if clamp != nil {
		return 0, clamp
	}
	return quota, nil
}

func opsAuthorityRelayEvent(c *gin.Context, info *relaycommon.RelayInfo, quota int, usage *dto.Usage) ([]byte, error) {
	if info == nil || usage == nil || info.TaskRelayInfo != nil || quota < 0 || quota > common.MaxQuota {
		return nil, model.ErrOPSWalletHoldConflict
	}
	other := model.NewLogOther()
	other.SetPublic("billing_scope", opsauthority.Scope())
	other.SetPublic("billing_source", "ops")
	other.SetPublic("project_relay_terminal", true)
	appendRequestPath(c, info, other)
	other.SetPublic("model_ratio", info.PriceData.ModelRatio)
	other.SetPublic("completion_ratio", info.PriceData.CompletionRatio)
	other.SetPublic("group_ratio", info.PriceData.GroupRatioInfo.GroupRatio)
	other.SetPublic("cache_ratio", info.PriceData.CacheRatio)
	other.SetPublic("cache_tokens", usage.PromptTokensDetails.CachedTokens)
	other.SetPublic("cache_creation_tokens_5m", usage.ClaudeCacheCreation5mTokens)
	other.SetPublic("cache_creation_tokens_1h", usage.ClaudeCacheCreation1hTokens)
	other.SetPublic("model_price", info.PriceData.ModelPrice)
	other.SetPublic("audio_ratio", info.PriceData.AudioRatio)
	other.SetPublic("audio_completion_ratio", info.PriceData.AudioCompletionRatio)
	other.SetPublic("image_ratio", info.PriceData.ImageRatio)
	other.SetPublic("usage_semantic", usage.UsageSemantic)
	other.SetPublic("usage_source", usage.UsageSource)
	normalized := BuildTieredTokenParams(usage, usage.UsageSemantic == "anthropic", map[string]bool{"cr": true, "cc": true, "cc1h": true, "ai": true, "ao": true, "img": true, "img_o": true})
	// The station wire contract bounds token counts. Reject invalid facts before
	// converting normalized totals or posting a monetary event to OPS.
	for _, count := range []float64{float64(usage.PromptTokens), float64(usage.CompletionTokens), normalized.Len, normalized.CR, normalized.CC, normalized.CC1h, normalized.CC + normalized.CC1h, normalized.AI, normalized.AO, normalized.Img, normalized.ImgO} {
		if count < 0 || count > 1_000_000_000_000 {
			return nil, model.ErrOPSWalletHoldConflict
		}
	}
	cacheCreation := int(normalized.CC + normalized.CC1h)
	other.SetPublic("cache_creation_tokens", cacheCreation)
	appendCacheCreationPricing(other, info.PriceData.CacheCreationPricingConfigured, cacheCreation, info.PriceData.CacheCreationRatio, info.PriceData.CacheCreation5mRatio, info.PriceData.CacheCreation1hRatio)
	other.SetPublic("input_tokens_total", normalized.Len)
	other.SetPublic("billing_tokens", map[string]any{"p": normalized.P, "c": normalized.C, "len": normalized.Len, "cr": normalized.CR, "cc": normalized.CC, "cc1h": normalized.CC1h, "ai": normalized.AI, "ao": normalized.AO, "img": normalized.Img, "img_o": normalized.ImgO})
	facts := map[string]any{}
	if input := info.BillingRequestInput; input != nil {
		maps.Copy(facts, input.Usage)
		if input.ReferenceImageCount != nil {
			facts["input_images"] = *input.ReferenceImageCount
		}
	}
	maps.Copy(facts, map[string]any{
		"text_input": usage.PromptTokensDetails.TextTokens, "text_output": usage.CompletionTokenDetails.TextTokens,
		"audio_input": usage.PromptTokensDetails.AudioTokens, "audio_output": usage.CompletionTokenDetails.AudioTokens,
		"image_input": usage.PromptTokensDetails.ImageTokens, "image_output": usage.CompletionTokenDetails.ImageTokens,
	})
	other.SetPublic("usage_facts", facts)
	if info.BillingImageCount != nil {
		other.SetPublic("image_count", *info.BillingImageCount)
	}
	if snap := info.TieredBillingSnapshot; snap != nil {
		other.SetPublic("billing_mode", snap.BillingMode)
		other.SetPublic("expr_b64", base64.StdEncoding.EncodeToString([]byte(snap.ExprString)))
	}
	attachQuotaSaturation(c, info, other)
	if info.ChannelMeta != nil {
		model.CaptureUpstreamLogReference(other, info.ChannelBaseUrl)
	}
	event, err := opspush.EventUSD(&model.Log{UserId: info.UserId, Type: model.LogTypeConsume, RequestId: info.RequestId, CreatedAt: info.StartTime.Unix(), ModelName: info.GetBillingModelName(), TokenId: info.TokenId, ChannelId: info.GetChannelID(), Group: info.UsingGroup, Quota: quota, PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, IsStream: info.IsStream, Other: other.JSONString()}, "", nil, opspush.Conversion{QuotaPerUnit: common.QuotaPerUnit, USDExchangeRate: 1})
	if err != nil {
		return nil, err
	}
	return common.Marshal(event)
}

// Keep the project billing entry points and wire protocol shared. The USD
// session additionally serializes admission across all overseas gateway nodes.
func newOPSAuthoritySession(c *gin.Context, info *relaycommon.RelayInfo, quota int) (*opswallet.Session, error) {
	if info == nil {
		return nil, model.ErrOPSWalletHoldConflict
	}
	quote, err := AgencyQuoteForUser(info.UserId, info.TokenId, info.GetBillingModelName(), info.StartTime.UnixMilli())
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if quote != nil {
		// Wallet custody fixes the accounting currency independently of a UI
		// display preference. The accepted pricing coefficients stay unchanged.
		quote.CurrencyCode, quote.QuotaPerUnit, quote.ExchangeRate = "USD", "500000", "1"
	}
	info.AgencyPricing = quote
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	return opswallet.NewSession(ctx, model.DB, opsbilling.Default, info, quota)
}

func opsAuthorityAPIError(err error) *types.NewAPIError {
	if errors.Is(err, opsbilling.ErrInsufficientBalance) || errors.Is(err, model.ErrOPSInsufficientAvailable) {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	}
	if errors.Is(err, model.ErrInsufficientAgencyTokenQuota) {
		return types.NewErrorWithStatusCode(err, types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	}
	return types.NewErrorWithStatusCode(opsbilling.ErrBalanceUnavailable, types.ErrorCodeQueryDataError, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
}
