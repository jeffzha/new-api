package opsmonitor

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ops_monitor_setting"
)

var modelStatsCache struct {
	sync.Mutex
	endMinute int64
	rows      []ModelTokenStats
}

func BuildDashboardSnapshot(filter DashboardFilter) (DashboardSnapshot, error) {
	now := time.Now().Unix()
	if filter.EndTs <= 0 {
		filter.EndTs = now
	}
	if filter.StartTs <= 0 || filter.StartTs >= filter.EndTs {
		filter.StartTs = filter.EndTs - 3600
	}
	if filter.EndTs-filter.StartTs > 31*24*3600 {
		filter.StartTs = filter.EndTs - 31*24*3600
	}
	modelFilter := model.OpsMetricFilter{
		StartTs: filter.StartTs, EndTs: filter.EndTs, ModelName: filter.ModelName,
		Group: filter.Group, ChannelID: filter.ChannelID, EndpointType: filter.EndpointType,
		NodeName: filter.NodeName,
	}
	rows, err := model.QueryOpsMinuteMetrics(modelFilter)
	if err != nil {
		return DashboardSnapshot{}, err
	}
	systemRows, err := model.QueryOpsSystemMetrics(filter.StartTs, filter.EndTs, filter.NodeName)
	if err != nil {
		return DashboardSnapshot{}, err
	}
	jobs, err := buildJobHealth()
	if err != nil {
		return DashboardSnapshot{}, err
	}
	tasks, err := buildTaskLifecycle(filter.StartTs, filter.EndTs, rows)
	if err != nil {
		return DashboardSnapshot{}, err
	}
	recentErrors, err := model.ListRecentOpsErrors(modelFilter, 50)
	if err != nil {
		return DashboardSnapshot{}, err
	}
	concurrency, err := GetConcurrencySnapshot()
	if err != nil {
		return DashboardSnapshot{}, err
	}
	modelTokenStats, err := buildCachedModelTokenStats(filter.EndTs)
	if err != nil {
		return DashboardSnapshot{}, err
	}
	settings := ops_monitor_setting.Get()
	rawAvailable := filter.StartTs >= now-int64(settings.RawRetentionDays)*24*3600
	overviewRows := rows
	if rawAvailable {
		overviewRows, err = model.QueryOpsRequestAggregates(modelFilter)
		if err != nil {
			return DashboardSnapshot{}, err
		}
	}
	overview := buildOverview(overviewRows, filter.EndTs-filter.StartTs)
	latency := buildLatency(rows)
	if rawAvailable {
		samples, total, sampleErr := model.ListOpsLatencySamples(modelFilter, 50000)
		if sampleErr != nil {
			return DashboardSnapshot{}, sampleErr
		}
		if total <= int64(len(samples)) {
			latency = buildExactLatency(samples, total)
		}
	}
	var realtimeExact *model.OpsRealtimeAggregate
	if filter.EndTs >= now-int64(settings.RawRetentionDays)*24*3600 {
		realtimeFilter := modelFilter
		realtimeFilter.StartTs = filter.EndTs - 60
		if realtimeFilter.StartTs < filter.StartTs {
			realtimeFilter.StartTs = filter.StartTs
		}
		realtime, realtimeErr := model.QueryOpsRealtimeAggregate(realtimeFilter)
		if realtimeErr != nil {
			return DashboardSnapshot{}, realtimeErr
		}
		realtimeExact = &realtime
	}
	requestDepth, attemptDepth, droppedRequests, droppedAttempts := QueueStats()
	snapshot := DashboardSnapshot{
		Filter:          filter,
		Overview:        overview,
		Realtime:        buildRealtime(rows, filter.EndTs, realtimeExact),
		ThroughputTrend: buildThroughputTrend(rows, filter.StartTs, filter.EndTs),
		SwitchTrend:     buildSwitchTrend(rows, filter.StartTs, filter.EndTs),
		ErrorTrend:      buildErrorTrend(rows, filter.StartTs, filter.EndTs),
		Latency:         latency,
		Errors:          buildErrorSummary(rows),
		System:          buildSystemSummary(systemRows),
		Jobs:            jobs,
		Tasks:           tasks,
		ModelTokenStats: modelTokenStats,
		Concurrency:     concurrency,
		RecentErrors:    recentErrors,
		Telemetry:       TelemetryHealth{RequestQueueDepth: requestDepth, AttemptQueueDepth: attemptDepth, DroppedRequests: droppedRequests, DroppedAttempts: droppedAttempts},
		Options:         buildDashboardOptions(rows),
	}
	snapshot.Health = computeHealthScore(overview, latency, snapshot.System.LatestByNode, jobs)
	snapshot.Diagnostics = buildDiagnostics(snapshot, settings)
	return snapshot, nil
}

