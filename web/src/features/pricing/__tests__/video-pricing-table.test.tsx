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
import userEvent from '@testing-library/user-event'
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
  MaterialPricingCatalog,
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

// MiniMax's own resolutions; 480P exists only in the configured expression.
const hailuoSchema: BillingUsageSchema = {
  input_images: usageNumber('count', 'Input image unit price', '输入图片单价'),
  resolution: {
    enum: ['512P', '768P', '720P', '1080P', '2K'],
    description: {
      en: 'Output video resolution',
      zh: '输出视频分辨率',
    },
  },
  seconds: usageNumber('second', 'Video generation unit price', '视频生成单价'),
}

const wangpaiSchema: BillingUsageSchema = {
  ...hailuoSchema,
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
  vendor_credits: usageNumber(
    'credit',
    'Upstream credit unit price',
    '上游额度单价'
  ),
}

const resolutionMatrix = [
  `u("resolution") == "480P" ? tier("480P", u("seconds") * 0.010958904109589043)`,
  `u("resolution") == "720P" ? tier("720P", u("seconds") * 0.020547945205479454)`,
  `u("resolution") == "1080P" ? tier("1080P", u("seconds") * 0.027397260273972605)`,
  `tier("2K", u("seconds") * 0.041095890410958909)`,
].join(' : ')

const upstreamCreditsExpr = `tier("upstream", u("output_credits") * ${CREDIT_USD} + u("material_credits") * ${CREDIT_USD})`

const officialMaterialPricing: MaterialPricingCatalog = {
  currency: 'CNY',
  items: [
    { material: 'audio', unit: 'free' },
    {
      material: 'image',
      unit: 'count',
      freeUnits: 5,
      rates: [{ amount: 0.2 }],
    },
    {
      material: 'video',
      unit: 'second',
      rates: [
        { label: { en: '768P', zh: '768P' }, amount: 0.5 },
        { label: { en: '2K', zh: '2K' }, amount: 0.8 },
      ],
    },
  ],
}

const hailuoExamples = [
  {
    label: 'H3 768P 5s',
    facts: { input_images: 0, resolution: '768P', seconds: 5 },
  },
  {
    label: '02 512P 6s',
    facts: { input_images: 0, resolution: '512P', seconds: 6 },
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

const hailuoVariant: BillingPluginVariant = {
  plugin_key: 'hailuo',
  plugin_name: 'Hailuo Video',
  billing_mode: 'tiered_expr',
  billing_expr: resolutionMatrix,
  billing_usage_schema: hailuoSchema,
  billing_usage_examples: hailuoExamples,
}

const wangpaiVariant: BillingPluginVariant = {
  billing_material_pricing: officialMaterialPricing,
  plugin_key: 'wangpaiai',
  plugin_name: 'Wangpai AI',
  billing_mode: 'tiered_expr',
  billing_expr: upstreamCreditsExpr,
  billing_usage_schema: wangpaiSchema,
  billing_usage_examples: wangpaiExamples,
}

const h3Model: PricingModel = {
  id: 11,
  model_name: 'MiniMax-H3',
  quota_type: 1,
  model_ratio: 1,
  completion_ratio: 1,
  enable_groups: ['default'],
  billing_mode: 'tiered_expr',
  billing_expr: resolutionMatrix,
  billing_usage_schema: hailuoSchema,
  billing_usage_examples: hailuoExamples,
  billing_plugin_variants: [hailuoVariant, wangpaiVariant],
}

const h3TwoKModel: PricingModel = {
  ...h3Model,
  id: 12,
  model_name: 'MiniMax-H3-2K',
  billing_expr: `tier("15s-2K", u("seconds") * 0.017351598173515986)`,
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

it('expands the H3 output list from the configured expression, not the sampled resolutions', () => {
  const table = getVideoPricingTable(h3Model)

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
  expect(table?.outputRows.map((row) => row.amountUSD)).toEqual([
    0.010958904109589043, 0.020547945205479454, 0.027397260273972605,
    0.041095890410958909,
  ])
})

it('does not mix another provider material charges into the model price list', () => {
  const table = getVideoPricingTable(h3Model)

  expect(table?.materialPricing).toBeUndefined()
})

it('quotes the fixed-duration 2K catalogue as one whole-request price', () => {
  const table = getVideoPricingTable(h3TwoKModel)

  expect(table?.outputRows).toHaveLength(1)
  expect(table?.outputRows[0]?.resolution).toBe('2K')
  expect(table?.outputRows[0]?.rule).toBe('request')
  expect(table?.outputRows[0]?.seconds).toBe(15)
  expect(table?.outputRows[0]?.amountUSD).toBeCloseTo(19 * CREDIT_USD, 12)
})

it('prices each provider variant from its own expression', () => {
  const hailuoTable = getVideoPricingTable(
    withPluginPricing(h3Model, hailuoVariant)
  )
  expect(hailuoTable?.outputRows.map((row) => row.resolution)).toEqual([
    '480P',
    '720P',
    '1080P',
    '2K',
  ])
  expect(hailuoTable?.materialPricing).toBeUndefined()

  const wangpaiTable = getVideoPricingTable(
    withPluginPricing(h3Model, wangpaiVariant)
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
    kind: 'catalog',
    catalog: officialMaterialPricing,
  })
})

it('renders official H3 material prices without exposing upstream credits', () => {
  const table = getVideoPricingTable(withPluginPricing(h3Model, wangpaiVariant))
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

  expect(screen.getByText('Audio material')).toBeInTheDocument()
  expect(screen.getByText('Image material')).toBeInTheDocument()
  expect(screen.getByText('Video material')).toBeInTheDocument()
  expect(screen.getByText('No charge')).toBeInTheDocument()
  expect(screen.getByText('First 5 free, then ¥0.20/image')).toBeInTheDocument()
  expect(screen.getByText('768P ¥0.50/s · 2K ¥0.80/s')).toBeInTheDocument()
  expect(screen.queryByText(/credit/i)).not.toBeInTheDocument()
})

it('keeps the combined supplier-charge fallback when no official catalogue exists', () => {
  const fallbackVariant = {
    ...wangpaiVariant,
    billing_material_pricing: undefined,
  }
  const table = getVideoPricingTable(
    withPluginPricing(h3Model, fallbackVariant)
  )
  expect(table?.materialPricing).toEqual({
    kind: 'upstream_credits',
    creditPriceUSD: CREDIT_USD,
  })
})

it('returns no video price list when the model does not price a resolution catalogue', () => {
  const musicModel: PricingModel = {
    ...h3Model,
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

it('renders each H3 provider price list in the model drawer instead of the raw expression', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({ data: { data: { groups: [] } } })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ModelDetailsContent
        model={h3Model}
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
  expect(screen.queryByText('Video material pricing')).not.toBeInTheDocument()
  expect(screen.getAllByText('480P').length).toBeGreaterThan(0)
  expect(screen.getAllByText('2K').length).toBeGreaterThan(0)

  await userEvent.setup().click(screen.getByRole('tab', { name: 'Wangpai AI' }))

  expect(screen.getAllByText('Video material pricing').length).toBeGreaterThan(
    0
  )
  expect(screen.queryByText(/u\("resolution"\)/)).not.toBeInTheDocument()
  expect(screen.queryByText(/Raw expression/)).not.toBeInTheDocument()
  expect(
    screen.queryByText(/Special billing expression/)
  ).not.toBeInTheDocument()
})
