import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, expect, it } from 'vitest'

import { OPSBalanceValue } from '../balance-value'

afterEach(cleanup)

it('displays OPS negative balance in CNY instead of converting local quota', () => {
  render(
    <OPSBalanceValue
      balance={{
        status: 'available',
        amount: '-0.000053',
        currency: 'CNY',
        quota: -3,
        stale: false,
      }}
    />
  )
  expect(screen.getByText(/0.000053/)).toBeVisible()
})

it('keeps missing OPS money unknown instead of showing zero', () => {
  render(<OPSBalanceValue />)
  expect(screen.getByText('—')).toBeVisible()
  expect(screen.getByLabelText('Reading balance')).toBeVisible()
  expect(screen.queryByText('Reading balance')).not.toBeInTheDocument()
})

it('marks an old OPS amount as the last known balance', () => {
  render(
    <OPSBalanceValue
      balance={{
        status: 'stale',
        amount: '2.3',
        currency: 'CNY',
        quota: 1,
        stale: true,
      }}
    />
  )
  expect(screen.getByLabelText('Last known balance')).toBeVisible()
  expect(screen.getByText(/2.30/)).toBeVisible()
})
