package middleware

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCollectAssetReferencesFindsDirectAndMetadataReferences(t *testing.T) {
	payload := map[string]interface{}{
		"content": []interface{}{
			map[string]interface{}{
				"type":      "image_url",
				"image_url": map[string]interface{}{"url": "asset://image-1"},
			},
		},
		"metadata": `{"nested":["asset://video-2","asset://image-1"]}`,
	}
	references := map[string]struct{}{}

	collectAssetReferences(payload, references, 0)

	assert.Equal(t, map[string]struct{}{
		"image-1": {},
		"video-2": {},
	}, references)
}
