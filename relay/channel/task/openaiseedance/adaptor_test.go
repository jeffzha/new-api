package openaiseedance

import (
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	seedancepricing "github.com/QuantumNous/new-api/setting/seedance_video_pricing"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAISeedanceBuildsNativeRequestFromOpenAIVideoShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"model":"doubao-seedance-2-0-260128","prompt":"A blue bird","seconds":"5","size":"1920x1080","input_reference":"https://example.com/frame.png"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{
		OriginModelName: defaultModel,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: defaultModel},
	}
	adaptor := &TaskAdaptor{baseURL: "https://vedioapi.laomandi.com"}
	require.Nil(t, adaptor.ValidateRequestAndSetAction(ctx, info))
	body, err := adaptor.BuildRequestBody(ctx, info)
	require.NoError(t, err)
	encoded, err := io.ReadAll(body)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, common.Unmarshal(encoded, &payload))
	require.Equal(t, defaultModel, payload["model"])
	require.Equal(t, "A blue bird", payload["prompt"])
	require.Equal(t, "https://example.com/frame.png", payload["image"])
	metadata, ok := payload["metadata"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(5), metadata["duration"])
	require.Equal(t, "1080p", metadata["resolution"])

	url, err := adaptor.BuildRequestURL(info)
	require.NoError(t, err)
	require.Equal(t, "https://vedioapi.laomandi.com/v1/video/generations", url)
}

func TestOpenAISeedanceDurationLimitDependsOnModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name     string
		model    string
		duration int
		valid    bool
	}{
		{name: "seedance 2.5 accepts 16 seconds", model: seedancepricing.AimodelSeedance25Model, duration: 16, valid: true},
		{name: "seedance 2.5 accepts 30 seconds", model: seedancepricing.AimodelSeedance25Model, duration: 30, valid: true},
		{name: "seedance 2.5 rejects 31 seconds", model: seedancepricing.AimodelSeedance25Model, duration: 31},
		{name: "seedance 2.0 keeps 15 second cap", model: defaultModel, duration: 16},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			body := fmt.Sprintf("{\"model\":%q,\"prompt\":\"A blue bird\",\"seconds\":\"%d\"}", test.model, test.duration)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			info := &relaycommon.RelayInfo{OriginModelName: test.model, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: test.model}}
			result := (&TaskAdaptor{}).ValidateRequestAndSetAction(ctx, info)
			if test.valid {
				require.Nil(t, result)
				return
			}
			require.NotNil(t, result)
		})
	}
}
