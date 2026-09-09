package ops_monitor_setting

import "github.com/QuantumNous/new-api/setting/config"

type Setting struct {
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
	RequestP99ThresholdMs         int     `json:"request_p99_threshold_ms"`
	TTFTP99ThresholdMs            int     `json:"ttft_p99_threshold_ms"`
	RequestErrorRateThreshold     float64 `json:"request_error_rate_threshold"`
	UpstreamErrorRateThreshold    float64 `json:"upstream_error_rate_threshold"`
}

var current = Setting{
	Enabled:                       true,
	RawRetentionDays:              30,
	AggregateRetentionDays:        180,
	SystemRetentionDays:           90,
	SystemCollectionIntervalSecs:  60,
	ConcurrencyEnforcementEnabled: false,
	ConcurrencyFailOpen:           true,
	DefaultLeaseSeconds:           300,
	RateLimitCooldownSeconds:      60,
	OverloadCooldownSeconds:       30,
	TemporaryUnschedulableSeconds: 10,
	SLAThreshold:                  0.995,
	RequestP99ThresholdMs:         10_000,
	TTFTP99ThresholdMs:            500,
	RequestErrorRateThreshold:     0.05,
	UpstreamErrorRateThreshold:    0.05,
}

func init() {
	config.GlobalConfig.Register("ops_monitor_setting", &current)
}

func Get() Setting {
	result := current
	if result.RawRetentionDays < 1 {
		result.RawRetentionDays = 1
	}
	if result.AggregateRetentionDays < result.RawRetentionDays {
		result.AggregateRetentionDays = result.RawRetentionDays
	}
	if result.SystemRetentionDays < 1 {
		result.SystemRetentionDays = 1
	}
	if result.SystemCollectionIntervalSecs < 15 {
		result.SystemCollectionIntervalSecs = 15
	}
	if result.DefaultLeaseSeconds < 30 {
		result.DefaultLeaseSeconds = 30
	}
	if result.RateLimitCooldownSeconds < 1 {
		result.RateLimitCooldownSeconds = 1
	}
	if result.OverloadCooldownSeconds < 1 {
		result.OverloadCooldownSeconds = 1
	}
	if result.TemporaryUnschedulableSeconds < 1 {
		result.TemporaryUnschedulableSeconds = 1
	}
	if result.SLAThreshold <= 0 || result.SLAThreshold > 1 {
		result.SLAThreshold = 0.995
	}
	if result.RequestP99ThresholdMs < 1 {
		result.RequestP99ThresholdMs = 10_000
	}
	if result.TTFTP99ThresholdMs < 1 {
		result.TTFTP99ThresholdMs = 500
	}
	if result.RequestErrorRateThreshold <= 0 || result.RequestErrorRateThreshold > 1 {
		result.RequestErrorRateThreshold = 0.05
	}
	if result.UpstreamErrorRateThreshold <= 0 || result.UpstreamErrorRateThreshold > 1 {
		result.UpstreamErrorRateThreshold = 0.05
	}
	return result
}
