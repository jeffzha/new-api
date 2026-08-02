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
export type OpsApiResponse<T> = {
  success: boolean
  message: string
  data?: T
}

export type OpsFilter = {
  start?: number
  end?: number
  model?: string
  group?: string
  channel_id?: number
  endpoint_type?: string
  node_name?: string
}

export type OpsOverview = {
  request_count: number
  success_count: number
  error_count: number
  sla_error_count: number
  business_limit_count: number
  upstream_error_count: number
  sla: number
  request_error_rate: number
  upstream_error_rate: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_creation_tokens: number
  total_tokens: number
  average_qps: number
  average_tps: number
  average_switches: number
}

export type OpsRealtime = {
  current_qps: number
  current_tps: number
  peak_qps: number
  peak_tps: number
  approximate: boolean
}

export type ThroughputPoint = {
  ts: number
  request_count: number
  token_count: number
  qps: number
  tps: number
}

export type SwitchPoint = {
  ts: number
  request_count: number
  switch_count: number
  average: number
}

export type ErrorTrendPoint = {
  ts: number
  sla_errors: number
  upstream_errors: number
  business_limits: number
}

export type Percentiles = {
  p50: number
  p90: number
  p95: number
  p99: number
  avg: number
  max: number
  samples: number
}

export type LatencySummary = {
  duration: Percentiles
  ttft: Percentiles
  histogram: Array<{ label: string; count: number }>
  approximate: boolean
}

export type OpsSystemMetric = {
  bucket_ts: number
  node_name: string
  cpu_percent: number
  memory_percent: number
  disk_percent: number
  network_receive_bps: number
  network_transmit_bps: number
  network_received_bytes: number
  network_sent_bytes: number
  goroutines: number
  db_healthy: boolean
  db_open_connections: number
  db_max_open_connections: number
  db_in_use_connections: number
  db_idle_connections: number
  db_wait_count: number
  db_wait_duration_ms: number
  redis_enabled: boolean
  redis_healthy: boolean
  redis_total_connections: number
  redis_idle_connections: number
  redis_max_connections: number
  queue_depth: number
}

export type OpsJob = {
  type: string
  status: string
  last_success_at: number
  last_error_at: number
  last_run_at: number
  last_error?: string
}

export type OpsTaskSummary = {
  submitted: number
  submit_succeeded: number
  terminal: number
  succeeded: number
  failed: number
  submit_success_rate: number
  generation_success_rate: number
  average_queue_ms: number
  average_generation_ms: number
  average_end_to_end_ms: number
}

export type ModelTokenStats = {
  model_name: string
  request_count: number
  output_tokens: number
  average_tokens_per_second: number
  average_ttft_ms: number
  average_duration_ms: number
  ttft_samples: number
}

export type ConcurrencyChannel = {
  channel_id: number
  channel_name: string
  channel_type: number
  multi_key_size: number
  platform: string
  groups: string[]
  status: number
  in_use: number
  capacity: number
  waiting: number
  load_percent: number
  limit_configured: boolean
  limit_enabled: boolean
  available: boolean
  unavailable_reason?: string
}

export type ConcurrencyAggregate = {
  name: string
  in_use: number
  capacity: number
  waiting: number
  load_percent: number
  available: number
  total: number
}

export type ConcurrencySnapshot = {
  enforcement_enabled: boolean
  redis_backed: boolean
  channels: ConcurrencyChannel[]
  platforms: ConcurrencyAggregate[]
  groups: ConcurrencyAggregate[]
  users: Array<{ user_id: number; in_use: number }>
  collected_at: number
}

export type OpsErrorEvent = {
  request_id: string
  occurred_at_ms: number
  method: string
  path: string
  endpoint_type: string
  user_id: number
  token_id: number
  channel_id: number
  model_name: string
  group: string
  status_code: number
  upstream_status_code: number
  error_owner: string
  business_limited: boolean
  error_code: string
  error_summary: string
  duration_ms: number
}

export type OpsUpstreamAttempt = {
  attempt_index: number
  channel_id: number
  channel_type: number
  channel_key_index: number
  status_code: number
  success: boolean
  switched_channel: boolean
  duration_ms: number
  error_code: string
  concurrency_wait_ms: number
  concurrency_tracked: boolean
}

export type OpsRequestDetail = {
  request: OpsErrorEvent
  attempts: OpsUpstreamAttempt[]
}

export type Diagnostic = {
  severity: string
  metric: string
  message: string
  value: number
  threshold: number
}

export type OpsSnapshot = {
  overview: OpsOverview
  realtime: OpsRealtime
  throughput_trend: ThroughputPoint[]
  switch_trend: SwitchPoint[]
  error_trend: ErrorTrendPoint[]
  latency: LatencySummary
  errors: {
    distribution: Array<{
      status_code: number
      total: number
      sla_errors: number
      business_limits: number
      upstream_errors: number
    }>
    top_status_code: number
  }
  health: {
    state: string
    score: number
    business_score: number
    infra_score: number
    error_score: number
    ttft_score: number
    storage_score: number
    compute_score: number
    job_score: number
  }
  system: { latest_by_node: OpsSystemMetric[]; trend: OpsSystemMetric[] }
  jobs: OpsJob[]
  tasks: OpsTaskSummary
  model_token_stats: ModelTokenStats[]
  concurrency: ConcurrencySnapshot
  recent_errors: OpsErrorEvent[]
  telemetry: {
    request_queue_depth: number
    attempt_queue_depth: number
    dropped_requests: number
    dropped_attempts: number
  }
  options: {
    models: string[]
    groups: string[]
    endpoint_types: string[]
    nodes: string[]
  }
  diagnostics: Diagnostic[]
}

export type OpsSettings = {
  enabled: boolean
  raw_retention_days: number
  aggregate_retention_days: number
  system_retention_days: number
  system_collection_interval_secs: number
  concurrency_enforcement_enabled: boolean
  concurrency_fail_open: boolean
  default_lease_seconds: number
  rate_limit_cooldown_seconds: number
  overload_cooldown_seconds: number
  temporary_unschedulable_seconds: number
  sla_threshold: number
  ttft_p99_threshold_ms: number
  request_error_rate_threshold: number
  upstream_error_rate_threshold: number
}

export type OpsConcurrencyLimit = {
  id?: number
  channel_id: number
  key_index: number
  enabled: boolean
  max_concurrency: number
  queue_size: number
  queue_timeout_ms: number
  lease_seconds: number
}
