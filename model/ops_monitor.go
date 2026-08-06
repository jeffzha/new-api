package model

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	OpsConcurrencyAllKeys         = -1
	OpsEndpointTypeUnmatchedRoute = "unmatched_route"
)

// OpsRequestEvent is the short-retention, normalized source of truth for the
// operations dashboard. It intentionally contains no request/response bodies.
type OpsRequestEvent struct {
	ID                  int64  `json:"id" gorm:"primaryKey"`
	RequestID           string `json:"request_id" gorm:"type:varchar(64);index"`
	NodeName            string `json:"node_name" gorm:"type:varchar(128);index"`
	OccurredAtMs        int64  `json:"occurred_at_ms" gorm:"index"`
	CompletedAtMs       int64  `json:"completed_at_ms"`
	Method              string `json:"method" gorm:"type:varchar(12)"`
	Path                string `json:"path" gorm:"type:varchar(255);index"`
	EndpointType        string `json:"endpoint_type" gorm:"type:varchar(48);index"`
	RelayFormat         string `json:"relay_format" gorm:"type:varchar(48)"`
	UserID              int    `json:"user_id" gorm:"index"`
	TokenID             int    `json:"token_id" gorm:"index"`
	ChannelID           int    `json:"channel_id" gorm:"index"`
	ChannelType         int    `json:"channel_type"`
	ChannelKeyIndex     int    `json:"channel_key_index"`
	ModelName           string `json:"model_name" gorm:"type:varchar(191);index"`
	UpstreamModelName   string `json:"upstream_model_name" gorm:"type:varchar(191)"`
	Group               string `json:"group" gorm:"column:group;type:varchar(64);index"`
	StatusCode          int    `json:"status_code" gorm:"index"`
	UpstreamStatusCode  int    `json:"upstream_status_code"`
	Success             bool   `json:"success" gorm:"index"`
	ErrorOwner          string `json:"error_owner" gorm:"type:varchar(24);index"`
	BusinessLimited     bool   `json:"business_limited" gorm:"index"`
	DurationMs          int64  `json:"duration_ms"`
	TTFTMs              int64  `json:"ttft_ms"`
	HasTTFT             bool   `json:"has_ttft"`
	IsStream            bool   `json:"is_stream"`
	InputTokens         int64  `json:"input_tokens"`
	OutputTokens        int64  `json:"output_tokens"`
	CacheReadTokens     int64  `json:"cache_read_tokens"`
	CacheCreationTokens int64  `json:"cache_creation_tokens"`
	RetryCount          int    `json:"retry_count"`
	SwitchCount         int    `json:"switch_count"`
	ErrorType           string `json:"error_type" gorm:"type:varchar(64)"`
	ErrorCode           string `json:"error_code" gorm:"type:varchar(128);index"`
	ErrorSummary        string `json:"error_summary" gorm:"type:varchar(512)"`
	TaskID              string `json:"task_id" gorm:"type:varchar(191);index"`
}

func (OpsRequestEvent) TableName() string { return "ops_request_events" }

// OpsUpstreamAttempt records provider attempts separately from downstream
// requests so retries do not inflate request/SLA figures.
type OpsUpstreamAttempt struct {
	ID                 int64  `json:"id" gorm:"primaryKey"`
	RequestID          string `json:"request_id" gorm:"type:varchar(64);index"`
	NodeName           string `json:"node_name" gorm:"type:varchar(128);index"`
	StartedAtMs        int64  `json:"started_at_ms" gorm:"index"`
	CompletedAtMs      int64  `json:"completed_at_ms"`
	AttemptIndex       int    `json:"attempt_index"`
	ChannelID          int    `json:"channel_id" gorm:"index"`
	ChannelType        int    `json:"channel_type"`
	ChannelKeyIndex    int    `json:"channel_key_index"`
	ModelName          string `json:"model_name" gorm:"type:varchar(191);index"`
	Group              string `json:"group" gorm:"column:group;type:varchar(64);index"`
	StatusCode         int    `json:"status_code" gorm:"index"`
	Success            bool   `json:"success"`
	SwitchedChannel    bool   `json:"switched_channel"`
	DurationMs         int64  `json:"duration_ms"`
	ErrorCode          string `json:"error_code" gorm:"type:varchar(128)"`
	ConcurrencyWaitMs  int64  `json:"concurrency_wait_ms"`
	ConcurrencyTracked bool   `json:"concurrency_tracked"`
}

