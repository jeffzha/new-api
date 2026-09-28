/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { requireServerSuccess } from '@/lib/server-error-message'

import { getAllLogs, getUserLogs } from '../api'
import { usageLogSchema, type UsageLog } from '../data/schema'
import type { GetLogsParams } from '../types'
import { buildBillingCalculation } from './billing-calculation'
import { parseLogOther } from './format'

export const ANALYTICS_LOG_LIMIT = 2000

type UsageDistribution = {
  name: string
  tokens: number
  quota: number
  requests: number
  standardQuota: number | null
}

export async function fetchUsageAnalytics(
  params: GetLogsParams,
  isAdmin: boolean,
  signal?: AbortSignal
) {
  const rows: UsageLog[] = []
  const seen = new Set<number>()
  let total = 0
  for (let page = 1; rows.length < ANALYTICS_LOG_LIMIT; page++) {
    signal?.throwIfAborted()
    const response = requireServerSuccess(
      await (isAdmin ? getAllLogs : getUserLogs)({
        ...params,
        p: page,
        page_size: 100,
      })
    )
    signal?.throwIfAborted()
    total = response.data?.total ?? 0
    const items = response.data?.items ?? []
    const before = rows.length
    for (const item of items) {
      const row = usageLogSchema.parse(item)
      if (!seen.has(row.id) && rows.length < ANALYTICS_LOG_LIMIT) {
        rows.push(row)
        seen.add(row.id)
      }
    }
    if (rows.length >= total || !items.length || rows.length === before) break
  }
  return { rows, total, truncated: rows.length < total }
}

export function summarizeUsage(rows: UsageLog[], bucketSeconds = 3600) {
  const models = new Map<string, UsageDistribution>()
  const groups = new Map<string, UsageDistribution>()
  const endpoints = new Map<string, UsageDistribution>()
  const buckets = new Map<
    number,
    {
      timestamp: number
      input: number
      output: number
      cache: number
      tokens: number
    }
  >()
  let input = 0,
    output = 0,
    cache = 0,
    quota = 0,
    duration = 0,
    requests = 0
  for (const row of rows) {
    // Only consumption records describe billable inference, not top-ups or refunds.
    if (row.type !== 2) continue
    const other = parseLogOther(row.other) ?? {}
    const billing = buildBillingCalculation(row, other, 500000)
    const tokens = billing.totalInputTokens + row.completion_tokens
    input += billing.totalInputTokens
    output += row.completion_tokens
    cache += billing.cacheReadTokens
    quota += row.quota
    duration += row.use_time
    requests++
    for (const [map, name] of [
      [models, row.model_name],
      [groups, row.group],
      [endpoints, other.request_path],
    ] as const) {
      const key = name || 'Unknown'
      const entry = map.get(key) ?? {
        name: key,
        tokens: 0,
        quota: 0,
        requests: 0,
        standardQuota: 0,
      }
      // Missing historical prices are unknown, never zero or inferred from a current rate.
      const standard = other.agency_standard_quota
      entry.standardQuota =
        entry.standardQuota !== null &&
        typeof standard === 'number' &&
        Number.isFinite(standard) &&
        standard >= 0
          ? entry.standardQuota + standard
          : null
      entry.tokens += tokens
      entry.quota += row.quota
      entry.requests++
      map.set(key, entry)
    }
    const date = new Date(row.created_at * 1000)
    const timestamp =
      bucketSeconds === 86400
        ? Math.floor(date.setHours(0, 0, 0, 0) / 1000)
        : Math.floor(row.created_at / bucketSeconds) * bucketSeconds
    const bucket = buckets.get(timestamp) ?? {
      timestamp,
      input: 0,
      output: 0,
      cache: 0,
      tokens: 0,
    }
    bucket.input += billing.totalInputTokens
    bucket.output += row.completion_tokens
    bucket.cache += billing.cacheReadTokens
    bucket.tokens += tokens
    buckets.set(timestamp, bucket)
  }
  return {
    input,
    output,
    cache,
    quota,
    requests,
    tokens: input + output,
    averageDuration: requests ? duration / requests : null,
    models: [...models.values()],
    groups: [...groups.values()],
    endpoints: [...endpoints.values()],
    series: [...buckets.values()].sort((a, b) => a.timestamp - b.timestamp),
  }
}
