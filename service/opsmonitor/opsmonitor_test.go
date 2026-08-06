package opsmonitor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ops_monitor_setting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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

func TestMiddlewarePreservesUnmatchedRelayLikePathsForErrorDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalQueue := requestQueue
	requestQueue = make(chan model.OpsRequestEvent, 2)
	t.Cleanup(func() {
		requestQueue = originalQueue
	})

	router := gin.New()
	router.Use(Middleware())
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		c.Status(http.StatusBadGateway)
	})

	unmatched := httptest.NewRecorder()
	unmatchedRequest := httptest.NewRequest(http.MethodGet, "/v1/auth/users?pageNo=1&pageSize=100", nil)
	router.ServeHTTP(unmatched, unmatchedRequest)
	require.Equal(t, http.StatusNotFound, unmatched.Code)
	require.Len(t, requestQueue, 1)
	unmatchedEvent := <-requestQueue
	assert.Equal(t, model.OpsEndpointTypeUnmatchedRoute, unmatchedEvent.EndpointType)
	assert.Equal(t, http.StatusNotFound, unmatchedEvent.StatusCode)
	assert.False(t, unmatchedEvent.Success)

	matched := httptest.NewRecorder()
	matchedRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	router.ServeHTTP(matched, matchedRequest)
	require.Equal(t, http.StatusBadGateway, matched.Code)
	require.Len(t, requestQueue, 1)
	event := <-requestQueue
	assert.Equal(t, "chat", event.EndpointType)
	assert.Equal(t, http.StatusBadGateway, event.StatusCode)
	assert.False(t, event.Success)
}

func TestPersistRequestBatchKeepsUnmatchedDetailsOutOfMetrics(t *testing.T) {
	originalDB := model.DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.OpsRequestEvent{}, &model.OpsMinuteMetric{}))
	model.DB = db
	t.Cleanup(func() {
		model.DB = originalDB
	})

	events := []model.OpsRequestEvent{
		{
			RequestID:     "scan-request",
			OccurredAtMs:  120_000,
			CompletedAtMs: 120_010,
			EndpointType:  model.OpsEndpointTypeUnmatchedRoute,
			StatusCode:    http.StatusNotFound,
			ErrorOwner:    "gateway",
		},
		{
			RequestID:     "model-request",
			OccurredAtMs:  120_000,
			CompletedAtMs: 120_020,
			EndpointType:  "chat",
			StatusCode:    http.StatusBadGateway,
			ErrorOwner:    "provider",
		},
	}
	require.NoError(t, persistRequestBatch(events))

	var storedEvents []model.OpsRequestEvent
	require.NoError(t, db.Order("id asc").Find(&storedEvents).Error)
	require.Len(t, storedEvents, 2)
	assert.Equal(t, model.OpsEndpointTypeUnmatchedRoute, storedEvents[0].EndpointType)

	var metrics []model.OpsMinuteMetric
	require.NoError(t, db.Find(&metrics).Error)
	require.Len(t, metrics, 1)
	assert.Equal(t, "chat", metrics[0].EndpointType)
	assert.EqualValues(t, 1, metrics[0].RequestCount)

	filter := model.OpsMetricFilter{StartTs: 0, EndTs: 300}
	aggregates, err := model.QueryOpsRequestAggregates(filter)
	require.NoError(t, err)
	require.Len(t, aggregates, 1)
	assert.EqualValues(t, 1, aggregates[0].RequestCount)
	assert.Equal(t, "provider", aggregates[0].ErrorOwner)

	realtime, err := model.QueryOpsRealtimeAggregate(filter)
	require.NoError(t, err)
	assert.EqualValues(t, 1, realtime.RequestCount)
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

func TestBuildDiagnosticsUsesUnifiedWarningAndCriticalThresholds(t *testing.T) {
	settings := ops_monitor_setting.Setting{
		SLAThreshold:               0.995,
		RequestP99ThresholdMs:      10_000,
		TTFTP99ThresholdMs:         500,
		RequestErrorRateThreshold:  0.05,
		UpstreamErrorRateThreshold: 0.05,
	}
	snapshot := DashboardSnapshot{
		Overview: DashboardOverview{
			RequestCount:      100,
			SLA:               0.9955,
			RequestErrorRate:  0.04,
			UpstreamErrorRate: 0.05,
		},
		Latency: LatencySummary{
			Duration: Percentiles{P99: 8_000, Samples: 100},
			TTFT:     Percentiles{P99: 500, Samples: 100},
		},
	}

	diagnostics := buildDiagnostics(snapshot, settings)

	require.Len(t, diagnostics, 5)
	assert.Equal(t, Diagnostic{Severity: "warning", Metric: "sla", Message: "SLA is near the configured threshold", Value: 0.9955, Threshold: 0.995}, diagnostics[0])
	assert.Equal(t, "warning", diagnostics[1].Severity)
	assert.Equal(t, "request_p99", diagnostics[1].Metric)
	assert.Equal(t, "critical", diagnostics[2].Severity)
	assert.Equal(t, "ttft_p99", diagnostics[2].Metric)
	assert.Equal(t, "warning", diagnostics[3].Severity)
	assert.Equal(t, "request_error_rate", diagnostics[3].Metric)
	assert.Equal(t, "critical", diagnostics[4].Severity)
	assert.Equal(t, "upstream_error_rate", diagnostics[4].Metric)
}

func TestBuildDiagnosticsMatchesSystemMetricColorThresholds(t *testing.T) {
	snapshot := DashboardSnapshot{
		System: SystemSummary{LatestByNode: []model.OpsSystemMetric{{
			NodeName:      "green",
			DBHealthy:     true,
			CPUPercent:    95,
			MemoryPercent: 85,
			DiskPercent:   80,
		}}},
	}

	diagnostics := buildDiagnostics(snapshot, ops_monitor_setting.Setting{})

	require.Len(t, diagnostics, 3)
	assert.Equal(t, "critical", diagnostics[0].Severity)
	assert.Equal(t, "cpu", diagnostics[0].Metric)
	assert.Equal(t, float64(95), diagnostics[0].Threshold)
	assert.Equal(t, "warning", diagnostics[1].Severity)
	assert.Equal(t, "memory", diagnostics[1].Metric)
	assert.Equal(t, float64(85), diagnostics[1].Threshold)
	assert.Equal(t, "warning", diagnostics[2].Severity)
	assert.Equal(t, "disk", diagnostics[2].Metric)
	assert.Equal(t, float64(80), diagnostics[2].Threshold)
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
