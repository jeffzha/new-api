/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it } from 'vitest'

import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import { CachedPriceCell } from '../../components/cached-price-cell'
import { ModelCard } from '../../components/model-card'
import { ModelPriceCell } from '../../components/model-price-cell'
import { parseTaskTiersFromExpr } from '../../lib/billing-expr'
import { getDynamicPricingSummary } from '../../lib/dynamic-price'
import { filterByQuotaType } from '../../lib/filters'
import type { PricingModel } from '../../types'
import { ListPriceNotice } from '../list-price-notice'
import { OfficialPrice } from '../official-price'

afterEach(() => {
  cleanup()
  useSystemConfigStore
    .getState()
    .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
})

const local: PricingModel = {
  id: 1,
  model_name: 'text',
  model_ratio: 1,
  completion_ratio: 2,
  quota_type: 0,
  enable_groups: ['default'],
}
const ops: PricingModel = {
  ...local,
  model_ratio: 2,
  list_price: { source: 'ops', comparison: 'differ', local },
}

it('opens the comparison with separate price sources and no discounts', async () => {
  render(
    <ListPriceNotice
      models={[ops]}
      status={{ version: 'v1', stale: false }}
      priceRate={1}
      usdExchangeRate={7.3}
    />
  )
  const user = userEvent.setup()
  const button = screen.getByRole('button', { name: 'Compare list prices' })
  button.focus()
  await user.keyboard('{Enter}')
  const dialog = screen.getByRole('dialog', { name: 'List price comparison' })
  expect(within(dialog).getByText('Operations platform')).toBeInTheDocument()
  expect(within(dialog).getByText('Unit prices differ')).toBeInTheDocument()
  expect(
    within(dialog).getByRole('region', { name: 'List price comparison' })
  ).toHaveClass('overflow-x-auto')
  expect(
    within(dialog).getByRole('columnheader', { name: 'Local list price' })
  ).toBeInTheDocument()
  await user.keyboard('{Escape}')
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
})

it('explains retained prices after a failed refresh', () => {
  render(
    <ListPriceNotice
      models={[ops]}
      status={{ version: 'v1', stale: true }}
      priceRate={1}
      usdExchangeRate={7.3}
    />
  )
  expect(screen.getByRole('alert')).toHaveTextContent(
    'Previously loaded list prices are still shown.'
  )
})

it('explains local fallback before the first successful read and handles an empty catalog', async () => {
  render(
    <ListPriceNotice
      models={[]}
      status={{ stale: true, error: 'OPS read HTTP 403' }}
      priceRate={1}
      usdExchangeRate={7.3}
    />
  )
  expect(screen.getByRole('alert')).toHaveTextContent(
    'Local list prices are shown.'
  )
  expect(screen.queryByText('OPS read HTTP 403')).not.toBeInTheDocument()
  await userEvent.click(
    screen.getByRole('button', { name: 'Compare list prices' })
  )
  expect(screen.getByRole('dialog')).toHaveTextContent('No models available')
})

it('renders projected token prices as unit prices instead of a special expression', () => {
  const summary = getDynamicPricingSummary(
    {
      ...local,
      billing_mode: 'tiered_expr',
      billing_expr: 'tier("default", p * 2 + c * 8 + cr * 0.2)',
    },
    { tokenUnit: 'M' }
  )
  expect(summary?.isSpecialExpression).toBe(false)
  expect(summary?.primaryEntries).toEqual(
    expect.arrayContaining([
      expect.objectContaining({ value: 2 }),
      expect.objectContaining({ value: 8 }),
    ])
  )
})

it('preserves resolution tiers and per-second units in projected task prices', () => {
  const schema = {
    seconds: { type: 'number' as const, unit: 'second' as const },
    input_images: { type: 'number' as const, unit: 'count' as const },
    resolution: { enum: ['2K'] },
  }
  const tiers = parseTaskTiersFromExpr(
    'u("resolution") == "2K" ? tier("ops-0", u("seconds") * 0.8 + u("input_images") * 0.2) : tier("default", u("seconds") * 0.5 + u("input_images") * 0.2)',
    schema
  )
  expect(tiers).toHaveLength(2)
  expect(tiers[0].conditions).toEqual([{ field: 'resolution', value: '2K' }])
  expect(tiers[0].unitPrices.seconds).toBe(0.8)
  expect(tiers[1].unitPrices.seconds).toBe(0.5)
})

const detailed: PricingModel = {
  ...local,
  sales_bps: 1000,
  group_ratio: { default: 0.1 },
  list_price: {
    source: 'ops',
    comparison: 'review',
    local,
    display_rows: [
      {
        when: { inputMax: 32000 },
        prices: {
          input_per_million: '0.2',
          audio_input_per_million: '20.4',
          cache_read_per_million: '0',
        },
      },
      {
        when: { inputMax: 128000 },
        prices: {
          input_per_million: '0.4',
          audio_input_per_million: '20.4',
          cache_read_per_million: '0.08',
        },
      },
      {
        when: {},
        prices: {
          input_per_million: '0.8',
          audio_input_per_million: '20.4',
          cache_read_per_million: '0.16',
        },
      },
    ],
  },
}

it('shows OPS tier ranges and audio prices without customer discounts in standard mode', () => {
  render(
    <ModelPriceCell
      model={detailed}
      options={{
        usdExchangeRate: 1,
        showRechargePrice: false,
        priceRate: 0.1,
        selectedGroup: 'default',
      }}
    />
  )
  expect(screen.getByText('$0.2–$0.8')).toBeInTheDocument()
  expect(screen.getByText('$20.4')).toBeInTheDocument()
  expect(screen.getAllByText('/ 1M tokens')).toHaveLength(2)
  expect(
    screen.getByText('Price range; see details for conditions')
  ).toBeInTheDocument()
})