func buildOverview(rows []model.OpsMinuteMetric, windowSeconds int64) DashboardOverview {
	var result DashboardOverview
	for _, row := range rows {
		result.RequestCount += row.RequestCount
		result.SuccessCount += row.SuccessCount
		result.InputTokens += row.InputTokens
		result.OutputTokens += row.OutputTokens
		result.CacheReadTokens += row.CacheReadTokens
		result.CacheCreationTokens += row.CacheCreationTokens
		result.AverageSwitches += float64(row.SwitchCount)
		if row.RequestCount > row.SuccessCount {
			errors := row.RequestCount - row.SuccessCount
			result.ErrorCount += errors
			if row.BusinessLimited {
				result.BusinessLimitCount += errors
			} else {
				result.SLAErrorCount += errors
			}
			if row.ErrorOwner == "provider" && row.StatusCode != 429 && row.StatusCode != 529 && row.StatusCode >= 400 {
				result.UpstreamErrorCount += errors
			}
		}
	}
	result.TotalTokens = result.InputTokens + result.OutputTokens + result.CacheReadTokens + result.CacheCreationTokens
	slaDenominator := result.SuccessCount + result.SLAErrorCount
	if slaDenominator > 0 {
		result.SLA = float64(result.SuccessCount) / float64(slaDenominator)
		result.RequestErrorRate = float64(result.SLAErrorCount) / float64(slaDenominator)
		result.UpstreamErrorRate = float64(result.UpstreamErrorCount) / float64(slaDenominator)
	}
	if result.RequestCount > 0 {
		result.AverageSwitches /= float64(result.RequestCount)
	}
	if windowSeconds > 0 {
		result.AverageQPS = float64(result.RequestCount) / float64(windowSeconds)
		result.AverageTPS = float64(result.TotalTokens) / float64(windowSeconds)
	}
	return result
}

func buildRealtime(rows []model.OpsMinuteMetric, endTs int64, exact *model.OpsRealtimeAggregate) RealtimeTraffic {
	minuteRequests := map[int64]int64{}
	minuteTokens := map[int64]int64{}
	for _, row := range rows {
		minuteRequests[row.BucketTs] += row.RequestCount
		minuteTokens[row.BucketTs] += row.InputTokens + row.OutputTokens + row.CacheReadTokens + row.CacheCreationTokens
	}
	result := RealtimeTraffic{Approximate: exact == nil}
	lastMinuteStart := endTs - 60
	var currentRequests, currentTokens int64
	for ts, requests := range minuteRequests {
		qps := float64(requests) / 60
		if qps > result.PeakQPS {
			result.PeakQPS = qps
		}
		tps := float64(minuteTokens[ts]) / 60
		if tps > result.PeakTPS {
			result.PeakTPS = tps
		}
		if ts+60 > lastMinuteStart && ts < endTs {
			currentRequests += requests
			currentTokens += minuteTokens[ts]
		}
	}
	result.CurrentQPS = float64(currentRequests) / 60
	result.CurrentTPS = float64(currentTokens) / 60
	if exact != nil {
		result.CurrentQPS = float64(exact.RequestCount) / 60
		result.CurrentTPS = float64(exact.InputTokens+exact.OutputTokens+exact.CacheReadTokens+exact.CacheCreationTokens) / 60
	}
	return result
}

