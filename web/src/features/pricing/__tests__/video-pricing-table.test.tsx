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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { ModelDetailsContent } from '../components/model-details'
import { VideoPricingTables } from '../components/video-pricing-tables'
import { withPluginPricing } from '../lib/plugin-pricing'
import { getVideoPricingTable } from '../lib/video-pricing-table'
import type {
  BillingPluginVariant,
  BillingUsageFieldSchema,
  BillingUsageSchema,
  PricingModel,
} from '../types'

vi.mock('@visactor/react-vchart', () => ({ VChart: () => null }))

// The Wangpai H3 catalogue quotes credits; this is the internal USD rate the
// billing expression applies to them (0.1 CNY per credit at the platform rate).
const CREDIT_USD = 0.013698630136986302

function usageNumber(
  unit: 'count' | 'second' | 'credit',
  en: string,
  zh: string
): BillingUsageFieldSchema {
  return { type: 'number', unit, description: { en, zh } }
}

const locopalSchema: BillingUsageSchema = {
  input_images: usageNumber('count', 'Input image unit price', '输入图片单价'),
  input_video_seconds: usageNumber(
    'second',
    'Input video unit price',
    '输入视频单价'
  ),
  resolution: {
    enum: ['768P', '2K'],
    description: {
      en: 'Output video resolution',
      zh: '输出视频分辨率',
    },
  },
  seconds: usageNumber('second', 'Video generation unit price', '视频生成单价'),
}

const wangpaiSchema: BillingUsageSchema = {
  input_images: usageNumber('count', 'Input image unit price', '输入图片单价'),
  material_credits: usageNumber(
    'credit',
    'Material unit price',
    '参考素材单价'
  ),
  mode: {
    enum: ['reference', 'text'],
    description: {
      en: 'Vendor path selected for the request',
      zh: '实际使用的上游生成路径',
    },
  },
  output_credits: usageNumber(
    'credit',
    'Video output unit price',
    '视频输出单价'
  ),
  resolution: {
    enum: ['480P', '720P', '1080P', '2K'],
    description: {
      en: 'Output video resolution',
      zh: '输出视频分辨率',
    },
  },
  seconds: usageNumber('second', 'Video generation unit price', '视频生成单价'),
  vendor_credits: usageNumber(
    'credit',
    'Upstream credit unit price',
    '上游额度单价'
  ),
}

const upstreamCreditsExpr = `tier("upstream", u("output_credits") * ${CREDIT_USD} + u("material_credits") * ${CREDIT_USD})`

const locopalExamples = [
  {
    label: 'H3 768P 5s',
    facts: { input_images: 0, resolution: '768P', seconds: 5 },
  },
  {
    label: 'H3 2K 5s',
    facts: { input_images: 0, resolution: '2K', seconds: 5 },
  },
]

const wangpaiExamples = [
  {
    label: 'H3 reference 480P 4s',
    facts: {
      input_images: 0,
      material_credits: 0,
      mode: 'reference',
      output_credits: 3.2,
      resolution: '480P',
      seconds: 4,
    },
  },
  {
    label: 'H3 reference 720P 5s',
    facts: {
      input_images: 0,
      material_credits: 0,
      mode: 'reference',
      output_credits: 7.5,
      resolution: '720P',
      seconds: 5,
    },
  },
  {
    label: 'H3 reference 1080P 5s',
    facts: {
      input_images: 0,
      material_credits: 0,
      mode: 'reference',
      output_credits: 10,
      resolution: '1080P',
      seconds: 5,
    },
  },
  {
    label: 'H3 reference 2K 15s',
    facts: {
      input_images: 0,
      material_credits: 0,
      mode: 'reference',
      output_credits: 45,
      resolution: '2K',
      seconds: 15,
    },
  },
]

const locopalVariant: BillingPluginVariant = {
  plugin_key: 'cn-locopal-video',
  plugin_name: 'LocoPal Video',
  billing_mode: 'tiered_expr',
  billing_expr:
    'u("resolution") == "2K" ? tier("2K", u("seconds") * 0.10958904109589041 + u("input_video_seconds") * 0.10958904109589041 + u("input_images") * 0.027397260273972605) : tier("768P", u("seconds") * 0.0684931506849315 + u("input_video_seconds") * 0.0684931506849315 + u("input_images") * 0.027397260273972605)',
  billing_usage_schema: locopalSchema,
  billing_usage_examples: locopalExamples,
}