it('uses the same OPS price source in the separate cache column including free tiers', () => {
  render(
    <CachedPriceCell
      model={detailed}
      options={{ usdExchangeRate: 1, tokenUnit: 'K' }}
    />
  )
  expect(screen.getByText('$0–$0.00016')).toBeInTheDocument()
  expect(screen.getByText('/ 1K tokens')).toBeInTheDocument()
})

it('shows non-overlapping context intervals and the default upper range', () => {
  render(
    <OfficialPrice model={detailed} usdExchangeRate={1} variant='details' />
  )
  expect(screen.getByText('Input context ≤ 32000 tokens')).toBeInTheDocument()
  expect(
    screen.getByText('Input context: 32000 < tokens ≤ 128000')
  ).toBeInTheDocument()
  expect(screen.getByText('Input context > 128000 tokens')).toBeInTheDocument()
})

it('shows every tier and unit in a scrollable price matrix including video and credit conditions', () => {
  const model: PricingModel = {
    ...local,
    list_price: {
      source: 'ops',
      comparison: 'review',
      local,
      display_rows: [
        {
          when: {
            operation: 'edit',
            resolution: '1080p',
            referenceVideo: true,
          },
          prices: {
            per_second: '1.6',
            per_reference_image: '0.02',
            per_million_tokens: '31',
          },
        },
        {
          when: { period: 'off_peak' },
          prices: { per_output_credit: '0.1', per_material_credit: '0.2' },
        },
        {
          when: {},
          prices: { per_image: '0.18', per_reference_video_second: '0' },
        },
      ],
    },
  }
  render(<OfficialPrice model={model} usdExchangeRate={1} variant='details' />)
  const table = screen.getByRole('table', { name: 'Operations price details' })
  expect(
    screen.getByRole('region', { name: 'Operations price details' })
  ).toHaveClass('overflow-x-auto!')
  expect(table.closest('[data-slot="table-container"]')).toBeNull()
  expect(
    within(table).getByText(
      'Video editing · Resolution: 1080p · With input video'
    )
  ).toBeInTheDocument()
  expect(
    within(table).getByRole('columnheader', {
      name: 'Video token unit price / 1M tokens',
    })
  ).toBeInTheDocument()
  expect(within(table).getByText('$31')).toBeInTheDocument()
  expect(
    within(table).getByRole('columnheader', {
      name: 'Image generation unit price / image',
    })
  ).toBeInTheDocument()
  expect(within(table).getByText('$0.18')).toBeInTheDocument()
  expect(
    within(table).getByRole('columnheader', {
      name: 'Output credit unit price / credit',
    })
  ).toBeInTheDocument()
  expect(within(table).getByText('$0.1')).toBeInTheDocument()
  expect(
    within(table).getByRole('columnheader', {
      name: 'Reference video unit price / second',
    })
  ).toBeInTheDocument()
  expect(within(table).getByText('$0')).toBeInTheDocument()
  expect(within(table).getByText('Duration unit price')).toBeInTheDocument()
  expect(
    screen.getByText(
      'Period times follow the operations platform configuration.'
    )
  ).toBeInTheDocument()
})

it('converts OPS CNY prices once and scales only token units', () => {
  useSystemConfigStore.getState().setConfig({
    currency: {
      ...DEFAULT_CURRENCY_CONFIG,
      quotaDisplayType: 'CNY',
      usdExchangeRate: 7.3,
    },
  })
  render(<OfficialPrice model={detailed} tokenUnit='K' usdExchangeRate={7.3} />)
  expect(screen.getByText('¥0.0002–¥0.0008')).toBeInTheDocument()
  expect(screen.getByText('¥0.0204')).toBeInTheDocument()
})

it('uses OPS list prices in cards instead of customer discounted prices', () => {
  render(
    <ModelCard
      model={detailed}
      onClick={() => {}}
      usdExchangeRate={1}
      showRechargePrice={false}
      priceRate={0.1}
      selectedGroup='default'
    />
  )
  expect(screen.getByText('$0.2–$0.8')).toBeInTheDocument()
})

it('applies the public recharge factor once to OPS prices in cards, tables and cache cells', () => {
  const options = {
    usdExchangeRate: 1,
    priceRate: 0.5,
    showRechargePrice: true,
    selectedGroup: 'default',
    tokenUnit: 'K' as const,
  }
  const cell = render(<ModelPriceCell model={detailed} options={options} />)
  expect(screen.getByText('$0.0001–$0.0004')).toBeInTheDocument()
  expect(screen.getByText('$0.0102')).toBeInTheDocument()
  cell.unmount()
  const cache = render(<CachedPriceCell model={detailed} options={options} />)
  expect(screen.getByText('$0–$0.00008')).toBeInTheDocument()
  cache.unmount()
  render(<ModelCard model={detailed} onClick={() => {}} {...options} />)
  expect(screen.getByText('$0.0001–$0.0004')).toBeInTheDocument()
})

it('filters OPS task prices by their units while retaining local fallback models', () => {
  const task: PricingModel = {
    ...ops,
    model_name: 'image',
    list_price: {
      source: 'ops',
      comparison: 'review',
      local,
      display_rows: [{ when: {}, prices: { per_image: '0.18' } }],
    },
  }
  const request: PricingModel = {
    ...ops,
    model_name: 'request',
    list_price: {
      source: 'ops',
      comparison: 'review',
      local,
      display_rows: [{ when: {}, prices: { per_request: '0.01' } }],
    },
  }
  const models = [detailed, task, request, local]
  expect(filterByQuotaType(models, 'task')).toEqual([task])
  expect(filterByQuotaType(models, 'token')).toEqual([detailed, local])
  expect(filterByQuotaType(models, 'request')).toEqual([request])
})