type trendAccumulator struct {
	requests       int64
	tokens         int64
	switches       int64
	slaErrors      int64
	upstreamErrors int64
	businessLimits int64
}

func trendBucketSeconds(startTs, endTs int64) int64 {
	duration := endTs - startTs
	if duration <= 2*3600 {
		return 60
	}
	if duration <= 24*3600 {
		return 300
	}
	return 3600
}

func aggregateTrend(rows []model.OpsMinuteMetric, bucketSeconds int64) map[int64]*trendAccumulator {
	buckets := map[int64]*trendAccumulator{}
	for _, row := range rows {
		ts := row.BucketTs - row.BucketTs%bucketSeconds
		bucket := buckets[ts]
		if bucket == nil {
			bucket = &trendAccumulator{}
			buckets[ts] = bucket
		}
		bucket.requests += row.RequestCount
		bucket.tokens += row.InputTokens + row.OutputTokens + row.CacheReadTokens + row.CacheCreationTokens
		bucket.switches += row.SwitchCount
		errors := row.RequestCount - row.SuccessCount
		if row.BusinessLimited {
			bucket.businessLimits += errors
		} else {
			bucket.slaErrors += errors
		}
		if row.ErrorOwner == "provider" && row.StatusCode >= 400 && row.StatusCode != 429 && row.StatusCode != 529 {
			bucket.upstreamErrors += errors
		}
	}
	return buckets
}

func trendTimestamps(startTs, endTs, bucketSeconds int64) []int64 {
	first := startTs - startTs%bucketSeconds
	timestamps := make([]int64, 0, (endTs-first)/bucketSeconds+1)
	for timestamp := first; timestamp < endTs; timestamp += bucketSeconds {
		timestamps = append(timestamps, timestamp)
	}
	return timestamps
}

func buildThroughputTrend(rows []model.OpsMinuteMetric, startTs, endTs int64) []ThroughputPoint {
	bucketSeconds := trendBucketSeconds(startTs, endTs)
	buckets := aggregateTrend(rows, bucketSeconds)
	points := make([]ThroughputPoint, 0, len(buckets))
	for _, ts := range trendTimestamps(startTs, endTs, bucketSeconds) {
		bucket := buckets[ts]
		if bucket == nil {
			bucket = &trendAccumulator{}
		}
		points = append(points, ThroughputPoint{Ts: ts, RequestCount: bucket.requests, TokenCount: bucket.tokens, QPS: float64(bucket.requests) / float64(bucketSeconds), TPS: float64(bucket.tokens) / float64(bucketSeconds)})
	}
	return points
}

func buildSwitchTrend(rows []model.OpsMinuteMetric, startTs, endTs int64) []SwitchPoint {
	bucketSeconds := trendBucketSeconds(startTs, endTs)
	buckets := aggregateTrend(rows, bucketSeconds)
	points := make([]SwitchPoint, 0, len(buckets))
	for _, ts := range trendTimestamps(startTs, endTs, bucketSeconds) {
		bucket := buckets[ts]
		if bucket == nil {
			bucket = &trendAccumulator{}
		}
		average := 0.0
		if bucket.requests > 0 {
			average = float64(bucket.switches) / float64(bucket.requests)
		}
		points = append(points, SwitchPoint{Ts: ts, RequestCount: bucket.requests, SwitchCount: bucket.switches, Average: average})
	}
	return points
}

