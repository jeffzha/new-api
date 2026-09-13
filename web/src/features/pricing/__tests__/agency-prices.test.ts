import { describe, expect, it } from 'vitest'

import {
  getDynamicDisplayGroupRatio,
  getDynamicPricingSummary,
} from '../lib/dynamic-price'
import { getDisplayGroupRatio } from '../lib/model-helpers'
import {
  formatFixedPrice,
  formatGroupPrice,
  formatPrice,
  formatRequestPrice,
} from '../lib/price'
import { scaleVideoTokenPricingTiers } from '../lib/provider-pricing'
import type { PricingModel } from '../types'

const customer: PricingModel & { sales_bps: number } = {
  id: 1,
  model_name: 'hy3',
  quota_type: 0,
  model_ratio: 2,
  completion_ratio: 3,
  enable_groups: ['default', 'vip'],
  group_ratio: { default: 2, vip: 0.5 },
  sales_bps: 9000,
}

describe('agency customer prices', () => {
  it('replaces ordinary group ratios for both summary and explicit group prices', () => {
    expect(getDisplayGroupRatio(customer)).toBe(0.9)
    expect(getDisplayGroupRatio(customer, 'default')).toBe(0.9)
    expect(getDisplayGroupRatio(customer, 'vip')).toBe(0.9)
    expect(formatPrice(customer, 'input', 'M')).toBe('$3.6')
    expect(
      formatGroupPrice(customer, 'default', 'output', 'M', false, 1, 1, {
        default: 2,
      })
    ).toBe('$10.8')
  })

  it('uses the same customer coefficient for per-call pricing and preserves free sales', () => {
    const request = { ...customer, quota_type: 1, model_price: 2 }
    expect(formatRequestPrice(request)).toBe('$1.8')
    expect(
      formatFixedPrice(request, 'default', false, 1, 1, { default: 2 })
    ).toBe('$1.8')
    expect(getDisplayGroupRatio({ ...customer, sales_bps: 0 })).toBe(0)
    expect(formatRequestPrice({ ...request, sales_bps: 0 })).toBe('$0')
  })

  it('uses customer prices for dynamic expression summaries and provider video tiers', () => {
    const tiered = {
      ...customer,
      billing_mode: 'tiered_expr',
      billing_expr: 'tier("base", p * 2 + c * 8)',
    }
    const summary = getDynamicPricingSummary(tiered, {
      tokenUnit: 'M',
      groupRatioMultiplier: getDynamicDisplayGroupRatio(tiered),
    })
    expect(summary?.primaryEntries.map((entry) => entry.formatted)).toEqual([
      '$1.8',
      '$7.2',
    ])
    expect(
      scaleVideoTokenPricingTiers(
        {
          kind: 'video_token_matrix',
          currency: 'CNY',
          unit: '1M_video_tokens',
          tiers: [{ resolution: '720p', without_video: 20, with_video: 10 }],
        },
        getDisplayGroupRatio(customer)
      )
    ).toEqual([{ resolution: '720p', without_video: 18, with_video: 9 }])
  })

  it('keeps ordinary unbound customer group pricing', () => {
    const ordinary = { ...customer, sales_bps: undefined }
    expect(getDisplayGroupRatio(ordinary)).toBe(0.5)
    expect(getDisplayGroupRatio(ordinary, 'default')).toBe(2)
    expect(formatPrice(ordinary, 'input', 'M')).toBe('$2')
  })
})
