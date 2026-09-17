package happyhorse

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHappyHorseModelListAndRatios(t *testing.T) {
	assert.ElementsMatch(t, []string{modelT2V, modelI2V, modelR2V, modelEdit}, ModelList)
	assert.Equal(t, []string{modelAlias}, (&TaskAdaptor{}).GetModelList())
	assert.Equal(t, 1.0, resolutionRatio(modelT2V, "480P"))
	assert.Equal(t, 2.0, resolutionRatio(modelT2V, "720P"))
	assert.InDelta(t, 8.0/3, resolutionRatio(modelT2V, "1080P"), 1e-9)
	assert.Equal(t, 1.0, resolutionRatio(modelEdit, "720P"))
	assert.InDelta(t, 16.0/9, resolutionRatio(modelEdit, "1080P"), 1e-9)
}

func TestHappyHorseUnifiedAliasRouting(t *testing.T) {
	assert.Equal(t, modelT2V, resolveModelForRequest(modelAlias, requestMetadata{}, "a sunset"))
	assert.Equal(t, modelI2V, resolveModelForRequest(modelAlias, requestMetadata{FirstFrame: "https://example.com/a.png"}, ""))
	assert.Equal(t, modelR2V, resolveModelForRequest(modelAlias, requestMetadata{ReferenceImages: []string{"https://example.com/a.png"}}, "[Image 1] moves"))
	assert.Equal(t, modelEdit, resolveModelForRequest(modelAlias, requestMetadata{Video: "https://example.com/a.mp4"}, "edit"))
}

func TestHappyHorseValidateRequestStoresParsedRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := `{"model":"HappyHorse1.1","prompt":"a red ball","resolution":"480P","duration":3}`
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/video/generations", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{
		ChannelMeta:     &relaycommon.ChannelMeta{},
		TaskRelayInfo:   &relaycommon.TaskRelayInfo{},
		OriginModelName: modelAlias,
	}

	require.Nil(t, (&TaskAdaptor{}).ValidateRequestAndSetAction(c, info))
	req, err := relaycommon.GetTaskRequest(c)
	require.NoError(t, err)
	assert.Equal(t, modelAlias, req.Model)
	assert.Equal(t, "a red ball", req.Prompt)
	assert.Equal(t, 3, req.Duration)
	assert.Equal(t, constant.TaskActionGenerate, info.Action)
}

func TestHappyHorseTieredPrices(t *testing.T) {
	expression, ok := billing_setting.GetBuiltinBillingExpr(modelAlias)
	require.True(t, ok)
	for _, tc := range []struct {
		resolution string
		want       float64
	}{
		{"480P", 2.25 / 7.3},
		{"720P", 4.5 / 7.3},
		{"1080P", 6.0 / 7.3},
	} {
		cost, trace, err := billingexpr.RunExprWithRequest(expression, billingexpr.TokenParams{}, billingexpr.RequestInput{Usage: map[string]any{"resolution": tc.resolution, "seconds": 5.0, "kind": "video"}})
		require.NoError(t, err)
		assert.InDelta(t, tc.want, cost, 1e-12)
		assert.NotEmpty(t, trace.MatchedTier)
	}
}

func TestHappyHorseValidationByModel(t *testing.T) {
	valid := relaycommon.TaskSubmitReq{Model: modelT2V, Prompt: "a sunset", Metadata: map[string]any{"duration": 5, "resolution": "720P", "ratio": "16:9"}}
	require.NoError(t, validate(valid, modelT2V))
	assert.Error(t, validate(relaycommon.TaskSubmitReq{Model: modelT2V}, modelT2V))
	i2v := relaycommon.TaskSubmitReq{Model: modelI2V, Metadata: map[string]any{"first_frame": "https://example.com/a.png"}}
	require.NoError(t, validate(i2v, modelI2V))
	r2v := relaycommon.TaskSubmitReq{Model: modelR2V, Prompt: "[Image 1] turns into a video", Images: []string{"https://example.com/a.png"}}
	require.NoError(t, validate(r2v, modelR2V))
	assert.Error(t, validate(relaycommon.TaskSubmitReq{Model: modelEdit, Prompt: "edit", Metadata: map[string]any{"video": "https://example.com/a.mp4", "duration": 5}}, modelEdit))
	edit := relaycommon.TaskSubmitReq{Model: modelEdit, Prompt: "edit", Metadata: map[string]any{"video": "https://example.com/a.mp4", "resolution": "720P"}}
	require.NoError(t, validate(edit, modelEdit))
}

