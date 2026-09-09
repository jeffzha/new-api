package upstreamevent

import (
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service/opsmonitor"
	"github.com/gin-gonic/gin"
)

func BuildUpstreamResponseEvent(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage, extra map[string]interface{}) ProviderEvent {
	event := baseRelayEvent(c, info, EventUpstreamResponseReceived)
	if usage != nil {
		event.UsageContext.UsageJSON = anyToMap(usage)
	}
	if raw, ok := GetRawUsage(c); ok {
		event.UsageContext.RawUsageJSON = map[string]interface{}{
			"provider": raw.Provider,
			"format":   raw.Format,
			"usage":    raw.Usage,
		}
	}
	if event.UsageContext.ExtraJSON == nil {
		event.UsageContext.ExtraJSON = map[string]interface{}{}
	}
	event.UsageContext.ExtraJSON["usage_quality_hint"] = usageQualityHint(event)
	for k, v := range extra {
		event.UsageContext.ExtraJSON[k] = v
	}
	ensureEvent(&event, extraString(event.UsageContext.ExtraJSON, "billing_stage"))
	return event
}

func EmitUpstreamResponse(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage, extra map[string]interface{}) {
	opsmonitor.ObserveUsage(c, info, usage)
	Emit(BuildUpstreamResponseEvent(c, info, usage, extra), PriorityHigh)
}

func BuildBillingDeltaEvent(c *gin.Context, info *relaycommon.RelayInfo, stage string, quotaDelta int, preConsumedQuota int, actualQuota int, extra map[string]interface{}) ProviderEvent {
	event := baseRelayEvent(c, info, EventBillingDownstreamDelta)
	if event.UsageContext.ExtraJSON == nil {
		event.UsageContext.ExtraJSON = map[string]interface{}{}
	}
	event.UsageContext.ExtraJSON["billing_stage"] = stage
	event.UsageContext.ExtraJSON["quota_delta"] = quotaDelta
	event.UsageContext.ExtraJSON["pre_consumed_quota"] = preConsumedQuota
	event.UsageContext.ExtraJSON["actual_quota"] = actualQuota
	if info != nil {
		event.UsageContext.ExtraJSON["billing_source"] = info.BillingSource
		event.UsageContext.ExtraJSON["subscription_id"] = info.SubscriptionId
		event.UsageContext.ExtraJSON["subscription_pre_consumed"] = info.SubscriptionPreConsumed
		event.UsageContext.ExtraJSON["subscription_post_delta"] = info.SubscriptionPostDelta
	}
	for k, v := range extra {
		event.UsageContext.ExtraJSON[k] = v
	}
	ensureEvent(&event, stage)
	return event
}

func EmitBillingDelta(c *gin.Context, info *relaycommon.RelayInfo, stage string, quotaDelta int, preConsumedQuota int, actualQuota int, extra map[string]interface{}) {
	if quotaDelta == 0 && preConsumedQuota == 0 && actualQuota == 0 {
		return
	}
	Emit(BuildBillingDeltaEvent(c, info, stage, quotaDelta, preConsumedQuota, actualQuota, extra), PriorityCritical)
}

