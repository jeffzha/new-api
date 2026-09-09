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
  parseSeedanceVideoPrices,
  SEEDANCE_25_MODEL,
  SEEDANCE_FAST_MODEL,
  SEEDANCE_STANDARD_MODEL,
} from '../seedance-video-pricing'

describe('Seedance video pricing upgrades', () => {
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
