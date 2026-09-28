/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { cleanup, render, screen, within } from '@testing-library/react'
import { afterEach, expect, it } from 'vitest'

import { ConsoleModelBreakdown } from '../console-model-breakdown'

afterEach(cleanup)

it('aggregates model usage across time buckets and orders the cost breakdown', () => {
  render(
    <ConsoleModelBreakdown
      loading={false}
      data={[
        {
          model_name: 'model-a',
          created_at: 100,
          count: 2,
          token_used: 100,
          quota: 500000,
        },
        {
          model_name: 'model-a',
          created_at: 200,
          count: 1,
          token_used: 80,
          quota: 1000000,
        },
        {
          model_name: 'model-b',
          created_at: 200,
          count: 1,
          token_used: 50,
          quota: 500000,
        },
      ]}
    />
  )
  const table = screen.getByRole('table', { name: 'Model cost breakdown' })
  const rows = within(table).getAllByRole('row')
  expect(rows[1]).toHaveTextContent('model-a3180$375.0%')
  expect(rows[2]).toHaveTextContent('model-b150$125.0%')
  expect(
    screen.getByRole('meter', { name: 'model-a Cost share' })
  ).toHaveAttribute('value', '75')
})

it('shows no data for an empty result and keeps zero-cost model shares finite', () => {
  const { rerender } = render(
    <ConsoleModelBreakdown loading={false} data={[]} />
  )
  expect(screen.getByText('No data')).toBeVisible()
  rerender(
    <ConsoleModelBreakdown
      loading={false}
      data={[
        {
          model_name: 'free-model',
          created_at: 100,
          count: 1,
          token_used: 0,
          quota: 0,
        },
      ]}
    />
  )
  expect(
    screen.getByRole('meter', { name: 'free-model Cost share' })
  ).toHaveAttribute('value', '0')
  expect(screen.getByText('0%')).toBeVisible()
})
