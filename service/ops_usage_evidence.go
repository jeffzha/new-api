package service

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service/opsbridge"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const opsOfficialUsageKey = "ops_official_usage"

type opsUsageEvidence struct {
	Provider string
	Usage    map[string]any
}

// ObserveOpsResponseUsage retains only usage, never prompts, results or media.
// It runs before local billing adds estimated tokens or fallback quantities.
func ObserveOpsResponseUsage(c *gin.Context, protocol string, body []byte) {
	if opsbridge.Default == nil {
		return
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() || root.Get("error").Exists() {
		return
	}
	var usage map[string]any
	if raw := root.Get("usage"); raw.IsObject() {
		if err := common.Unmarshal([]byte(raw.Raw), &usage); err != nil {
			return
		}
	}
	if protocol == "openai_images" && len(usage) == 0 {
		images := root.Get("data")
		if !images.IsArray() || len(images.Array()) == 0 {
			return
		}
		usage = map[string]any{"images": len(images.Array())}
	}
	if len(usage) > 0 {
		c.Set(opsOfficialUsageKey, opsUsageEvidence{Provider: protocol, Usage: usage})
	}
}

// Each completed image event carries per-image usage. Partial images and
// intermediate previews never enter the accumulator.
func ObserveOpsImageStreamUsage(c *gin.Context, body []byte) {
	if opsbridge.Default == nil {
		return
	}
	root := gjson.ParseBytes(body)
	typeName := root.Get("type").String()
	if typeName != "image_generation.completed" && typeName != "image_edit.completed" {
		return
	}
	value, _ := c.Get(opsOfficialUsageKey)
	evidence, _ := value.(opsUsageEvidence)
	if evidence.Usage == nil {
		evidence = opsUsageEvidence{Provider: "openai_image_stream", Usage: map[string]any{
			"images": 0, "completed_usages": []map[string]any{},
		}}
	}
	count := min(evidence.Usage["images"].(int)+1, dto.MaxImageN+1)
	if count > dto.MaxImageN {
		evidence.Usage["invalid_count"] = true
	} else {
		var usage map[string]any
		raw := root.Get("usage")
		if raw.IsObject() && common.Unmarshal([]byte(raw.Raw), &usage) == nil && len(usage) > 0 {
			evidence.Usage["completed_usages"] = append(evidence.Usage["completed_usages"].([]map[string]any), usage)
		}
	}
	evidence.Usage["images"] = count
	c.Set(opsOfficialUsageKey, evidence)
}

func opsObservedUsage(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage) (string, map[string]any, error) {
	if info.IsStream && (info.StreamStatus == nil || info.StreamStatus.HasErrors() ||
		(info.StreamStatus.EndReason != relaycommon.StreamEndReasonDone && info.StreamStatus.EndReason != relaycommon.StreamEndReasonEOF)) {
		return "", nil, errors.New("stream lacks a successful terminal state")
	}
	if value, exists := c.Get(opsOfficialUsageKey); exists {
		if evidence, ok := value.(opsUsageEvidence); ok && len(evidence.Usage) > 0 {
			return evidence.Provider, evidence.Usage, nil
		}
	}
	if common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens) {
		return "", nil, errors.New("locally estimated usage is not official")
	}
	return opsNativeUsage(usage)
}