func buildErrorTrend(rows []model.OpsMinuteMetric, startTs, endTs int64) []ErrorTrendPoint {
	bucketSeconds := trendBucketSeconds(startTs, endTs)
	buckets := aggregateTrend(rows, bucketSeconds)
	points := make([]ErrorTrendPoint, 0, len(buckets))
	for _, ts := range trendTimestamps(startTs, endTs, bucketSeconds) {
		bucket := buckets[ts]
		if bucket == nil {
			bucket = &trendAccumulator{}
		}
		points = append(points, ErrorTrendPoint{Ts: ts, SLAErrors: bucket.slaErrors, UpstreamErrors: bucket.upstreamErrors, BusinessLimits: bucket.businessLimits})
	}
	return points
}

func buildLatency(rows []model.OpsMinuteMetric) LatencySummary {
	latencyBuckets := []int64{0, 0, 0, 0, 0, 0}
	ttftBuckets := []int64{0, 0, 0, 0, 0, 0}
	var durationSum, durationSamples, maxDuration int64
	var ttftSum, ttftSamples, maxTTFT int64
	for _, row := range rows {
		durationSamples += row.SuccessCount
		durationSum += row.TotalDurationMs
		if row.MaxDurationMs > maxDuration {
			maxDuration = row.MaxDurationMs
		}
		latencyBuckets[0] += row.LatencyLT100
		latencyBuckets[1] += row.LatencyLT200
		latencyBuckets[2] += row.LatencyLT500
		latencyBuckets[3] += row.LatencyLT1000
		latencyBuckets[4] += row.LatencyLT2000
		latencyBuckets[5] += row.LatencyGTE2000
		ttftSamples += row.TTFTCount
		ttftSum += row.TTFTSumMs
		if row.MaxTTFTMs > maxTTFT {
			maxTTFT = row.MaxTTFTMs
		}
		ttftBuckets[0] += row.TTFTLT100
		ttftBuckets[1] += row.TTFTLT200
		ttftBuckets[2] += row.TTFTLT500
		ttftBuckets[3] += row.TTFTLT1000
		ttftBuckets[4] += row.TTFTLT2000
		ttftBuckets[5] += row.TTFTGTE2000
	}
	labels := []string{"0-100ms", "100-200ms", "200-500ms", "500-1000ms", "1000-2000ms", "2000ms+"}
	histogram := make([]HistogramBucket, len(labels))
	for i, label := range labels {
		histogram[i] = HistogramBucket{Label: label, Count: latencyBuckets[i]}
	}
	return LatencySummary{
		Duration:  percentileSummary(latencyBuckets, durationSum, durationSamples, maxDuration),
		TTFT:      percentileSummary(ttftBuckets, ttftSum, ttftSamples, maxTTFT),
		Histogram: histogram, Approximate: true,
	}
}

func buildExactLatency(samples []model.OpsLatencySample, total int64) LatencySummary {
	durations := make([]int64, 0, len(samples))
	ttfts := make([]int64, 0, len(samples))
	buckets := make([]int64, 6)
	for _, sample := range samples {
		durations = append(durations, sample.DurationMs)
		addSampleBucket(buckets, sample.DurationMs)
		if sample.HasTTFT {
			ttfts = append(ttfts, sample.TTFTMs)
		}
	}
	labels := []string{"0-100ms", "100-200ms", "200-500ms", "500-1000ms", "1000-2000ms", "2000ms+"}
	histogram := make([]HistogramBucket, len(labels))
	for index, label := range labels {
		histogram[index] = HistogramBucket{Label: label, Count: buckets[index]}
	}
	return LatencySummary{
		Duration:    exactPercentiles(durations),
		TTFT:        exactPercentiles(ttfts),
		Histogram:   histogram,
		Approximate: total > int64(len(samples)),
	}
}

func addSampleBucket(buckets []int64, value int64) {
	switch {
	case value < 100:
		buckets[0]++
	case value < 200:
		buckets[1]++
	case value < 500:
		buckets[2]++
	case value < 1000:
		buckets[3]++
	case value < 2000:
		buckets[4]++
	default:
		buckets[5]++
	}
}

