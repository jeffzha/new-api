import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import { expect, it } from 'vitest'

import { api } from '@/lib/http-client'

import { HealthRecords } from '../records'

it('shows only OPS amounts and distinguishes an explicit zero from an unmatched run', async () => {
  const adapter = api.defaults.adapter
  const client = new QueryClient()
  api.defaults.adapter = async (config) => ({
    config,
    status: 200,
    statusText: 'OK',
    headers: {},
    data: {
      success: true,
      data: config.url?.endsWith('/run-costs')
        ? {
            state: 'confirmed',
            week_micro: 50,
            total_micro: 100,
            amounts: { 1: 0 },
          }
        : [
            {
              id: 1,
              model: 'Confirmed model',
              state: 'succeeded',
              actual_micro: 888,
            },
            {
              id: 2,
              model: 'Unmatched model',
              state: 'failed',
              actual_micro: 999,
            },
          ],
    },
  })
  const view = render(
    <QueryClientProvider client={client}>
      <HealthRecords
        access={{
          probe: false,
          rules: false,
          notify: false,
          export: false,
          worker: false,
        }}
      />
    </QueryClientProvider>
  )
  try {
    expect(await screen.findByText('\u00a50.000050')).toBeVisible()
    expect(screen.getByText('\u00a50.000100')).toBeVisible()
    expect(
      within(
        await screen.findByRole('row', { name: /Confirmed model/ })
      ).getByText('\u00a50.000000')
    ).toBeVisible()
    expect(
      within(screen.getByRole('row', { name: /Unmatched model/ })).getByText(
        'Awaiting OPS receipt'
      )
    ).toBeVisible()
    expect(screen.queryByText(/0\.000(888|999)/)).not.toBeInTheDocument()
  } finally {
    view.unmount()
    client.clear()
    api.defaults.adapter = adapter
  }
})
