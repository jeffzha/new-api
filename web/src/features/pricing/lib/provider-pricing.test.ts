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
import { assert, describe, test } from 'vitest'

import { SORT_OPTIONS } from '../constants.ts'
import type { PricingModel, VideoTokenMatrixPricing } from '../types.ts'
import { sortModels } from './filters.ts'
import {
  getLowestVideoTokenMatrixPrice,
  getVideoTokenMatrixPricing,
  scaleVideoTokenPricingTiers,
} from './provider-pricing.ts'

const matrix: VideoTokenMatrixPricing = {
  kind: 'video_token_matrix',
  currency: 'USD',
  unit: '1M_video_tokens',
  tiers: [
    { resolution: '720p', without_video: 7, with_video: 4.3 },
    { resolution: '1080p', without_video: 7.7, with_video: 4.7 },
  ],
}

function modelWithProviderPricing(
  providerPricing?: VideoTokenMatrixPricing
): PricingModel {
  return {
    id: 1,
    model_name: 'provider-defined-model',
    quota_type: 0,
    model_ratio: 37.5,
    completion_ratio: 1,
    enable_groups: ['default'],
    provider_pricing: providerPricing,
  }
}

describe('provider pricing', () => {
  test('recognizes a video matrix from its contract instead of the model name', () => {
    assert.equal(
      getVideoTokenMatrixPricing(modelWithProviderPricing(matrix)),
      matrix
    )
    assert.equal(getVideoTokenMatrixPricing(modelWithProviderPricing()), null)
  })

  test('rejects malformed provider pricing instead of rendering partial data', () => {
    const malformedModel = {
      ...modelWithProviderPricing(),
      provider_pricing: {
        ...matrix,
        tiers: [{ resolution: '720p', without_video: Number.NaN }],
      },
    } as unknown as PricingModel

    assert.equal(getVideoTokenMatrixPricing(malformedModel), null)
  })

  test('applies the selected group ratio directly to USD matrix prices', () => {
    const scaled = scaleVideoTokenPricingTiers(matrix, 1.5)
    assert.equal(scaled[0]?.resolution, '720p')
    assert.closeTo(scaled[0]?.without_video ?? 0, 10.5, 1e-12)
    assert.closeTo(scaled[0]?.with_video ?? 0, 6.45, 1e-12)
    assert.equal(scaled[1]?.resolution, '1080p')
    assert.closeTo(scaled[1]?.without_video ?? 0, 11.55, 1e-12)
    assert.closeTo(scaled[1]?.with_video ?? 0, 7.05, 1e-12)
  })

  test('sorts matrix models by their lowest valid base USD price', () => {
    const providerModel = modelWithProviderPricing(matrix)
    const genericModel = {
      ...modelWithProviderPricing(),
      id: 2,
      model_name: 'generic-model',
      model_ratio: 30,
    }

    assert.equal(getLowestVideoTokenMatrixPrice(matrix), 4.3)
    assert.deepEqual(
      sortModels([genericModel, providerModel], SORT_OPTIONS.PRICE_LOW).map(
        (model) => model.model_name
      ),
      ['provider-defined-model', 'generic-model']
    )
  })
})