func exactPercentiles(values []int64) Percentiles {
	result := Percentiles{Samples: int64(len(values))}
	if len(values) == 0 {
		return result
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	var sum int64
	for _, value := range values {
		sum += value
	}
	result.Avg = sum / int64(len(values))
	result.Max = values[len(values)-1]
	result.P50 = interpolatedPercentile(values, 0.50)
	result.P90 = interpolatedPercentile(values, 0.90)
	result.P95 = interpolatedPercentile(values, 0.95)
	result.P99 = interpolatedPercentile(values, 0.99)
	return result
}

func interpolatedPercentile(values []int64, percentile float64) int64 {
	if len(values) == 0 {
		return 0
	}
	position := float64(len(values)-1) * percentile
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return values[lower]
	}
	fraction := position - float64(lower)
	return int64(math.Round(float64(values[lower]) + float64(values[upper]-values[lower])*fraction))
}

func percentileSummary(buckets []int64, sum, samples, max int64) Percentiles {
	result := Percentiles{Samples: samples, Max: max}
	if samples <= 0 {
		return result
	}
	result.Avg = sum / samples
	result.P50 = approximatePercentile(buckets, max, 0.50)
	result.P90 = approximatePercentile(buckets, max, 0.90)
	result.P95 = approximatePercentile(buckets, max, 0.95)
	result.P99 = approximatePercentile(buckets, max, 0.99)
	return result
}

func approximatePercentile(buckets []int64, max int64, percentile float64) int64 {
	total := int64(0)
	for _, count := range buckets {
		total += count
	}
	if total == 0 {
		return 0
	}
	target := int64(math.Ceil(float64(total) * percentile))
	bounds := [][2]int64{{0, 100}, {100, 200}, {200, 500}, {500, 1000}, {1000, 2000}, {2000, max}}
	seen := int64(0)
	for i, count := range buckets {
		if count == 0 {
			continue
		}
		if seen+count >= target {
			lower, upper := bounds[i][0], bounds[i][1]
			if upper < lower {
				upper = lower
			}
			fraction := float64(target-seen) / float64(count)
			return lower + int64(float64(upper-lower)*fraction)
		}
		seen += count
	}
	return max
}

func buildErrorSummary(rows []model.OpsMinuteMetric) ErrorSummary {
	byStatus := map[int]*ErrorStatusDistribution{}
	for _, row := range rows {
		errors := row.RequestCount - row.SuccessCount
		if errors <= 0 {
			continue
		}
		distribution := byStatus[row.StatusCode]
		if distribution == nil {
			distribution = &ErrorStatusDistribution{StatusCode: row.StatusCode}
			byStatus[row.StatusCode] = distribution
		}
		distribution.Total += errors
		if row.BusinessLimited {
			distribution.BusinessLimits += errors
		} else {
			distribution.SLAErrors += errors
		}
		if row.ErrorOwner == "provider" && row.StatusCode >= 400 && row.StatusCode != 429 && row.StatusCode != 529 {
			distribution.UpstreamErrors += errors
		}
	}
	result := ErrorSummary{}
	for _, distribution := range byStatus {
		result.Distribution = append(result.Distribution, *distribution)
	}
	sort.Slice(result.Distribution, func(i, j int) bool {
		if result.Distribution[i].Total == result.Distribution[j].Total {
			return result.Distribution[i].StatusCode < result.Distribution[j].StatusCode
		}
		return result.Distribution[i].Total > result.Distribution[j].Total
	})
	if len(result.Distribution) > 20 {
		result.Distribution = result.Distribution[:20]
	}
	if len(result.Distribution) > 0 {
		result.TopStatusCode = result.Distribution[0].StatusCode
	}
	return result
}

func buildSystemSummary(rows []model.OpsSystemMetric) SystemSummary {
	latest := map[string]model.OpsSystemMetric{}
	for _, row := range rows {
		if current, ok := latest[row.NodeName]; !ok || row.BucketTs > current.BucketTs {
			latest[row.NodeName] = row
		}
	}
	result := SystemSummary{Trend: rows}
	for _, row := range latest {
		result.LatestByNode = append(result.LatestByNode, row)
	}
	sort.Slice(result.LatestByNode, func(i, j int) bool { return result.LatestByNode[i].NodeName < result.LatestByNode[j].NodeName })
	return result
}

