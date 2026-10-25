import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { OperationsDashboard } from '..'

const clients: QueryClient[] = []

afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  vi.restoreAllMocks()
})

it('links user and API Key filters, retains empty selections and hides stale results while loading', async () => {
  let finish: (value: unknown) => void = () => {}
  let pending = false
  const get = vi.spyOn(api, 'get').mockImplementation(async (url, config) => {
    if (String(url).includes('settings')) {
      return { data: { success: true, data: {} } }
    }
    if (pending) {
      return new Promise((resolve) => {
        finish = resolve
      })
    }
    const selected = (config?.params as { user_id?: number } | undefined)
      ?.user_id
    return {
      data: {
        success: true,
        data: {
          options: {
            users: [
              { id: 701, name: 'Alice' },
              { id: 702, name: 'Bob' },
            ],
            tokens: selected === 702 ? [] : [{ id: 801, name: 'Alice key' }],
            models: ['test-model'],
            groups: ['default'],
            endpoint_types: ['chat'],
            nodes: [],
          },
        },
      },
    }
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      <OperationsDashboard />
    </QueryClientProvider>
  )
  const user = userEvent.setup()
  await screen.findByRole('combobox', { name: 'User' })
  await user.selectOptions(
    screen.getByRole('combobox', { name: 'User' }),
    '701'
  )
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith(
      '/api/ops/dashboard/snapshot',
      expect.objectContaining({
        params: expect.objectContaining({ user_id: 701 }),
      })
    )
  )
  await screen.findByRole('tab', { name: 'Overview' })
  await user.selectOptions(
    screen.getByRole('combobox', { name: 'API Key' }),
    '801'
  )
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith(
      '/api/ops/dashboard/snapshot',
      expect.objectContaining({
        params: expect.objectContaining({ user_id: 701, token_id: 801 }),
      })
    )
  )
  await screen.findByRole('tab', { name: 'Overview' })
  pending = true
  await user.selectOptions(
    screen.getByRole('combobox', { name: 'User' }),
    '702'
  )
  expect(screen.getByRole('combobox', { name: 'API Key' })).toHaveValue('0')
  expect(
    await screen.findByText('Loading results for the selected filters...')
  ).toBeVisible()
  expect(
    screen.queryByRole('tab', { name: 'Overview' })
  ).not.toBeInTheDocument()
  await act(async () =>
    finish({ data: { success: true, data: { options: {} } } })
  )
  expect(screen.getByRole('combobox', { name: 'User' })).toHaveValue('702')
  expect(await screen.findByRole('tab', { name: 'Overview' })).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Clear filters' }))
  expect(screen.getByRole('combobox', { name: 'User' })).toHaveValue('0')
})
