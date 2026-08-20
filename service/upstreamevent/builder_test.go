package upstreamevent

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildUpstreamResponseEventIncludesContextsAndRawUsage(t *testing.T) {
	oldCfg := currentConfig()
	configValue.Store(Config{SourceSystem: "new-api:test"})
	t.Cleanup(func() { configValue.Store(oldCfg) })

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	c.Set(common.RequestIdKey, "req_test")
	SetRawUsage(c, "anthropic", "claude_messages", dto.ClaudeUsage{
		InputTokens:              11,
		OutputTokens:             7,
		CacheReadInputTokens:     3,
		CacheCreationInputTokens: 5,
	})

	info := &relaycommon.RelayInfo{
		TokenId:         123,
		TokenKey:        "sk-test-secret",
		UserId:          456,
		UsingGroup:      "default",
		OriginModelName: "claude-3-5-sonnet",
		RelayMode:       relayconstant.RelayModeChatCompletions,
		RelayFormat:     types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         789,
			UpstreamModelName: "claude-3-5-sonnet-20241022",
		},
	}

	event := BuildUpstreamResponseEvent(c, info, &dto.Usage{
		PromptTokens:     11,
		CompletionTokens: 7,
		TotalTokens:      18,
	}, map[string]interface{}{"quota": 42})

	require.NotEmpty(t, event.EventID)
	assert.Equal(t, "new-api:test", event.SourceSystem)
	assert.Equal(t, EventUpstreamResponseReceived, event.EventType)
	assert.Equal(t, "req_test", event.RequestID)
	assert.Equal(t, "123", event.CustomerContext.TokenID)
	assert.Equal(t, "cret", event.CustomerContext.APIKeyLast4)
	assert.Equal(t, "default", event.CustomerContext.Group)
	assert.Equal(t, "claude-3-5-sonnet", event.RoutingContext.ModelName)
	assert.Equal(t, "claude-3-5-sonnet-20241022", event.RoutingContext.UpstreamModelName)
	assert.Equal(t, "text_generation", event.RoutingContext.CallType)
	assert.Equal(t, float64(18), event.UsageContext.UsageJSON["total_tokens"])
	assert.Equal(t, "anthropic", event.UsageContext.RawUsageJSON["provider"])
	assert.Equal(t, "official", event.UsageContext.ExtraJSON["usage_quality_hint"])
	assert.Equal(t, 42, event.UsageContext.ExtraJSON["quota"])
}

func TestBuildBillingDeltaEventUsesSelectedChannelContextWithoutChannelMeta(t *testing.T) {
	oldCfg := currentConfig()
	configValue.Store(Config{SourceSystem: "new-api:test"})
	t.Cleanup(func() { configValue.Store(oldCfg) })

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	common.SetContextKey(c, constant.ContextKeyChannelId, 4)
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, "https://tokenhub.tencentmaas.com")
	common.SetContextKey(c, constant.ContextKeyOriginalModel, "hy3")

	info := &relaycommon.RelayInfo{
		TokenId:         5,
		UserId:          1,
		UsingGroup:      "hunyuan-test",
		OriginModelName: "hy3",
		RelayMode:       relayconstant.RelayModeChatCompletions,
		RelayFormat:     types.RelayFormatOpenAI,
	}

	var event ProviderEvent
	require.NotPanics(t, func() {
		event = BuildBillingDeltaEvent(c, info, "preconsume", 2, 2, 2, nil)
	})

	assert.Equal(t, "4", event.RoutingContext.ChannelID)
	assert.Equal(t, constant.ChannelTypeNames[constant.ChannelTypeOpenAI], event.RoutingContext.ChannelType)
	assert.Equal(t, "hy3", event.RoutingContext.UpstreamModelName)
	assert.Equal(t, "https://tokenhub.tencentmaas.com", event.RoutingContext.UpstreamBaseURL)
	assert.False(t, event.RoutingContext.IsModelMapped)
	assert.Equal(t, "preconsume", event.UsageContext.ExtraJSON["billing_stage"])
}

func TestMetadataFromBodyKeepsBillingFieldsAndRedactsPayload(t *testing.T) {
	body := []byte(`{
		"model":"doubao-seedance-2-0-filter-off",
		"prompt":"sensitive prompt",
		"url":"https://example.com/private.jpg",
		"duration":15,
		"resolution":"720p",
		"metadata":{
			"seed":123,
			"content":[{"type":"image_url","image_url":{"url":"https://example.com/private.jpg"}}]
		}
	}`)

	metadata := metadataFromBody(body)

	require.NotNil(t, metadata)
	assert.Equal(t, "doubao-seedance-2-0-filter-off", metadata["model"])
	assert.Equal(t, float64(15), metadata["duration"])
	assert.Equal(t, "720p", metadata["resolution"])
	assert.NotContains(t, metadata, "prompt")
	assert.NotContains(t, metadata, "url")

	nested, ok := metadata["metadata"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, float64(123), nested["seed"])
	assert.Equal(t, 1, nested["content_count"])
	assert.Equal(t, true, nested["has_image_reference"])
	assert.NotContains(t, nested, "content")
}

func TestEventIDUsesSaltForDistinctLifecycleEvents(t *testing.T) {
	first := eventID("new-api:test", EventBillingDownstreamDelta, "req_1", "", "", "preconsume")
	second := eventID("new-api:test", EventBillingDownstreamDelta, "req_1", "", "", "settle")

	require.NotEmpty(t, first)
	assert.Equal(t, first, eventID("new-api:test", EventBillingDownstreamDelta, "req_1", "", "", "preconsume"))
	assert.NotEqual(t, first, second)
}