func (OpsUpstreamAttempt) TableName() string { return "ops_upstream_attempts" }

// OpsMinuteMetric is an additive minute aggregate. Status/error dimensions are
// kept as normal columns so all supported databases can filter them without
// dialect-specific JSON expressions.
type OpsMinuteMetric struct {
	ID                  int64  `json:"id" gorm:"primaryKey"`
	BucketTs            int64  `json:"bucket_ts" gorm:"uniqueIndex:idx_ops_minute_dimensions,priority:1;index"`
	NodeName            string `json:"node_name" gorm:"type:varchar(128);uniqueIndex:idx_ops_minute_dimensions,priority:2"`
	ModelName           string `json:"model_name" gorm:"type:varchar(191);uniqueIndex:idx_ops_minute_dimensions,priority:3;index"`
	Group               string `json:"group" gorm:"column:group;type:varchar(64);uniqueIndex:idx_ops_minute_dimensions,priority:4;index"`
	ChannelID           int    `json:"channel_id" gorm:"uniqueIndex:idx_ops_minute_dimensions,priority:5;index"`
	EndpointType        string `json:"endpoint_type" gorm:"type:varchar(48);uniqueIndex:idx_ops_minute_dimensions,priority:6;index"`
	StatusCode          int    `json:"status_code" gorm:"uniqueIndex:idx_ops_minute_dimensions,priority:7;index"`
	ErrorOwner          string `json:"error_owner" gorm:"type:varchar(24);uniqueIndex:idx_ops_minute_dimensions,priority:8"`
	BusinessLimited     bool   `json:"business_limited" gorm:"uniqueIndex:idx_ops_minute_dimensions,priority:9"`
	RequestCount        int64  `json:"request_count"`
	SuccessCount        int64  `json:"success_count"`
	InputTokens         int64  `json:"input_tokens"`
	OutputTokens        int64  `json:"output_tokens"`
	CacheReadTokens     int64  `json:"cache_read_tokens"`
	CacheCreationTokens int64  `json:"cache_creation_tokens"`
	TotalDurationMs     int64  `json:"total_duration_ms"`
	MaxDurationMs       int64  `json:"max_duration_ms"`
	TTFTSumMs           int64  `json:"ttft_sum_ms"`
	TTFTCount           int64  `json:"ttft_count"`
	MaxTTFTMs           int64  `json:"max_ttft_ms"`
	LatencyLT100        int64  `json:"latency_lt_100" gorm:"column:latency_lt_100"`
	LatencyLT200        int64  `json:"latency_lt_200" gorm:"column:latency_lt_200"`
	LatencyLT500        int64  `json:"latency_lt_500" gorm:"column:latency_lt_500"`
	LatencyLT1000       int64  `json:"latency_lt_1000" gorm:"column:latency_lt_1000"`
	LatencyLT2000       int64  `json:"latency_lt_2000" gorm:"column:latency_lt_2000"`
	LatencyGTE2000      int64  `json:"latency_gte_2000" gorm:"column:latency_gte_2000"`
	TTFTLT100           int64  `json:"ttft_lt_100" gorm:"column:ttft_lt_100"`
	TTFTLT200           int64  `json:"ttft_lt_200" gorm:"column:ttft_lt_200"`
	TTFTLT500           int64  `json:"ttft_lt_500" gorm:"column:ttft_lt_500"`
	TTFTLT1000          int64  `json:"ttft_lt_1000" gorm:"column:ttft_lt_1000"`
	TTFTLT2000          int64  `json:"ttft_lt_2000" gorm:"column:ttft_lt_2000"`
	TTFTGTE2000         int64  `json:"ttft_gte_2000" gorm:"column:ttft_gte_2000"`
	RetryCount          int64  `json:"retry_count"`
	SwitchCount         int64  `json:"switch_count"`
	UpdatedAt           int64  `json:"updated_at"`
}

func (OpsMinuteMetric) TableName() string { return "ops_minute_metrics" }

