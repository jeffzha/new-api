package openaiseedance

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
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

func TestOpenAISeedanceExtractsPromptFromContentText(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"model":"doubao-seedance-2.0","seconds":5,"content":[{"type":"text","text":"A blue bird"},{"type":"image_url","image_url":{"url":"https://example.com/frame.png"}}]}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{OriginModelName: seedancepricing.AimodelSeedance20Model, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: seedancepricing.AimodelSeedance20Model}}
	adaptor := &TaskAdaptor{baseURL: "https://aimodel.szhtp.com"}
	require.Nil(t, adaptor.ValidateRequestAndSetAction(ctx, info))

	body, err := adaptor.BuildRequestBody(ctx, info)
	require.NoError(t, err)
	encoded, err := io.ReadAll(body)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, common.Unmarshal(encoded, &payload))
	require.Equal(t, "A blue bird", payload["prompt"])
	content := payload["content"].([]any)
	require.Equal(t, "A blue bird", content[0].(map[string]any)["text"])
}

func TestOpenAISeedanceRejectsImageOnlyContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"model":"doubao-seedance-2.0","seconds":5,"content":[{"type":"image_url","image_url":{"url":"https://example.com/frame.png"}}]}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{OriginModelName: seedancepricing.AimodelSeedance20Model}
	result := (&TaskAdaptor{baseURL: "https://aimodel.szhtp.com"}).ValidateRequestAndSetAction(ctx, info)
	require.NotNil(t, result)
	require.Equal(t, "invalid_request", result.Code)
}

func TestOpenAISeedanceForwardsMultipleReferenceImagesToAimodel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	body := `{"model":"doubao-seedance-2.0","prompt":"A blue bird","seconds":"5","content":[
		{"type":"image_url","image_url":{"url":"https://example.com/r1.png"},"role":"reference_image"},
		{"type":"image_url","image_url":{"url":"https://example.com/r2.png"},"role":"reference_image"},
		{"type":"image_url","image_url":{"url":"https://example.com/r3.png"},"role":"reference_image"}]}`
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{
		OriginModelName: seedancepricing.AimodelSeedance20Model,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: seedancepricing.AimodelSeedance20Model},
	}
	adaptor := &TaskAdaptor{baseURL: "https://aimodel.szhtp.com"}
	require.Nil(t, adaptor.ValidateRequestAndSetAction(ctx, info))
	request, err := getTaskRequest(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{
		"https://example.com/r1.png",
		"https://example.com/r2.png",
		"https://example.com/r3.png",
	}, request.ReferenceImages)

	bodyReader, err := adaptor.BuildRequestBody(ctx, info)
	require.NoError(t, err)
	encoded, err := io.ReadAll(bodyReader)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, common.Unmarshal(encoded, &payload))
	content, ok := payload["content"].([]any)
	require.True(t, ok)
	var refRoles []any
	for _, item := range content {
		entry := item.(map[string]any)
		if entry["type"] == "image_url" {
			refRoles = append(refRoles, entry["role"])
		}
	}
	require.Equal(t, []any{"reference_image", "reference_image", "reference_image"}, refRoles)
	// With multiple reference images the top-level "image" field is omitted so
	// the upstream does not double-count the first image.
	require.Nil(t, payload["image"])
}

func TestOpenAISeedanceRejectsTooManyReferenceImages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	urls := make([]string, 0, maxReferenceImages+1)
	for i := 0; i < maxReferenceImages+1; i++ {
		urls = append(urls, fmt.Sprintf("https://example.com/r%d.png", i))
	}
	encodedImages := strings.Join(urls, `","`)
	body := fmt.Sprintf(`{"model":"doubao-seedance-2.0","prompt":"A blue bird","seconds":"5","images":["%s"]}`, encodedImages)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{OriginModelName: seedancepricing.AimodelSeedance20Model}
	result := (&TaskAdaptor{baseURL: "https://aimodel.szhtp.com"}).ValidateRequestAndSetAction(ctx, info)
	require.NotNil(t, result)
}

func TestOpenAISeedanceForwardsMultipleReferenceImagesToLaomandi(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	body := `{"model":"doubao-seedance-2-0-260128","prompt":"A blue bird","seconds":"10","images":[
		"https://example.com/r1.png",
		"https://example.com/r2.png",
		"https://example.com/r3.png"]}`
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{
		OriginModelName: defaultModel,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: defaultModel},
	}
	adaptor := &TaskAdaptor{baseURL: "https://vedioapi.laomandi.com"}
	require.Nil(t, adaptor.ValidateRequestAndSetAction(ctx, info))

	bodyReader, err := adaptor.BuildRequestBody(ctx, info)
	require.NoError(t, err)
	encoded, err := io.ReadAll(bodyReader)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, common.Unmarshal(encoded, &payload))
	content, ok := payload["content"].([]any)
	require.True(t, ok, "expected content[] for laomandi multi-image request")
	require.Equal(t, 4, len(content), "text + 3 reference images")
	refRoles := make([]any, 0, 3)
	refURLs := make([]any, 0, 3)
	for _, item := range content {
		entry := item.(map[string]any)
		if entry["type"] == "image_url" {
			refRoles = append(refRoles, entry["role"])
			refURLs = append(refURLs, entry["image_url"].(map[string]any)["url"])
		}
	}
	require.Equal(t, []any{"reference_image", "reference_image", "reference_image"}, refRoles)
	require.Equal(t, []any{"https://example.com/r1.png", "https://example.com/r2.png", "https://example.com/r3.png"}, refURLs)
	// Laomandi keeps its single-image top-level field only for one image.
	require.Nil(t, payload["image"])
}

func TestOpenAISeedanceSingleImageLaomandiKeepsLegacyShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	body := `{"model":"doubao-seedance-2-0-260128","prompt":"A blue bird","seconds":"10","input_reference":"https://example.com/frame.png"}`
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{
		OriginModelName: defaultModel,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: defaultModel},
	}
	adaptor := &TaskAdaptor{baseURL: "https://vedioapi.laomandi.com"}
	require.Nil(t, adaptor.ValidateRequestAndSetAction(ctx, info))
	bodyReader, err := adaptor.BuildRequestBody(ctx, info)
	require.NoError(t, err)
	encoded, err := io.ReadAll(bodyReader)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, common.Unmarshal(encoded, &payload))
	require.Equal(t, "https://example.com/frame.png", payload["image"])
	require.Nil(t, payload["content"])
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
