/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { InformationCircleIcon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useTranslation } from 'react-i18next'

import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'

const METRIC_HELP_COPY = {
  effectiveRequestSuccessRate: {
    labelKey: 'Effective request success rate',
    definitionKey:
      'The share of effective requests that succeeded. Failures caused by local business limits are excluded from the denominator.',
    formula: 'S / (S + F) × 100%; F = R − S − B',
  },
  requestsAndTokens: {
    labelKey: 'Requests and tokens',
    definitionKey:
      'Observed downstream requests and normalized tokens in the selected window. Total tokens include input, output, cache-read, and cache-creation tokens.',
    formula: 'R; T = Tᵢₙ + Tₒᵤₜ + Tcache-read + Tcache-create',
  },
  realtimeThroughput: {
    labelKey: 'Current QPS / TPS',
    definitionKey:
      'Current rates use the latest 60 seconds. Peak rates are the highest one-minute request and token rates in the selected window.',
    formula: 'QPS = R₆₀ / 60; TPS = T₆₀ / 60',
  },
  errorRates: {
    labelKey: 'Request / upstream error rate',
    definitionKey:
      'Request error rate counts non-business-limit failures. Upstream error rate counts provider-owned HTTP failures except 429 and 529. Both use effective requests as the denominator.',
    formula: 'request = F / (S + F); upstream = U / (S + F)',
  },
  requestLatencyP99: {
    labelKey: 'Request latency P99',
    definitionKey:
      'The request duration that 99% of sampled gateway requests complete within, measured from gateway entry to downstream response completion.',
    formula: 'P99 = sort(D)[ceil(N × 0.99)]',
  },
  ttftP99: {
    labelKey: 'TTFT P99',
    definitionKey:
      'The time to first token that 99% of sampled streaming requests complete within, measured from gateway entry to the first response chunk.',
    formula: 'TTFT P99 = sort(TTFT)[ceil(N × 0.99)]',
  },
  averageAccountSwitches: {
    labelKey: 'Average account switches',
    definitionKey:
      'Average number of previously unseen upstream accounts selected after the first account within each downstream request.',
    formula: 'Σ switch_count(request) / R',
  },
  telemetryHealth: {
    labelKey: 'Telemetry health',
    definitionKey:
      'Health of the asynchronous request and attempt metric queues. Drops make it unhealthy; queued records without drops indicate backlog.',
    formula:
      'drops = dropped_requests + dropped_attempts; queue = request_queue + attempt_queue',
  },
  requestDuration: {
    labelKey: 'Duration',
    definitionKey:
      'Elapsed gateway time from request entry until the downstream response is completed.',
    formula: 'duration = completed_at − started_at',
  },
  ttft: {
    labelKey: 'TTFT',
    definitionKey:
      'Elapsed time from gateway request entry until the first streamed response chunk is received.',
    formula: 'TTFT = first_response_at − started_at',
  },
  latencyPercentile: {
    labelKey: 'Latency percentile',
    definitionKey:
      'The latency value at or below which the selected percentage of valid samples falls.',
    formula: 'Pp = sort(samples)[ceil(N × p)]',
  },
  modelRequests: {
    labelKey: 'Requests',
    definitionKey:
      'Successful requests for models whose names start with gpt during the fixed latest 30-day statistics window.',
    formula: 'count(successful gpt* requests)',
  },
  tokensPerSecond: {
    labelKey: 'Tokens/s',
    definitionKey:
      'Average per-request output token generation speed for successful requests in the fixed latest 30-day window.',
    formula: 'AVG(output_tokens × 1000 / duration_ms)',
  },
  averageTtft: {
    labelKey: 'Average TTFT',
    definitionKey:
      'Arithmetic mean time to first token across successful requests that contain a valid first-response timestamp.',
    formula: 'Σ TTFT_ms / TTFT_samples',
  },
  averageDuration: {
    labelKey: 'Average duration',
    definitionKey:
      'Arithmetic mean end-to-end gateway duration across the included successful model requests.',
    formula: 'Σ duration_ms / request_count',
  },
  cpuUsage: {
    labelKey: 'CPU',
    definitionKey:
      'Container CPU consumed between two collection samples as a percentage of the CPU cores allocated by cgroup.',
    formula: 'ΔCPU_time / (Δwall_time × allocated_cores) × 100%',
  },
  memoryUsage: {
    labelKey: 'Memory',
    definitionKey:
      'Current container memory usage as a percentage of the cgroup memory limit.',
    formula: 'memory.current / memory.limit × 100%',
  },
  diskUsage: {
    labelKey: 'Disk',
    definitionKey:
      'Used percentage of the filesystem that stores the application working data.',
    formula: 'used_bytes / total_bytes × 100%',
  },
  networkThroughput: {
    labelKey: 'Network',
    definitionKey:
      'Container or host receive and transmit byte rates calculated from cumulative network counters between collection samples.',
    formula: 'B/s = Δbytes / Δseconds',
  },
  databasePool: {
    labelKey: 'DB',
    definitionKey:
      'Database reachability from a two-second ping plus the current in-use, open, and configured maximum connection counts.',
    formula: 'health = Ping(2s); pool = in_use / open / max_open',
  },
  redisPool: {
    labelKey: 'Redis',
    definitionKey:
      'Redis reachability from a two-second ping plus active, total, and configured maximum pool connections.',
    formula: 'active = total − idle; pool = active / total / max',
  },
  goroutines: {
    labelKey: 'Goroutines',
    definitionKey:
      'Number of goroutines that exist in the current Go process at collection time.',
    formula: 'runtime.NumGoroutine()',
  },
  queueDepth: {
    labelKey: 'Queue',
    definitionKey:
      'Total number of requests currently waiting in all configured concurrency queues on the node.',
    formula: 'Σ waiting(channel, key)',
  },
  compositeHealth: {
    labelKey: 'Composite health score',
    definitionKey:
      'Overall 0–100 health score combining request behavior and infrastructure. With no observed requests, the score is idle at 100.',
    formula: 'round(business × 70% + infrastructure × 30%)',
  },
  businessScore: {
    labelKey: 'Business',
    definitionKey:
      'Business health subscore combining the error-rate score and the time-to-first-token score with equal weights.',
    formula: 'error_score × 50% + TTFT_score × 50%',
  },
  errorScore: {
    labelKey: 'Error score',
    definitionKey:
      'Score derived from the worse of request and upstream error rates: 100 at or below 1%, 0 at or above 10%, linear in between.',
    formula: 'linear_desc(max(request_error, upstream_error), 1%, 10%)',
  },
  ttftScore: {
    labelKey: 'TTFT score',
    definitionKey:
      'Score derived from TTFT P99: 100 at or below 1000 ms, 0 at or above 3000 ms, linear in between.',
    formula: 'linear_desc(TTFT_P99, 1000 ms, 3000 ms)',
  },
  storageScore: {
    labelKey: 'Storage',
    definitionKey:
      'Storage health score across nodes: 0 if a database is down, otherwise at most 50 if enabled Redis is down, otherwise 100.',
    formula: 'DB down → 0; Redis down → 50; otherwise 100',
  },
  computeScore: {
    labelKey: 'Compute',
    definitionKey:
      'Worst node compute score. Each node averages CPU and memory scores using their healthy and critical utilization boundaries.',
    formula: 'min_nodes((CPU_score[80%,100%] + memory_score[85%,100%]) / 2)',
  },
  jobsScore: {
    labelKey: 'Jobs',
    definitionKey:
      'Percentage of known background job types whose latest heartbeat state is healthy.',
    formula: '(1 − unhealthy_jobs / all_jobs) × 100',
  },
  jobHealthStatus: {
    labelKey: 'Status',
    definitionKey:
      'Current background job health. A job is failed when its latest error is newer than its latest success; polling jobs are stale after 15 minutes without a successful run.',
    formula:
      'failed: last_error > last_success; stale: polling job idle > 15 min; otherwise healthy',
  },
  jobLastSuccess: {
    labelKey: 'Last success',
    definitionKey:
      'Completion time of the most recent successful system task for this background job type.',
    formula: 'max(updated_at where status = succeeded)',
  },
  jobLastError: {
    labelKey: 'Last error',
    definitionKey:
      'Error message and completion time from the most recent failed system task for this background job type.',
    formula: 'error at max(updated_at where status = failed)',
  },
  systemResourceTrend: {
    labelKey: 'System resource trend',
    definitionKey:
      'CPU, memory, and disk percentages averaged across reporting nodes for each collection bucket.',
    formula: 'AVG_nodes(metric_percent) per bucket',
  },
  concurrencyInUse: {
    labelKey: 'In use',
    definitionKey:
      'Number of unexpired concurrency leases currently held by requests in the displayed scope.',
    formula: 'count(active leases)',
  },
  concurrencyCapacity: {
    labelKey: 'Capacity',
    definitionKey:
      'Configured enabled concurrency limit. Per-key limits are summed unless an all-keys limit is configured.',
    formula: 'all_keys_limit or Σ enabled key limits',
  },
  concurrencyWaiting: {
    labelKey: 'Waiting',
    definitionKey:
      'Number of requests currently queued while waiting to acquire a concurrency lease.',
    formula: 'count(active waiters)',
  },
  concurrencyLoad: {
    labelKey: 'Load',
    definitionKey:
      'Current concurrency occupancy relative to configured capacity. It is unavailable when no capacity is configured.',
    formula: 'in_use / capacity × 100%',
  },
  concurrencyAvailability: {
    labelKey: 'Availability',
    definitionKey:
      'A channel is available only when enabled, outside runtime cooldowns, and below capacity. Aggregates show available channels over total channels.',
    formula:
      'channel: enabled ∧ no cooldown ∧ in_use < capacity; aggregate: available / total',
  },
  activeUserConcurrency: {
    labelKey: 'Active user concurrency',
    definitionKey:
      'Current unexpired concurrency leases grouped by downstream user; users with zero active leases are omitted.',
    formula: 'count(active user leases) by user_id',
  },
  submitSuccessRate: {
    labelKey: 'Submit success rate',
    definitionKey:
      'Share of video submission endpoint requests that completed successfully in the selected request window.',
    formula:
      'successful video_submit requests / all video_submit requests × 100%',
  },
  generationSuccessRate: {
    labelKey: 'Generation success rate',
    definitionKey:
      'Share of terminal asynchronous tasks whose final status is success.',
    formula: 'SUCCESS / (SUCCESS + FAILURE) × 100%',
  },
  averageQueueTime: {
    labelKey: 'Average queue time',
    definitionKey:
      'Average valid interval from task submission to generation start; created time is used when submit time is missing.',
    formula: 'AVG(start_time − submit_time)',
  },
  averageGenerationTime: {
    labelKey: 'Average generation time',
    definitionKey:
      'Average valid interval from generation start to task completion for terminal tasks.',
    formula: 'AVG(finish_time − start_time)',
  },
  averageEndToEndTime: {
    labelKey: 'Average end-to-end time',
    definitionKey:
      'Average valid interval from task submission to task completion.',
    formula: 'AVG(finish_time − submit_time)',
  },
  taskFailures: {
    labelKey: 'Failures',
    definitionKey:
      'Number of asynchronous tasks whose terminal status is FAILURE in the selected task time range.',
    formula: 'count(status = FAILURE)',
  },
  errorDistribution: {
    labelKey: 'Error status distribution',
    definitionKey:
      'The 20 effective HTTP status codes with the most failed requests in the selected window.',
    formula:
      'top 20 by count(request_count − success_count), grouped by effective status',
  },
  effectiveStatus: {
    labelKey: 'Status',
    definitionKey:
      'Effective HTTP status used for operational classification; an upstream status is preferred when one was recorded.',
    formula: 'upstream_status_code or downstream_status_code',
  },
  concurrencyWait: {
    labelKey: 'Concurrency wait',
    definitionKey:
      'Time an upstream attempt spent waiting to acquire its concurrency lease before execution.',
    formula: 'lease_acquired_at − wait_started_at',
  },
  switchedAttempt: {
    labelKey: 'Switched',
    definitionKey:
      'Whether this attempt selected an upstream channel or key identity not previously used by the same downstream request.',
    formula: 'identity ∉ previously_seen_identities',
  },
} as const