func BuildTaskBillingDeltaEvent(task *model.Task, stage string, quotaDelta int, preConsumedQuota int, actualQuota int, extra map[string]interface{}) ProviderEvent {
	event := ProviderEvent{
		SchemaVersion:  SchemaVersion,
		SourceSystem:   currentConfig().SourceSystem,
		EventType:      EventBillingDownstreamDelta,
		OccurredAt:     nowRFC3339(),
		TaskID:         task.TaskID,
		UpstreamTaskID: task.GetUpstreamTaskID(),
		CustomerContext: CustomerContext{
			GatewayCustomerID: intString(task.UserId),
			GatewayUserID:     intString(task.UserId),
			TokenID:           intString(task.PrivateData.TokenId),
			Group:             task.Group,
		},
		RoutingContext: RoutingContext{
			ChannelID:         intString(task.ChannelId),
			ModelName:         taskModelName(task),
			OriginModelName:   task.Properties.OriginModelName,
			UpstreamModelName: task.Properties.UpstreamModelName,
			CallType:          "video_generation",
			RelayMode:         "video_task",
		},
		UsageContext: UsageContext{
			ExtraJSON: map[string]interface{}{
				"billing_stage":      stage,
				"quota_delta":        quotaDelta,
				"pre_consumed_quota": preConsumedQuota,
				"actual_quota":       actualQuota,
				"billing_source":     task.PrivateData.BillingSource,
				"subscription_id":    task.PrivateData.SubscriptionId,
			},
		},
	}
	if bc := task.PrivateData.BillingContext; bc != nil {
		event.RoutingContext.ModelName = bc.OriginModelName
		event.UsageContext.RequestMetadataJSON = map[string]interface{}{}
		for k, v := range bc.OtherRatios {
			event.UsageContext.RequestMetadataJSON[k] = v
		}
	}
	for k, v := range extra {
		event.UsageContext.ExtraJSON[k] = v
	}
	ensureEvent(&event, stage)
	return event
}

func EmitTaskBillingDelta(task *model.Task, stage string, quotaDelta int, preConsumedQuota int, actualQuota int, extra map[string]interface{}) {
	if task == nil || (quotaDelta == 0 && preConsumedQuota == 0 && actualQuota == 0) {
		return
	}
	Emit(BuildTaskBillingDeltaEvent(task, stage, quotaDelta, preConsumedQuota, actualQuota, extra), PriorityCritical)
}

func BuildTaskSubmitRequestEvent(c *gin.Context, info *relaycommon.RelayInfo, requestBody []byte) ProviderEvent {
	event := baseRelayEvent(c, info, EventTaskSubmitRequest)
	if info != nil && info.TaskRelayInfo != nil {
		event.TaskID = info.PublicTaskID
		event.UsageContext.ExtraJSON = map[string]interface{}{
			"action": info.Action,
		}
	}
	event.PayloadHashes.RequestBodyHash = sha256Hex(requestBody)
	event.UsageContext.RequestMetadataJSON = metadataFromBody(requestBody)
	ensureEvent(&event, "")
	return event
}

func EmitTaskSubmitRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody []byte) {
	Emit(BuildTaskSubmitRequestEvent(c, info, requestBody), PriorityHigh)
}

func BuildTaskSubmitResponseEvent(c *gin.Context, info *relaycommon.RelayInfo, upstreamTaskID string, taskData []byte, platform constant.TaskPlatform, quota int) ProviderEvent {
	event := baseRelayEvent(c, info, EventTaskSubmitResponse)
	event.UpstreamTaskID = upstreamTaskID
	if info != nil && info.TaskRelayInfo != nil {
		event.TaskID = info.PublicTaskID
		event.UsageContext.ExtraJSON = map[string]interface{}{
			"action":   info.Action,
			"platform": string(platform),
			"quota":    quota,
		}
	}
	event.PayloadHashes.ResponseBodyHash = sha256Hex(taskData)
	event.UsageContext.ResponseMetadataJSON = metadataFromBody(taskData)
	ensureEvent(&event, upstreamTaskID)
	return event
}

func EmitTaskSubmitResponse(c *gin.Context, info *relaycommon.RelayInfo, upstreamTaskID string, taskData []byte, platform constant.TaskPlatform, quota int) {
	opsmonitor.ObserveRelay(c, info)
	Emit(BuildTaskSubmitResponseEvent(c, info, upstreamTaskID, taskData, platform, quota), PriorityCritical)
}

