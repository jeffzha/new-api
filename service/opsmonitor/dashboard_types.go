package opsmonitor

import "github.com/QuantumNous/new-api/model"

type DashboardFilter struct {
	StartTs      int64  `json:"start_ts"`
	EndTs        int64  `json:"end_ts"`
	ModelName    string `json:"model_name,omitempty"`
	Group        string `json:"group,omitempty"`
	ChannelID    int    `json:"channel_id,omitempty"`
	EndpointType string `json:"endpoint_type,omitempty"`
	NodeName     string `json:"node_name,omitempty"`
}

type DashboardSnapshot struct {
	Filter          DashboardFilter         `json:"filter"`
	Overview        DashboardOverview       `json:"overview"`
	Realtime        RealtimeTraffic         `json:"realtime"`
	ThroughputTrend []ThroughputPoint       `json:"throughput_trend"`
	SwitchTrend     []SwitchPoint           `json:"switch_trend"`
	ErrorTrend      []ErrorTrendPoint       `json:"error_trend"`
	Latency         LatencySummary          `json:"latency"`
	Errors          ErrorSummary            `json:"errors"`
	Health          HealthScore             `json:"health"`
	System          SystemSummary           `json:"system"`
	Jobs            []JobHealth             `json:"jobs"`
	Tasks           TaskLifecycleSummary    `json:"tasks"`
	ModelTokenStats []ModelTokenStats       `json:"model_token_stats"`
	Concurrency     ConcurrencySnapshot     `json:"concurrency"`
	RecentErrors    []model.OpsRequestEvent `json:"recent_errors"`
	Telemetry       TelemetryHealth         `json:"telemetry"`
	Options         DashboardOptions        `json:"options"`
	Diagnostics     []Diagnostic            `json:"diagnostics"`
}

type DashboardOptions struct {
	Models        []string `json:"models"`
	Groups        []string `json:"groups"`
	EndpointTypes []string `json:"endpoint_types"`
	Nodes         []string `json:"nodes"`
}

type Diagnostic struct {
	Severity  string  `json:"severity"`
	Metric    string  `json:"metric"`
	Message   string  `json:"message"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
}

type DashboardOverview struct {
	RequestCount        int64   `json:"request_count"`
	SuccessCount        int64   `json:"success_count"`
	ErrorCount          int64   `json:"error_count"`
	SLAErrorCount       int64   `json:"sla_error_count"`
	BusinessLimitCount  int64   `json:"business_limit_count"`
	UpstreamErrorCount  int64   `json:"upstream_error_count"`
	SLA                 float64 `json:"sla"`
	RequestErrorRate    float64 `json:"request_error_rate"`
	UpstreamErrorRate   float64 `json:"upstream_error_rate"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	TotalTokens         int64   `json:"total_tokens"`
	AverageQPS          float64 `json:"average_qps"`
	AverageTPS          float64 `json:"average_tps"`
	AverageSwitches     float64 `json:"average_switches"`
}

type RealtimeTraffic struct {
	CurrentQPS  float64 `json:"current_qps"`
	CurrentTPS  float64 `json:"current_tps"`
	PeakQPS     float64 `json:"peak_qps"`
	PeakTPS     float64 `json:"peak_tps"`
	Approximate bool    `json:"approximate"`
}

type ThroughputPoint struct {
	Ts           int64   `json:"ts"`
	RequestCount int64   `json:"request_count"`
	TokenCount   int64   `json:"token_count"`
	QPS          float64 `json:"qps"`
	TPS          float64 `json:"tps"`
}

type SwitchPoint struct {
	Ts           int64   `json:"ts"`
	RequestCount int64   `json:"request_count"`
	SwitchCount  int64   `json:"switch_count"`
	Average      float64 `json:"average"`
}

type ErrorTrendPoint struct {
	Ts             int64 `json:"ts"`
	SLAErrors      int64 `json:"sla_errors"`
	UpstreamErrors int64 `json:"upstream_errors"`
	BusinessLimits int64 `json:"business_limits"`
}

type Percentiles struct {
	P50     int64 `json:"p50"`
	P90     int64 `json:"p90"`
	P95     int64 `json:"p95"`
	P99     int64 `json:"p99"`
	Avg     int64 `json:"avg"`
	Max     int64 `json:"max"`
	Samples int64 `json:"samples"`
}

type HistogramBucket struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
}

type LatencySummary struct {
	Duration    Percentiles       `json:"duration"`
	TTFT        Percentiles       `json:"ttft"`
	Histogram   []HistogramBucket `json:"histogram"`
	Approximate bool              `json:"approximate"`
}

type ErrorStatusDistribution struct {
	StatusCode     int   `json:"status_code"`
	Total          int64 `json:"total"`
	SLAErrors      int64 `json:"sla_errors"`
	BusinessLimits int64 `json:"business_limits"`
	UpstreamErrors int64 `json:"upstream_errors"`
}

type ErrorSummary struct {
	Distribution  []ErrorStatusDistribution `json:"distribution"`
	TopStatusCode int                       `json:"top_status_code"`
}

type HealthScore struct {
	State         string  `json:"state"`
	Score         int     `json:"score"`
	BusinessScore float64 `json:"business_score"`
	InfraScore    float64 `json:"infra_score"`
	ErrorScore    float64 `json:"error_score"`
	TTFTScore     float64 `json:"ttft_score"`
	StorageScore  float64 `json:"storage_score"`
	ComputeScore  float64 `json:"compute_score"`
	JobScore      float64 `json:"job_score"`
}

type SystemSummary struct {
	LatestByNode []model.OpsSystemMetric `json:"latest_by_node"`
	Trend        []model.OpsSystemMetric `json:"trend"`
}

type JobHealth struct {
	Type          string `json:"type"`
	Status        string `json:"status"`
	LastSuccessAt int64  `json:"last_success_at"`
	LastErrorAt   int64  `json:"last_error_at"`
	LastRunAt     int64  `json:"last_run_at"`
	LastError     string `json:"last_error,omitempty"`
}

type TaskLifecycleSummary struct {
	Submitted             int64   `json:"submitted"`
	SubmitSucceeded       int64   `json:"submit_succeeded"`
	Terminal              int64   `json:"terminal"`
	Succeeded             int64   `json:"succeeded"`
	Failed                int64   `json:"failed"`
	SubmitSuccessRate     float64 `json:"submit_success_rate"`
	GenerationSuccessRate float64 `json:"generation_success_rate"`
	AverageQueueMs        int64   `json:"average_queue_ms"`
	AverageGenerationMs   int64   `json:"average_generation_ms"`
	AverageEndToEndMs     int64   `json:"average_end_to_end_ms"`
}

type ModelTokenStats struct {
	ModelName         string  `json:"model_name"`
	RequestCount      int64   `json:"request_count"`
	OutputTokens      int64   `json:"output_tokens"`
	AverageTokensPS   float64 `json:"average_tokens_per_second"`
	AverageTTFTMs     int64   `json:"average_ttft_ms"`
	AverageDurationMs int64   `json:"average_duration_ms"`
	TTFTSamples       int64   `json:"ttft_samples"`
}

type TelemetryHealth struct {
	RequestQueueDepth int   `json:"request_queue_depth"`
	AttemptQueueDepth int   `json:"attempt_queue_depth"`
	DroppedRequests   int64 `json:"dropped_requests"`
	DroppedAttempts   int64 `json:"dropped_attempts"`
}
