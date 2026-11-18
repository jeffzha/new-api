/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createRootRoute,
  createRoute,
  createRouter,
  createMemoryHistory,
  RouterProvider,
} from '@tanstack/react-router'
import {
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { usageLogSchema } from '../../data/schema'
import { UsageAnalyticsPanel } from '../usage-analytics-panel'
import { UsageAnalyticsView } from '../usage-analytics-view'
import { UsageLogsProvider } from '../usage-logs-provider'

let client: QueryClient

it('fills the trend column and shows input, output and included cache totals without hover', () => {
  render(
    <UsageAnalyticsView
      showModelTable
      rows={[
        usageLogSchema.parse({
          id: 1,
          user_id: 1,
          created_at: 7200,
          type: 2,
          content: '',
          model_name: 'model-a',
          prompt_tokens: 800,
          completion_tokens: 200,
          other: JSON.stringify({ cache_tokens: 300 }),
        }),
      ]}
    />
  )
  const trend = screen.getByRole('region', { name: 'Token usage trend' })
  expect(trend).toHaveClass('flex', 'flex-col')
  expect(trend.querySelector('[data-slot="chart"]')).toHaveClass(
    'flex-1',
    'min-h-64'
  )
  const totals = within(trend).getByLabelText('Tokens')
  expect(
    within(totals)
      .getAllByRole('definition')
      .map((node) => node.textContent)
  ).toEqual(['800', '200', '300'])
  expect(
    within(trend).getByText('Cache reads are included in input tokens.')
  ).toBeVisible()
})

it('keeps every model in the readable legend when the chart groups smaller shares', () => {
  render(
    <UsageAnalyticsView
      rows={[
        'gpt-4.1',
        'claude-sonnet-4',
        'gemini-pro',
        'deepseek',
        'qwen',
        'small-model',
        'another-model',
      ].map((name, index) =>
        usageLogSchema.parse({
          id: index + 1,
          user_id: 1,
          created_at: 7200,
          type: 2,
          content: '',
          model_name: name,
          prompt_tokens: 100,
          completion_tokens: 0,
          quota: 0,
        })
      )}
    />
  )
  const legend = screen.getByRole('list', { name: 'Distribution details' })
  expect(within(legend).getAllByRole('listitem')).toHaveLength(7)
  expect(within(legend).getByText('small-model')).toBeVisible()
  expect(within(legend).getAllByText('14.3%')).toHaveLength(7)
  expect(screen.getByLabelText('Distribution total')).toHaveTextContent('700')
  expect(screen.getByText('Other: 200 (28.6%)')).toBeVisible()
})

it('shows a clear empty distribution instead of a fabricated total for no records', () => {
  render(<UsageAnalyticsView rows={[]} />)
  const models = screen.getByRole('region', { name: 'Model distribution' })
  expect(within(models).getByText('No data')).toBeVisible()
  expect(
    within(models).queryByLabelText('Distribution total')
  ).not.toBeInTheDocument()
})

it('labels each distribution with totals, shares and the selected metric without requiring hover', async () => {
  render(
    <UsageAnalyticsView
      showDimensions
      rows={[100, 300].map((tokens, index) =>
        usageLogSchema.parse({
          id: index + 1,
          user_id: 1,
          created_at: 7200,
          type: 2,
          content: '',
          model_name: `model-${index}`,
          group: 'default',
          prompt_tokens: tokens,
          completion_tokens: 0,
          quota: 500000,
          use_time: 1,
        })
      )}
    />
  )
  const models = screen.getByRole('region', { name: 'Model distribution' })
  expect(within(models).getByLabelText('Distribution total')).toHaveTextContent(
    '400'
  )
  expect(within(models).getByText('75.0%')).toBeVisible()
  expect(within(models).getByText('25.0%')).toBeVisible()
  await userEvent.click(screen.getByRole('tab', { name: 'Usage' }))
  expect(within(models).getByLabelText('Distribution total')).toHaveTextContent(
    '$2'
  )
  expect(within(models).getAllByText('50.0%')).toHaveLength(2)
})

it('keeps model request counts visible when token usage and cost are zero', async () => {
  render(
    <UsageAnalyticsView
      showModelTable
      rows={[
        usageLogSchema.parse({
          id: 1,
          user_id: 1,
          created_at: 7200,
          type: 2,
          content: '',
          model_name: 'free-model',
          group: 'default',
          prompt_tokens: 0,
          completion_tokens: 0,
          quota: 0,
          use_time: 1,
          other: JSON.stringify({ agency_standard_quota: 0 }),
        }),
      ]}
    />
  )
  const table = screen.getByRole('table', { name: 'Model distribution' })
  expect(
    within(table).getByRole('row', { name: /free-model 1 0/ })
  ).toBeVisible()
  await userEvent.click(screen.getByRole('tab', { name: 'Usage' }))
  expect(within(table).getByText('free-model')).toBeVisible()
})

function Fixture() {
  return (
    <UsageLogsProvider>
      <UsageAnalyticsPanel />
    </UsageLogsProvider>
  )
}
async function renderPanel() {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  useAuthStore.getState().auth.setUser({ id: 1, username: 'tester', role: 1 })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const logs = createRoute({
    getParentRoute: () => auth,
    path: '/usage-logs/$section',
    component: Fixture,
    validateSearch: (search: Record<string, unknown>) => search,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([logs])]),
    history: createMemoryHistory({ initialEntries: ['/usage-logs/common'] }),
  })
  await router.load()
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return router
}
function mockPendingAnalyticsPage() {
  let finish!: (value: unknown) => void
  const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
    const params = new URL(url, 'http://localhost').searchParams
    if (params.get('p') === '1') {
      return {
        data: {
          success: true,
          data: {
            total: 101,
            items: Array.from({ length: 100 }, (_, index) =>
              usageLogSchema.parse({
                id: index + 1,
                user_id: 1,
                created_at: 7200,
                type: 2,
                content: '',
                model_name: 'first-page-model',
                prompt_tokens: 1,
              })
            ),
          },
        },
      }
    }
    return new Promise((resolve) => {
      finish = resolve
    })
  })
  return { get, finish: (value: unknown) => finish(value) }
}
afterEach(() => {
  cleanup()
  client?.clear()
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
})
it('shows the first page immediately and clears loading when all pages finish', async () => {
  const pending = mockPendingAnalyticsPage()
  await renderPanel()
  const region = await screen.findByRole('region', { name: 'Usage analytics' })
  expect(region).toHaveAttribute('aria-busy', 'true')
  expect(within(region).getByText('Analyzed 100 of 101 records')).toBeVisible()
  expect(within(region).getByText('Loading...')).toBeVisible()
  await waitFor(() => expect(pending.get).toHaveBeenCalledTimes(2))
  await act(async () =>
    pending.finish({
      data: {
        success: true,
        data: {
          total: 101,
          items: [
            usageLogSchema.parse({
              id: 101,
              user_id: 1,
              created_at: 7200,
              type: 2,
              content: '',
              model_name: 'last-page-model',
              prompt_tokens: 1,
            }),
          ],
        },
      },
    })
  )
  expect(await screen.findByText('Analyzed 101 of 101 records')).toBeVisible()
  expect(region).toHaveAttribute('aria-busy', 'false')
  expect(within(region).queryByText('Loading...')).not.toBeInTheDocument()
  expect(
    within(region).getByRole('button', { name: 'last-page-model' })
  ).toBeVisible()
})
it('replaces partial charts with an error when a later page fails', async () => {
  const pending = mockPendingAnalyticsPage()
  await renderPanel()
  await screen.findByText('Analyzed 100 of 101 records')
  await waitFor(() => expect(pending.get).toHaveBeenCalledTimes(2))
  await act(async () =>
    pending.finish({
      data: { success: false, message: 'Later page unavailable' },
    })
  )
  expect(await screen.findByText('Failed to load')).toBeVisible()
  expect(
    screen.queryByRole('region', { name: 'Usage analytics' })
  ).not.toBeInTheDocument()
})
it('cancels an unfinished filter and does not show its partial data in the new filter', async () => {
  const pending = mockPendingAnalyticsPage()
  const router = await renderPanel()
  await screen.findByText('Analyzed 100 of 101 records')
  await waitFor(() => expect(pending.get).toHaveBeenCalledTimes(2))
  const previousSignal = pending.get.mock.calls[1][1]?.signal
  let finishNext!: (value: unknown) => void
  pending.get.mockImplementation(
    async () =>
      new Promise((resolve) => {
        finishNext = resolve
      })
  )
  await act(async () => {
    await router.navigate({ href: '/usage-logs/common?model=different-model' })
  })
  expect(previousSignal?.aborted).toBe(true)
  expect(
    screen.queryByText('Analyzed 100 of 101 records')
  ).not.toBeInTheDocument()
  expect(screen.getByRole('status', { name: 'Loading...' })).toBeVisible()
  await act(async () =>
    pending.finish({
      data: { success: true, data: { total: 101, items: [] } },
    })
  )
  expect(
    screen.queryByRole('region', { name: 'Usage analytics' })
  ).not.toBeInTheDocument()
  await waitFor(() => expect(finishNext).toBeTypeOf('function'))
  await act(async () =>
    finishNext({
      data: { success: true, data: { total: 0, items: [] } },
    })
  )
  expect(await screen.findByText('Analyzed 0 of 0 records')).toBeVisible()
})
it('selecting a model or group updates the shared table URL filters without losing the other filters', async () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'tester', role: 1 })
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        total: 1,
        items: [
          {
            id: 1,
            user_id: 1,
            created_at: 7200,
            type: 2,
            content: '',
            model_name: 'model-a',
            group: 'premium',
            prompt_tokens: 100,
            completion_tokens: 20,
            quota: 30,
            use_time: 2,
          },
        ],
      },
    },
  })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const logs = createRoute({
    getParentRoute: () => auth,
    path: '/usage-logs/$section',
    component: Fixture,
    validateSearch: (search: Record<string, unknown>) => search,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([logs])]),
    history: createMemoryHistory({
      initialEntries: ['/usage-logs/common?token=production&page=3'],
    }),
  })
  await router.load()
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  const models = await screen.findByRole('region', {
    name: 'Model distribution',
  })
  expect(
    within(models)
      .getAllByRole('columnheader')
      .map((cell) => cell.textContent)
  ).toEqual(['Model', 'Requests', 'Tokens', 'Actual', 'Standard'])
  expect(within(models).getByText('Not provided')).toBeVisible()
  await userEvent.click(within(models).getByRole('button', { name: 'model-a' }))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      model: 'model-a',
      token: 'production',
      page: 1,
    })
  )
  const groups = await screen.findByRole('region', {
    name: 'Group distribution',
  })
  await userEvent.click(within(groups).getByRole('button', { name: 'premium' }))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      model: 'model-a',
      group: 'premium',
      token: 'production',
      page: 1,
    })
  )
  await waitFor(() =>
    expect(api.get).toHaveBeenCalledWith(
      expect.stringContaining('model_name=model-a'),
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    )
  )
  expect(
    screen.getByRole('region', { name: 'Token usage trend' })
  ).toBeVisible()
})
