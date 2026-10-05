package deepseek

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
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

func TestResponsesChatCompatibilityIsChannelScoped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	t.Run("native Responses remains the default", func(t *testing.T) {
		info := &relaycommon.RelayInfo{
			RelayMode: relayconstant.RelayModeResponses,
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelBaseUrl:    "https://example.com",
				UpstreamModelName: "deepseek-v4-flash",
			},
		}

		converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(c, info, dto.OpenAIResponsesRequest{
			Model: "deepseek-v4-flash",
			Input: []byte(`"hello"`),
		})
		require.NoError(t, err)
		require.IsType(t, dto.OpenAIResponsesRequest{}, converted)

		requestURL, err := (&Adaptor{}).GetRequestURL(info)
		require.NoError(t, err)
		require.Equal(t, "https://example.com/responses", requestURL)
	})

	t.Run("enabled channel converts request and endpoint", func(t *testing.T) {
		info := &relaycommon.RelayInfo{
			RelayMode:   relayconstant.RelayModeResponses,
			RelayFormat: types.RelayFormatOpenAIResponses,
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelBaseUrl:    "https://example.com",
				UpstreamModelName: "deepseek-v4-flash",
				ChannelOtherSettings: dto.ChannelOtherSettings{
					ResponsesUseChatCompletions: true,
				},
			},
		}
		info.InitRequestConversionChain()

		converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(c, info, dto.OpenAIResponsesRequest{
			Model:        "deepseek-v4-flash",
			Instructions: []byte(`"system rules"`),
			Input:        []byte(`"hello"`),
		})
		require.NoError(t, err)
		chatRequest, ok := converted.(*dto.GeneralOpenAIRequest)
		require.True(t, ok)
		require.Equal(t, "deepseek-v4-flash", chatRequest.Model)
		require.Len(t, chatRequest.Messages, 2)
		require.Equal(t, "system rules", chatRequest.Messages[0].StringContent())
		require.Equal(t, "hello", chatRequest.Messages[1].StringContent())
		require.Equal(t, types.RelayFormatOpenAI, info.GetFinalRequestRelayFormat())

		requestURL, err := (&Adaptor{}).GetRequestURL(info)
		require.NoError(t, err)
		require.Equal(t, "https://example.com/v1/chat/completions", requestURL)
	})

	t.Run("pass-through request keeps native Responses endpoint", func(t *testing.T) {
		info := &relaycommon.RelayInfo{
			RelayMode:   relayconstant.RelayModeResponses,
			RelayFormat: types.RelayFormatOpenAIResponses,
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelBaseUrl: "https://example.com",
				ChannelOtherSettings: dto.ChannelOtherSettings{
					ResponsesUseChatCompletions: true,
				},
			},
		}
		info.InitRequestConversionChain()

		requestURL, err := (&Adaptor{}).GetRequestURL(info)
		require.NoError(t, err)
		require.Equal(t, "https://example.com/responses", requestURL)
	})
}

func TestResponsesChatCompatibilityConvertsResponseBackToResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		RelayMode:   relayconstant.RelayModeResponses,
		RelayFormat: types.RelayFormatOpenAIResponses,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "deepseek-v4-flash",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				ResponsesUseChatCompletions: true,
			},
		},
	}
	info.InitRequestConversionChain()
	info.AppendRequestConversion(types.RelayFormatOpenAI)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{
			"id":"chatcmpl-test",
			"object":"chat.completion",
			"model":"deepseek-v4-flash",
			"choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}
		}`)),
	}

	usage, apiErr := (&Adaptor{}).DoResponse(c, resp, info)
	require.Nil(t, apiErr)
	convertedUsage, ok := usage.(*dto.Usage)
	require.True(t, ok)
	require.Equal(t, 4, convertedUsage.TotalTokens)

	var response dto.OpenAIResponsesResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, "response", response.Object)
	require.JSONEq(t, `"completed"`, string(response.Status))
	require.Contains(t, recorder.Body.String(), `"text":"OK"`)
}

func TestResponsesChatCompatibilityConvertsStreamBackToResponses(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(common.RequestIdKey, "deepseek-responses-test")
	info := &relaycommon.RelayInfo{
		RelayMode:   relayconstant.RelayModeResponses,
		RelayFormat: types.RelayFormatOpenAIResponses,
		IsStream:    true,
		DisablePing: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "deepseek-v4-flash",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				ResponsesUseChatCompletions: true,
			},
		},
	}
	info.InitRequestConversionChain()
	info.AppendRequestConversion(types.RelayFormatOpenAI)
	body := strings.Join([]string{
		`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1710000000,"model":"deepseek-v4-flash","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1710000000,"model":"deepseek-v4-flash","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1710000000,"model":"deepseek-v4-flash","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1710000000,"model":"deepseek-v4-flash","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	usage, apiErr := (&Adaptor{}).DoResponse(c, resp, info)
	require.Nil(t, apiErr)
	convertedUsage, ok := usage.(*dto.Usage)
	require.True(t, ok)
	require.Equal(t, 4, convertedUsage.TotalTokens)

	got := recorder.Body.String()
	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	require.Contains(t, got, `event: response.created`)
	require.Contains(t, got, `event: response.output_text.delta`)
	require.Contains(t, got, `"delta":"OK"`)
	require.Contains(t, got, `event: response.completed`)
	require.Contains(t, got, `"input_tokens":3`)
	require.Contains(t, got, `"output_tokens":1`)
}