func buildJobHealth() ([]JobHealth, error) {
	tasks, err := model.ListRecentSystemTasksForOps(1000)
	if err != nil {
		return nil, err
	}
	byType := map[string]*JobHealth{}
	for _, task := range tasks {
		job := byType[task.Type]
		if job == nil {
			job = &JobHealth{Type: task.Type}
			byType[task.Type] = job
		}
		if task.UpdatedAt > job.LastRunAt {
			job.LastRunAt = task.UpdatedAt
		}
		if task.Status == model.SystemTaskStatusSucceeded && task.UpdatedAt > job.LastSuccessAt {
			job.LastSuccessAt = task.UpdatedAt
		}
		if task.Status == model.SystemTaskStatusFailed && task.UpdatedAt > job.LastErrorAt {
			job.LastErrorAt = task.UpdatedAt
			job.LastError = task.Error
		}
	}
	jobs := make([]JobHealth, 0, len(byType))
	now := time.Now().Unix()
	for _, job := range byType {
		job.Status = "healthy"
		if job.LastErrorAt > job.LastSuccessAt {
			job.Status = "failed"
		} else if (job.Type == model.SystemTaskTypeAsyncTaskPoll || job.Type == model.SystemTaskTypeMidjourneyPoll) && job.LastSuccessAt > 0 && now-job.LastSuccessAt > 15*60 {
			job.Status = "stale"
		}
		jobs = append(jobs, *job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Type < jobs[j].Type })
	return jobs, nil
}

func normalizeTaskTimestamp(value int64) int64 {
	if value > 1_000_000_000_000 {
		return value / 1000
	}
	return value
}

func buildTaskLifecycle(startTs, endTs int64, metrics []model.OpsMinuteMetric) (TaskLifecycleSummary, error) {
	rows, err := model.ListOpsTaskLifecycleRows(startTs, endTs)
	if err != nil {
		return TaskLifecycleSummary{}, err
	}
	result := TaskLifecycleSummary{}
	for _, metric := range metrics {
		if metric.EndpointType != "video_submit" {
			continue
		}
		result.Submitted += metric.RequestCount
		result.SubmitSucceeded += metric.SuccessCount
	}
	var queueTotal, queueCount, generationTotal, generationCount, endToEndTotal, endToEndCount int64
	for _, row := range rows {
		submit := normalizeTaskTimestamp(row.SubmitTime)
		if submit == 0 {
			submit = normalizeTaskTimestamp(row.CreatedAt)
		}
		start := normalizeTaskTimestamp(row.StartTime)
		finish := normalizeTaskTimestamp(row.FinishTime)
		if start > submit {
			queueTotal += (start - submit) * 1000
			queueCount++
		}
		if finish > start && start > 0 {
			generationTotal += (finish - start) * 1000
			generationCount++
		}
		if finish > submit {
			endToEndTotal += (finish - submit) * 1000
			endToEndCount++
		}
		if row.Status == model.TaskStatusSuccess {
			result.Succeeded++
			result.Terminal++
		}
		if row.Status == model.TaskStatusFailure {
			result.Failed++
			result.Terminal++
		}
	}
	if result.Submitted > 0 {
		result.SubmitSuccessRate = float64(result.SubmitSucceeded) / float64(result.Submitted)
	}
	if result.Terminal > 0 {
		result.GenerationSuccessRate = float64(result.Succeeded) / float64(result.Terminal)
	}
	if queueCount > 0 {
		result.AverageQueueMs = queueTotal / queueCount
	}
	if generationCount > 0 {
		result.AverageGenerationMs = generationTotal / generationCount
	}
	if endToEndCount > 0 {
		result.AverageEndToEndMs = endToEndTotal / endToEndCount
	}
	return result, nil
}