// OpsSystemMetric stores one resource sample per node and natural minute.
type OpsSystemMetric struct {
	ID                    int64   `json:"id" gorm:"primaryKey"`
	BucketTs              int64   `json:"bucket_ts" gorm:"uniqueIndex:idx_ops_system_bucket_node,priority:1;index"`
	NodeName              string  `json:"node_name" gorm:"type:varchar(128);uniqueIndex:idx_ops_system_bucket_node,priority:2;index"`
	CPUPercent            float64 `json:"cpu_percent"`
	MemoryPercent         float64 `json:"memory_percent"`
	DiskPercent           float64 `json:"disk_percent"`
	NetworkReceiveBps     float64 `json:"network_receive_bps"`
	NetworkTransmitBps    float64 `json:"network_transmit_bps"`
	NetworkReceivedBytes  uint64  `json:"network_received_bytes"`
	NetworkSentBytes      uint64  `json:"network_sent_bytes"`
	Goroutines            int     `json:"goroutines"`
	DBHealthy             bool    `json:"db_healthy"`
	DBOpenConnections     int     `json:"db_open_connections"`
	DBMaxOpenConnections  int     `json:"db_max_open_connections"`
	DBInUseConnections    int     `json:"db_in_use_connections"`
	DBIdleConnections     int     `json:"db_idle_connections"`
	DBWaitCount           int64   `json:"db_wait_count"`
	DBWaitDurationMs      int64   `json:"db_wait_duration_ms"`
	RedisEnabled          bool    `json:"redis_enabled"`
	RedisHealthy          bool    `json:"redis_healthy"`
	RedisTotalConnections int     `json:"redis_total_connections"`
	RedisIdleConnections  int     `json:"redis_idle_connections"`
	RedisMaxConnections   int     `json:"redis_max_connections"`
	QueueDepth            int64   `json:"queue_depth"`
	CreatedAt             int64   `json:"created_at"`
}

func (OpsSystemMetric) TableName() string { return "ops_system_metrics" }

// OpsConcurrencyLimit is deliberately separate from Channel so monitoring can
// be merged or removed without changing the upstream channel schema.
type OpsConcurrencyLimit struct {
	ID             int64 `json:"id" gorm:"primaryKey"`
	ChannelID      int   `json:"channel_id" gorm:"uniqueIndex:idx_ops_concurrency_scope,priority:1;index"`
	KeyIndex       int   `json:"key_index" gorm:"uniqueIndex:idx_ops_concurrency_scope,priority:2"`
	Enabled        bool  `json:"enabled"`
	MaxConcurrency int   `json:"max_concurrency"`
	QueueSize      int   `json:"queue_size"`
	QueueTimeoutMs int   `json:"queue_timeout_ms"`
	LeaseSeconds   int   `json:"lease_seconds"`
	CreatedAt      int64 `json:"created_at"`
	UpdatedAt      int64 `json:"updated_at"`
}

func (OpsConcurrencyLimit) TableName() string { return "ops_concurrency_limits" }

func (limit *OpsConcurrencyLimit) BeforeCreate(_ *gorm.DB) error {
	now := time.Now().Unix()
	if limit.CreatedAt == 0 {
		limit.CreatedAt = now
	}
	limit.UpdatedAt = now
	return nil
}

func CreateOpsRequestEvents(events []OpsRequestEvent) error {
	if len(events) == 0 {
		return nil
	}
	return DB.CreateInBatches(events, 100).Error
}

func CreateOpsUpstreamAttempts(attempts []OpsUpstreamAttempt) error {
	if len(attempts) == 0 {
		return nil
	}
	return DB.CreateInBatches(attempts, 100).Error
}

