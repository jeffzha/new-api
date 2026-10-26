import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { OperationsDashboard } from '..'

const clients: QueryClient[] = []

async function chooseOption(
  user: ReturnType<typeof userEvent.setup>,
  field: string,
  option: string
) {
  await user.click(screen.getByRole('combobox', { name: field }))
  await user.click(await screen.findByRole('option', { name: option }))
}

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
  await chooseOption(user, 'User', 'Alice (#701)')
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith(
      '/api/ops/dashboard/snapshot',
      expect.objectContaining({
        params: expect.objectContaining({ user_id: 701 }),
      })
    )
  )
  await screen.findByRole('tab', { name: 'Overview' })
  await chooseOption(user, 'API Key', 'Alice key (#801)')
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
  await chooseOption(user, 'User', 'Bob (#702)')
  expect(screen.getByRole('combobox', { name: 'API Key' })).toHaveValue(
    'All API Keys'
  )
  expect(
    await screen.findByText('Loading results for the selected filters...')
  ).toBeVisible()
  expect(
    screen.queryByRole('tab', { name: 'Overview' })
  ).not.toBeInTheDocument()
  await act(async () =>
    finish({ data: { success: true, data: { options: {} } } })
  )
  expect(screen.getByRole('combobox', { name: 'User' })).toHaveValue(
    'Bob (#702)'
  )
  expect(await screen.findByRole('tab', { name: 'Overview' })).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Clear filters' }))
  expect(screen.getByRole('combobox', { name: 'User' })).toHaveValue(
    'All users'
  )
})

it('searches the full model directory and shows an empty result without changing the selection', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        options: {
          users: [],
          tokens: [],
          models: ['deepseek-v4-1-flash-1017', 'unused-video-model'],
          groups: ['default', 'unused-group'],
          endpoint_types: ['chat', 'video_content'],
          nodes: [],
        },
      },
    },
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
  const input = await screen.findByRole('combobox', { name: 'Model' })
  await user.click(input)
  await user.type(input, 'unused')
  expect(
    screen.getByRole('option', { name: 'unused-video-model' })
  ).toBeVisible()
  expect(
    screen.queryByRole('option', { name: 'deepseek-v4-1-flash-1017' })
  ).not.toBeInTheDocument()
  await user.clear(input)
  await user.type(input, 'does-not-exist')
  expect(screen.getByText('No results found')).toBeVisible()
  await user.keyboard('{Escape}')
  expect(input).toHaveValue('All models')
})