func buildModelTokenStats(startTs, endTs int64) ([]ModelTokenStats, error) {
	rows, err := model.QueryOpsModelTokenStats(startTs*1000, endTs*1000, "gpt")
	if err != nil {
		return nil, err
	}
	result := make([]ModelTokenStats, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.ModelName) == "" {
			continue
		}
		result = append(result, ModelTokenStats{
			ModelName:         row.ModelName,
			RequestCount:      row.RequestCount,
			OutputTokens:      row.OutputTokens,
			AverageTokensPS:   row.AverageTokensPS,
			AverageTTFTMs:     int64(math.Round(row.AverageTTFTMs)),
			AverageDurationMs: int64(math.Round(row.AverageDurationMs)),
			TTFTSamples:       row.TTFTSamples,
		})
	}
	return result, nil
}

func buildCachedModelTokenStats(endTs int64) ([]ModelTokenStats, error) {
	endMinute := endTs - endTs%60
	modelStatsCache.Lock()
	defer modelStatsCache.Unlock()
	if modelStatsCache.endMinute == endMinute && modelStatsCache.rows != nil {
		return modelStatsCache.rows, nil
	}
	rows, err := buildModelTokenStats(endMinute-30*24*3600, endMinute)
	if err != nil {
		return nil, err
	}
	modelStatsCache.endMinute = endMinute
	modelStatsCache.rows = rows
	return rows, nil
}

func buildDashboardOptions(rows []model.OpsMinuteMetric) DashboardOptions {
	models := map[string]struct{}{}
	groups := map[string]struct{}{}
	endpoints := map[string]struct{}{}
	nodes := map[string]struct{}{}
	for _, row := range rows {
		if row.ModelName != "" {
			models[row.ModelName] = struct{}{}
		}
		if row.Group != "" {
			groups[row.Group] = struct{}{}
		}
		if row.EndpointType != "" {
			endpoints[row.EndpointType] = struct{}{}
		}
		if row.NodeName != "" {
			nodes[row.NodeName] = struct{}{}
		}
	}
	return DashboardOptions{
		Models:        sortedSetValues(models),
		Groups:        sortedSetValues(groups),
		EndpointTypes: sortedSetValues(endpoints),
		Nodes:         sortedSetValues(nodes),
	}
}