func UpsertOpsMinuteMetric(metric *OpsMinuteMetric) error {
	if metric == nil || metric.RequestCount <= 0 {
		return nil
	}
	metric.UpdatedAt = time.Now().Unix()
	return DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "bucket_ts"}, {Name: "node_name"}, {Name: "model_name"},
			{Name: "group"}, {Name: "channel_id"}, {Name: "endpoint_type"},
			{Name: "status_code"}, {Name: "error_owner"}, {Name: "business_limited"},
		},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"request_count":         gorm.Expr("ops_minute_metrics.request_count + ?", metric.RequestCount),
			"success_count":         gorm.Expr("ops_minute_metrics.success_count + ?", metric.SuccessCount),
			"input_tokens":          gorm.Expr("ops_minute_metrics.input_tokens + ?", metric.InputTokens),
			"output_tokens":         gorm.Expr("ops_minute_metrics.output_tokens + ?", metric.OutputTokens),
			"cache_read_tokens":     gorm.Expr("ops_minute_metrics.cache_read_tokens + ?", metric.CacheReadTokens),
			"cache_creation_tokens": gorm.Expr("ops_minute_metrics.cache_creation_tokens + ?", metric.CacheCreationTokens),
			"total_duration_ms":     gorm.Expr("ops_minute_metrics.total_duration_ms + ?", metric.TotalDurationMs),
			"max_duration_ms":       gorm.Expr("CASE WHEN ops_minute_metrics.max_duration_ms > ? THEN ops_minute_metrics.max_duration_ms ELSE ? END", metric.MaxDurationMs, metric.MaxDurationMs),
			"ttft_sum_ms":           gorm.Expr("ops_minute_metrics.ttft_sum_ms + ?", metric.TTFTSumMs),
			"ttft_count":            gorm.Expr("ops_minute_metrics.ttft_count + ?", metric.TTFTCount),
			"max_ttft_ms":           gorm.Expr("CASE WHEN ops_minute_metrics.max_ttft_ms > ? THEN ops_minute_metrics.max_ttft_ms ELSE ? END", metric.MaxTTFTMs, metric.MaxTTFTMs),
			"latency_lt_100":        gorm.Expr("ops_minute_metrics.latency_lt_100 + ?", metric.LatencyLT100),
			"latency_lt_200":        gorm.Expr("ops_minute_metrics.latency_lt_200 + ?", metric.LatencyLT200),
			"latency_lt_500":        gorm.Expr("ops_minute_metrics.latency_lt_500 + ?", metric.LatencyLT500),
			"latency_lt_1000":       gorm.Expr("ops_minute_metrics.latency_lt_1000 + ?", metric.LatencyLT1000),
			"latency_lt_2000":       gorm.Expr("ops_minute_metrics.latency_lt_2000 + ?", metric.LatencyLT2000),
			"latency_gte_2000":      gorm.Expr("ops_minute_metrics.latency_gte_2000 + ?", metric.LatencyGTE2000),
			"ttft_lt_100":           gorm.Expr("ops_minute_metrics.ttft_lt_100 + ?", metric.TTFTLT100),
			"ttft_lt_200":           gorm.Expr("ops_minute_metrics.ttft_lt_200 + ?", metric.TTFTLT200),
			"ttft_lt_500":           gorm.Expr("ops_minute_metrics.ttft_lt_500 + ?", metric.TTFTLT500),
			"ttft_lt_1000":          gorm.Expr("ops_minute_metrics.ttft_lt_1000 + ?", metric.TTFTLT1000),
			"ttft_lt_2000":          gorm.Expr("ops_minute_metrics.ttft_lt_2000 + ?", metric.TTFTLT2000),
			"ttft_gte_2000":         gorm.Expr("ops_minute_metrics.ttft_gte_2000 + ?", metric.TTFTGTE2000),
			"retry_count":           gorm.Expr("ops_minute_metrics.retry_count + ?", metric.RetryCount),
			"switch_count":          gorm.Expr("ops_minute_metrics.switch_count + ?", metric.SwitchCount),
			"updated_at":            metric.UpdatedAt,
		}),
	}).Create(metric).Error
}

func UpsertOpsSystemMetric(metric *OpsSystemMetric) error {
	if metric == nil {
		return nil
	}
	return DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "bucket_ts"}, {Name: "node_name"}},
		DoUpdates: clause.AssignmentColumns([]string{"cpu_percent", "memory_percent", "disk_percent", "network_receive_bps", "network_transmit_bps", "network_received_bytes", "network_sent_bytes", "goroutines", "db_healthy", "db_open_connections", "db_max_open_connections", "db_in_use_connections", "db_idle_connections", "db_wait_count", "db_wait_duration_ms", "redis_enabled", "redis_healthy", "redis_total_connections", "redis_idle_connections", "redis_max_connections", "queue_depth", "created_at"}),
	}).Create(metric).Error
}

type OpsMetricFilter struct {
	StartTs      int64
	EndTs        int64
	ModelName    string
	Group        string
	ChannelID    int
	EndpointType string
	NodeName     string
}

