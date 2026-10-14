package plugins_test

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadTaskPlugin(t *testing.T, key string) *jsplugin.LoadedPlugin {
	t.Helper()
	source, err := builtinplugins.Source(key)
	require.NoError(t, err)
	plugin, err := jsplugin.NewRegistry().RegisterFactory(source, jsplugin.Options{Key: key})
	require.NoError(t, err)
	return plugin
}

func callTaskPlugin(t *testing.T, plugin *jsplugin.LoadedPlugin, hook string, args ...any) map[string]any {
	t.Helper()
	value, err := plugin.Engine.Call(t.Context(), hook, args...)
	require.NoError(t, err)
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, common.Unmarshal(encoded, &decoded))
	return decoded
}

func h3Context(model string, body map[string]any) map[string]any {
	return map[string]any{
		"model":         model,
		"upstreamModel": model,
		"baseUrl":       "https://wangpaiai.com",
		"apiKey":        "test-key",
		"requestBody":   body,
	}
}

func TestWangpaiH3CatalogHasAll97PriceTiers(t *testing.T) {
	plugin := loadTaskPlugin(t, "wangpaiai")
	images := []any{"https://cdn.example/first.jpg", "https://cdn.example/last.png"}
	resolutions := []struct {
		name, quality string
		credits       float64
	}{
		{"480P", "Pro 480p", 0.8},
		{"720P", "Pro 720p", 1.5},
		{"1080P", "Pro 1080p", 2},
		{"2K", "Pro 2k", 3},
	}
	models := []struct {
		name, upstream string
	}{
		{"MiniMax-H3", "h3-1"},
		{"MiniMax-H3-Sensitive", "h3-vip"},
	}

	checked := 0
	for _, model := range models {
		for _, resolution := range resolutions {
			for seconds := 4; seconds <= 15; seconds++ {
				body := map[string]any{
					"prompt": "Move naturally from the first reference to the second reference",
					"images": images, "seconds": seconds, "resolution": resolution.name,
				}
				ctx := h3Context(model.name, body)
				descriptor := callTaskPlugin(t, plugin, "buildSubmitRequest", ctx)
				requestBody := descriptor["body"].(map[string]any)
				inputs := requestBody["inputs"].(map[string]any)
				assert.Equal(t, model.upstream, requestBody["modelId"])
				assert.Equal(t, fmt.Sprint(seconds), inputs["duration"])
				assert.Equal(t, resolution.quality, inputs["quality"])
				assert.Equal(t, images[0], inputs["image_url"])
				assert.Equal(t, images[1], inputs["image_url_2"])

				usage := callTaskPlugin(t, plugin, "extractUsage", ctx)
				assert.InDelta(t, resolution.credits*float64(seconds), usage["vendor_credits"], 1e-9)
				assert.Equal(t, float64(2), usage["input_images"])
				checked++
			}
		}
	}

	fixedBody := map[string]any{
		"prompt": "A cinematic transition", "images": images,
		"videos": []any{"https://cdn.example/input.mp4"},
		"audios": []any{"https://cdn.example/input.mp3"}, "ratio": "3:4",
	}
	fixedCtx := h3Context("MiniMax-H3-2K", fixedBody)
	fixed := callTaskPlugin(t, plugin, "buildSubmitRequest", fixedCtx)
	fixedRequest := fixed["body"].(map[string]any)
	fixedInputs := fixedRequest["inputs"].(map[string]any)
	assert.Equal(t, "wp_a4f5de6f0c3a", fixedRequest["modelId"])
	assert.Equal(t, "15", fixedInputs["duration"])
	assert.Equal(t, images[1], fixedInputs["image_url_2"])
	assert.Equal(t, "https://cdn.example/input.mp4", fixedInputs["video_url"])
	assert.Equal(t, "https://cdn.example/input.mp3", fixedInputs["audio_url"])
	assert.Equal(t, float64(19), callTaskPlugin(t, plugin, "extractUsage", fixedCtx)["vendor_credits"])
	checked++

	assert.Equal(t, 97, checked)
}

