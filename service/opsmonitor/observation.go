package opsmonitor

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/ops_monitor_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

const observationContextKey = "ops_monitor_observation"

type requestObservation struct {
	mu                  sync.Mutex
	requestID           string
	nodeName            string
	startedAt           time.Time
	method              string
	path                string
	endpointType        string
	relayFormat         string
	userID              int
	tokenID             int
	channelID           int
	channelType         int
	channelKeyIndex     int
	modelName           string
	upstreamModelName   string
	group               string
	upstreamStatusCode  int
	errorOwner          string
	businessLimited     bool
	ttftMs              int64
	hasTTFT             bool
	isStream            bool
	inputTokens         int64
	outputTokens        int64
	cacheReadTokens     int64
	cacheCreationTokens int64
	retryCount          int
	switchCount         int
	errorType           string
	errorCode           string
	errorSummary        string
	taskID              string
	seenAccounts        map[[2]int]struct{}
}

// Middleware observes only public relay/task/asset endpoints. Admin and web
// requests remain outside the operational SLA denominator.
func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		setting := ops_monitor_setting.Get()
		endpointType := classifyEndpoint(c.Request.Method, c.Request.URL.Path)
		if !setting.Enabled || endpointType == "" {
			c.Next()
			return
		}

		observation := &requestObservation{
			requestID:       c.GetString(common.RequestIdKey),
			nodeName:        common.NodeName,
			startedAt:       time.Now(),
			method:          c.Request.Method,
			path:            c.Request.URL.Path,
			endpointType:    endpointType,
			channelKeyIndex: model.OpsConcurrencyAllKeys,
			seenAccounts:    make(map[[2]int]struct{}),
		}
		c.Set(observationContextKey, observation)
		c.Next()

		completedAt := time.Now()
		statusCode := c.Writer.Status()
		if statusCode == 0 {
			statusCode = 200
		}
		enqueueRequest(observation.event(statusCode, completedAt))
	}
}

func classifyEndpoint(method, path string) string {
	normalized := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(path)), "/")
	switch {
	case strings.HasPrefix(normalized, "/v1/chat/completions"):
		return "chat"
	case strings.HasPrefix(normalized, "/v1/responses"):
		return "responses"
	case strings.HasPrefix(normalized, "/v1/messages"):
		return "messages"
	case strings.Contains(normalized, "/embeddings") || strings.Contains(normalized, ":embedcontent") || strings.Contains(normalized, ":batchembedcontents"):
		return "embedding"
	case strings.Contains(normalized, "/rerank"):
		return "rerank"
	case strings.Contains(normalized, "/images/generations") || strings.Contains(normalized, "/images/edits"):
		return "image_generation"
	case strings.Contains(normalized, "/audio/"):
		return "audio"
	case method == http.MethodPost && (normalized == "/v1/video/generations" || normalized == "/v1/videos"):
		return "video_submit"
	case strings.HasSuffix(normalized, "/content") && strings.HasPrefix(normalized, "/v1/videos/"):
		return "video_content"
	case strings.Contains(normalized, "/video/generations/") || strings.HasPrefix(normalized, "/v1/videos/"):
		return "video_poll"
	case (strings.HasPrefix(normalized, "/api/v3/open/") || strings.HasPrefix(normalized, "/api/openapi-maas/")) && (strings.Contains(normalized, "visualvalidate") || strings.Contains(normalized, "real-person-auth")):
		return "identity_verification"
	case (strings.HasPrefix(normalized, "/api/v3/open/") || strings.HasPrefix(normalized, "/api/openapi-maas/")) && strings.Contains(normalized, "asset"):
		return "asset_management"
	case strings.HasPrefix(normalized, "/mj/") || strings.HasPrefix(normalized, "/suno/"):
		return "async_task"
	case strings.HasPrefix(normalized, "/v1/"), strings.HasPrefix(normalized, "/v1beta/"), strings.HasPrefix(normalized, "/v1beta1/"):
		return "relay_other"
	default:
		return ""
	}
}

func getObservation(c *gin.Context) *requestObservation {
	if c == nil {
		return nil
	}
	value, exists := c.Get(observationContextKey)
	if !exists {
		return nil
	}
	observation, _ := value.(*requestObservation)
	return observation
}

