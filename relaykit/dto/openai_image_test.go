package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReferenceImageCount covers the additive per-reference-image surcharge
// input: it must count a supplied images array, return 0 when absent, and reject
// malformed or oversized values so billing never silently skips/miscalculates.
func TestReferenceImageCount(t *testing.T) {
	t.Run("no reference images", func(t *testing.T) {
		var req ImageRequest
		n, err := req.ReferenceImageCount()
		require.NoError(t, err)
		assert.Equal(t, 0, n)
	})

	t.Run("parses reference image array length", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{
			"model":  "qwen-image-3.0",
			"images": []string{"https://a/1.png", "https://a/2.png"},
		})
		var req ImageRequest
		require.NoError(t, json.Unmarshal(raw, &req))
		n, err := req.ReferenceImageCount()
		require.NoError(t, err)
		assert.Equal(t, 2, n)
	})

	t.Run("rejects non-array reference images", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{"model": "qwen-image-3.0", "images": "https://a/1.png"})
		var req ImageRequest
		require.NoError(t, json.Unmarshal(raw, &req))
		_, err := req.ReferenceImageCount()
		require.Error(t, err)
	})

	t.Run("rejects oversized reference image array", func(t *testing.T) {
		refs := make([]string, MaxReferenceImageN+1)
		for i := range refs {
			refs[i] = "https://a/img"
		}
		raw, _ := json.Marshal(map[string]any{"model": "qwen-image-3.0", "images": refs})
		var req ImageRequest
		require.NoError(t, json.Unmarshal(raw, &req))
		_, err := req.ReferenceImageCount()
		require.Error(t, err)
	})

	t.Run("counts ali-native wan input images", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{
			"model": "qwen-image-3.0",
			"input": map[string]any{
				"prompt": "edit", "images": []string{"https://a/1.png", "https://a/2.png", "https://a/3.png"},
			},
		})
		var req ImageRequest
		require.NoError(t, json.Unmarshal(raw, &req))
		n, err := req.ReferenceImageCount()
		require.NoError(t, err)
		assert.Equal(t, 3, n)
	})

	t.Run("counts ali-native messages image content", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{
			"model": "qwen-image-3.0",
			"input": map[string]any{
				"prompt": "edit",
				"messages": []any{
					map[string]any{"role": "user", "content": []any{
						map[string]any{"image": "https://a/1.png"},
						map[string]any{"text": "make it blue"},
						map[string]any{"image": "https://a/2.png"},
					}},
				},
			},
		})
		var req ImageRequest
		require.NoError(t, json.Unmarshal(raw, &req))
		n, err := req.ReferenceImageCount()
		require.NoError(t, err)
		assert.Equal(t, 2, n)
	})

	t.Run("ali-native with no reference images", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{
			"model": "qwen-image-3.0",
			"input": map[string]any{"prompt": "a house"},
		})
		var req ImageRequest
		require.NoError(t, json.Unmarshal(raw, &req))
		n, err := req.ReferenceImageCount()
		require.NoError(t, err)
		assert.Equal(t, 0, n)
	})
}
