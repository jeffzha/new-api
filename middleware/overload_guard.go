package middleware

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service/opsmonitor"
)

const (
	overloadReasonCPU    = "cpu"
	overloadReasonMemory = "memory"
	overloadReasonDisk   = "disk"
)

// Overload rejection error codes recorded in the operational request store.
const (
	ErrorCodeSystemOverloadCPUShed = types.ErrorCode("system_overload_cpu_shed")
	ErrorCodeSystemOverloadMemory  = types.ErrorCode("system_overload_memory")
	ErrorCodeSystemOverloadDisk    = types.ErrorCode("system_overload_disk")
)

var overloadReasons = []string{overloadReasonCPU, overloadReasonMemory, overloadReasonDisk}

// overloadBreach describes the host metric that exceeded its configured
// threshold for the current sample.
type overloadBreach struct {
	reason    string
	code      types.ErrorCode
	value     float64
	threshold int
}

// overloadEpisode tracks one continuous overload window so the recovery log can
// report how long it lasted and how many requests it rejected.
type overloadEpisode struct {
	reason          string
	reasons         map[string]struct{}
	startedAt       time.Time
	rejected        int64
	peakHost        common.SystemStatus
	peakConsecutive map[string]int
}

type overloadGuard struct {
	mu          sync.Mutex
	consecutive map[string]int
	active      *overloadEpisode
}

func newOverloadGuard() *overloadGuard {
	return &overloadGuard{consecutive: make(map[string]int, len(overloadReasons))}
}

var systemOverloadGuard = newOverloadGuard()

func init() {
	common.RegisterSystemSampleHook(func(status common.SystemStatus) {
		systemOverloadGuard.observeSample(status, common.GetPerformanceMonitorConfig())
	})
}

// evaluateOverload keeps the original host metric gate order (cpu, memory, disk)
// so the protection behaves exactly as before; only reporting was added.
func evaluateOverload(status common.SystemStatus, config common.PerformanceMonitorConfig) *overloadBreach {
	if config.CPUThreshold > 0 && int(status.CPUUsage) > config.CPUThreshold {
		return &overloadBreach{
			reason:    overloadReasonCPU,
			code:      ErrorCodeSystemOverloadCPUShed,
			value:     status.CPUUsage,
			threshold: config.CPUThreshold,
		}
	}
	if config.MemoryThreshold > 0 && int(status.MemoryUsage) > config.MemoryThreshold {
		return &overloadBreach{
			reason:    overloadReasonMemory,
			code:      ErrorCodeSystemOverloadMemory,
			value:     status.MemoryUsage,
			threshold: config.MemoryThreshold,
		}
	}
	if config.DiskThreshold > 0 && int(status.DiskUsage) > config.DiskThreshold {
		return &overloadBreach{
			reason:    overloadReasonDisk,
			code:      ErrorCodeSystemOverloadDisk,
			value:     status.DiskUsage,
			threshold: config.DiskThreshold,
		}
	}
	return nil
}

// observeSample refreshes the consecutive breach counters and closes an open
// episode once the host metrics fall back below their thresholds.
func (guard *overloadGuard) observeSample(status common.SystemStatus, config common.PerformanceMonitorConfig) {
	guard.mu.Lock()
	for _, reason := range overloadReasons {
		if metricBreached(status, config, reason) {
			guard.consecutive[reason]++
		} else {
			guard.consecutive[reason] = 0
		}
		if guard.active != nil && guard.consecutive[reason] > guard.active.peakConsecutive[reason] {
			guard.active.peakConsecutive[reason] = guard.consecutive[reason]
		}
	}
	guard.mu.Unlock()
	if evaluateOverload(status, config) == nil {
		guard.closeEpisode(status, config, "recovered")
	}
}

// recordRejection logs the start of an overload episode on the first rejected
// request and counts every rejected request afterwards.
func (guard *overloadGuard) recordRejection(breach *overloadBreach, status common.SystemStatus, config common.PerformanceMonitorConfig, path string, requestID string) {
	if breach == nil {
		return
	}
	guard.mu.Lock()
	started := guard.active == nil
	if started {
		guard.active = &overloadEpisode{
			reason:          breach.reason,
			reasons:         make(map[string]struct{}, len(overloadReasons)),
			startedAt:       time.Now(),
			peakHost:        status,
			peakConsecutive: make(map[string]int, len(overloadReasons)),
		}
	}
	episode := guard.active
	episode.reason = breach.reason
	episode.reasons[breach.reason] = struct{}{}
	episode.rejected++
	episode.peakHost = peakSystemStatus(episode.peakHost, status)
	for _, reason := range overloadReasons {
		if guard.consecutive[reason] > episode.peakConsecutive[reason] {
			episode.peakConsecutive[reason] = guard.consecutive[reason]
		}
	}
	consecutive := guard.formatConsecutiveLocked()
	guard.mu.Unlock()

	if !started {
		return
	}
	container := opsmonitor.GetContainerResourceSnapshot()
	logger.LogWarn(context.Background(),
		"[overload] triggered reason=%s code=%s rejected=1 %s %s %s %s path=%s request_id=%s",
		breach.reason, breach.code, formatHostStatus(status), formatContainerStatus(container),
		formatThresholds(config), consecutive, path, requestID)
}

