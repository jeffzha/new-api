package opsmonitor

import (
	"bufio"
	"context"
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ops_monitor_setting"
)

type cgroupCPUSample struct {
	usageSeconds float64
	sampledAt    time.Time
}

var cgroupSampler struct {
	sync.Mutex
	previous cgroupCPUSample
}

func systemCollectorLoop() {
	for {
		setting := ops_monitor_setting.Get()
		if setting.Enabled {
			collectSystemMetric()
		}
		interval := time.Duration(setting.SystemCollectionIntervalSecs) * time.Second
		time.Sleep(interval)
	}
}

func collectSystemMetric() {
	now := time.Now().UTC()
	identity := common.GetNodeIdentity()
	nodeName := strings.TrimSpace(identity.Name)
	if nodeName == "" {
		nodeName, _ = os.Hostname()
	}
	cpuPercent := cgroupCPUPercent()
	if cpuPercent < 0 {
		cpuPercent = common.GetSystemStatus().CPUUsage
	}
	memoryPercent := cgroupMemoryPercent()
	if memoryPercent < 0 {
		memoryPercent = common.GetSystemStatus().MemoryUsage
	}
	diskInfo := common.GetDiskSpaceInfo()
	metric := &model.OpsSystemMetric{
		BucketTs:      now.Truncate(time.Minute).Unix(),
		NodeName:      nodeName,
		CPUPercent:    cpuPercent,
		MemoryPercent: memoryPercent,
		DiskPercent:   diskInfo.UsedPercent,
		Goroutines:    runtime.NumGoroutine(),
		CreatedAt:     now.Unix(),
	}
	if network, err := common.GetNetworkIOStatus(); err == nil {
		metric.NetworkReceiveBps = network.ReceiveBytesPerSecond
		metric.NetworkTransmitBps = network.TransmitBytesPerSecond
		metric.NetworkReceivedBytes = network.ReceivedBytes
		metric.NetworkSentBytes = network.SentBytes
	}
	if sqlDB, err := model.DB.DB(); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		metric.DBHealthy = sqlDB.PingContext(ctx) == nil
		cancel()
		stats := sqlDB.Stats()
		metric.DBOpenConnections = stats.OpenConnections
		metric.DBMaxOpenConnections = stats.MaxOpenConnections
		metric.DBInUseConnections = stats.InUse
		metric.DBIdleConnections = stats.Idle
		metric.DBWaitCount = stats.WaitCount
		metric.DBWaitDurationMs = stats.WaitDuration.Milliseconds()
	}
	metric.RedisEnabled = common.RedisEnabled && common.RDB != nil
	if metric.RedisEnabled {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		metric.RedisHealthy = common.RDB.Ping(ctx).Err() == nil
		pool := common.RDB.PoolStats()
		metric.RedisTotalConnections = int(pool.TotalConns)
		metric.RedisIdleConnections = int(pool.IdleConns)
		metric.RedisMaxConnections = common.RDB.Options().PoolSize
		cancel()
	}
	metric.QueueDepth = currentQueueDepth()
	if err := model.UpsertOpsSystemMetric(metric); err != nil {
		logger.LogError(context.Background(), "persist ops system metric failed: "+err.Error())
	}
}