func TestHappyHorseResponseAndTaskResult(t *testing.T) {
	adaptor := &TaskAdaptor{}
	response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{\"output\":{\"task_id\":\"task-1\",\"task_status\":\"PENDING\"}}"))}
	parsed, taskErr := adaptor.ParseResponse(nil, response, nil)
	require.Nil(t, taskErr)
	require.Equal(t, "task-1", parsed.UpstreamTaskID)
	result, err := adaptor.ParseTaskResult(&model.Task{}, nil, []byte("{\"output\":{\"task_status\":\"SUCCEEDED\",\"video_url\":\"https://example.com/out.mp4\"},\"usage\":{\"duration\":5.5,\"output_video_duration\":5.5}}"))
	require.NoError(t, err)
	assert.Equal(t, model.TaskStatusSuccess, result.Status)
	assert.Equal(t, "https://example.com/out.mp4", result.Url)
	assert.Equal(t, 5.5, result.UsageFacts["duration"])
}

func TestHappyHorseURLAndHeader(t *testing.T) {
	adaptor := &TaskAdaptor{baseURL: "https://workspace.cn-beijing.maas.aliyuncs.com", apiKey: "secret"}
	url, err := adaptor.BuildRequestURL(nil)
	require.NoError(t, err)
	assert.Equal(t, "https://workspace.cn-beijing.maas.aliyuncs.com/api/v1/services/aigc/video-generation/video-synthesis", url)
	req, err := http.NewRequest(http.MethodPost, url, nil)
	require.NoError(t, err)
	require.NoError(t, adaptor.BuildRequestHeader(nil, req, nil))
	assert.Equal(t, "Bearer secret", req.Header.Get("Authorization"))
	assert.Equal(t, "enable", req.Header.Get("X-DashScope-Async"))
}

func TestHappyHorseBuildRequestUsesNativeMedia(t *testing.T) {
	adaptor := &TaskAdaptor{baseURL: "https://example.com", apiKey: "secret"}
	for _, tc := range []struct {
		name string
		req  relaycommon.TaskSubmitReq
		want string
	}{
		{"first frame", relaycommon.TaskSubmitReq{Model: modelI2V, Prompt: "animate", Images: []string{"https://example.com/a.png"}}, "\"type\":\"first_frame\""},
		{"reference image", relaycommon.TaskSubmitReq{Model: modelR2V, Prompt: "[Image 1] moves", Images: []string{"https://example.com/a.png"}}, "\"type\":\"reference_image\""},
		{"video edit", relaycommon.TaskSubmitReq{Model: modelEdit, Prompt: "edit", Metadata: map[string]any{"video": "https://example.com/a.mp4", "resolution": "720P"}}, "\"type\":\"video\""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Set("task_request", tc.req)
			reader, err := adaptor.BuildRequestBody(ctx, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: tc.req.Model}})
			require.NoError(t, err)
			body, err := io.ReadAll(reader)
			require.NoError(t, err)
			assert.Contains(t, string(body), tc.want)
		})
	}
}

