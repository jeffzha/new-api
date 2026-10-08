package opsmonitor

import (
	"runtime"
	"sync"
	"time"
)

// containerSnapshotCPUSampler measures the gateway container's own CPU usage as
// a percentage of its cgroup quota. It is intentionally separate from the ops
// collector sampler so diagnostics never shrink the collector's window.
type containerSnapshotCPUSampler struct {
	mu       sync.Mutex
	previous cgroupCPUSample
}

var snapshotCPUSampler containerSnapshotCPUSampler

func (sampler *containerSnapshotCPUSampler) percent() float64 {
	if runtime.GOOS != "linux" {
		return -1
	}
	usage, cores, err := readCgroupCPU()
	if err != nil || cores <= 0 {
		return -1
	}
	now := time.Now()
	sampler.mu.Lock()
	previous := sampler.previous
	usable := !previous.sampledAt.IsZero() && now.Sub(previous.sampledAt) <= 30*time.Second && usage >= previous.usageSeconds
	sampler.previous = cgroupCPUSample{usageSeconds: usage, sampledAt: now}
	sampler.mu.Unlock()
	if !usable {
		// The first diagnostic call has no baseline; measure a short window so the
		// reported value still reflects the container instead of an empty field.
		time.Sleep(100 * time.Millisecond)
		nextUsage, _, nextErr := readCgroupCPU()
		nextNow := time.Now()
		if nextErr != nil || nextUsage < usage {
			return -1
		}
		elapsed := nextNow.Sub(now).Seconds()
		if elapsed <= 0 {
			return -1
		}
		sampler.mu.Lock()
		sampler.previous = cgroupCPUSample{usageSeconds: nextUsage, sampledAt: nextNow}
		sampler.mu.Unlock()
		return containerCPUPercent(nextUsage-usage, elapsed, cores)
	}
	elapsed := now.Sub(previous.sampledAt).Seconds()
	if elapsed <= 0 {
		return -1
	}
	return containerCPUPercent(usage-previous.usageSeconds, elapsed, cores)
}

func containerCPUPercent(deltaSeconds, elapsedSeconds, cores float64) float64 {
	if elapsedSeconds <= 0 || cores <= 0 {
		return -1
	}
	percent := deltaSeconds / (elapsedSeconds * cores) * 100
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

// ContainerResourceSnapshot reports container-scoped usage so gateway guards can
// log what the container itself was doing while the host metrics tripped.
// Negative percentages mean the container does not expose cgroup values.
type ContainerResourceSnapshot struct {
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryPercent float64 `json:"memory_percent"`
}

// GetContainerResourceSnapshot returns the container-scoped usage snapshot.
func GetContainerResourceSnapshot() ContainerResourceSnapshot {
	return ContainerResourceSnapshot{
		CPUPercent:    snapshotCPUSampler.percent(),
		MemoryPercent: cgroupMemoryPercent(),
	}
}
