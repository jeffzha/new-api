package doubao

import (
	"testing"

	"github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertToRequestPayloadSupportsSeedance25Fields(t *testing.T) {
	req := &common.TaskSubmitReq{
		Model:    seedance25Model,
		Prompt:   "edit @Video1",
		Duration: -1,
		Metadata: map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{
					"type": "video_url",
					"video_url": map[string]interface{}{
						"url": "https://example.com/reference.mp4",
					},
					"role": "reference_video",
				},
			},
			"omni_reference_task_type": "edit",
			"output_format":            "mov",
			"ratio":                    "adaptive",
			"generate_audio":           true,
		},
	}

	payload, err := (&TaskAdaptor{}).convertToRequestPayload(req)

	require.NoError(t, err)
	require.NotNil(t, payload.Duration)
	assert.Equal(t, -1, int(*payload.Duration))
	require.NotNil(t, payload.OmniReferenceTaskType)
	assert.Equal(t, "edit", *payload.OmniReferenceTaskType)
	require.NotNil(t, payload.OutputFormat)
	assert.Equal(t, "mov", *payload.OutputFormat)
	require.NotNil(t, payload.GenerateAudio)
	assert.True(t, bool(*payload.GenerateAudio))
	assert.Equal(t, "adaptive", payload.Ratio)
	require.Len(t, payload.Content, 2)
	assert.Equal(t, "reference_video", payload.Content[0].Role)
	assert.Equal(t, "edit @Video1", payload.Content[1].Text)
}

func TestConvertToRequestPayloadKeepsSeedance20Behavior(t *testing.T) {
	req := &common.TaskSubmitReq{
		Model:    "doubao-seedance-2-0-260128",
		Prompt:   "a cat",
		Duration: 5,
		Metadata: map[string]interface{}{
			"resolution": "1080p",
		},
	}

	payload, err := (&TaskAdaptor{}).convertToRequestPayload(req)

	require.NoError(t, err)
	require.NotNil(t, payload.Duration)
	assert.Equal(t, 5, int(*payload.Duration))
	assert.Equal(t, "1080p", payload.Resolution)
	require.Len(t, payload.Content, 1)
	assert.Equal(t, "a cat", payload.Content[0].Text)
}
