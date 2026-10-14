package service

import (
	"errors"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service/opsbridge"
	"github.com/gin-gonic/gin"
)

// OpsPricingStore uses the existing atomic, optimistic pricing service. No new
// pricing table, option writer, or database migration is introduced here.
type OpsPricingStore struct{}

func (OpsPricingStore) Read(names []string) (map[string]opsbridge.LocalPrice, error) {
	snapshot, err := model.GetModelPricingSnapshot(names)
	if err != nil {
		return nil, err
	}
	result := map[string]opsbridge.LocalPrice{}
	for _, name := range names {
		result[name] = opsbridge.LocalPrice{Version: snapshot.EmptyVersion, Configured: map[string]any{}}
	}
	for _, entry := range snapshot.Entries {
		result[entry.ModelName] = opsbridge.LocalPrice{Version: entry.Version, Configured: entry.Configured}
	}
	return result, nil
}

func (OpsPricingStore) Apply(before map[string]opsbridge.LocalPrice, after map[string]map[string]any) error {
	changes := make([]model.ModelPricingChange, 0, len(after))
	for name, price := range after {
		changes = append(changes, model.ModelPricingChange{ModelName: name, ExpectedVersion: before[name].Version, Pricing: price})
	}
	return model.UpdateModelPricing(changes)
}

func captureOpsUsage(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage) {
	b := opsbridge.Default
	if b == nil || info == nil || c.GetBool("ops_usage_written") {
		return
	}
	kind := opsbridge.RelayType(c.Request.Method, c.Request.URL.Path)
	if kind == "" {
		return
	}
	requestID := info.RequestId
	if requestID == "" {
		requestID = c.GetString(common.RequestIdKey)
	}
	provider, native, err := opsObservedUsage(c, info, usage)
	if err != nil {
		if gapErr := b.MarkUsageGap(requestID); gapErr != nil {
			common.SysError(gapErr.Error())
		}
		common.SysError("Ops usage requires official terminal usage; request retained for repair")
		return
	}
	if requestID == "" {
		common.SysError("Ops usage missing request identity")
		return
	}
	record := map[string]any{"event_id": requestID, "request_id": requestID, "occurred_at": time.Now().UTC().Format(time.RFC3339),
		"customer_context": map[string]any{"gateway_user_id": strconv.Itoa(info.UserId)},
		"routing_context":  map[string]any{"channel_id": strconv.Itoa(info.ChannelId), "model_name": info.OriginModelName, "call_type": kind},
		"provider":         provider, "usage": native, "is_stream": info.IsStream, "terminal": true}
	if info.TokenId > 0 {
		record["customer_context"].(map[string]any)["api_key_id"] = strconv.Itoa(info.TokenId)
	}
	if err := b.RecordUsage(requestID, record); err != nil {
		common.SysError(err.Error())
		if gapErr := b.MarkUsageGap(requestID); gapErr != nil {
			common.SysError(gapErr.Error())
		}
		return
	}
	c.Set("ops_usage_written", true)
}

func opsNativeUsage(usage *dto.Usage) (string, map[string]any, error) {
	if usage == nil || usage.BillingUsage == nil || usage.BillingUsage.Estimated {
		return "", nil, errors.New("official usage envelope is absent")
	}
	envelope := usage.BillingUsage
	var raw any
	provider := ""
	switch {
	case envelope.ClaudeUsage != nil:
		provider = "anthropic"
		raw = envelope.ClaudeUsage
	case envelope.GeminiUsageMetadata != nil:
		provider = "google"
		raw = envelope.GeminiUsageMetadata
	case envelope.OpenAIUsage != nil:
		provider = "openai"
		u := envelope.OpenAIUsage
		input := u.PromptTokensDetails
		if u.InputTokensDetails != nil {
			input = *u.InputTokensDetails
		}
		output := u.CompletionTokenDetails
		if u.OutputTokensDetails != nil {
			output = *u.OutputTokensDetails
		}
		prompt, completion := u.PromptTokens, u.CompletionTokens
		if u.InputTokens != 0 {
			prompt = u.InputTokens
		}
		if u.OutputTokens != 0 {
			completion = u.OutputTokens
		}
		in := map[string]any{"cached_tokens": input.CachedTokens}
		if input.AudioTokens != 0 {
			in["audio_tokens"] = input.AudioTokens
		}
		if input.ImageTokens != 0 {
			in["image_tokens"] = input.ImageTokens
		}
		if input.CachedTokensDetails != nil {
			in["cached_tokens_details"] = input.CachedTokensDetails
		}
		if input.CacheWriteTokens != 0 || input.CachedCreationTokens != 0 {
			return "", nil, errors.New("native cache-write semantics require a dedicated adapter")
		}
		out := map[string]any{}
		if output.AudioTokens != 0 {
			out["audio_tokens"] = output.AudioTokens
		}
		if output.ImageTokens != 0 {
			out["image_tokens"] = output.ImageTokens
		}
		if output.ReasoningTokens != 0 {
			out["reasoning_tokens"] = output.ReasoningTokens
		}
		raw = map[string]any{"prompt_tokens": prompt, "completion_tokens": completion, "prompt_tokens_details": in, "completion_tokens_details": out}
	default:
		return "", nil, errors.New("unsupported official usage envelope")
	}
	data, err := common.Marshal(raw)
	if err != nil {
		return "", nil, err
	}
	var result map[string]any
	err = common.Unmarshal(data, &result)
	return provider, result, err
}
