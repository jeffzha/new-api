package deepseek

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestClaudeOpenAICompatibilityIsChannelScoped(t *testing.T) {
	gin.SetMode(gin.TestMode)

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
				ChannelBaseUrl:    "https://example.com",
				UpstreamModelName: "deepseek-v4-pro",
				ChannelOtherSettings: dto.ChannelOtherSettings{
					ClaudeUseOpenAICompatible: true,
				},
			},
		}
		request := &dto.ClaudeRequest{
			Model:    "deepseek-v4-pro",
			Messages: []dto.ClaudeMessage{{Role: "user", Content: "hello"}},
		}

		converted, err := (&Adaptor{}).ConvertClaudeRequest(c, info, request)
		require.NoError(t, err)
		openAIRequest, ok := converted.(*dto.GeneralOpenAIRequest)
		require.True(t, ok)
		require.Equal(t, "deepseek-v4-pro", openAIRequest.Model)
		require.Len(t, openAIRequest.Messages, 1)
		require.Equal(t, types.RelayFormatOpenAI, info.GetFinalRequestRelayFormat())

		url, err := (&Adaptor{}).GetRequestURL(info)
		require.NoError(t, err)
		require.Equal(t, "https://example.com/v1/chat/completions", url)
	})
}

func TestClaudeOpenAICompatibilityConvertsResponseBackToClaude(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	info := &relaycommon.RelayInfo{
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: types.RelayFormatClaude,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "deepseek-v4-pro",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				ClaudeUseOpenAICompatible: true,
			},
		},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{
			"id":"chatcmpl-test",
			"object":"chat.completion",
			"model":"deepseek-v4-pro",
			"choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}
		}`)),
	}

	usage, apiErr := (&Adaptor{}).DoResponse(c, resp, info)
	require.Nil(t, apiErr)
	convertedUsage, ok := usage.(*dto.Usage)
	require.True(t, ok)
	require.Equal(t, 4, convertedUsage.TotalTokens)

	var response dto.ClaudeResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, "message", response.Type)
	require.Len(t, response.Content, 1)
	require.Equal(t, "OK", response.Content[0].GetText())
}