export type MetricHelpId = keyof typeof METRIC_HELP_COPY

export function MetricHelp({
  metric,
  label,
  className,
}: {
  metric: MetricHelpId
  label?: string
  className?: string
}) {
  const { t } = useTranslation()
  const copy = METRIC_HELP_COPY[metric]
  const visibleLabel = label ?? t(copy.labelKey)
  const trigger = (
    <button
      type='button'
      className={cn(
        'inline-flex cursor-help items-center gap-1 text-left font-inherit text-inherit underline decoration-dotted underline-offset-4',
        className
      )}
      aria-label={t('Explain metric: {{metric}}', { metric: visibleLabel })}
    >
      <span>{visibleLabel}</span>
      <HugeiconsIcon
        icon={InformationCircleIcon}
        strokeWidth={2}
        className='size-3.5 shrink-0 opacity-70'
        aria-hidden='true'
      />
    </button>
  )

  return (
    <Tooltip>
      <TooltipTrigger render={trigger} />
      <TooltipContent
        side='top'
        align='start'
        className='block max-w-sm space-y-2 py-2.5 leading-relaxed whitespace-normal'
      >
        <p>
          <span className='font-semibold'>{t('Definition')}:</span>{' '}
          {t(copy.definitionKey)}
        </p>
        <p>
          <span className='font-semibold'>{t('Calculation')}:</span>{' '}
          <code className='font-mono text-[0.7rem]'>{copy.formula}</code>
        </p>
      </TooltipContent>
    </Tooltip>
  )
}
