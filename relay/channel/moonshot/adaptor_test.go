package moonshot

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestClaudeOpenAICompatibilityIsChannelScoped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	request := &dto.ClaudeRequest{
		Model:    "kimi-k3",
		Messages: []dto.ClaudeMessage{{Role: "user", Content: "hello"}},
	}

	t.Run("native Claude remains the default", func(t *testing.T) {
		info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatClaude, ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://example.com"}}
		url, err := (&Adaptor{}).GetRequestURL(info)
		require.NoError(t, err)
		require.Equal(t, "https://example.com/anthropic/v1/messages", url)
	})

	t.Run("enabled channel converts request and endpoint", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		info := &relaycommon.RelayInfo{
			RelayFormat: types.RelayFormatClaude,
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelBaseUrl: "https://example.com",
				ChannelOtherSettings: dto.ChannelOtherSettings{
					ClaudeUseOpenAICompatible: true,
				},
			},
		}

		converted, err := (&Adaptor{}).ConvertClaudeRequest(c, info, request)
		require.NoError(t, err)
		openAIRequest, ok := converted.(*dto.GeneralOpenAIRequest)
		require.True(t, ok)
		require.Equal(t, "kimi-k3", openAIRequest.Model)
		require.Len(t, openAIRequest.Messages, 1)
		require.Equal(t, types.RelayFormatOpenAI, info.GetFinalRequestRelayFormat())

		url, err := (&Adaptor{}).GetRequestURL(info)
		require.NoError(t, err)
		require.Equal(t, "https://example.com/v1/chat/completions", url)
	})
}

func TestConvertOpenAIRequestKimiK26UsesOnlyAllowedTemperature(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{
		Model:       "kimi-k2.6",
		Temperature: common.GetPointer[float64](0.7),
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "kimi-k2.6",
		},
	}

	converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, info, request)

	require.NoError(t, err)
	convertedRequest, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.NotNil(t, convertedRequest.Temperature)
	require.Equal(t, 1.0, *convertedRequest.Temperature)
}

func TestConvertOpenAIRequestKimiK26KeepsOmittedTemperatureOmitted(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{
		Model: "kimi-k2.6",
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "kimi-k2.6",
		},
	}

	converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, info, request)

	require.NoError(t, err)
	convertedRequest, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Nil(t, convertedRequest.Temperature)
}

func TestConvertOpenAIRequestOtherMoonshotModelKeepsTemperature(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{
		Model:       "kimi-k2.5",
		Temperature: common.GetPointer[float64](0.7),
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "kimi-k2.5",
		},
	}

	converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, info, request)

	require.NoError(t, err)
	convertedRequest, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.NotNil(t, convertedRequest.Temperature)
	require.Equal(t, 0.7, *convertedRequest.Temperature)
}

func TestResponsesRequestUsesChatCompletionsUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAIResponses,
		ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://example.com"},
	}
	info.InitRequestConversionChain()

	converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(c, info, dto.OpenAIResponsesRequest{
		Model: "kimi-k3",
		Input: json.RawMessage(`"hello"`),
	})
	require.NoError(t, err)
	request, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Equal(t, "kimi-k3", request.Model)
	require.Equal(t, types.RelayFormatOpenAI, info.GetFinalRequestRelayFormat())

	url, err := (&Adaptor{}).GetRequestURL(info)
	require.NoError(t, err)
	require.Equal(t, "https://example.com/v1/chat/completions", url)
}
