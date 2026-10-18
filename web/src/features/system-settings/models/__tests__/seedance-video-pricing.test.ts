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
import { describe, expect, test } from 'vitest'

import {
  applySeedancePricingFormValues,
  buildSeedancePricingFormValues,
  parseSeedanceVideoPrices,
  parseSeedanceRequestPrices,
  SEEDANCE_25_MODEL,
  SEEDANCE_FAST_MODEL,
  SEEDANCE_MINI_MODEL,
  SEEDANCE_STANDARD_MODEL,
} from '../seedance-video-pricing'

describe('Seedance video pricing upgrades', () => {
  test('keeps Mini per-request list prices separate from legacy token rates', () => {
    const legacy = JSON.stringify({
      [SEEDANCE_MINI_MODEL]: { '720p': { without_video: 9.2, with_video: 5.6 } },
    })
    expect(parseSeedanceVideoPrices(legacy)[SEEDANCE_MINI_MODEL]).toBeUndefined()
    expect(parseSeedanceRequestPrices('{}').rows).toEqual([
      { without_video: 23, with_video: 14 },
    ])
    expect(parseSeedanceRequestPrices(JSON.stringify({
      [SEEDANCE_MINI_MODEL]: { without_video: 24, with_video: 15 },
    })).rows).toEqual([{ without_video: 24, with_video: 15 }])
  })

  test('preserves customized alias prices when saving the token matrix', () => {
    const prices = parseSeedanceVideoPrices(
      JSON.stringify({
        'doubao-seedance-2.0-mini': {
          '720p': { without_video: 24, with_video: 15 },
        },
        'doubao-seedance-2.0': {
          '720p': { without_video: 48, with_video: 30 },
        },
        'doubao-seedance-2.5': {
          '1080p': { without_video: 78, with_video: 47 },
        },
      })
    )

    const saved = applySeedancePricingFormValues(
      buildSeedancePricingFormValues(prices),
      prices
    )

    expect(Object.keys(saved)).toHaveLength(5)
    expect(saved['doubao-seedance-2.0']['720p']).toEqual({
      without_video: 48,
      with_video: 30,
    })
    expect(saved['doubao-seedance-2.5']['1080p']).toEqual({
      without_video: 78,
      with_video: 47,
    })
    expect(saved).toEqual(prices)
  })

  test('preserves customized 2.0 prices and fills 2.5 list-price defaults', () => {
    const legacyPrices = JSON.stringify({
      [SEEDANCE_STANDARD_MODEL]: {
        '720p': { without_video: 49.5, with_video: 29.5 },
      },
      [SEEDANCE_FAST_MODEL]: {
        default: { without_video: 38.5, with_video: 23.5 },
      },
    })

    const prices = parseSeedanceVideoPrices(legacyPrices)

    expect(prices[SEEDANCE_STANDARD_MODEL]['720p']).toEqual({
      without_video: 49.5,
      with_video: 29.5,
    })
    expect(prices[SEEDANCE_FAST_MODEL].default).toEqual({
      without_video: 38.5,
      with_video: 23.5,
    })
    expect(prices[SEEDANCE_25_MODEL]['720p']).toEqual({
      without_video: 70,
      with_video: 42,
    })
    expect(prices[SEEDANCE_25_MODEL]['1080p']).toEqual({
      without_video: 77,
      with_video: 46,
    })
  })
})
