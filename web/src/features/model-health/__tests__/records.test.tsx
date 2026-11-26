import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
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

it('reconciles a verified yuan amount exactly without changing the OPS fee', async () => {
  const adapter = api.defaults.adapter
  const client = new QueryClient()
  const posted: unknown[] = []
  api.defaults.adapter = async (config) => {
    if (config.url?.endsWith('/reconcile')) {
      posted.push(JSON.parse(config.data as string))
    }
    return {
      config,
      status: 200,
      statusText: 'OK',
      headers: {},
      data: {
        success: true,
        data: config.url?.endsWith('/run-costs')
          ? {
              state: 'confirmed',
              amounts: { 1: 249 },
              week_micro: 249,
              total_micro: 249,
            }
          : [
              {
                id: 1,
                model: 'Video fixture',
                state: 'uncertain',
                actual_micro: null,
              },
            ],
      },
    }
  }
  const view = render(
    <QueryClientProvider client={client}>
      <HealthRecords
        access={{
          probe: false,
          rules: true,
          notify: false,
          export: false,
          worker: false,
        }}
      />
    </QueryClientProvider>
  )
  try {
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole('button', { name: 'Verify detection cost' })
    )
    const dialog = screen.getByRole('alertdialog')
    const amount = within(dialog).getByLabelText('Verified cost (CNY)')
    const submit = within(dialog).getByRole('button', { name: 'Reconcile' })
    expect(submit).toBeDisabled()
    await user.type(amount, '0.0000001')
    expect(submit).toBeDisabled()
    expect(amount).toHaveAttribute('aria-invalid', 'true')
    await user.clear(amount)
    await user.type(amount, '1.234567')
    expect(submit).toBeEnabled()
    expect(amount).toHaveAttribute('aria-invalid', 'false')
    await user.click(submit)
    await waitFor(() =>
      expect(posted).toEqual([{ id: 1, actual_micro: 1234567 }])
    )
    expect(await screen.findAllByText('\u00a50.000249')).toHaveLength(3)
  } finally {
    view.unmount()
    client.clear()
    api.defaults.adapter = adapter
  }
})
