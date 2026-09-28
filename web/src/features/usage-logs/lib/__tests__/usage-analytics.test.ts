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
      data: { success: true, data: { total: 5000, items: [record(1)] } },
    })
    expect(await fetchUsageAnalytics({}, true)).toMatchObject({
      total: 5000,
      truncated: true,
    })
    expect(get).toHaveBeenCalledTimes(2)
    expect(get).toHaveBeenCalledWith(expect.stringMatching(/^\/api\/log\?/))
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
  it('rejects a failed page instead of presenting partial data as complete', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: false, message: 'Unavailable' },
    })
    await expect(fetchUsageAnalytics({}, false)).rejects.toThrow('Unavailable')
    expect(summarizeUsage([]).averageDuration).toBeNull()
  })
})
