package opsmonitor

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ops_monitor_setting"
)

const (
	requestQueueSize = 10000
	attemptQueueSize = 20000
	writerBatchSize  = 200
)

var (
	startOnce       sync.Once
	requestQueue    chan model.OpsRequestEvent
	attemptQueue    chan model.OpsUpstreamAttempt
	droppedRequests atomic.Int64
	droppedAttempts atomic.Int64
)

func Start() {
	startOnce.Do(func() {
		requestQueue = make(chan model.OpsRequestEvent, requestQueueSize)
		attemptQueue = make(chan model.OpsUpstreamAttempt, attemptQueueSize)
		startConcurrency()
		go writerLoop()
		go systemCollectorLoop()
		if common.IsMasterNode {
			go retentionLoop()
		}
	})
}

func QueueStats() (int, int, int64, int64) {
	requestDepth := 0
	attemptDepth := 0
	if requestQueue != nil {
		requestDepth = len(requestQueue)
	}
	if attemptQueue != nil {
		attemptDepth = len(attemptQueue)
	}
	return requestDepth, attemptDepth, droppedRequests.Load(), droppedAttempts.Load()
}

func enqueueRequest(event model.OpsRequestEvent) {
	if requestQueue == nil {
		return
	}
	select {
	case requestQueue <- event:
	default:
		droppedRequests.Add(1)
		logger.LogWarn(context.Background(), "ops request event queue is full; dropping event")
	}
}

func enqueueAttempt(attempt model.OpsUpstreamAttempt) {
	if attemptQueue == nil {
		return
	}
	select {
	case attemptQueue <- attempt:
	default:
		droppedAttempts.Add(1)
		logger.LogWarn(context.Background(), "ops upstream attempt queue is full; dropping event")
	}
}

func writerLoop() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	requests := make([]model.OpsRequestEvent, 0, writerBatchSize)
	attempts := make([]model.OpsUpstreamAttempt, 0, writerBatchSize)
	flush := func() {
		if len(requests) > 0 {
			if err := persistRequestBatch(requests); err != nil {
				logger.LogError(context.Background(), "persist ops request batch failed: "+err.Error())
			}
			requests = requests[:0]
		}
		if len(attempts) > 0 {
			if err := model.CreateOpsUpstreamAttempts(attempts); err != nil {
				logger.LogError(context.Background(), "persist ops attempt batch failed: "+err.Error())
			}
			attempts = attempts[:0]
		}
	}

	for {
		select {
		case event := <-requestQueue:
			requests = append(requests, event)
			if len(requests) >= writerBatchSize {
				flush()
			}
		case attempt := <-attemptQueue:
			attempts = append(attempts, attempt)
			if len(attempts) >= writerBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

type minuteMetricKey struct {
	bucketTs        int64
	nodeName        string
	modelName       string
	group           string
	channelID       int
	endpointType    string
	statusCode      int
	errorOwner      string
	businessLimited bool
}

func persistRequestBatch(events []model.OpsRequestEvent) error {
	if err := model.CreateOpsRequestEvents(events); err != nil {
		return err
	}
	aggregates := make(map[minuteMetricKey]*model.OpsMinuteMetric)
	for _, event := range events {
		if event.EndpointType == model.OpsEndpointTypeUnmatchedRoute {
			continue
		}
		effectiveStatusCode := event.StatusCode
		if event.UpstreamStatusCode > 0 {
			effectiveStatusCode = event.UpstreamStatusCode
		}
		key := minuteMetricKey{
			bucketTs:        (event.OccurredAtMs / 1000 / 60) * 60,
			nodeName:        event.NodeName,
			modelName:       event.ModelName,
			group:           event.Group,
			channelID:       event.ChannelID,
			endpointType:    event.EndpointType,
			statusCode:      effectiveStatusCode,
			errorOwner:      event.ErrorOwner,
			businessLimited: event.BusinessLimited,
		}
		metric := aggregates[key]
		if metric == nil {
			metric = &model.OpsMinuteMetric{
				BucketTs:        key.bucketTs,
				NodeName:        key.nodeName,
				ModelName:       key.modelName,
				Group:           key.group,
				ChannelID:       key.channelID,
				EndpointType:    key.endpointType,
				StatusCode:      key.statusCode,
				ErrorOwner:      key.errorOwner,
				BusinessLimited: key.businessLimited,
			}
			aggregates[key] = metric
		}
		addEventToMinuteMetric(metric, event)
	}
	for _, metric := range aggregates {
		if err := model.UpsertOpsMinuteMetric(metric); err != nil {
			return err
		}
	}
	return nil
}

func addEventToMinuteMetric(metric *model.OpsMinuteMetric, event model.OpsRequestEvent) {
	metric.RequestCount++
	if event.Success {
		metric.SuccessCount++
		metric.TotalDurationMs += event.DurationMs
		metric.InputTokens += event.InputTokens
		metric.OutputTokens += event.OutputTokens
		metric.CacheReadTokens += event.CacheReadTokens
		metric.CacheCreationTokens += event.CacheCreationTokens
		if event.DurationMs > metric.MaxDurationMs {
			metric.MaxDurationMs = event.DurationMs
		}
		addDurationBucket(event.DurationMs, &metric.LatencyLT100, &metric.LatencyLT200, &metric.LatencyLT500, &metric.LatencyLT1000, &metric.LatencyLT2000, &metric.LatencyGTE2000)
	}
	if event.Success && event.HasTTFT {
		metric.TTFTCount++
		metric.TTFTSumMs += event.TTFTMs
		if event.TTFTMs > metric.MaxTTFTMs {
			metric.MaxTTFTMs = event.TTFTMs
		}
		addDurationBucket(event.TTFTMs, &metric.TTFTLT100, &metric.TTFTLT200, &metric.TTFTLT500, &metric.TTFTLT1000, &metric.TTFTLT2000, &metric.TTFTGTE2000)
	}
	metric.RetryCount += int64(event.RetryCount)
	metric.SwitchCount += int64(event.SwitchCount)
}

func addDurationBucket(value int64, lt100, lt200, lt500, lt1000, lt2000, gte2000 *int64) {
	switch {
	case value < 100:
		(*lt100)++
	case value < 200:
		(*lt200)++
	case value < 500:
		(*lt500)++
	case value < 1000:
		(*lt1000)++
	case value < 2000:
		(*lt2000)++
	default:
		(*gte2000)++
	}
}

func retentionLoop() {
	for {
		setting := ops_monitor_setting.Get()
		now := time.Now()
		err := model.DeleteOpsDataBefore(
			now.Add(-time.Duration(setting.RawRetentionDays)*24*time.Hour).UnixMilli(),
			now.Add(-time.Duration(setting.AggregateRetentionDays)*24*time.Hour).Unix(),
			now.Add(-time.Duration(setting.SystemRetentionDays)*24*time.Hour).Unix(),
		)
		if err != nil {
			common.SysError(fmt.Sprintf("ops retention cleanup failed: %v", err))
		}
		time.Sleep(24 * time.Hour)
	}
}