func (observation *requestObservation) event(statusCode int, completedAt time.Time) model.OpsRequestEvent {
	observation.mu.Lock()
	defer observation.mu.Unlock()

	success := statusCode >= 200 && statusCode < 400 && observation.errorCode == ""
	errorOwner := observation.errorOwner
	businessLimited := observation.businessLimited
	if !success && errorOwner == "" {
		errorOwner = "gateway"
		businessLimited = statusCode == http.StatusBadRequest ||
			statusCode == http.StatusUnauthorized ||
			statusCode == http.StatusForbidden ||
			statusCode == http.StatusTooManyRequests
	}
	return model.OpsRequestEvent{
		RequestID:           observation.requestID,
		NodeName:            observation.nodeName,
		OccurredAtMs:        observation.startedAt.UnixMilli(),
		CompletedAtMs:       completedAt.UnixMilli(),
		Method:              observation.method,
		Path:                observation.path,
		EndpointType:        observation.endpointType,
		RelayFormat:         observation.relayFormat,
		UserID:              observation.userID,
		TokenID:             observation.tokenID,
		ChannelID:           observation.channelID,
		ChannelType:         observation.channelType,
		ChannelKeyIndex:     observation.channelKeyIndex,
		ModelName:           observation.modelName,
		UpstreamModelName:   observation.upstreamModelName,
		Group:               observation.group,
		StatusCode:          statusCode,
		UpstreamStatusCode:  observation.upstreamStatusCode,
		Success:             success,
		ErrorOwner:          errorOwner,
		BusinessLimited:     businessLimited,
		DurationMs:          completedAt.Sub(observation.startedAt).Milliseconds(),
		TTFTMs:              observation.ttftMs,
		HasTTFT:             observation.hasTTFT,
		IsStream:            observation.isStream,
		InputTokens:         observation.inputTokens,
		OutputTokens:        observation.outputTokens,
		CacheReadTokens:     observation.cacheReadTokens,
		CacheCreationTokens: observation.cacheCreationTokens,
		RetryCount:          observation.retryCount,
		SwitchCount:         observation.switchCount,
		ErrorType:           observation.errorType,
		ErrorCode:           observation.errorCode,
		ErrorSummary:        observation.errorSummary,
		TaskID:              observation.taskID,
	}
}

// ObserveRelay attaches routing metadata without persisting another event.
func ObserveRelay(c *gin.Context, info *relaycommon.RelayInfo) {
	observation := getObservation(c)
	if observation == nil || info == nil {
		return
	}
	observation.mu.Lock()
	defer observation.mu.Unlock()
	observation.userID = info.UserId
	observation.tokenID = info.TokenId
	observation.modelName = info.OriginModelName
	observation.group = info.UsingGroup
	if observation.group == "" {
		observation.group = info.TokenGroup
	}
	observation.relayFormat = string(info.RelayFormat)
	observation.isStream = info.IsStream
	observation.retryCount = info.RetryIndex
	if info.TaskRelayInfo != nil {
		observation.taskID = info.PublicTaskID
	}
	if info.ChannelMeta != nil {
		observation.channelID = info.ChannelId
		observation.channelType = info.ChannelType
		observation.channelKeyIndex = info.ChannelMultiKeyIndex
		observation.upstreamModelName = info.UpstreamModelName
	}
	if info.HasSendResponse() {
		observation.ttftMs = info.FirstResponseTime.Sub(info.StartTime).Milliseconds()
		if observation.ttftMs < 0 {
			observation.ttftMs = 0
		}
		observation.hasTTFT = info.IsStream
	}
}

// ObserveUsage normalizes provider usage into disjoint total-input, output,
// cache-read and cache-creation counters.
func ObserveUsage(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage) {
	ObserveRelay(c, info)
	observation := getObservation(c)
	if observation == nil || usage == nil {
		return
	}
	input, output, cacheRead, cacheCreation := normalizedUsage(usage)
	observation.mu.Lock()
	observation.inputTokens = input
	observation.outputTokens = output
	observation.cacheReadTokens = cacheRead
	observation.cacheCreationTokens = cacheCreation
	observation.mu.Unlock()
}

