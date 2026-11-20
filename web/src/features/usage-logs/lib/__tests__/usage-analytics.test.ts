/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { afterEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { usageLogSchema } from '../../data/schema'
import { fetchUsageAnalytics, summarizeUsage } from '../usage-analytics'

const record = (id: number, extra = {}) =>
  usageLogSchema.parse({
    id,
    user_id: 1,
    created_at: 7200,
    type: 2,
    content: '',
    model_name: 'model-a',
    group: 'default',
    prompt_tokens: 100,
    completion_tokens: 20,
    quota: 30,
    use_time: 2,
    ...extra,
  })
afterEach(() => vi.restoreAllMocks())

describe('usage analytics', () => {
  it('sums recorded standard costs while preserving unknown historical prices', () => {
    const result = summarizeUsage([
      record(1, { other: JSON.stringify({ agency_standard_quota: 40 }) }),
      record(2, { other: JSON.stringify({ agency_standard_quota: 0 }) }),
      record(3, { model_name: 'unknown-cost' }),
      record(4, {
        model_name: 'unknown-cost',
        other: JSON.stringify({ agency_standard_quota: 80 }),
      }),
      record(5, {
        model_name: 'invalid-cost',
        other: JSON.stringify({ agency_standard_quota: -10 }),
      }),
    ])
    expect(result.models).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          name: 'model-a',
          requests: 2,
          standardQuota: 40,
        }),
        expect.objectContaining({ name: 'unknown-cost', standardQuota: null }),
        expect.objectContaining({ name: 'invalid-cost', standardQuota: null }),
      ])
    )
    expect(result.groups[0].standardQuota).toBeNull()
  })
  it('daily buckets follow the same local calendar dates as the date filter', () => {
    const first = new Date(2026, 8, 28, 0, 15).getTime() / 1000
    const last = new Date(2026, 8, 28, 23, 45).getTime() / 1000
    const result = summarizeUsage(
      [record(1, { created_at: first }), record(2, { created_at: last })],
      86400
    )
    expect(result.series).toHaveLength(1)
    expect(result.series[0]).toMatchObject({
      timestamp: new Date(2026, 8, 28).getTime() / 1000,
      tokens: 240,
    })
  })
  it('keeps input and cache semantics consistent across OpenAI and Anthropic records', () => {
    const result = summarizeUsage([
      record(1, {
        other: JSON.stringify({
          cache_tokens: 40,
          request_path: '/v1/responses',
        }),
      }),
      record(2, {
        other: JSON.stringify({
          usage_semantic: 'anthropic',
          cache_tokens: 40,
          cache_creation_tokens: 10,
        }),
      }),
      record(3, { type: 1, quota: 9000 }),
    ])
    expect(result).toMatchObject({
      requests: 2,
      input: 250,
      output: 40,
      cache: 80,
      tokens: 290,
      quota: 60,
      averageDuration: 2,
    })
    expect(result.series).toEqual([
      { timestamp: 7200, input: 250, output: 40, cache: 80, tokens: 290 },
    ])
    expect(result.endpoints.map((row) => row.name)).toEqual([
      '/v1/responses',
      'Unknown',
    ])
  })
  it('preserves all applied filters while fetching every page and deduplicating records', async () => {
    const get = vi
      .spyOn(api, 'get')
      .mockResolvedValueOnce({
        data: {
          success: true,
          data: { total: 3, items: [record(1), record(2)] },
        },
      })
      .mockResolvedValueOnce({
        data: {
          success: true,
          data: { total: 3, items: [record(2), record(3)] },
        },
      })
    const result = await fetchUsageAnalytics(
      {
        model_name: 'model-a',
        group: 'default',
        token_name: 'prod',
        start_timestamp: 100,
        end_timestamp: 8000,
      },
      false
    )
    expect(result.rows.map((row) => row.id)).toEqual([1, 2, 3])
    expect(result.truncated).toBe(false)
    for (const [url] of get.mock.calls) {
      const parsed = new URL(String(url), 'http://localhost')
      expect(parsed.pathname).toBe('/api/log/self')
      expect(Object.fromEntries(parsed.searchParams)).toMatchObject({
        model_name: 'model-a',
        group: 'default',
        token_name: 'prod',
        page_size: '100',
        start_timestamp: '100',
        end_timestamp: '8000',
      })
    }
  })
  it('stops duplicate pages and clearly reports incomplete analytics', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: { total: 2, items: [record(1)] } },
    })
    expect(await fetchUsageAnalytics({}, true)).toMatchObject({
      total: 2,
      truncated: true,
    })
    expect(get).toHaveBeenCalledTimes(2)
    expect(get).toHaveBeenCalledWith(
      expect.stringMatching(/^\/api\/log\/\?/),
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    )
  })
  it('does not issue requests after the caller cancels an obsolete filter', async () => {
    const get = vi.spyOn(api, 'get')
    const controller = new AbortController()
    controller.abort()
    await expect(
      fetchUsageAnalytics({}, false, controller.signal)
    ).rejects.toThrow()
    expect(get).not.toHaveBeenCalled()
  })
  it('publishes the first page before loading the remaining pages with bounded concurrency', async () => {
    const pending = new Map<number, (value: unknown) => void>()
    const pageRows = (page: number) =>
      Array.from({ length: 100 }, (_, index) =>
        record((page - 1) * 100 + index + 1)
      )
    const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
      const page = Number(
        new URL(url, 'http://localhost').searchParams.get('p')
      )
      if (page === 1) {
        return {
          data: { success: true, data: { total: 500, items: pageRows(1) } },
        }
      }
      return new Promise((resolve) => pending.set(page, resolve))
    })
    const progress = vi.fn()
    const result = fetchUsageAnalytics({}, true, undefined, progress)
    await vi.waitFor(() => expect(pending.size).toBe(3))
    expect(get).toHaveBeenCalledTimes(4)
    expect(progress).toHaveBeenCalledWith(
      expect.objectContaining({
        rows: pageRows(1),
        total: 500,
        truncated: true,
      })
    )
    pending.get(2)?.({
      data: { success: true, data: { total: 500, items: pageRows(2) } },
    })
    await vi.waitFor(() =>
      expect(progress).toHaveBeenLastCalledWith(
        expect.objectContaining({ rows: [...pageRows(1), ...pageRows(2)] })
      )
    )
    for (const page of [4, 3]) {
      pending.get(page)?.({
        data: { success: true, data: { total: 500, items: pageRows(page) } },
      })
    }
    await vi.waitFor(() => expect(pending.has(5)).toBe(true))
    pending.get(5)?.({
      data: { success: true, data: { total: 500, items: pageRows(5) } },
    })
    expect((await result).rows.map((row) => row.id)).toEqual(
      Array.from({ length: 500 }, (_, index) => index + 1)
    )
  })
  it('aborts an in-flight HTTP request when filters are canceled and publishes no obsolete data', async () => {
    let finish!: (value: unknown) => void
    const get = vi.spyOn(api, 'get').mockImplementation(
      async () =>
        new Promise((resolve) => {
          finish = resolve
        })
    )
    const controller = new AbortController()
    const progress = vi.fn()
    const result = fetchUsageAnalytics({}, false, controller.signal, progress)
    const rejected = expect(result).rejects.toThrow()
    controller.abort()
    expect(get.mock.calls[0][1]?.signal?.aborted).toBe(true)
    finish({ data: { success: true, data: { total: 1, items: [record(1)] } } })
    await rejected
    expect(progress).not.toHaveBeenCalled()
  })
  it('caps page requests as well as records when overlapping pages never reach the reported total', async () => {
    const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
      const page = Number(
        new URL(url, 'http://localhost').searchParams.get('p')
      )
      return {
        data: { success: true, data: { total: 5000, items: [record(page)] } },
      }
    })
    const result = await fetchUsageAnalytics({}, true)
    expect(get).toHaveBeenCalledTimes(20)
    expect(result).toMatchObject({ total: 5000, truncated: true })
  })
  it('rejects a failed page instead of presenting partial data as complete', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: false, message: 'Unavailable' },
    })
    await expect(fetchUsageAnalytics({}, false)).rejects.toThrow('Unavailable')
    expect(summarizeUsage([]).averageDuration).toBeNull()
  })
  it('cancels sibling page requests when a concurrent page fails', async () => {
    const get = vi.spyOn(api, 'get').mockImplementation(async (url, config) => {
      const page = Number(
        new URL(url, 'http://localhost').searchParams.get('p')
      )
      if (page === 1) {
        return {
          data: { success: true, data: { total: 400, items: [record(1)] } },
        }
      }
      if (page === 2) {
        return { data: { success: false, message: 'Page unavailable' } }
      }
      return new Promise((_, reject) => {
        config?.signal?.addEventListener?.(
          'abort',
          () => reject(new Error('Canceled')),
          { once: true }
        )
      })
    })
    const progress = vi.fn()
    await expect(
      fetchUsageAnalytics({}, true, undefined, progress)
    ).rejects.toThrow('Page unavailable')
    expect(get).toHaveBeenCalledTimes(4)
    expect(get.mock.calls[2][1]?.signal?.aborted).toBe(true)
    expect(get.mock.calls[3][1]?.signal?.aborted).toBe(true)
    expect(progress).toHaveBeenCalledTimes(1)
  })
})