func cgroupCPUPercent() float64 {
	if runtime.GOOS != "linux" {
		return -1
	}
	usage, cores, err := readCgroupCPU()
	if err != nil || cores <= 0 {
		return -1
	}
	now := time.Now()
	cgroupSampler.Lock()
	previous := cgroupSampler.previous
	cgroupSampler.previous = cgroupCPUSample{usageSeconds: usage, sampledAt: now}
	cgroupSampler.Unlock()
	if previous.sampledAt.IsZero() || usage < previous.usageSeconds {
		return -1
	}
	elapsed := now.Sub(previous.sampledAt).Seconds()
	if elapsed <= 0 {
		return -1
	}
	percent := (usage - previous.usageSeconds) / (elapsed * cores) * 100
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

func readCgroupCPU() (float64, float64, error) {
	if content, err := os.ReadFile("/sys/fs/cgroup/cpu.stat"); err == nil {
		usageUsec := int64(0)
		scanner := bufio.NewScanner(strings.NewReader(string(content)))
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) == 2 && fields[0] == "usage_usec" {
				usageUsec, _ = strconv.ParseInt(fields[1], 10, 64)
				break
			}
		}
		if usageUsec <= 0 {
			return 0, 0, errors.New("cgroup v2 CPU usage unavailable")
		}
		cores := float64(runtime.NumCPU())
		if maxContent, maxErr := os.ReadFile("/sys/fs/cgroup/cpu.max"); maxErr == nil {
			fields := strings.Fields(string(maxContent))
			if len(fields) == 2 && fields[0] != "max" {
				quota, quotaErr := strconv.ParseFloat(fields[0], 64)
				period, periodErr := strconv.ParseFloat(fields[1], 64)
				if quotaErr == nil && periodErr == nil && period > 0 {
					cores = quota / period
				}
			}
		}
		return float64(usageUsec) / 1_000_000, cores, nil
	}
	usageContent, usageErr := os.ReadFile("/sys/fs/cgroup/cpuacct/cpuacct.usage")
	quotaContent, quotaErr := os.ReadFile("/sys/fs/cgroup/cpu/cpu.cfs_quota_us")
	periodContent, periodErr := os.ReadFile("/sys/fs/cgroup/cpu/cpu.cfs_period_us")
	if usageErr != nil || quotaErr != nil || periodErr != nil {
		return 0, 0, errors.New("cgroup CPU files unavailable")
	}
	usageNanos, err := strconv.ParseInt(strings.TrimSpace(string(usageContent)), 10, 64)
	if err != nil {
		return 0, 0, err
	}
	quota, _ := strconv.ParseFloat(strings.TrimSpace(string(quotaContent)), 64)
	period, _ := strconv.ParseFloat(strings.TrimSpace(string(periodContent)), 64)
	cores := float64(runtime.NumCPU())
	if quota > 0 && period > 0 {
		cores = quota / period
	}
	return float64(usageNanos) / 1_000_000_000, cores, nil
}

func cgroupMemoryPercent() float64 {
	if runtime.GOOS != "linux" {
		return -1
	}
	current, limit, err := readCgroupMemory()
	if err != nil || limit <= 0 || current < 0 {
		return -1
	}
	percent := float64(current) / float64(limit) * 100
	if percent > 100 {
		return 100
	}
	return percent
}

func readCgroupMemory() (int64, int64, error) {
	if currentContent, err := os.ReadFile("/sys/fs/cgroup/memory.current"); err == nil {
		maxContent, maxErr := os.ReadFile("/sys/fs/cgroup/memory.max")
		if maxErr != nil || strings.TrimSpace(string(maxContent)) == "max" {
			return 0, 0, errors.New("cgroup v2 memory limit unavailable")
		}
		current, currentErr := strconv.ParseInt(strings.TrimSpace(string(currentContent)), 10, 64)
		limit, limitErr := strconv.ParseInt(strings.TrimSpace(string(maxContent)), 10, 64)
		if currentErr != nil || limitErr != nil {
			return 0, 0, errors.New("invalid cgroup v2 memory values")
		}
		return current, limit, nil
	}
	currentContent, currentErr := os.ReadFile("/sys/fs/cgroup/memory/memory.usage_in_bytes")
	limitContent, limitErr := os.ReadFile("/sys/fs/cgroup/memory/memory.limit_in_bytes")
	if currentErr != nil || limitErr != nil {
		return 0, 0, errors.New("cgroup memory files unavailable")
	}
	current, err := strconv.ParseInt(strings.TrimSpace(string(currentContent)), 10, 64)
	if err != nil {
		return 0, 0, err
	}
	limit, err := strconv.ParseInt(strings.TrimSpace(string(limitContent)), 10, 64)
	if err != nil {
		return 0, 0, err
	}
	return current, limit, nil
}