func sortedSetValues(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func buildDiagnostics(snapshot DashboardSnapshot, settings ops_monitor_setting.Setting) []Diagnostic {
	diagnostics := make([]Diagnostic, 0)
	if snapshot.Overview.RequestCount > 0 {
		if snapshot.Overview.SLA < settings.SLAThreshold {
			diagnostics = append(diagnostics, Diagnostic{Severity: "critical", Metric: "sla", Message: "SLA is below the configured threshold", Value: snapshot.Overview.SLA, Threshold: settings.SLAThreshold})
		} else if snapshot.Overview.SLA < math.Min(1, settings.SLAThreshold+0.001) {
			diagnostics = append(diagnostics, Diagnostic{Severity: "warning", Metric: "sla", Message: "SLA is near the configured threshold", Value: snapshot.Overview.SLA, Threshold: settings.SLAThreshold})
		}
	}
	if snapshot.Latency.Duration.Samples > 0 {
		severity := lowerIsBetterDiagnosticSeverity(float64(snapshot.Latency.Duration.P99), float64(settings.RequestP99ThresholdMs))
		if severity != "" {
			diagnostics = append(diagnostics, Diagnostic{Severity: severity, Metric: "request_p99", Message: "Request P99 latency is near or above the configured threshold", Value: float64(snapshot.Latency.Duration.P99), Threshold: float64(settings.RequestP99ThresholdMs)})
		}
	}
	if snapshot.Latency.TTFT.Samples > 0 {
		severity := lowerIsBetterDiagnosticSeverity(float64(snapshot.Latency.TTFT.P99), float64(settings.TTFTP99ThresholdMs))
		if severity != "" {
			diagnostics = append(diagnostics, Diagnostic{Severity: severity, Metric: "ttft_p99", Message: "TTFT P99 is near or above the configured threshold", Value: float64(snapshot.Latency.TTFT.P99), Threshold: float64(settings.TTFTP99ThresholdMs)})
		}
	}
	if snapshot.Overview.RequestCount > 0 {
		if severity := lowerIsBetterDiagnosticSeverity(snapshot.Overview.RequestErrorRate, settings.RequestErrorRateThreshold); severity != "" {
			diagnostics = append(diagnostics, Diagnostic{Severity: severity, Metric: "request_error_rate", Message: "Request error rate is near or above the configured threshold", Value: snapshot.Overview.RequestErrorRate, Threshold: settings.RequestErrorRateThreshold})
		}
		if severity := lowerIsBetterDiagnosticSeverity(snapshot.Overview.UpstreamErrorRate, settings.UpstreamErrorRateThreshold); severity != "" {
			diagnostics = append(diagnostics, Diagnostic{Severity: severity, Metric: "upstream_error_rate", Message: "Upstream error rate is near or above the configured threshold", Value: snapshot.Overview.UpstreamErrorRate, Threshold: settings.UpstreamErrorRateThreshold})
		}
	}
	for _, system := range snapshot.System.LatestByNode {
		if !system.DBHealthy {
			diagnostics = append(diagnostics, Diagnostic{Severity: "critical", Metric: "database", Message: system.NodeName + ": database health check failed"})
		}
		if system.RedisEnabled && !system.RedisHealthy {
			diagnostics = append(diagnostics, Diagnostic{Severity: "critical", Metric: "redis", Message: system.NodeName + ": Redis health check failed"})
		}
		if severity, threshold := boundedUsageDiagnosticSeverity(system.CPUPercent, 80, 95); severity != "" {
			diagnostics = append(diagnostics, Diagnostic{Severity: severity, Metric: "cpu", Message: system.NodeName + ": CPU utilization is high", Value: system.CPUPercent, Threshold: threshold})
		}
		if severity, threshold := boundedUsageDiagnosticSeverity(system.MemoryPercent, 85, 95); severity != "" {
			diagnostics = append(diagnostics, Diagnostic{Severity: severity, Metric: "memory", Message: system.NodeName + ": memory utilization is high", Value: system.MemoryPercent, Threshold: threshold})
		}
		if severity, threshold := boundedUsageDiagnosticSeverity(system.DiskPercent, 80, 95); severity != "" {
			diagnostics = append(diagnostics, Diagnostic{Severity: severity, Metric: "disk", Message: system.NodeName + ": disk utilization is high", Value: system.DiskPercent, Threshold: threshold})
		}
	}
	for _, job := range snapshot.Jobs {
		if job.Status != "healthy" {
			diagnostics = append(diagnostics, Diagnostic{Severity: "warning", Metric: "job", Message: job.Type + ": background job is unhealthy"})
		}
	}
	if snapshot.Telemetry.DroppedRequests > 0 || snapshot.Telemetry.DroppedAttempts > 0 {
		diagnostics = append(diagnostics, Diagnostic{Severity: "critical", Metric: "telemetry", Message: "Operations telemetry events were dropped", Value: float64(snapshot.Telemetry.DroppedRequests + snapshot.Telemetry.DroppedAttempts)})
	}
	return diagnostics
}

func lowerIsBetterDiagnosticSeverity(value, criticalThreshold float64) string {
	if value >= criticalThreshold {
		return "critical"
	}
	if value+1e-12 >= criticalThreshold*0.8 {
		return "warning"
	}
	return ""
}

func boundedUsageDiagnosticSeverity(value, warningThreshold, criticalThreshold float64) (string, float64) {
	if value >= criticalThreshold {
		return "critical", criticalThreshold
	}
	if value >= warningThreshold {
		return "warning", warningThreshold
	}
	return "", 0
}