func applyOpsMetricFilter(query *gorm.DB, filter OpsMetricFilter) *gorm.DB {
	query = query.Where("bucket_ts >= ? AND bucket_ts < ?", filter.StartTs, filter.EndTs)
	if filter.ModelName != "" {
		query = query.Where("model_name = ?", filter.ModelName)
	}
	if filter.Group != "" {
		query = query.Where(commonGroupCol+" = ?", filter.Group)
	}
	if filter.ChannelID > 0 {
		query = query.Where("channel_id = ?", filter.ChannelID)
	}
	if filter.EndpointType != "" {
		query = query.Where("endpoint_type = ?", filter.EndpointType)
	}
	if filter.NodeName != "" {
		query = query.Where("node_name = ?", filter.NodeName)
	}
	return query
}

func QueryOpsMinuteMetrics(filter OpsMetricFilter) ([]OpsMinuteMetric, error) {
	var rows []OpsMinuteMetric
	query := applyOpsMetricFilter(DB.Model(&OpsMinuteMetric{}), filter)
	err := query.Order("bucket_ts asc").Find(&rows).Error
	return rows, err
}

func applyOpsEventFilter(query *gorm.DB, filter OpsMetricFilter) *gorm.DB {
	query = query.Where("occurred_at_ms >= ? AND occurred_at_ms < ?", filter.StartTs*1000, filter.EndTs*1000)
	if filter.ModelName != "" {
		query = query.Where("model_name = ?", filter.ModelName)
	}
	if filter.Group != "" {
		query = query.Where(commonGroupCol+" = ?", filter.Group)
	}
	if filter.ChannelID > 0 {
		query = query.Where("channel_id = ?", filter.ChannelID)
	}
	if filter.EndpointType != "" {
		query = query.Where("endpoint_type = ?", filter.EndpointType)
	}
	if filter.NodeName != "" {
		query = query.Where("node_name = ?", filter.NodeName)
	}
	return query
}

// QueryOpsRequestAggregates returns exact totals from raw events for windows
// still covered by raw retention. It keeps error dimensions so SLA exclusions
// remain identical to the minute-aggregate path.
func QueryOpsRequestAggregates(filter OpsMetricFilter) ([]OpsMinuteMetric, error) {
	var rows []OpsMinuteMetric
	query := applyOpsEventFilter(DB.Model(&OpsRequestEvent{}), filter).
		Where("endpoint_type <> ?", OpsEndpointTypeUnmatchedRoute)
	statusExpression := "CASE WHEN upstream_status_code > 0 THEN upstream_status_code ELSE status_code END"
	err := query.Select(statusExpression+` AS status_code, error_owner, business_limited,
		COUNT(*) AS request_count,
		COALESCE(SUM(CASE WHEN success = ? THEN 1 ELSE 0 END), 0) AS success_count,
		COALESCE(SUM(CASE WHEN success = ? THEN input_tokens ELSE 0 END), 0) AS input_tokens,
		COALESCE(SUM(CASE WHEN success = ? THEN output_tokens ELSE 0 END), 0) AS output_tokens,
		COALESCE(SUM(CASE WHEN success = ? THEN cache_read_tokens ELSE 0 END), 0) AS cache_read_tokens,
		COALESCE(SUM(CASE WHEN success = ? THEN cache_creation_tokens ELSE 0 END), 0) AS cache_creation_tokens,
		COALESCE(SUM(retry_count), 0) AS retry_count,
		COALESCE(SUM(switch_count), 0) AS switch_count`, true, true, true, true, true).
		Group(statusExpression + ", error_owner, business_limited").
		Scan(&rows).Error
	return rows, err
}

type OpsRealtimeAggregate struct {
	RequestCount        int64 `json:"request_count"`
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
}