// closeEpisode writes the recovery log when an episode is still open.
func (guard *overloadGuard) closeEpisode(status common.SystemStatus, config common.PerformanceMonitorConfig, reason string) {
	guard.mu.Lock()
	episode := guard.active
	guard.active = nil
	consecutive := guard.formatConsecutiveLocked()
	guard.mu.Unlock()
	if episode == nil {
		return
	}
	container := opsmonitor.GetContainerResourceSnapshot()
	logger.LogInfo(context.Background(),
		fmt.Sprintf("[overload] recovered reason=%s duration=%.1fs rejected=%d %s %s %s %s %s %s %s",
			reason, time.Since(episode.startedAt).Seconds(), episode.rejected,
			formatReasons(episode), formatPeakHostStatus(episode.peakHost), formatHostStatus(status),
			formatContainerStatus(container), formatThresholds(config), formatPeakConsecutive(episode), consecutive))
}

func (guard *overloadGuard) formatConsecutiveLocked() string {
	return fmt.Sprintf("consecutive{cpu=%d,memory=%d,disk=%d}",
		guard.consecutive[overloadReasonCPU],
		guard.consecutive[overloadReasonMemory],
		guard.consecutive[overloadReasonDisk])
}

func formatPeakConsecutive(episode *overloadEpisode) string {
	return fmt.Sprintf("peak_consecutive{cpu=%d,memory=%d,disk=%d}",
		episode.peakConsecutive[overloadReasonCPU],
		episode.peakConsecutive[overloadReasonMemory],
		episode.peakConsecutive[overloadReasonDisk])
}

func metricBreached(status common.SystemStatus, config common.PerformanceMonitorConfig, reason string) bool {
	switch reason {
	case overloadReasonCPU:
		return config.CPUThreshold > 0 && int(status.CPUUsage) > config.CPUThreshold
	case overloadReasonMemory:
		return config.MemoryThreshold > 0 && int(status.MemoryUsage) > config.MemoryThreshold
	case overloadReasonDisk:
		return config.DiskThreshold > 0 && int(status.DiskUsage) > config.DiskThreshold
	default:
		return false
	}
}

func peakSystemStatus(current, candidate common.SystemStatus) common.SystemStatus {
	return common.SystemStatus{
		CPUUsage:    max(current.CPUUsage, candidate.CPUUsage),
		MemoryUsage: max(current.MemoryUsage, candidate.MemoryUsage),
		DiskUsage:   max(current.DiskUsage, candidate.DiskUsage),
	}
}

func formatHostStatus(status common.SystemStatus) string {
	return fmt.Sprintf("host{cpu=%.1f%%,memory=%.1f%%,disk=%.1f%%}",
		status.CPUUsage, status.MemoryUsage, status.DiskUsage)
}

func formatPeakHostStatus(status common.SystemStatus) string {
	return fmt.Sprintf("peak_host{cpu=%.1f%%,memory=%.1f%%,disk=%.1f%%}",
		status.CPUUsage, status.MemoryUsage, status.DiskUsage)
}

func formatContainerStatus(snapshot opsmonitor.ContainerResourceSnapshot) string {
	return fmt.Sprintf("container{cpu=%.1f%%,memory=%.1f%%}", snapshot.CPUPercent, snapshot.MemoryPercent)
}

func formatThresholds(config common.PerformanceMonitorConfig) string {
	return fmt.Sprintf("thresholds{cpu=%d%%,memory=%d%%,disk=%d%%}",
		config.CPUThreshold, config.MemoryThreshold, config.DiskThreshold)
}

func formatReasons(episode *overloadEpisode) string {
	reasons := make([]string, 0, len(episode.reasons))
	for _, reason := range overloadReasons {
		if _, ok := episode.reasons[reason]; ok {
			reasons = append(reasons, reason)
		}
	}
	if len(reasons) == 0 {
		return "reasons=none"
	}
	return "reasons=" + strings.Join(reasons, ",")
}
