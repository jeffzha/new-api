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
import type {
  PricingModel,
  VideoTokenMatrixPricing,
  VideoTokenPricingTier,
} from '../types'

const PRICE_FORMATTERS = new Map<string, Intl.NumberFormat>()

function isVideoTokenPricingTier(
  value: unknown
): value is VideoTokenPricingTier {
  if (!value || typeof value !== 'object') return false
  const tier = value as Record<string, unknown>
  return (
    typeof tier.resolution === 'string' &&
    tier.resolution.trim().length > 0 &&
    typeof tier.without_video === 'number' &&
    Number.isFinite(tier.without_video) &&
    tier.without_video >= 0 &&
    typeof tier.with_video === 'number' &&
    Number.isFinite(tier.with_video) &&
    tier.with_video >= 0
  )
}

export function getVideoTokenMatrixPricing(
  model: PricingModel
): VideoTokenMatrixPricing | null {
  const pricing: unknown = model.provider_pricing
  if (!pricing || typeof pricing !== 'object') return null

  const candidate = pricing as Record<string, unknown>
  if (
    candidate.kind !== 'video_token_matrix' ||
    candidate.currency !== 'CNY' ||
    candidate.unit !== '1M_video_tokens' ||
    !Array.isArray(candidate.tiers) ||
    candidate.tiers.length === 0 ||
    !candidate.tiers.every(isVideoTokenPricingTier)
  ) {
    return null
  }

  return pricing as VideoTokenMatrixPricing
}

export function scaleVideoTokenPricingTiers(
  pricing: VideoTokenMatrixPricing,
  groupRatio: number
): VideoTokenPricingTier[] {
  return pricing.tiers.map((tier) => ({
    ...tier,
    without_video: tier.without_video * groupRatio,
    with_video: tier.with_video * groupRatio,
  }))
}

export function getLowestVideoTokenMatrixPrice(
  pricing: VideoTokenMatrixPricing
): number | null {
  let lowest = Number.POSITIVE_INFINITY
  for (const tier of pricing.tiers) {
    for (const price of [tier.without_video, tier.with_video]) {
      if (Number.isFinite(price) && price >= 0 && price < lowest) {
        lowest = price
      }
    }
  }
  return lowest === Number.POSITIVE_INFINITY ? null : lowest
}

export function formatProviderPrice(value: number, currency: string): string {
  let formatter = PRICE_FORMATTERS.get(currency)
  if (!formatter) {
    formatter = new Intl.NumberFormat(undefined, {
      style: 'currency',
      currency,
      currencyDisplay: 'narrowSymbol',
      minimumFractionDigits: 0,
      maximumFractionDigits: 6,
    })
    PRICE_FORMATTERS.set(currency, formatter)
  }
  return formatter.format(value)
}