const wangpaiVariant: BillingPluginVariant = {
  plugin_key: 'wangpaiai',
  plugin_name: 'Wangpai AI',
  billing_mode: 'tiered_expr',
  billing_expr: upstreamCreditsExpr,
  billing_usage_schema: wangpaiSchema,
  billing_usage_examples: wangpaiExamples,
}

const locopalH3Model: PricingModel = {
  id: 11,
  model_name: 'MiniMax-H3',
  quota_type: 1,
  model_ratio: 1,
  completion_ratio: 1,
  enable_groups: ['default'],
  billing_mode: 'tiered_expr',
  billing_expr: locopalVariant.billing_expr,
  billing_usage_schema: locopalSchema,
  billing_usage_examples: locopalExamples,
  billing_plugin_variants: [locopalVariant],
}

const bridgeH3Model: PricingModel = {
  ...locopalH3Model,
  id: 12,
  model_name: 'MiniMax-H3-261013',
  billing_expr: upstreamCreditsExpr,
  billing_usage_schema: wangpaiSchema,
  billing_usage_examples: wangpaiExamples,
  billing_material_pricing: undefined,
  billing_plugin_variants: [wangpaiVariant],
}

const h3TwoKModel: PricingModel = {
  ...bridgeH3Model,
  id: 12,
  model_name: 'MiniMax-H3-2K',
  billing_expr: upstreamCreditsExpr,
  billing_usage_schema: wangpaiSchema,
  billing_usage_examples: [
    {
      label: 'H3 2K fixed 15s',
      facts: {
        input_images: 0,
        material_credits: 0,
        mode: 'text',
        output_credits: 19,
        resolution: '2K',
        seconds: 15,
      },
    },
  ],
  billing_plugin_variants: [wangpaiVariant],
}

const h3SensitiveModel: PricingModel = {
  ...bridgeH3Model,
  id: 13,
  model_name: 'MiniMax-H3-Sensitive',
}

it('expands the H3 output list from the configured expression, not the sampled resolutions', () => {
  const table = getVideoPricingTable(bridgeH3Model)

  expect(table?.outputRows.map((row) => row.resolution)).toEqual([
    '480P',
    '720P',
    '1080P',
    '2K',
  ])
  expect(table?.outputRows.map((row) => row.rule)).toEqual([
    'second',
    'second',
    'second',
    'second',
  ])
  const prices = table?.outputRows.map((row) => row.amountUSD) ?? []
  expect(prices).toHaveLength(4)
  expect(prices[0]).toBeCloseTo(0.8 * CREDIT_USD, 12)
  expect(prices[1]).toBeCloseTo(1.5 * CREDIT_USD, 12)
  expect(prices[2]).toBeCloseTo(2 * CREDIT_USD, 12)
  expect(prices[3]).toBeCloseTo(3 * CREDIT_USD, 12)
})

it.each([bridgeH3Model, h3TwoKModel, h3SensitiveModel])(
  'does not mix LocoPal material charges into $model_name',
  (model) => {
    const table = getVideoPricingTable(model)

    expect(table?.materialPricing).toEqual({
      kind: 'upstream_credits',
      creditPriceUSD: CREDIT_USD,
    })
  }
)

it('quotes the fixed-duration 2K catalogue as one whole-request price', () => {
  const table = getVideoPricingTable(h3TwoKModel)

  expect(table?.outputRows).toHaveLength(1)
  expect(table?.outputRows[0]?.resolution).toBe('2K')
  expect(table?.outputRows[0]?.rule).toBe('request')
  expect(table?.outputRows[0]?.seconds).toBe(15)
  expect(table?.outputRows[0]?.amountUSD).toBeCloseTo(19 * CREDIT_USD, 12)
})

it('prices each provider variant from its own expression', () => {
  const locopalTable = getVideoPricingTable(
    withPluginPricing(locopalH3Model, locopalVariant)
  )
  expect(locopalTable?.outputRows.map((row) => row.resolution)).toEqual([
    '768P',
    '2K',
  ])
  expect(locopalTable?.materialPricing).toEqual({
    kind: 'usage_fields',
    rows: [
      {
        field: 'input_video_seconds',
        definition: locopalSchema.input_video_seconds,
        rates: [
          { label: '2K', amountUSD: 0.1095890410958904 },
          { label: '768P', amountUSD: 0.0684931506849315 },
        ],
      },
      {
        field: 'input_images',
        definition: locopalSchema.input_images,
        rates: [{ amountUSD: 0.027397260273972605 }],
      },
    ],
  })

  const wangpaiTable = getVideoPricingTable(
    withPluginPricing(bridgeH3Model, wangpaiVariant)
  )
  expect(wangpaiTable?.outputRows.map((row) => row.resolution)).toEqual([
    '480P',
    '720P',
    '1080P',
    '2K',
  ])
  expect(wangpaiTable?.outputRows.map((row) => row.amountUSD)).toEqual([
    (3.2 * CREDIT_USD) / 4,
    (7.5 * CREDIT_USD) / 5,
    (10 * CREDIT_USD) / 5,
    (45 * CREDIT_USD) / 15,
  ])
  expect(wangpaiTable?.materialPricing).toEqual({
    kind: 'upstream_credits',
    creditPriceUSD: CREDIT_USD,
  })
})

