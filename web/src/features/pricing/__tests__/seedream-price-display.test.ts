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
import { describe, expect, it } from 'vitest'

import { parseTiersFromExpr } from '../lib/billing-expr'

const seedreamProExpression =
  '(param("size") == "1K" ? tier("normal_le_2610000px", fixed(0.03082191780821918)) : param("size") == "1.5K" ? tier("normal_le_2610000px", fixed(0.03082191780821918)) : param("size") == "1024x1024" ? tier("normal_le_2610000px", fixed(0.03082191780821918)) : param("size") == "1024*1024" ? tier("normal_le_2610000px", fixed(0.03082191780821918)) : tier("normal_gt_2610000px", fixed(0.06164383561643836))) * image_count'

describe('Seedream tiered image pricing', () => {
  it('parses conditional request pricing multiplied by image_count', () => {
    const tiers = parseTiersFromExpr(seedreamProExpression)

    expect(tiers).toHaveLength(5)
    expect(tiers.every((tier) => tier.imageCount)).toBe(true)
    expect(tiers[0]).toMatchObject({
      billingUnit: 'request',
      fixedPrice: 0.03082191780821918,
      conditionText: 'param("size") == "1K"',
    })
    expect(tiers.at(-1)).toMatchObject({
      billingUnit: 'request',
      fixedPrice: 0.06164383561643836,
    })
  })
})
