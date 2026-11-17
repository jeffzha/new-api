package openai

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/service/opsbridge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpsImageHandlersCaptureOfficialUsageBeforeBillingFallback(t *testing.T) {
	previousTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previousTimeout })
	previous := opsbridge.Default
	opsbridge.Default = &opsbridge.Bridge{}
	t.Cleanup(func() { opsbridge.Default = previous })
	for _, tc := range []struct {
		name, body, contentType string
		stream                  bool
	}{
		{"JSON", `{"data":[{"b64_json":"private-image"}],"usage":{"input_tokens":10,"output_tokens":20}}`, "application/json", false},
		{"JSON as stream", `{"data":[{"b64_json":"private-image"}],"usage":{"input_tokens":10,"output_tokens":20}}`, "application/json", true},
		{"SSE", "data: {\"type\":\"image_generation.completed\",\"b64_json\":\"private-image\",\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}\n\ndata: [DONE]\n\n", "text/event-stream", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, response, info := newImageTestContext(t, tc.body, tc.contentType, tc.stream)
			if tc.stream {
				_, err := OpenaiImageStreamHandler(c, info, response)
				require.Nil(t, err)
			} else {
				_, err := OpenaiImageHandler(c, info, response)
				require.Nil(t, err)
			}
			evidence, exists := c.Get("ops_official_usage")
			require.True(t, exists)
			raw, err := common.Marshal(evidence)
			require.NoError(t, err)
			assert.Contains(t, string(raw), `"input_tokens":10`)
			assert.Contains(t, string(raw), `"output_tokens":20`)
			assert.NotContains(t, string(raw), "private-image")
		})
	}
}

func TestOpsTranscriptionDoesNotTreatEstimateAsOfficial(t *testing.T) {
	previous := opsbridge.Default
	opsbridge.Default = &opsbridge.Bridge{}
	t.Cleanup(func() { opsbridge.Default = previous })
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"text":"private-transcript","usage":{"type":"duration","seconds":1.125}}`, true},
		{`{"text":"private-transcript","usage":{"type":"tokens","input_tokens":10,"output_tokens":2,"total_tokens":12}}`, true},
		{`{"text":"private-transcript"}`, false},
	} {
		c, _, response, info := newImageTestContext(t, tc.body, "application/json", false)
		err, usage := OpenaiSTTHandler(c, response, info, "json")
		require.Nil(t, err)
		require.NotNil(t, usage)
		evidence, exists := c.Get("ops_official_usage")
		assert.Equal(t, tc.want, exists)
		if exists {
			raw, err := common.Marshal(evidence)
			require.NoError(t, err)
			assert.NotContains(t, string(raw), "private-transcript")
		}
	}
}