func BuildTaskTerminalEvent(task *model.Task, status string, result *relaycommon.TaskInfo, responseBody []byte) ProviderEvent {
	eventType := EventTaskCompleted
	if status != string(model.TaskStatusSuccess) {
		eventType = EventTaskFailed
	}
	event := ProviderEvent{
		SchemaVersion:  SchemaVersion,
		SourceSystem:   currentConfig().SourceSystem,
		EventType:      eventType,
		OccurredAt:     nowRFC3339(),
		TaskID:         task.TaskID,
		UpstreamTaskID: task.GetUpstreamTaskID(),
		CustomerContext: CustomerContext{
			GatewayCustomerID: intString(task.UserId),
			GatewayUserID:     intString(task.UserId),
			TokenID:           intString(task.PrivateData.TokenId),
			Group:             task.Group,
		},
		RoutingContext: RoutingContext{
			ChannelID:         intString(task.ChannelId),
			ModelName:         taskModelName(task),
			OriginModelName:   task.Properties.OriginModelName,
			UpstreamModelName: task.Properties.UpstreamModelName,
			CallType:          "video_generation",
			RelayMode:         "video_task",
		},
		PayloadHashes: PayloadHashes{
			ResponseBodyHash: sha256Hex(responseBody),
		},
		UsageContext: UsageContext{
			ResponseMetadataJSON: metadataFromBody(responseBody),
			ExtraJSON: map[string]interface{}{
				"task_status": status,
				"action":      task.Action,
				"progress":    task.Progress,
				"quota":       task.Quota,
			},
		},
	}
	if bc := task.PrivateData.BillingContext; bc != nil {
		event.RoutingContext.ModelName = bc.OriginModelName
		event.RoutingContext.OriginModelName = bc.OriginModelName
		event.UsageContext.RequestMetadataJSON = map[string]interface{}{}
		for k, v := range bc.OtherRatios {
			event.UsageContext.RequestMetadataJSON[k] = v
		}
		event.UsageContext.ExtraJSON["billing_source"] = task.PrivateData.BillingSource
		event.UsageContext.ExtraJSON["per_call_billing"] = bc.PerCallBilling
	}
	if result != nil {
		event.UsageContext.ExtraJSON["total_tokens"] = result.TotalTokens
		event.UsageContext.ExtraJSON["completion_tokens"] = result.CompletionTokens
		event.UsageContext.ExtraJSON["finish_reason"] = result.Reason
		event.UsageContext.ExtraJSON["result_url_present"] = result.Url != ""
	}
	ensureEvent(&event, status)
	return event
}

func EmitTaskTerminal(task *model.Task, status string, result *relaycommon.TaskInfo, responseBody []byte) {
	priority := PriorityCritical
	Emit(BuildTaskTerminalEvent(task, status, result, responseBody), priority)
}