func QueryOpsRealtimeAggregate(filter OpsMetricFilter) (OpsRealtimeAggregate, error) {
	var result OpsRealtimeAggregate
	query := applyOpsEventFilter(DB.Model(&OpsRequestEvent{}), filter).
		Where("endpoint_type <> ?", OpsEndpointTypeUnmatchedRoute)
	err := query.Select(`
		COUNT(*) AS request_count,
		COALESCE(SUM(CASE WHEN success = ? THEN input_tokens ELSE 0 END), 0) AS input_tokens,
		COALESCE(SUM(CASE WHEN success = ? THEN output_tokens ELSE 0 END), 0) AS output_tokens,
		COALESCE(SUM(CASE WHEN success = ? THEN cache_read_tokens ELSE 0 END), 0) AS cache_read_tokens,
		COALESCE(SUM(CASE WHEN success = ? THEN cache_creation_tokens ELSE 0 END), 0) AS cache_creation_tokens`, true, true, true, true).
		Scan(&result).Error
	return result, err
}

type OpsLatencySample struct {
	DurationMs int64 `json:"duration_ms"`
	TTFTMs     int64 `json:"ttft_ms"`
	HasTTFT    bool  `json:"has_ttft"`
}

func ListOpsLatencySamples(filter OpsMetricFilter, limit int) ([]OpsLatencySample, int64, error) {
	if limit <= 0 || limit > 50000 {
		limit = 50000
	}
	query := applyOpsEventFilter(DB.Model(&OpsRequestEvent{}), filter).Where("success = ?", true)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if total > int64(limit) {
		return nil, total, nil
	}
	var rows []OpsLatencySample
	err := query.Select("duration_ms, ttft_ms, has_ttft").Order("occurred_at_ms desc").Limit(limit).Find(&rows).Error
	return rows, total, err
}

func QueryOpsSystemMetrics(startTs, endTs int64, nodeName string) ([]OpsSystemMetric, error) {
	var rows []OpsSystemMetric
	query := DB.Where("bucket_ts >= ? AND bucket_ts < ?", startTs, endTs)
	if nodeName != "" {
		query = query.Where("node_name = ?", nodeName)
	}
	err := query.Order("bucket_ts asc, node_name asc").Find(&rows).Error
	return rows, err
}

func ListRecentOpsErrors(filter OpsMetricFilter, limit int) ([]OpsRequestEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := applyOpsEventFilter(DB.Model(&OpsRequestEvent{}), filter).Where("success = ?", false)
	var rows []OpsRequestEvent
	err := query.Order("occurred_at_ms desc").Limit(limit).Find(&rows).Error
	return rows, err
}

func GetOpsRequestEvent(requestID string) (*OpsRequestEvent, []OpsUpstreamAttempt, error) {
	var event OpsRequestEvent
	if err := DB.Where("request_id = ?", requestID).Order("id desc").First(&event).Error; err != nil {
		return nil, nil, err
	}
	var attempts []OpsUpstreamAttempt
	if err := DB.Where("request_id = ?", requestID).Order("attempt_index asc, id asc").Find(&attempts).Error; err != nil {
		return nil, nil, err
	}
	return &event, attempts, nil
}

func DeleteOpsDataBefore(rawBeforeMs, aggregateBeforeTs, systemBeforeTs int64) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if rawBeforeMs > 0 {
			if err := tx.Where("occurred_at_ms < ?", rawBeforeMs).Delete(&OpsRequestEvent{}).Error; err != nil {
				return err
			}
			if err := tx.Where("started_at_ms < ?", rawBeforeMs).Delete(&OpsUpstreamAttempt{}).Error; err != nil {
				return err
			}
		}
		if aggregateBeforeTs > 0 {
			if err := tx.Where("bucket_ts < ?", aggregateBeforeTs).Delete(&OpsMinuteMetric{}).Error; err != nil {
				return err
			}
		}
		if systemBeforeTs > 0 {
			if err := tx.Where("bucket_ts < ?", systemBeforeTs).Delete(&OpsSystemMetric{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func ListOpsConcurrencyLimits() ([]OpsConcurrencyLimit, error) {
	var limits []OpsConcurrencyLimit
	err := DB.Order("channel_id asc, key_index asc").Find(&limits).Error
	return limits, err
}

func GetOpsConcurrencyLimit(channelID, keyIndex int) (*OpsConcurrencyLimit, error) {
	var limit OpsConcurrencyLimit
	err := DB.Where("channel_id = ? AND key_index IN ?", channelID, []int{keyIndex, OpsConcurrencyAllKeys}).
		Order("key_index desc").First(&limit).Error
	if err != nil {
		return nil, err
	}
	return &limit, nil
}

func UpsertOpsConcurrencyLimit(limit *OpsConcurrencyLimit) error {
	if limit == nil {
		return nil
	}
	now := time.Now().Unix()
	if limit.KeyIndex < OpsConcurrencyAllKeys {
		limit.KeyIndex = OpsConcurrencyAllKeys
	}
	limit.UpdatedAt = now
	if limit.CreatedAt == 0 {
		limit.CreatedAt = now
	}
	return DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "channel_id"}, {Name: "key_index"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"enabled", "max_concurrency", "queue_size", "queue_timeout_ms", "lease_seconds", "updated_at",
		}),
	}).Create(limit).Error
}

