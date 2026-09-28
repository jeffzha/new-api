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

import { UsageAnalyticsPanel } from '../usage-analytics-panel'
import { UsageLogsProvider } from '../usage-logs-provider'

let client: QueryClient
function Fixture() {
  return (
    <UsageLogsProvider>
      <UsageAnalyticsPanel />
    </UsageLogsProvider>
  )
}
afterEach(() => {
  cleanup()
  client?.clear()
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
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
      expect.stringContaining('model_name=model-a')
    )
  )
  expect(
    screen.getByRole('region', { name: 'Token usage trend' })
  ).toBeVisible()
})
