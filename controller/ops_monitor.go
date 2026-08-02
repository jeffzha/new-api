package controller

import (
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opsmonitor"
	"github.com/QuantumNous/new-api/setting/ops_monitor_setting"

	"github.com/gin-gonic/gin"
)

func GetOpsDashboardSnapshot(c *gin.Context) {
	filter := opsmonitor.DashboardFilter{
		StartTs:      parseOpsInt64(c.Query("start")),
		EndTs:        parseOpsInt64(c.Query("end")),
		ModelName:    strings.TrimSpace(c.Query("model")),
		Group:        strings.TrimSpace(c.Query("group")),
		ChannelID:    parseOpsInt(c.Query("channel_id")),
		EndpointType: strings.TrimSpace(c.Query("endpoint_type")),
		NodeName:     strings.TrimSpace(c.Query("node_name")),
	}
	snapshot, err := opsmonitor.BuildDashboardSnapshot(filter)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, snapshot)
}

func GetOpsRequestDetail(c *gin.Context) {
	requestID := strings.TrimSpace(c.Param("request_id"))
	if requestID == "" {
		common.ApiErrorMsg(c, "request id is required")
		return
	}
	event, attempts, err := model.GetOpsRequestEvent(requestID)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"request": event, "attempts": attempts})
}

func GetOpsConcurrency(c *gin.Context) {
	snapshot, err := opsmonitor.GetConcurrencySnapshot()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, snapshot)
}

func GetOpsSettings(c *gin.Context) {
	common.ApiSuccess(c, ops_monitor_setting.Get())
}

type updateOpsSettingsRequest struct {
	Enabled                       bool    `json:"enabled"`
	RawRetentionDays              int     `json:"raw_retention_days"`
	AggregateRetentionDays        int     `json:"aggregate_retention_days"`
	SystemRetentionDays           int     `json:"system_retention_days"`
	SystemCollectionIntervalSecs  int     `json:"system_collection_interval_secs"`
	ConcurrencyEnforcementEnabled bool    `json:"concurrency_enforcement_enabled"`
	ConcurrencyFailOpen           bool    `json:"concurrency_fail_open"`
	DefaultLeaseSeconds           int     `json:"default_lease_seconds"`
	RateLimitCooldownSeconds      int     `json:"rate_limit_cooldown_seconds"`
	OverloadCooldownSeconds       int     `json:"overload_cooldown_seconds"`
	TemporaryUnschedulableSeconds int     `json:"temporary_unschedulable_seconds"`
	SLAThreshold                  float64 `json:"sla_threshold"`
	TTFTP99ThresholdMs            int     `json:"ttft_p99_threshold_ms"`
	RequestErrorRateThreshold     float64 `json:"request_error_rate_threshold"`
	UpstreamErrorRateThreshold    float64 `json:"upstream_error_rate_threshold"`
}

func UpdateOpsSettings(c *gin.Context) {
	var request updateOpsSettingsRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiErrorMsg(c, err.Error())
		return
	}
	if request.RawRetentionDays < 1 || request.RawRetentionDays > 365 ||
		request.AggregateRetentionDays < request.RawRetentionDays || request.AggregateRetentionDays > 3650 ||
		request.SystemRetentionDays < 1 || request.SystemRetentionDays > 3650 ||
		request.SystemCollectionIntervalSecs < 15 || request.SystemCollectionIntervalSecs > 3600 ||
		request.DefaultLeaseSeconds < 30 || request.DefaultLeaseSeconds > 86400 ||
		request.RateLimitCooldownSeconds < 1 || request.RateLimitCooldownSeconds > 86400 ||
		request.OverloadCooldownSeconds < 1 || request.OverloadCooldownSeconds > 86400 ||
		request.TemporaryUnschedulableSeconds < 1 || request.TemporaryUnschedulableSeconds > 3600 ||
		request.SLAThreshold <= 0 || request.SLAThreshold > 1 ||
		request.TTFTP99ThresholdMs < 1 || request.TTFTP99ThresholdMs > 600000 ||
		request.RequestErrorRateThreshold <= 0 || request.RequestErrorRateThreshold > 1 ||
		request.UpstreamErrorRateThreshold <= 0 || request.UpstreamErrorRateThreshold > 1 {
		common.ApiErrorMsg(c, "invalid operations monitoring settings")
		return
	}
	values := map[string]string{
		"ops_monitor_setting.enabled":                         strconv.FormatBool(request.Enabled),
		"ops_monitor_setting.raw_retention_days":              strconv.Itoa(request.RawRetentionDays),
		"ops_monitor_setting.aggregate_retention_days":        strconv.Itoa(request.AggregateRetentionDays),
		"ops_monitor_setting.system_retention_days":           strconv.Itoa(request.SystemRetentionDays),
		"ops_monitor_setting.system_collection_interval_secs": strconv.Itoa(request.SystemCollectionIntervalSecs),
		"ops_monitor_setting.concurrency_enforcement_enabled": strconv.FormatBool(request.ConcurrencyEnforcementEnabled),
		"ops_monitor_setting.concurrency_fail_open":           strconv.FormatBool(request.ConcurrencyFailOpen),
		"ops_monitor_setting.default_lease_seconds":           strconv.Itoa(request.DefaultLeaseSeconds),
		"ops_monitor_setting.rate_limit_cooldown_seconds":     strconv.Itoa(request.RateLimitCooldownSeconds),
		"ops_monitor_setting.overload_cooldown_seconds":       strconv.Itoa(request.OverloadCooldownSeconds),
		"ops_monitor_setting.temporary_unschedulable_seconds": strconv.Itoa(request.TemporaryUnschedulableSeconds),
		"ops_monitor_setting.sla_threshold":                   strconv.FormatFloat(request.SLAThreshold, 'f', -1, 64),
		"ops_monitor_setting.ttft_p99_threshold_ms":           strconv.Itoa(request.TTFTP99ThresholdMs),
		"ops_monitor_setting.request_error_rate_threshold":    strconv.FormatFloat(request.RequestErrorRateThreshold, 'f', -1, 64),
		"ops_monitor_setting.upstream_error_rate_threshold":   strconv.FormatFloat(request.UpstreamErrorRateThreshold, 'f', -1, 64),
	}
	if err := model.UpdateOptionsBulk(values); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, ops_monitor_setting.Get())
}