func DeleteOpsConcurrencyLimit(channelID, keyIndex int) error {
	return DB.Where("channel_id = ? AND key_index = ?", channelID, keyIndex).Delete(&OpsConcurrencyLimit{}).Error
}

type OpsChannelInfo struct {
	ID          int         `json:"id"`
	Name        string      `json:"name"`
	Type        int         `json:"type"`
	Status      int         `json:"status"`
	Group       string      `json:"group"`
	ChannelInfo ChannelInfo `json:"channel_info"`
}

func ListOpsChannels() ([]OpsChannelInfo, error) {
	var channels []OpsChannelInfo
	err := DB.Model(&Channel{}).
		Select("id, name, type, status, " + commonGroupCol + ", channel_info").
		Order("id asc").Find(&channels).Error
	return channels, err
}

func ListRecentSystemTasksForOps(limit int) ([]SystemTask, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	var tasks []SystemTask
	err := DB.Order("updated_at desc, id desc").Limit(limit).Find(&tasks).Error
	return tasks, err
}

type OpsTaskLifecycleRow struct {
	TaskID     string     `json:"task_id"`
	Platform   string     `json:"platform"`
	Status     TaskStatus `json:"status"`
	CreatedAt  int64      `json:"created_at"`
	SubmitTime int64      `json:"submit_time"`
	StartTime  int64      `json:"start_time"`
	FinishTime int64      `json:"finish_time"`
}

func ListOpsTaskLifecycleRows(startTs, endTs int64) ([]OpsTaskLifecycleRow, error) {
	var rows []OpsTaskLifecycleRow
	err := DB.Model(&Task{}).
		Select("task_id, platform, status, created_at, submit_time, start_time, finish_time").
		Where("created_at >= ? AND created_at < ?", startTs, endTs).
		Order("created_at asc").Find(&rows).Error
	return rows, err
}

type OpsModelTokenStat struct {
	ModelName         string  `json:"model_name"`
	RequestCount      int64   `json:"request_count"`
	OutputTokens      int64   `json:"output_tokens"`
	AverageTokensPS   float64 `json:"average_tokens_per_second" gorm:"column:average_tokens_per_second"`
	AverageTTFTMs     float64 `json:"average_ttft_ms"`
	AverageDurationMs float64 `json:"average_duration_ms"`
	TTFTSamples       int64   `json:"ttft_samples"`
}

func QueryOpsModelTokenStats(startMs, endMs int64, modelPrefix string) ([]OpsModelTokenStat, error) {
	query := DB.Model(&OpsRequestEvent{}).
		Where("occurred_at_ms >= ? AND occurred_at_ms < ? AND success = ?", startMs, endMs, true)
	if modelPrefix != "" {
		query = query.Where("model_name LIKE ?", modelPrefix+"%")
	}
	var rows []OpsModelTokenStat
	err := query.Select(`
		model_name,
		COUNT(*) AS request_count,
		COALESCE(SUM(output_tokens), 0) AS output_tokens,
		COALESCE(AVG(CASE WHEN duration_ms > 0 AND output_tokens > 0 THEN output_tokens * 1000.0 / duration_ms ELSE NULL END), 0) AS average_tokens_per_second,
		COALESCE(AVG(CASE WHEN has_ttft = ? THEN ttft_ms ELSE NULL END), 0) AS average_ttft_ms,
		COALESCE(AVG(duration_ms), 0) AS average_duration_ms,
		COALESCE(SUM(CASE WHEN has_ttft = ? THEN 1 ELSE 0 END), 0) AS ttft_samples`, true, true).
		Group("model_name").Order("request_count desc, model_name asc").Scan(&rows).Error
	return rows, err
}
