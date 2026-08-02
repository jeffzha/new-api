package opsmonitor

import (
	"context"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyEndpointSeparatesVideoLifecycleAndMobileAssets(t *testing.T) {
	tests := []struct {
		method   string
		path     string
		expected string
	}{
		{method: http.MethodPost, path: "/v1/video/generations", expected: "video_submit"},
		{method: http.MethodPost, path: "/v1/videos", expected: "video_submit"},
		{method: http.MethodGet, path: "/v1/video/generations", expected: "relay_other"},
		{method: http.MethodGet, path: "/v1/video/generations/task-1", expected: "video_poll"},
		{method: http.MethodGet, path: "/v1/videos/task-1/content", expected: "video_content"},
		{method: http.MethodPost, path: "/api/openapi-maas/exp/aicc/v2/asset/query", expected: "asset_management"},
		{method: http.MethodPost, path: "/api/openapi-maas/exp/aicc/v2/real-person-auth/asset-group/by-byted-token", expected: "identity_verification"},
	}

	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			assert.Equal(t, test.expected, classifyEndpoint(test.method, test.path))
		})
	}
}

func TestNormalizedUsageKeepsTokenClassesDisjoint(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 20,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:         30,
			CachedCreationTokens: 10,
		},
	}

	input, output, cacheRead, cacheCreation := normalizedUsage(usage)

	assert.Equal(t, int64(60), input)
	assert.Equal(t, int64(20), output)
	assert.Equal(t, int64(30), cacheRead)
	assert.Equal(t, int64(10), cacheCreation)
	assert.Equal(t, int64(120), input+output+cacheRead+cacheCreation)
}

func TestExactPercentilesUseLinearInterpolation(t *testing.T) {
	result := exactPercentiles([]int64{100, 200, 300, 400})

	assert.Equal(t, int64(250), result.P50)
	assert.Equal(t, int64(370), result.P90)
	assert.Equal(t, int64(385), result.P95)
	assert.Equal(t, int64(397), result.P99)
	assert.Equal(t, int64(250), result.Avg)
	assert.Equal(t, int64(400), result.Max)
	assert.Equal(t, int64(4), result.Samples)
}

func TestLocalConcurrencyEnforcesCapacityAndReleasesEveryScope(t *testing.T) {
	localMu.Lock()
	localState = map[concurrencyScope]*localConcurrencyState{}
	localMu.Unlock()
	limit := model.OpsConcurrencyLimit{
		KeyIndex:       model.OpsConcurrencyAllKeys,
		Enabled:        true,
		MaxConcurrency: 1,
		QueueSize:      0,
		QueueTimeoutMs: 100,
	}

	first, err := acquireLocalConcurrency(context.Background(), 7, 0, "request-1", true, limit)
	require.NoError(t, err)
	second, err := acquireLocalConcurrency(context.Background(), 7, 0, "request-2", true, limit)
	assert.ErrorIs(t, err, errConcurrencyQueueFull)
	assert.Nil(t, second)

	first.release()
	third, err := acquireLocalConcurrency(context.Background(), 7, 0, "request-3", true, limit)
	require.NoError(t, err)
	third.release()

	localMu.Lock()
	defer localMu.Unlock()
	assert.Zero(t, localState[concurrencyScope{channelID: 7, keyIndex: model.OpsConcurrencyAllKeys}].inUse)
	assert.Zero(t, localState[concurrencyScope{channelID: 7, keyIndex: 0}].inUse)
}