func TestWangpaiH3RejectsUnsupportedInputsAndSettlesActualCost(t *testing.T) {
	plugin := loadTaskPlugin(t, "wangpaiai")
	valid := func() map[string]any {
		return map[string]any{"prompt": "test", "images": []any{"https://cdn.example/first.jpg"}, "seconds": 5, "resolution": "720P"}
	}
	for _, seconds := range []any{3, 16, 4.5} {
		body := valid()
		body["seconds"] = seconds
		_, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", h3Context("MiniMax-H3", body))
		require.Error(t, err)
	}
	for _, resolution := range []string{"768P", "4K"} {
		body := valid()
		body["resolution"] = resolution
		_, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", h3Context("MiniMax-H3", body))
		require.Error(t, err)
	}
	_, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", h3Context("MiniMax-H3", map[string]any{"prompt": "test"}))
	require.Error(t, err)
	tooMany := valid()
	tooMany["images"] = []any{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}
	_, err = plugin.Engine.Call(t.Context(), "buildSubmitRequest", h3Context("MiniMax-H3", tooMany))
	require.Error(t, err)
	_, err = plugin.Engine.Call(t.Context(), "buildSubmitRequest", h3Context("unknown", valid()))
	require.Error(t, err)

	settled := callTaskPlugin(t, plugin, "extractUsageOnComplete",
		map[string]any{"state": map[string]any{"quotedOutputCredits": 7.5}},
		map[string]any{"status": "SUCCESS"},
		map[string]any{"generation": map[string]any{"cost": 9}},
	)
	assert.Equal(t, map[string]any{"vendor_credits": float64(9), "output_credits": float64(7.5), "material_credits": float64(1.5)}, settled)
	zero := callTaskPlugin(t, plugin, "extractUsageOnComplete", map[string]any{}, map[string]any{"status": "SUCCESS"}, map[string]any{"generation": map[string]any{"cost": 0}})
	assert.Equal(t, float64(0), zero["vendor_credits"])
	pending := callTaskPlugin(t, plugin, "parseTaskResult", map[string]any{}, map[string]any{"generation": map[string]any{"status": "done"}})
	assert.Equal(t, "IN_PROGRESS", pending["status"])
	assert.Equal(t, "99%", pending["progress"])
}

func TestWangpaiMediaProxyAndVideoBridgePreserveReferences(t *testing.T) {
	wangpai := loadTaskPlugin(t, "wangpaiai")
	direct := callTaskPlugin(t, wangpai, "parseTaskResult", map[string]any{"baseUrl": "https://wangpaiai.com"}, map[string]any{"generation": map[string]any{"status": "done", "resultUrl": "https://cdn.wangpaiai.com/video.mp4"}})
	assert.Equal(t, "https://cdn.wangpaiai.com/video.mp4", direct["url"])
	relayed := callTaskPlugin(t, wangpai, "parseTaskResult", map[string]any{"baseUrl": "http://127.0.0.1:8788"}, map[string]any{"generation": map[string]any{"status": "done", "resultUrl": "https://cdn.wangpaiai.com/video.mp4"}})
	assert.Contains(t, relayed["url"], "http://127.0.0.1:8788/_fetch?url=")

	bridge := loadTaskPlugin(t, "newapi-video")
	ctx := map[string]any{
		"model": "MiniMax-H3", "upstreamModel": "MiniMax-H3",
		"baseUrl": "https://llm.example", "apiKey": "test-key",
		"requestBody": map[string]any{
			"prompt": "test", "seconds": 5, "resolution": "720P",
			"images": []any{"https://cdn.example/first.jpg", "https://cdn.example/last.png"},
		},
	}
	descriptor := callTaskPlugin(t, bridge, "buildSubmitRequest", ctx)
	body := descriptor["body"].(map[string]any)
	assert.Equal(t, []any{"https://cdn.example/first.jpg", "https://cdn.example/last.png"}, body["images"])
	assert.Equal(t, float64(7.5), callTaskPlugin(t, bridge, "extractUsage", ctx)["vendor_credits"])

	aliasCtx := map[string]any{
		"model": "MiniMax-H3-261013", "upstreamModel": "MiniMax-H3-261013",
		"baseUrl": "https://llm.example", "apiKey": "test-key",
		"requestBody": map[string]any{"prompt": "test", "seconds": 5, "resolution": "720P"},
	}
	aliasDescriptor := callTaskPlugin(t, bridge, "buildSubmitRequest", aliasCtx)
	aliasBody := aliasDescriptor["body"].(map[string]any)
	assert.Contains(t, bridge.Meta.Models, "MiniMax-H3-261013")
	assert.Equal(t, "MiniMax-H3", aliasBody["model"])
	assert.Equal(t, "text_to_video", aliasDescriptor["action"])
}

func TestH3CompletedResponsesExposeActualCreditsToBridge(t *testing.T) {
	for _, key := range []string{"wangpaiai", "newapi-video"} {
		t.Run(key, func(t *testing.T) {
			plugin := loadTaskPlugin(t, key)
			data := map[string]any{"generation": map[string]any{"cost": 9}}
			if key == "newapi-video" {
				data = map[string]any{"usage": map[string]any{"vendor_credits": 9}}
			}
			value, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "render"}, map[string]any{}, map[string]any{"status": "SUCCESS", "data": data})
			require.NoError(t, err)
			encoded, err := common.Marshal(value)
			require.NoError(t, err)
			var rendered map[string]any
			require.NoError(t, common.Unmarshal(encoded, &rendered))
			require.Contains(t, rendered, "usage")
			assert.Equal(t, float64(9), rendered["usage"].(map[string]any)["vendor_credits"])
		})
	}
}
