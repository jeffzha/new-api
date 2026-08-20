package mobilecloudseedance

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertRequestPreservesAssetAndExplicitFalse(t *testing.T) {
	generateAudio := false
	request := relaycommon.TaskSubmitReq{
		Prompt:        "animate the reference",
		Model:         ModelName,
		Resolution:    "480P",
		Duration:      5,
		GenerateAudio: &generateAudio,
		Metadata: map[string]interface{}{
			"ratio": "1:1",
			"content": []interface{}{
				map[string]interface{}{
					"type": "image_url",
					"role": "first_frame",
					"image_url": map[string]interface{}{
						"url": "asset://asset-test",
					},
				},
			},
		},
	}

	payload, err := convertRequest(&request)
	require.NoError(t, err)
	assert.Equal(t, ModelName, payload.Model)
	assert.Equal(t, "480p", payload.Resolution)
	assert.Equal(t, "1:1", payload.Ratio)
	require.NotNil(t, payload.GenerateAudio)
	assert.False(t, bool(*payload.GenerateAudio))
	require.Len(t, payload.Content, 2)
	require.NotNil(t, payload.Content[0].ImageURL)
	assert.Equal(t, "asset://asset-test", payload.Content[0].ImageURL.URL)
	assert.Equal(t, "first_frame", payload.Content[0].Role)
	assert.Equal(t, "text", payload.Content[1].Type)
}

func TestConvertRequestRejectsUnsupportedMobileCloudResolution(t *testing.T) {
	request := relaycommon.TaskSubmitReq{
		Prompt:     "test",
		Model:      ModelName,
		Resolution: "4K",
		Duration:   5,
	}

	_, err := convertRequest(&request)
	require.EqualError(t, err, "resolution must be 480p, 720p, or 1080p")
}

func TestConvertRequestRejectsTooManyReferenceVideos(t *testing.T) {
	content := make([]interface{}, 0, 4)
	for i := 0; i < 4; i++ {
		content = append(content, map[string]interface{}{
			"type":      "video_url",
			"video_url": map[string]interface{}{"url": "https://example.com/reference.mp4"},
		})
	}
	request := relaycommon.TaskSubmitReq{
		Prompt:   "test",
		Model:    ModelName,
		Duration: 5,
		Metadata: map[string]interface{}{"content": content},
	}

	_, err := convertRequest(&request)
	require.EqualError(t, err, "content supports at most 3 video inputs")
}

func TestConvertRequestSeconds(t *testing.T) {
	payload, err := convertRequest(&relaycommon.TaskSubmitReq{
		Prompt:  "test",
		Seconds: "6",
	})

	require.NoError(t, err)
	require.NotNil(t, payload.Duration)
	assert.Equal(t, dto.IntValue(6), *payload.Duration)
}

func TestConvertRequestRejectsInvalidSeconds(t *testing.T) {
	_, err := convertRequest(&relaycommon.TaskSubmitReq{
		Prompt:  "test",
		Seconds: "six",
	})

	assert.EqualError(t, err, "seconds must be an integer")
}
