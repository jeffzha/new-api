package service

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service/opsbridge"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpsObservedModalUsageNeverUsesDisplayFallbacks(t *testing.T) {
	previous := opsbridge.Default
	opsbridge.Default = &opsbridge.Bridge{}
	t.Cleanup(func() { opsbridge.Default = previous })
	for _, tc := range []struct {
		name, protocol, body, field string
		want                        float64
	}{
		{"image tokens", "openai_images", `{"usage":{"input_tokens":10,"output_tokens":20},"data":[{"b64_json":"private-image"}]}`, "input_tokens", 10},
		{"actual images", "openai_images", `{"data":[{"url":"private-result"},{"url":"private-result-2"}]}`, "images", 2},
		{"audio tokens", "openai_audio", `{"usage":{"type":"tokens","input_tokens":3,"output_tokens":1},"text":"private-transcript"}`, "input_tokens", 3},
		{"audio duration", "openai_audio", `{"usage":{"type":"duration","seconds":1.125},"text":"private-transcript"}`, "seconds", 1.125},
		{"rerank tokens", "rerank", `{"usage":{"total_tokens":15},"results":[{"document":"private-document"}]}`, "total_tokens", 15},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			common.SetContextKey(c, constant.ContextKeyLocalCountTokens, true)
			ObserveOpsResponseUsage(c, tc.protocol, []byte(tc.body))
			provider, native, err := opsObservedUsage(c, &relaycommon.RelayInfo{}, &dto.Usage{PromptTokens: 999})
			require.NoError(t, err)
			assert.Equal(t, tc.protocol, provider)
			assert.InDelta(t, tc.want, native[tc.field], 0.000001)
			data, err := common.Marshal(native)
			require.NoError(t, err)
			assert.NotContains(t, string(data), "private-")
			assert.NotContains(t, string(data), "999")
		})
	}
	for _, body := range []string{`{"data":[]}`, `{"error":{"message":"private"},"usage":{"input_tokens":10}}`, `{"text":"transcript"}`} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		ObserveOpsResponseUsage(c, "openai_images", []byte(body))
		_, _, err := opsObservedUsage(c, &relaycommon.RelayInfo{}, &dto.Usage{PromptTokens: 999})
		require.Error(t, err)
	}
}

func TestOpsImageStreamRequiresCompleteSuccess(t *testing.T) {
	previous := opsbridge.Default
	opsbridge.Default = &opsbridge.Bridge{}
	t.Cleanup(func() { opsbridge.Default = previous })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	ObserveOpsImageStreamUsage(c, []byte(`{"type":"image_generation.partial_image","b64_json":"private"}`))
	_, exists := c.Get(opsOfficialUsageKey)
	assert.False(t, exists)
	for range 2 {
		ObserveOpsImageStreamUsage(c, []byte(`{"type":"image_generation.completed","usage":{"input_tokens":10,"output_tokens":20},"b64_json":"private"}`))
	}
	info := &relaycommon.RelayInfo{IsStream: true, StreamStatus: relaycommon.NewStreamStatus()}
	_, _, err := opsObservedUsage(c, info, nil)
	require.Error(t, err)
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
	provider, native, err := opsObservedUsage(c, info, nil)
	require.NoError(t, err)
	assert.Equal(t, "openai_image_stream", provider)
	assert.Equal(t, 2, native["images"])
	assert.Len(t, native["completed_usages"], 2)
	info.StreamStatus.RecordError("upstream stream error")
	_, _, err = opsObservedUsage(c, info, nil)
	require.Error(t, err)
	info.StreamStatus = relaycommon.NewStreamStatus()
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, nil)
	_, _, err = opsObservedUsage(c, info, nil)
	require.Error(t, err)
}
