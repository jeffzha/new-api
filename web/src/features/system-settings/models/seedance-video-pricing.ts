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
export const SEEDANCE_VIDEO_PRICING_OPTION_KEY =
  'seedance_video_pricing.prices_cny'

export const SEEDANCE_STANDARD_MODEL = 'doubao-seedance-2-0-260128'
export const SEEDANCE_FAST_MODEL = 'doubao-seedance-2-0-fast-260128'
export const MAX_SEEDANCE_VIDEO_PRICE_CNY = 1_000_000

export type SeedanceVideoPrice = {
  without_video: number
  with_video: number
}

export type SeedanceVideoPrices = Record<
  string,
  Record<string, SeedanceVideoPrice>
>

export type SeedancePricingFormValues = {
  rows: Array<SeedanceVideoPrice>
}

export const SEEDANCE_PRICE_ROWS = [
  {
    model: SEEDANCE_STANDARD_MODEL,
    resolution: '720p',
    modelLabelKey: 'Seedance 2.0 Standard',
  },
  {
    model: SEEDANCE_STANDARD_MODEL,
    resolution: '1080p',
    modelLabelKey: 'Seedance 2.0 Standard',
  },
  {
    model: SEEDANCE_STANDARD_MODEL,
    resolution: '4k',
    modelLabelKey: 'Seedance 2.0 Standard',
  },
  {
    model: SEEDANCE_FAST_MODEL,
    resolution: 'default',
    modelLabelKey: 'Seedance 2.0 Fast',
  },
] as const

export const DEFAULT_SEEDANCE_VIDEO_PRICES_CNY: SeedanceVideoPrices = {
  [SEEDANCE_STANDARD_MODEL]: {
    '720p': {
      without_video: 46,
      with_video: 28,
    },
    '1080p': {
      without_video: 51,
      with_video: 31,
    },
    '4k': {
      without_video: 26,
      with_video: 16,
    },
  },
  [SEEDANCE_FAST_MODEL]: {
    default: {
      without_video: 37,
      with_video: 22,
    },
  },
}

export const DEFAULT_SEEDANCE_VIDEO_PRICES_CNY_JSON = JSON.stringify(
  DEFAULT_SEEDANCE_VIDEO_PRICES_CNY
)

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isValidPrice(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0
}

function clonePrices(prices: SeedanceVideoPrices): SeedanceVideoPrices {
  const clone: SeedanceVideoPrices = {}

  for (const [model, resolutions] of Object.entries(prices)) {
    clone[model] = {}
    for (const [resolution, price] of Object.entries(resolutions)) {
      clone[model][resolution] = { ...price }
    }
  }

  return clone
}

export function parseSeedanceVideoPrices(raw: string): SeedanceVideoPrices {
  const prices = clonePrices(DEFAULT_SEEDANCE_VIDEO_PRICES_CNY)

  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return prices
  }

  if (!isRecord(parsed)) return prices

  for (const { model, resolution } of SEEDANCE_PRICE_ROWS) {
    const resolutions = parsed[model]
    if (!isRecord(resolutions)) continue

    const candidate = resolutions[resolution]
    if (!isRecord(candidate)) continue

    const withoutVideo = candidate.without_video
    const withVideo = candidate.with_video
    if (!isValidPrice(withoutVideo) || !isValidPrice(withVideo)) continue

    prices[model][resolution] = {
      without_video: withoutVideo,
      with_video: withVideo,
    }
  }

  return prices
}

export function buildSeedancePricingFormValues(
  prices: SeedanceVideoPrices
): SeedancePricingFormValues {
  return {
    rows: SEEDANCE_PRICE_ROWS.map(({ model, resolution }) => ({
      ...DEFAULT_SEEDANCE_VIDEO_PRICES_CNY[model][resolution],
      ...prices[model]?.[resolution],
    })),
  }
}

export function applySeedancePricingFormValues(
  values: SeedancePricingFormValues
): SeedanceVideoPrices {
  const prices = clonePrices(DEFAULT_SEEDANCE_VIDEO_PRICES_CNY)

  SEEDANCE_PRICE_ROWS.forEach(({ model, resolution }, index) => {
    prices[model] ??= {}
    prices[model][resolution] = { ...values.rows[index] }
  })

  return prices
}
