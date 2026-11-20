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
const ANALYTICS_PAGE_SIZE = 100
const ANALYTICS_CONCURRENCY = 3

export type UsageAnalyticsResult = {
  rows: UsageLog[]
  total: number
  truncated: boolean
}

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
  signal?: AbortSignal,
  onProgress?: (result: UsageAnalyticsResult) => void
): Promise<UsageAnalyticsResult> {
  const rows: UsageLog[] = []
  const seen = new Set<number>()
  let total = 0
  const controller = new AbortController()
  const cancel = () => controller.abort(signal?.reason)
  signal?.throwIfAborted()
  signal?.addEventListener('abort', cancel, { once: true })
  const maxPages = Math.ceil(ANALYTICS_LOG_LIMIT / ANALYTICS_PAGE_SIZE)
  try {
    for (
      let page = 1;
      page <= maxPages && rows.length < ANALYTICS_LOG_LIMIT;
    ) {
      controller.signal.throwIfAborted()
      // Publish the first page promptly, then overlap only a small batch of reads.
      const batchSize =
        page === 1
          ? 1
          : Math.min(
              ANALYTICS_CONCURRENCY,
              Math.ceil((total - rows.length) / ANALYTICS_PAGE_SIZE),
              maxPages - page + 1
            )
      const completed = new Map<number, UsageLog[]>()
      const responses = await Promise.all(
        Array.from({ length: batchSize }, async (_, index) => {
          const response = requireServerSuccess(
            await (isAdmin ? getAllLogs : getUserLogs)(
              {
                ...params,
                p: page + index,
                page_size: ANALYTICS_PAGE_SIZE,
              },
              controller.signal
            )
          )
          controller.signal.throwIfAborted()
          total = Math.max(total, response.data?.total ?? 0)
          const items = (response.data?.items ?? []).map((item) =>
            usageLogSchema.parse(item)
          )
          completed.set(index, items)
          const published = new Set(seen)
          const available = [...completed.entries()]
            .sort(([a], [b]) => a - b)
            .flatMap(([, batch]) => batch)
            .filter((row) => {
              if (published.has(row.id)) return false
              published.add(row.id)
              return true
            })
          const partial = [...rows, ...available].slice(0, ANALYTICS_LOG_LIMIT)
          onProgress?.({
            rows: partial,
            total,
            truncated: partial.length < total,
          })
          return items
        })
      )
      controller.signal.throwIfAborted()
      const before = rows.length
      for (const batch of responses) {
        for (const row of batch) {
          if (!seen.has(row.id) && rows.length < ANALYTICS_LOG_LIMIT) {
            rows.push(row)
            seen.add(row.id)
          }
        }
      }
      if (rows.length >= total || rows.length === before) break
      page += batchSize
    }
    return { rows, total, truncated: rows.length < total }
  } finally {
    signal?.removeEventListener('abort', cancel)
    controller.abort()
  }
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
