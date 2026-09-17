package happyhorse

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHappyHorseModelListAndRatios(t *testing.T) {
	assert.ElementsMatch(t, []string{modelT2V, modelI2V, modelR2V, modelEdit, modelEdit11}, ModelList)
	assert.Equal(t, 1.0, resolutionRatio(modelT2V, "480P"))
	assert.Equal(t, 2.0, resolutionRatio(modelT2V, "720P"))
	assert.InDelta(t, 8.0/3, resolutionRatio(modelT2V, "1080P"), 1e-9)
	assert.Equal(t, 1.0, resolutionRatio(modelEdit, "720P"))
	assert.InDelta(t, 16.0/9, resolutionRatio(modelEdit, "1080P"), 1e-9)
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
