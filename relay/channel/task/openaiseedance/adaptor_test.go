package openaiseedance

import (
	"github.com/QuantumNous/new-api/common"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
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