func ObserveError(c *gin.Context, info *relaycommon.RelayInfo, relayFormat types.RelayFormat, err *types.NewAPIError) {
	ObserveRelay(c, info)
	observation := getObservation(c)
	if observation == nil || err == nil {
		return
	}
	classification := classifyRelayError(err)
	observation.mu.Lock()
	observation.relayFormat = string(relayFormat)
	observation.upstreamStatusCode = classification.upstreamStatusCode
	observation.errorOwner = classification.owner
	observation.businessLimited = classification.businessLimited
	observation.errorType = string(err.GetErrorType())
	observation.errorCode = string(err.GetErrorCode())
	observation.errorSummary = common.LocalLogPreview(err.MaskSensitiveError())
	observation.mu.Unlock()
}

func ObserveTaskError(c *gin.Context, info *relaycommon.RelayInfo, err *dto.TaskError) {
	ObserveRelay(c, info)
	observation := getObservation(c)
	if observation == nil || err == nil {
		return
	}
	observation.mu.Lock()
	observation.errorCode = err.Code
	observation.errorType = "task_error"
	observation.errorSummary = common.LocalLogPreview(err.Message)
	observation.upstreamStatusCode = err.StatusCode
	if err.LocalError {
		observation.errorOwner = "gateway"
		observation.businessLimited = err.StatusCode == 400 || err.StatusCode == 401 || err.StatusCode == 403 || err.StatusCode == 429
	} else {
		observation.errorOwner = "provider"
	}
	observation.mu.Unlock()
}

func normalizedUsage(usage *dto.Usage) (int64, int64, int64, int64) {
	if usage.BillingUsage != nil {
		billing := usage.BillingUsage
		if billing.ClaudeUsage != nil && strings.EqualFold(billing.Semantic, dto.BillingUsageSemanticAnthropic) {
			claude := billing.ClaudeUsage
			cacheCreation := claude.GetCacheCreationTotalTokens()
			return nonNegativeInt(claude.InputTokens), nonNegativeInt(claude.OutputTokens), nonNegativeInt(claude.CacheReadInputTokens), nonNegativeInt(cacheCreation)
		}
		if billing.GeminiUsageMetadata != nil && strings.EqualFold(billing.Semantic, dto.BillingUsageSemanticGemini) {
			gemini := billing.GeminiUsageMetadata
			input := gemini.PromptTokenCount - gemini.CachedContentTokenCount
			return nonNegativeInt(input), nonNegativeInt(gemini.CandidatesTokenCount), nonNegativeInt(gemini.CachedContentTokenCount), 0
		}
		if billing.OpenAIUsage != nil && strings.EqualFold(billing.Semantic, dto.BillingUsageSemanticOpenAI) {
			return usageValues(billing.OpenAIUsage)
		}
	}
	return usageValues(usage)
}

func usageValues(usage *dto.Usage) (int64, int64, int64, int64) {
	input := usage.InputTokens
	if input == 0 {
		input = usage.PromptTokens
	}
	output := usage.OutputTokens
	if output == 0 {
		output = usage.CompletionTokens
	}
	cacheRead := usage.PromptTokensDetails.CachedTokens
	if usage.InputTokensDetails != nil && usage.InputTokensDetails.CachedTokens > cacheRead {
		cacheRead = usage.InputTokensDetails.CachedTokens
	}
	cacheCreation := usage.PromptTokensDetails.CacheCreationTokensTotal()
	if usage.InputTokensDetails != nil && usage.InputTokensDetails.CacheCreationTokensTotal() > cacheCreation {
		cacheCreation = usage.InputTokensDetails.CacheCreationTokensTotal()
	}
	claudeSplit := usage.ClaudeCacheCreation5mTokens + usage.ClaudeCacheCreation1hTokens
	if claudeSplit > cacheCreation {
		cacheCreation = claudeSplit
	}
	input -= cacheRead + cacheCreation
	return nonNegativeInt(input), nonNegativeInt(output), nonNegativeInt(cacheRead), nonNegativeInt(cacheCreation)
}

func nonNegativeInt(value int) int64 {
	if value < 0 {
		return 0
	}
	return int64(value)
}
