/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { getCoreRowModel, useReactTable } from '@tanstack/react-table'
import { act, cleanup, render, screen, within } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'

import { ThemeCustomizationProvider } from '@/context/theme-customization-provider'

import { StaticDataTable } from '../../static/static-data-table'
import { DataTableView } from '../data-table-view'
import { TableEmpty } from '../table-empty'

const rows = Array.from({ length: 20 }, (_, index) => ({
  model: `model-${index + 1}`,
  requests: index + 1,
}))
const columns = [
  { accessorKey: 'model', header: 'Model' },
  { accessorKey: 'requests', header: 'Requests' },
]

function Fixture(props: { empty?: boolean; splitHeader?: boolean }) {
  const table = useReactTable({
    data: props.empty ? [] : rows,
    columns,
    getCoreRowModel: getCoreRowModel(),
  })
  return <DataTableView table={table} splitHeader={props.splitHeader} />
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  document.cookie = 'theme_preset=; Max-Age=0; path=/'
  document.body.removeAttribute('data-theme-preset')
})

it.each(['prism-console', 'signal-console'])(
  '%s gives overflowing tables one aligned header and a scrollbar starting below it',
  (preset) => {
    document.cookie = `theme_preset=${preset}; path=/`
    const { rerender } = render(
      <ThemeCustomizationProvider>
        <Fixture splitHeader />
      </ThemeCustomizationProvider>
    )
    const table = screen.getByRole('table')
    const scrollArea = table.closest('[data-slot="table-container"]')
    expect(scrollArea).toHaveClass(
      'table-scroll-area',
      '[&>[data-slot=scroll-area-scrollbar]]:p-0.5'
    )
    expect(screen.getAllByRole('table')).toHaveLength(1)
    expect(within(table).getAllByRole('columnheader')).toHaveLength(2)
    expect(within(table).getAllByRole('row')).toHaveLength(21)
    expect(
      scrollArea?.querySelector('[data-orientation="vertical"]')
    ).toHaveStyle({ top: 'var(--table-header-height)', height: 'auto' })
    expect(
      scrollArea?.querySelector('[data-orientation="horizontal"]')
    ).toBeInTheDocument()

    rerender(
      <ThemeCustomizationProvider>
        <Fixture splitHeader empty />
      </ThemeCustomizationProvider>
    )
    expect(screen.getAllByRole('columnheader')).toHaveLength(2)
    expect(screen.queryByText('model-1')).not.toBeInTheDocument()
  }
)

it.each(['prism-console', 'signal-console'])(
  '%s uses the same body-only scrollbar for static model reports',
  (preset) => {
    document.cookie = `theme_preset=${preset}; path=/`
    render(
      <ThemeCustomizationProvider>
        <StaticDataTable
          className='console-distribution-table console-report-table'
          data={rows}
          columns={[
            { id: 'model', header: 'Model', cell: (row) => row.model },
            { id: 'requests', header: 'Requests', cell: (row) => row.requests },
          ]}
        />
      </ThemeCustomizationProvider>
    )
    const table = screen.getByRole('table')
    const scrollArea = table.closest('[data-slot="table-container"]')
    expect(scrollArea).toHaveClass(
      'table-scroll-area',
      '[&>[data-slot=scroll-area-scrollbar]]:p-0.5'
    )
    expect(
      scrollArea?.querySelector('[data-orientation="vertical"]')
    ).toHaveStyle({ top: 'var(--table-header-height)' })
    expect(within(table).getAllByRole('row')).toHaveLength(21)
  }
)

it('keeps the scrollbar below the unscaled header and follows header resizing', () => {
  document.cookie = 'theme_preset=prism-console; path=/'
  vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockImplementation(
    function (this: HTMLElement) {
      return this.tagName === 'THEAD' ? 40 : 0
    }
  )
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(
    function (this: HTMLElement) {
      return new DOMRect(0, 0, 100, this.tagName === 'THEAD' ? 50 : 0)
    }
  )
  let resizeHeader: ((height: number) => void) | undefined
  vi.stubGlobal(
    'ResizeObserver',
    class {
      constructor(private callback: ResizeObserverCallback) {}
      observe(target: Element) {
        if (target.tagName !== 'THEAD') return
        resizeHeader = (height) =>
          this.callback(
            [
              {
                target,
                borderBoxSize: [{ blockSize: height, inlineSize: 100 }],
                contentBoxSize: [],
                devicePixelContentBoxSize: [],
                contentRect: new DOMRect(0, 0, 100, height),
              },
            ],
            this
          )
      }
      unobserve() {}
      disconnect() {}
    }
  )
  render(
    <ThemeCustomizationProvider>
      <Fixture splitHeader />
    </ThemeCustomizationProvider>
  )
  const scrollArea = screen
    .getByRole('table')
    .closest('[data-slot="table-container"]')
  expect(scrollArea).toHaveStyle({ '--table-header-height': '40px' })
  act(() => resizeHeader?.(88))
  expect(scrollArea).toHaveStyle({ '--table-header-height': '88px' })
})

it('lets empty rows fit their scroll area while preserving the default height', () => {
  render(
    <table>
      <tbody>
        <TableEmpty colSpan={2} />
      </tbody>
    </table>
  )
  expect(screen.getByRole('cell')).toHaveClass('h-(--table-empty-height,400px)')
})

it('retains the existing native scroll container outside the console themes', () => {
  document.cookie = 'theme_preset=default; path=/'
  render(
    <ThemeCustomizationProvider>
      <Fixture />
    </ThemeCustomizationProvider>
  )
  expect(screen.getByRole('table').parentElement).toHaveClass(
    'overflow-x-auto',
    'overflow-y-hidden'
  )
  expect(
    document.querySelector('[data-slot="scroll-area-scrollbar"]')
  ).not.toBeInTheDocument()
})