func baseRelayEvent(c *gin.Context, info *relaycommon.RelayInfo, eventType string) ProviderEvent {
	cfg := currentConfig()
	event := ProviderEvent{
		SchemaVersion: SchemaVersion,
		SourceSystem:  cfg.SourceSystem,
		EventType:     eventType,
		OccurredAt:    nowRFC3339(),
		UsageContext:  UsageContext{},
	}
	if c != nil {
		event.RequestID = c.GetString(common.RequestIdKey)
		event.UpstreamRequestID = c.GetString(common.UpstreamRequestIdKey)
		if c.Request != nil {
			event.RoutingContext.Method = c.Request.Method
			event.RoutingContext.Path = c.Request.URL.Path
		}
	}
	if info == nil {
		ensureEvent(&event, "")
		return event
	}
	if event.RequestID == "" {
		event.RequestID = info.RequestId
	}
	group := info.UsingGroup
	if group == "" {
		group = info.TokenGroup
	}
	if group == "" {
		group = info.UserGroup
	}
	event.CustomerContext = CustomerContext{
		GatewayCustomerID: intString(info.UserId),
		GatewayUserID:     intString(info.UserId),
		TokenID:           intString(info.TokenId),
		APIKeyFingerprint: keyFingerprint(info.TokenKey),
		APIKeyLast4:       keyLast4(info.TokenKey),
		APIKeyRedacted:    info.TokenKey != "",
		Group:             group,
	}
	channelID := 0
	channelType := ""
	upstreamModelName := ""
	upstreamBaseURL := ""
	isModelMapped := false
	if info.ChannelMeta != nil {
		channelID = info.ChannelMeta.ChannelId
		channelType = constant.ChannelTypeNames[info.ChannelMeta.ChannelType]
		upstreamModelName = info.ChannelMeta.UpstreamModelName
		upstreamBaseURL = info.ChannelMeta.ChannelBaseUrl
		isModelMapped = info.ChannelMeta.IsModelMapped
	} else if c != nil {
		channelID = common.GetContextKeyInt(c, constant.ContextKeyChannelId)
		channelType = constant.ChannelTypeNames[common.GetContextKeyInt(c, constant.ContextKeyChannelType)]
		upstreamModelName = common.GetContextKeyString(c, constant.ContextKeyOriginalModel)
		upstreamBaseURL = common.GetContextKeyString(c, constant.ContextKeyChannelBaseUrl)
	}
	if upstreamModelName == "" {
		upstreamModelName = info.OriginModelName
	}
	event.RoutingContext.ChannelID = intString(channelID)
	event.RoutingContext.ChannelType = channelType
	event.RoutingContext.ModelName = info.OriginModelName
	event.RoutingContext.OriginModelName = info.OriginModelName
	event.RoutingContext.UpstreamModelName = upstreamModelName
	event.RoutingContext.CallType = callTypeFromRelayMode(info.RelayMode)
	event.RoutingContext.RelayMode = relayModeName(info.RelayMode)
	event.RoutingContext.RelayFormat = string(info.GetFinalRequestRelayFormat())
	event.RoutingContext.UpstreamBaseURL = upstreamBaseURL
	event.RoutingContext.IsStream = info.IsStream
	event.RoutingContext.IsModelMapped = isModelMapped
	if info.TaskRelayInfo != nil {
		event.TaskID = info.PublicTaskID
		event.UsageContext.ExtraJSON = map[string]interface{}{
			"action": info.Action,
		}
	}
	ensureEvent(&event, "")
	return event
}

func ensureEvent(event *ProviderEvent, salt string) {
	if event.SchemaVersion == "" {
		event.SchemaVersion = SchemaVersion
	}
	if event.SourceSystem == "" {
		event.SourceSystem = currentConfig().SourceSystem
	}
	if event.OccurredAt == "" {
		event.OccurredAt = nowRFC3339()
	}
	if event.EventID == "" {
		event.EventID = eventID(event.SourceSystem, event.EventType, event.RequestID, event.TaskID, event.UpstreamTaskID, salt)
	}
}

func eventID(parts ...string) string {
	key := strings.Join(parts, "|")
	sum := sha256.Sum256([]byte(key))
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])
	if len(encoded) > 26 {
		encoded = encoded[:26]
	}
	return "evt_" + strings.ToLower(encoded)
}