func ListOpsConcurrencyLimits(c *gin.Context) {
	limits, err := model.ListOpsConcurrencyLimits()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, limits)
}

type upsertOpsConcurrencyLimitRequest struct {
	ChannelID      int  `json:"channel_id"`
	KeyIndex       *int `json:"key_index"`
	Enabled        bool `json:"enabled"`
	MaxConcurrency int  `json:"max_concurrency"`
	QueueSize      int  `json:"queue_size"`
	QueueTimeoutMs int  `json:"queue_timeout_ms"`
	LeaseSeconds   int  `json:"lease_seconds"`
}

func UpsertOpsConcurrencyLimit(c *gin.Context) {
	var request upsertOpsConcurrencyLimitRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiErrorMsg(c, err.Error())
		return
	}
	if request.ChannelID <= 0 || request.MaxConcurrency < 0 || request.MaxConcurrency > 100000 ||
		request.QueueSize < 0 || request.QueueSize > 100000 || request.QueueTimeoutMs < 0 || request.QueueTimeoutMs > 3600000 ||
		request.LeaseSeconds < 0 || request.LeaseSeconds > 86400 || (request.Enabled && request.MaxConcurrency == 0) {
		common.ApiErrorMsg(c, "invalid concurrency limit")
		return
	}
	channel, err := model.GetChannelById(request.ChannelID, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	keyIndex := model.OpsConcurrencyAllKeys
	if request.KeyIndex != nil {
		keyIndex = *request.KeyIndex
		if keyIndex < 0 {
			keyIndex = model.OpsConcurrencyAllKeys
		}
	}
	if keyIndex >= 0 && (channel.ChannelInfo.MultiKeySize <= 0 || keyIndex >= channel.ChannelInfo.MultiKeySize) {
		common.ApiErrorMsg(c, "channel key index is out of range")
		return
	}
	limit := &model.OpsConcurrencyLimit{
		ChannelID: request.ChannelID, KeyIndex: keyIndex, Enabled: request.Enabled,
		MaxConcurrency: request.MaxConcurrency, QueueSize: request.QueueSize,
		QueueTimeoutMs: request.QueueTimeoutMs, LeaseSeconds: request.LeaseSeconds,
	}
	if err := model.UpsertOpsConcurrencyLimit(limit); err != nil {
		common.ApiError(c, err)
		return
	}
	opsmonitor.ReloadConcurrencyLimits()
	common.ApiSuccess(c, limit)
}

func DeleteOpsConcurrencyLimit(c *gin.Context) {
	channelID := parseOpsInt(c.Param("channel_id"))
	keyIndex := parseOpsInt(c.Query("key_index"))
	if c.Query("key_index") == "" {
		keyIndex = model.OpsConcurrencyAllKeys
	}
	if channelID <= 0 {
		common.ApiErrorMsg(c, "channel id is required")
		return
	}
	if err := model.DeleteOpsConcurrencyLimit(channelID, keyIndex); err != nil {
		common.ApiError(c, err)
		return
	}
	opsmonitor.ReloadConcurrencyLimits()
	common.ApiSuccess(c, gin.H{"deleted": true})
}

func parseOpsInt(value string) int {
	parsed, _ := strconv.Atoi(strings.TrimSpace(value))
	return parsed
}

func parseOpsInt64(value string) int64 {
	parsed, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if parsed > time.Now().AddDate(10, 0, 0).Unix() {
		return 0
	}
	return parsed
}
