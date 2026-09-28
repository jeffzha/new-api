/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { getCoreRowModel, useReactTable } from '@tanstack/react-table'
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it } from 'vitest'

import { LogsFilterInput, LogsFilterToolbar } from '../logs-filter-toolbar'

function Fixture() {
  const table = useReactTable({
    data: [],
    columns: [],
    getCoreRowModel: getCoreRowModel(),
  })
  return (
    <LogsFilterToolbar
      table={table}
      primaryFilters={<LogsFilterInput aria-label='Model' />}
      advancedFilters={<LogsFilterInput aria-label='Request ID' />}
      hasActiveFilters={false}
      onSearch={() => {}}
      onReset={() => {}}
    />
  )
}
afterEach(cleanup)
it('expanded log filters stay content-sized and return to the compact primary row', async () => {
  render(<Fixture />)
  const filters = screen.getByRole('group', { name: 'Filter' })
  expect(filters).toHaveClass('shrink-0')
  expect(screen.queryByLabelText('Request ID')).not.toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'Expand' }))
  expect(screen.getByLabelText('Request ID')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Collapse' })).toHaveAttribute(
    'aria-expanded',
    'true'
  )
  await userEvent.click(screen.getByRole('button', { name: 'Collapse' }))
  expect(screen.queryByLabelText('Request ID')).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Search' })).toBeEnabled()
})