func TestHappyHorseBuildRequestPreservesOptionalZeroValues(t *testing.T) {
	watermark := false
	seed := 0
	duration := 3
	adaptor := &TaskAdaptor{baseURL: "https://example.com", apiKey: "secret"}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := relaycommon.TaskSubmitReq{Model: modelT2V, Prompt: "sunset", Metadata: map[string]any{
		"duration": duration, "resolution": "480P", "ratio": "16:9",
		"watermark": watermark, "seed": seed,
	}}
	ctx.Set("task_request", req)
	reader, err := adaptor.BuildRequestBody(ctx, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: modelT2V}})
	require.NoError(t, err)
	body, err := io.ReadAll(reader)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, common.Unmarshal(body, &decoded))
	parameters, ok := decoded["parameters"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, false, parameters["watermark"])
	assert.Equal(t, float64(0), parameters["seed"])
	assert.Equal(t, float64(3), parameters["duration"])
}

func TestHappyHorseValidationRejectsExplicitMediaMismatch(t *testing.T) {
	tests := []struct {
		name string
		req  relaycommon.TaskSubmitReq
	}{
		{"text-to-video with image", relaycommon.TaskSubmitReq{Model: modelT2V, Prompt: "x", Images: []string{"https://example.com/a.png"}}},
		{"image-to-video with video", relaycommon.TaskSubmitReq{Model: modelI2V, Prompt: "x", Metadata: map[string]any{"first_frame": "https://example.com/a.png", "video": "https://example.com/a.mp4"}}},
		{"reference-to-video with first frame", relaycommon.TaskSubmitReq{Model: modelR2V, Prompt: "[Image 1] x", Metadata: map[string]any{"first_frame": "https://example.com/a.png", "reference_image": []string{"https://example.com/a.png"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Error(t, validate(test.req, test.req.Model))
		})
	}
}

func TestHappyHorseInputReferenceRoutesByMediaType(t *testing.T) {
	imageReq := relaycommon.TaskSubmitReq{Model: modelI2V, Prompt: "animate", InputReference: "https://example.com/frame.png"}
	require.NoError(t, validate(imageReq, modelI2V))
	videoReq := relaycommon.TaskSubmitReq{Model: modelEdit, Prompt: "edit", InputReference: "https://example.com/source.mp4", Metadata: map[string]any{"resolution": "720P"}}
	require.NoError(t, validate(videoReq, modelEdit))
}

func TestHappyHorseBuildRequestBodyModes(t *testing.T) {
	adaptor := &TaskAdaptor{baseURL: "https://example.com", apiKey: "secret"}
	tests := []struct {
		name       string
		req        relaycommon.TaskSubmitReq
		wantModel  string
		wantMedia  string
		wantAbsent []string
	}{
		{"t2v", relaycommon.TaskSubmitReq{Model: modelAlias, Prompt: "sunset", Metadata: map[string]any{"duration": 5, "resolution": "480P", "ratio": "16:9"}}, modelT2V, "", nil},
		{"i2v", relaycommon.TaskSubmitReq{Model: modelAlias, Prompt: "animate", Images: []string{"https://example.com/frame.png"}}, modelI2V, "first_frame", nil},
		{"r2v", relaycommon.TaskSubmitReq{Model: modelAlias, Prompt: "[Image 1] moves", Images: []string{"https://example.com/ref.png"}}, modelR2V, "reference_image", []string{"first_frame"}},
		{"edit", relaycommon.TaskSubmitReq{Model: modelAlias, Prompt: "edit", InputReference: "https://example.com/source.mp4", Metadata: map[string]any{"resolution": "720P"}}, modelEdit, "video", []string{"duration", "ratio", "first_frame"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Set("task_request", test.req)
			reader, err := adaptor.BuildRequestBody(ctx, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: test.req.Model}})
			require.NoError(t, err)
			body, err := io.ReadAll(reader)
			require.NoError(t, err)
			var decoded map[string]any
			require.NoError(t, common.Unmarshal(body, &decoded))
			assert.Equal(t, test.wantModel, decoded["model"])
			encoded := string(body)
			if test.wantMedia != "" {
				assert.Contains(t, encoded, "\"type\":\""+test.wantMedia+"\"")
			}
			for _, absent := range test.wantAbsent {
				assert.NotContains(t, encoded, "\""+absent+"\"")
			}
		})
	}
}
