package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpsAggregatesUseEffectiveStatusAndSuccessfulUsage(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&OpsRequestEvent{}))
	t.Cleanup(func() { DB.Exec("DELETE FROM ops_request_events") })
	require.NoError(t, DB.Create([]OpsRequestEvent{
		{
			RequestID: "success", OccurredAtMs: 10_000, Success: true, StatusCode: 200,
			ModelName: "gpt-test", InputTokens: 10, OutputTokens: 20, CacheReadTokens: 3,
			DurationMs: 1000, TTFTMs: 100, HasTTFT: true,
		},
		{
			RequestID: "provider-error", OccurredAtMs: 11_000, Success: false, StatusCode: 200,
			UpstreamStatusCode: 429, ErrorOwner: "provider", InputTokens: 999, OutputTokens: 999,
		},
	}).Error)

	rows, err := QueryOpsRequestAggregates(OpsMetricFilter{StartTs: 0, EndTs: 20})
	require.NoError(t, err)
	require.Len(t, rows, 2)

	byStatus := make(map[int]OpsMinuteMetric, len(rows))
	for _, row := range rows {
		byStatus[row.StatusCode] = row
	}
	assert.Equal(t, int64(1), byStatus[200].SuccessCount)
	assert.Equal(t, int64(10), byStatus[200].InputTokens)
	assert.Equal(t, int64(20), byStatus[200].OutputTokens)
	assert.Equal(t, int64(3), byStatus[200].CacheReadTokens)
	assert.Equal(t, int64(1), byStatus[429].RequestCount)
	assert.Zero(t, byStatus[429].InputTokens)
	assert.Zero(t, byStatus[429].OutputTokens)
}

func TestOpsModelTokenStatsAveragePerRequestSpeed(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&OpsRequestEvent{}))
	t.Cleanup(func() { DB.Exec("DELETE FROM ops_request_events") })
	require.NoError(t, DB.Create([]OpsRequestEvent{
		{RequestID: "one", OccurredAtMs: 10_000, Success: true, ModelName: "gpt-test", OutputTokens: 10, DurationMs: 1000, TTFTMs: 100, HasTTFT: true},
		{RequestID: "two", OccurredAtMs: 11_000, Success: true, ModelName: "gpt-test", OutputTokens: 40, DurationMs: 2000},
		{RequestID: "other", OccurredAtMs: 12_000, Success: true, ModelName: "claude-test", OutputTokens: 100, DurationMs: 1000},
	}).Error)

	rows, err := QueryOpsModelTokenStats(0, 20_000, "gpt")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "gpt-test", rows[0].ModelName)
	assert.Equal(t, int64(2), rows[0].RequestCount)
	assert.Equal(t, int64(50), rows[0].OutputTokens)
	assert.InDelta(t, 15, rows[0].AverageTokensPS, 0.001)
	assert.InDelta(t, 100, rows[0].AverageTTFTMs, 0.001)
	assert.InDelta(t, 1500, rows[0].AverageDurationMs, 0.001)
	assert.Equal(t, int64(1), rows[0].TTFTSamples)
}

func TestUpsertOpsMinuteMetricAddsCountersAndKeepsMaximums(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&OpsMinuteMetric{}))
	t.Cleanup(func() { DB.Exec("DELETE FROM ops_minute_metrics") })
	first := &OpsMinuteMetric{
		BucketTs: 60, NodeName: "node", ModelName: "model", Group: "default",
		ChannelID: 1, EndpointType: "chat", StatusCode: 200,
		RequestCount: 1, SuccessCount: 1, TotalDurationMs: 100, MaxDurationMs: 100,
	}
	second := &OpsMinuteMetric{
		BucketTs: 60, NodeName: "node", ModelName: "model", Group: "default",
		ChannelID: 1, EndpointType: "chat", StatusCode: 200,
		RequestCount: 2, SuccessCount: 2, TotalDurationMs: 500, MaxDurationMs: 300,
	}
	require.NoError(t, UpsertOpsMinuteMetric(first))
	require.NoError(t, UpsertOpsMinuteMetric(second))

	var stored OpsMinuteMetric
	require.NoError(t, DB.First(&stored).Error)
	assert.Equal(t, int64(3), stored.RequestCount)
	assert.Equal(t, int64(3), stored.SuccessCount)
	assert.Equal(t, int64(600), stored.TotalDurationMs)
	assert.Equal(t, int64(300), stored.MaxDurationMs)
}