it('renders LocoPal H3 material prices by resolution without claiming free images', () => {
  const table = getVideoPricingTable(
    withPluginPricing(locopalH3Model, locopalVariant)
  )
  expect(table).not.toBeNull()
  if (!table) throw new Error('Expected a video pricing table')

  render(
    <VideoPricingTables
      table={table}
      tokenUnit='M'
      priceRate={1}
      usdExchangeRate={7.3}
    />
  )

  expect(screen.getByText('Input image unit price')).toBeInTheDocument()
  expect(screen.getByText('Input video unit price')).toBeInTheDocument()
  expect(screen.getByText(/^\$0\.027397 \/ unit$/)).toBeInTheDocument()
  expect(
    screen.getByText(/^2K \$0\.109589 \/ s · 768P \$0\.068493 \/ s$/)
  ).toBeInTheDocument()
  expect(screen.queryByText(/First 5 free/i)).not.toBeInTheDocument()
  expect(screen.queryByText(/credit/i)).not.toBeInTheDocument()
})

it('explains Wangpai material billing without inventing fixed unit prices', () => {
  const table = getVideoPricingTable(
    withPluginPricing(bridgeH3Model, wangpaiVariant)
  )
  expect(table?.materialPricing).toEqual({
    kind: 'upstream_credits',
    creditPriceUSD: CREDIT_USD,
  })
  if (!table) throw new Error('Expected a video pricing table')

  render(
    <VideoPricingTables
      table={table}
      tokenUnit='M'
      priceRate={1}
      usdExchangeRate={7.3}
    />
  )

  expect(
    screen.getAllByText(
      "Settled according to the supplier's final material charge; the supplier does not publish fixed per-image or input-video prices"
    ).length
  ).toBeGreaterThan(0)
  expect(screen.queryByText('¥0.20/image')).not.toBeInTheDocument()
  expect(
    screen.queryByText('768P ¥0.50/s · 2K ¥0.80/s')
  ).not.toBeInTheDocument()
  expect(screen.queryByText(/credit/i)).not.toBeInTheDocument()
})

it('returns no video price list when the model does not price a resolution catalogue', () => {
  const musicModel: PricingModel = {
    ...bridgeH3Model,
    model_name: 'incho_music',
    billing_expr: 'tier("music", u("clips") * 0.22)',
    billing_usage_schema: {
      clips: usageNumber('count', 'Song generation unit price', '生成歌曲单价'),
    },
    billing_usage_examples: undefined,
    billing_plugin_variants: undefined,
  }

  expect(getVideoPricingTable(musicModel)).toBeNull()
})

it('renders the bridge H3 price list in the model drawer instead of the raw expression', () => {
  vi.spyOn(api, 'get').mockResolvedValue({ data: { data: { groups: [] } } })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ModelDetailsContent
        model={bridgeH3Model}
        groupRatio={{ default: 2 }}
        usableGroup={{ default: { desc: '', ratio: 2 } }}
        endpointMap={{}}
        autoGroups={[]}
        priceRate={1}
        usdExchangeRate={7.3}
        tokenUnit='M'
      />
    </QueryClientProvider>
  )

  expect(screen.getAllByText('Video output pricing').length).toBeGreaterThan(0)
  expect(screen.getAllByText('Video material pricing').length).toBeGreaterThan(
    0
  )
  expect(screen.getAllByText('480P').length).toBeGreaterThan(0)
  expect(screen.getAllByText('2K').length).toBeGreaterThan(0)
  expect(
    screen.getAllByText(
      "Settled according to the supplier's final material charge; the supplier does not publish fixed per-image or input-video prices"
    ).length
  ).toBeGreaterThan(0)
  expect(screen.queryByText(/u\("resolution"\)/)).not.toBeInTheDocument()
  expect(screen.queryByText(/Raw expression/)).not.toBeInTheDocument()
  expect(
    screen.queryByText(/Special billing expression/)
  ).not.toBeInTheDocument()
})