func metadataFromBody(body []byte) map[string]interface{} {
	if len(body) == 0 {
		return nil
	}
	var raw map[string]interface{}
	if err := common.Unmarshal(body, &raw); err != nil {
		return nil
	}
	allowed := map[string]bool{
		"model": true, "duration": true, "seconds": true, "resolution": true, "size": true,
		"ratio": true, "aspect_ratio": true, "n": true, "quality": true, "status": true,
		"task_id": true, "id": true, "asset_id": true, "asset_ids": true, "material_id": true,
		"material_ids": true, "AssetType": true, "name": true,
	}
	out := map[string]interface{}{}
	for k, v := range raw {
		if allowed[k] {
			out[k] = v
		}
	}
	if metadata, ok := raw["metadata"].(map[string]interface{}); ok {
		out["metadata"] = metadataSummary(metadata)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func metadataSummary(metadata map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for _, key := range []string{"duration", "seconds", "resolution", "ratio", "aspect_ratio", "seed"} {
		if v, ok := metadata[key]; ok {
			out[key] = v
		}
	}
	if content, ok := metadata["content"].([]interface{}); ok {
		out["content_count"] = len(content)
		hasVideo := false
		hasImage := false
		for _, item := range content {
			itemMap, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			if itemMap["type"] == "video_url" || itemMap["video_url"] != nil {
				hasVideo = true
			}
			if itemMap["type"] == "image_url" || itemMap["image_url"] != nil {
				hasImage = true
			}
		}
		out["has_video_reference"] = hasVideo
		out["has_image_reference"] = hasImage
	}
	return out
}

func usageQualityHint(event ProviderEvent) string {
	if len(event.UsageContext.RawUsageJSON) > 0 {
		return "official"
	}
	return "estimated"
}

func extraString(extra map[string]interface{}, key string) string {
	if extra == nil {
		return ""
	}
	if value, ok := extra[key]; ok {
		return fmt.Sprint(value)
	}
	return ""
}

func taskModelName(task *model.Task) string {
	if task == nil {
		return ""
	}
	if bc := task.PrivateData.BillingContext; bc != nil && bc.OriginModelName != "" {
		return bc.OriginModelName
	}
	if task.Properties.OriginModelName != "" {
		return task.Properties.OriginModelName
	}
	return task.Properties.UpstreamModelName
}

func relayModeName(mode int) string {
	switch mode {
	case relayconstant.RelayModeChatCompletions:
		return "chat_completions"
	case relayconstant.RelayModeCompletions:
		return "completions"
	case relayconstant.RelayModeEmbeddings:
		return "embeddings"
	case relayconstant.RelayModeImagesGenerations:
		return "images_generations"
	case relayconstant.RelayModeImagesEdits:
		return "images_edits"
	case relayconstant.RelayModeAudioSpeech:
		return "audio_speech"
	case relayconstant.RelayModeAudioTranscription:
		return "audio_transcription"
	case relayconstant.RelayModeAudioTranslation:
		return "audio_translation"
	case relayconstant.RelayModeVideoSubmit:
		return "video_submit"
	case relayconstant.RelayModeVideoFetchByID:
		return "video_fetch"
	case relayconstant.RelayModeResponses:
		return "responses"
	case relayconstant.RelayModeResponsesCompact:
		return "responses_compact"
	case relayconstant.RelayModeRerank:
		return "rerank"
	case relayconstant.RelayModeRealtime:
		return "realtime"
	case relayconstant.RelayModeGemini:
		return "gemini"
	default:
		return intString(mode)
	}
}

func callTypeFromRelayMode(mode int) string {
	switch mode {
	case relayconstant.RelayModeChatCompletions, relayconstant.RelayModeCompletions, relayconstant.RelayModeResponses, relayconstant.RelayModeResponsesCompact, relayconstant.RelayModeGemini:
		return "text_generation"
	case relayconstant.RelayModeEmbeddings:
		return "embedding"
	case relayconstant.RelayModeImagesGenerations, relayconstant.RelayModeImagesEdits:
		return "image_generation"
	case relayconstant.RelayModeAudioSpeech:
		return "speech"
	case relayconstant.RelayModeAudioTranscription:
		return "transcription"
	case relayconstant.RelayModeAudioTranslation:
		return "translation"
	case relayconstant.RelayModeVideoSubmit, relayconstant.RelayModeVideoFetchByID:
		return "video_generation"
	case relayconstant.RelayModeRerank:
		return "rerank"
	case relayconstant.RelayModeRealtime:
		return "realtime"
	default:
		return "unknown"
	}
}

func cloneExtra(extra map[string]interface{}) map[string]interface{} {
	if len(extra) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(extra))
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func statusCodeExtra(statusCode int, err error) map[string]interface{} {
	extra := map[string]interface{}{}
	if statusCode != 0 {
		extra["status_code"] = statusCode
	}
	if err != nil {
		extra["error_message"] = localPreview(err.Error(), 512)
	}
	return extra
}

func MethodFromRequest(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return http.MethodPost
	}
	return c.Request.Method
}
