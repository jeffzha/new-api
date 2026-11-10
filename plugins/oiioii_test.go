package plugins_test

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOiiOiiSuper2UsesExistingDoubaoNativeContract(t *testing.T) {
	plugin := loadTaskPlugin(t, "oiioii")
	for _, model := range []string{"doubao-seedance-2-0-260128", "doubao-seedance-2-5-260628"} {
		t.Run(model, func(t *testing.T) {
			body := map[string]any{
				"model": model, "duration": float64(4), "resolution": "480p", "ratio": "16:9",
				"generate_audio": false, "watermark": false,
				"content": []any{map[string]any{"type": "text", "text": "A red wooden cube on a white tabletop"}},
			}
			value, err := plugin.Engine.CallPath(t.Context(), "native", []string{"createTask"}, map[string]any{
				"body": map[string]any{"kind": "json", "value": body},
			})
			require.NoError(t, err)
			encoded, err := common.Marshal(value)
			require.NoError(t, err)
			var intent map[string]any
			require.NoError(t, common.Unmarshal(encoded, &intent))
			assert.Equal(t, model, intent["model"])
			assert.Equal(t, "text_to_video", intent["action"])
			ctx := map[string]any{
				"model": model, "upstreamModel": model, "baseUrl": "https://api.oiioii.tv",
				"apiKey": "test-key", "requestBody": intent["requestBody"],
			}
			request := callTaskPlugin(t, plugin, "buildSubmitRequest", ctx)
			assert.Equal(t, "https://api.oiioii.tv/api/v3/contents/generations/tasks", request["url"])
			assert.Equal(t, "POST", request["method"])
			assert.Equal(t, "Bearer test-key", request["headers"].(map[string]any)["Authorization"])
			assert.Equal(t, body, request["body"], "native parameters and explicit false switches must survive relay")
			usage := callTaskPlugin(t, plugin, "extractUsage", ctx)
			assert.Equal(t, map[string]any{"tokens": float64(38430), "resolution": "480p", "video_input": "none"}, usage)
			ctx["model"] = "public-seedance"
			mapped := callTaskPlugin(t, plugin, "buildSubmitRequest", ctx)
			assert.Equal(t, model, mapped["body"].(map[string]any)["model"])
			assert.Equal(t, model, mapped["rewriteModel"])
		})
	}
}

func TestOiiOiiSuper2TaskResultUsageAndDownload(t *testing.T) {
	plugin := loadTaskPlugin(t, "oiioii")
	ctx := map[string]any{"taskId": "upstream-uuid", "baseUrl": "https://api.oiioii.tv", "apiKey": "test-key"}
	submitted := callTaskPlugin(t, plugin, "parseSubmitResponse", ctx, map[string]any{
		"statusCode": 202, "body": map[string]any{"id": "upstream-uuid"},
	})
	assert.Equal(t, "upstream-uuid", submitted["taskId"])
	query := callTaskPlugin(t, plugin, "buildQueryRequest", ctx)
	assert.Equal(t, "https://api.oiioii.tv/api/v3/contents/generations/tasks/upstream-uuid", query["url"])
	assert.Equal(t, "GET", query["method"])
	assert.Equal(t, "Bearer test-key", query["headers"].(map[string]any)["Authorization"])
	for status, expected := range map[string]string{
		"queued": "QUEUED", "running": "IN_PROGRESS", "failed": "FAILURE", "new_status": "UNKNOWN",
	} {
		t.Run(status, func(t *testing.T) {
			parsed := callTaskPlugin(t, plugin, "parseTaskResult", ctx, map[string]any{
				"status": status, "error": map[string]any{"message": "Upstream failure"},
			})
			assert.Equal(t, expected, parsed["status"])
			if expected == "FAILURE" {
				assert.Equal(t, "Upstream failure", parsed["reason"])
			}
		})
	}
	for _, tokens := range []float64{40594, 38830} {
		body := map[string]any{
			"id": "upstream-uuid", "status": "succeeded", "duration": 4, "resolution": "480p",
			"content": map[string]any{"video_url": "https://cdn.example/video.mp4"},
			"usage":   map[string]any{"completion_tokens": tokens, "total_tokens": tokens},
		}
		parsed := callTaskPlugin(t, plugin, "parseTaskResult", ctx, body)
		assert.Equal(t, "SUCCESS", parsed["status"])
		assert.Equal(t, "https://cdn.example/video.mp4", parsed["url"])
		assert.Equal(t, tokens, parsed["completionTokens"])
		assert.Equal(t, map[string]any{"tokens": tokens, "resolution": "480p"},
			callTaskPlugin(t, plugin, "extractUsageOnComplete", ctx, parsed, body),
			"settlement must use returned usage, not the submit-time estimate")
		for _, method := range []string{"GET", "HEAD"} {
			download := callTaskPlugin(t, plugin, "buildContentRequest", map[string]any{
				"artifactKey": "video", "data": body, "apiKey": "test-key",
				"clientRequest": map[string]any{"method": method},
			})
			assert.Equal(t, "https://cdn.example/video.mp4", download["url"])
			assert.Equal(t, method, download["method"])
			assert.Equal(t, true, download["credentialless"])
			assert.NotContains(t, download, "headers", "upstream credentials must not reach the video CDN")
		}
	}
}

func TestOiiOiiSuper2RejectsUnsupportedOptionsBeforeDispatch(t *testing.T) {
	plugin := loadTaskPlugin(t, "oiioii")
	for _, extra := range []map[string]any{
		{"duration": 0}, {"duration": 16}, {"duration": 4.5},
		{"resolution": "invalid"}, {"seed": 0}, {"camera_fixed": false},
		{"input_reference": "https://example.test/image.png"},
		{"ratio": "invalid"}, {"size": "1280x720"}, {"generate_audio": "false"},
		{"content": []any{map[string]any{"type": "draft_task", "draft_task": map[string]any{"id": "private-upstream-task"}}}},
		{"callback_url": "https://example.test/callback"},
	} {
		body := map[string]any{"model": "doubao-seedance-2-0-260128", "content": []any{map[string]any{"type": "text", "text": "A red cube"}}}
		for key, value := range extra {
			body[key] = value
		}
		_, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{"body": map[string]any{"kind": "json", "value": body}})
		require.Error(t, err, "unsupported fields must not be silently ignored: %v", extra)
	}
	zero := callTaskPlugin(t, plugin, "extractUsageOnComplete", map[string]any{}, map[string]any{}, map[string]any{"status": "succeeded", "usage": map[string]any{"completion_tokens": 0, "total_tokens": 99}})
	assert.Equal(t, float64(0), zero["tokens"])
	result := callTaskPlugin(t, plugin, "parseTaskResult", map[string]any{}, map[string]any{"status": "succeeded", "usage": map[string]any{"completion_tokens": 0, "total_tokens": 99}})
	assert.Equal(t, float64(0), result["completionTokens"])
}
