/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it } from 'vitest'

import { OpsFeeCell } from '../ops-fee-cell'

afterEach(cleanup)

it('shows a recorded historical charge while keeping the pricing review explicit', async () => {
  render(
    <OpsFeeCell
      fee={{
        status: 'unconfirmed',
        amount: null,
        station_amount: '0.263012',
        currency: 'USD',
      }}
    />
  )
  expect(screen.getByLabelText('$0.263012')).toBeVisible()
  const source = screen.getByLabelText(
    'Recorded charge; pricing review pending'
  )
  expect(source).toHaveAttribute('tabindex', '0')
  await userEvent.setup().tab()
  expect(source).toHaveFocus()
  expect(
    await screen.findByText('Recorded charge; pricing review pending')
  ).toBeVisible()
})

it('keeps a confirmed operations result authoritative when the recorded charge differs', () => {
  render(
    <OpsFeeCell
      fee={{
        status: 'available',
        amount: '0.42',
        station_amount: '0.43',
        currency: 'USD',
      }}
    />
  )
  expect(screen.getByLabelText('$0.42')).toBeVisible()
  expect(screen.queryByLabelText('$0.43')).not.toBeInTheDocument()
})

it('shows an explicit zero recorded charge without treating it as missing', () => {
  render(
    <OpsFeeCell
      fee={{
        status: 'unconfirmed',
        amount: null,
        station_amount: '0',
        currency: 'USD',
      }}
    />
  )
  expect(screen.getByLabelText('$0')).toBeVisible()
})

it.each([null, '', '-1', 'NaN', 'Infinity', '1e6', '1,000'])(
  'keeps an absent or invalid recorded charge %s in review',
  (stationAmount) => {
    render(
      <OpsFeeCell
        fee={{
          status: 'unconfirmed',
          amount: null,
          station_amount: stationAmount,
          currency: 'USD',
        }}
      />
    )
    expect(screen.getByText('Operations fee unconfirmed')).toBeVisible()
    expect(
      screen.queryByLabelText('Recorded charge; pricing review pending')
    ).not.toBeInTheDocument()
  }
)

it('keeps a pending call pending rather than presenting its temporary reservation as a charge', () => {
  render(
    <OpsFeeCell
      fee={{
        status: 'waiting',
        amount: null,
        station_amount: '1',
        currency: 'USD',
      }}
    />
  )
  expect(screen.getByText('Awaiting operations fee')).toBeVisible()
  expect(screen.queryByLabelText('$1')).not.toBeInTheDocument()
})
