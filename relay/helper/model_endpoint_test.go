package helper

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVideoModelsRejectTextEndpointsBeforePricing(t *testing.T) {
	for _, tc := range []struct {
		path   string
		format types.RelayFormat
		body   string
	}{
		{"/v1/chat/completions", types.RelayFormatOpenAI, `{"model":"doubao-seedance-2-0-fast-260128","messages":[{"role":"user","content":"hello"}]}`},
		{"/v1/chat/completions", types.RelayFormatOpenAI, `{"model":"doubao-seedance-2-0-fast-260128","prompt":"hello"}`},
		{"/v1/completions", types.RelayFormatOpenAI, `{"model":"MiniMax-H3-261013","prompt":"hello"}`},
		{"/v1/responses", types.RelayFormatOpenAIResponses, `{"model":"doubao-seedance-2-0-fast-260128","input":"hello"}`},
		{"/v1/responses", types.RelayFormatOpenAIResponses, `{"model":"doubao-seedance-2-0-fast-260128"}`},
	} {
		t.Run(tc.path+tc.body, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			_, err := GetAndValidateRequest(ctx, tc.format)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "POST /v1/videos")
			assert.Contains(t, err.Error(), tc.path)
			assert.NotContains(t, err.Error(), "tiered expr")
		})
	}
}

func TestTextModelsRemainAccepted(t *testing.T) {
	for _, model := range []string{"deepseek-v4.1-flash", "doubao-seed-2.1-lite", "qwen3-vl-plus", "MiniMax-M2.5"} {
		t.Run(model, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hello"}]}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			_, err := GetAndValidateRequest(ctx, types.RelayFormatOpenAI)
			require.NoError(t, err)
		})
	}
}
