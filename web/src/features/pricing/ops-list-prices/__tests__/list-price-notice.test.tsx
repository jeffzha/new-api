/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it } from 'vitest'

import { parseTaskTiersFromExpr } from '../../lib/billing-expr'
import { getDynamicPricingSummary } from '../../lib/dynamic-price'
import type { PricingModel } from '../../types'
import { ListPriceNotice } from '../list-price-notice'

afterEach(cleanup)

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
