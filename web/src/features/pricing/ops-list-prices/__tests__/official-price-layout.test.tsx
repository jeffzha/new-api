/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { cleanup, render, screen, within } from '@testing-library/react'
import { afterEach, expect, it } from 'vitest'

import type { PricingModel } from '../../types'
import { OfficialPrice } from '../official-price'

afterEach(cleanup)

it('keeps native USD prices unchanged when a legacy exchange rate is configured', () => {
  const usd: PricingModel = {
    ...local,
    list_price: {
      source: 'ops',
      currency: 'USD',
      comparison: 'match',
      local,
      display_rows: [
        {
          when: {},
          prices: { input_per_million: '2', output_per_million: '4' },
        },
      ],
    },
  }
  render(<OfficialPrice model={usd} usdExchangeRate={7.3} variant='details' />)
  expect(screen.getByText('$2 / 1M tokens')).toBeInTheDocument()
  expect(screen.getByText('$4 / 1M tokens')).toBeInTheDocument()
})

const local: PricingModel = {
  id: 1,
  model_name: 'context-model',
  model_ratio: 1,
  completion_ratio: 2,
  quota_type: 0,
  enable_groups: ['default'],
}
const model: PricingModel = {
  ...local,
  list_price: {
    source: 'ops',
    comparison: 'review',
    local,
    display_rows: [
      {
        when: { inputMax: 32000 },
        prices: { input_per_million: '0.2', output_per_million: '2' },
      },
      {
        when: {},
        prices: { input_per_million: '0.8', output_per_million: '8' },
      },
    ],
  },
}

it('keeps each summary amount and its unit together while price items can wrap', () => {
  render(<OfficialPrice model={model} usdExchangeRate={1} />)
  const inputPrice = screen.getByText('Input').parentElement
  expect(inputPrice).toHaveClass('inline-flex', 'items-baseline')
  expect(inputPrice).not.toHaveClass('flex-col')
  expect(inputPrice).toHaveTextContent('$0.2–$0.8')
  const amount = screen.getByText('$0.2–$0.8')
  expect(amount.parentElement).toHaveClass('whitespace-nowrap')
  expect(amount.parentElement).toHaveTextContent('$0.2–$0.8 / 1M tokens')
  expect(screen.getAllByText('/ 1M tokens')).toHaveLength(2)
})

it.each([
  ['per_second', '0.00022', '$0.00022', 'second'],
  ['per_request', '0.2', '$0.2', 'request'],
  ['per_image', '0.18', '$0.18', 'image'],
] as const)(
  'keeps the %s unit attached to the amount on a compact card',
  (key, price, amount, unit) => {
    const single: PricingModel = {
      ...model,
      list_price: {
        source: 'ops',
        comparison: 'review',
        local,
        display_rows: [{ when: {}, prices: { [key]: price } }],
      },
    }
    render(<OfficialPrice model={single} usdExchangeRate={1} />)
    const value = screen.getByText(amount).parentElement
    expect(value).toHaveClass('whitespace-nowrap')
    expect(value).toHaveTextContent(`${amount} / ${unit}`)
    expect(screen.getByText(`/ ${unit}`)).not.toHaveClass('basis-full')
  }
)

it('aligns each context tier under separate input and output columns for comparison', () => {
  render(<OfficialPrice model={model} usdExchangeRate={1} variant='details' />)
  const table = screen.getByRole('table', { name: 'Operations price details' })
  expect(
    within(table).getByRole('columnheader', { name: /Input/ })
  ).toBeInTheDocument()
  expect(
    within(table).getByRole('columnheader', { name: /Output/ })
  ).toBeInTheDocument()
  const firstTier = within(table).getByRole('row', {
    name: /Input context ≤ 32000 tokens/,
  })
  expect(within(firstTier).getAllByRole('cell')).toHaveLength(3)
  expect(within(firstTier).getByText('$0.2')).toBeInTheDocument()
  expect(within(firstTier).getByText('$2')).toBeInTheDocument()
})

it('confines wide price matrices to a keyboard accessible scroll region on narrow screens', () => {
  render(<OfficialPrice model={model} usdExchangeRate={1} variant='details' />)
  const region = screen.getByRole('region', {
    name: 'Operations price details',
  })
  expect(region).toHaveClass('overflow-x-auto!')
  expect(region).toHaveAttribute('tabindex', '0')
  expect(within(region).getByRole('table')).toBeInTheDocument()
})

it('keeps one default price in the original two-column price tiles instead of a conditions table', () => {
  const single: PricingModel = {
    ...model,
    list_price: {
      source: 'ops',
      comparison: 'review',
      local,
      display_rows: [
        { when: {}, prices: { per_image: '0.18', per_reference_image: '0' } },
      ],
    },
  }
  render(<OfficialPrice model={single} usdExchangeRate={1} variant='details' />)
  expect(screen.queryByRole('table')).not.toBeInTheDocument()
  const prices = screen.getByRole('group', { name: 'Operations price details' })
  expect(prices).toHaveClass('grid-cols-1', 'sm:grid-cols-2')
  expect(within(prices).getByText('$0.18 / image')).toBeInTheDocument()
  expect(within(prices).getByText('$0 / image')).toBeInTheDocument()
})
