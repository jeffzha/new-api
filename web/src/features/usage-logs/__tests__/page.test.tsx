/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
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

import { ThemeCustomizationProvider } from '@/context/theme-customization-provider'
import { api } from '@/lib/api'
import { Route as UsageLogsRoute } from '@/routes/_authenticated/usage-logs/$section'
import { useAuthStore } from '@/stores/auth-store'

import { UsageLogs } from '..'
import type { UsageLog } from '../data/schema'

let client: QueryClient

afterEach(() => {
  cleanup()
  client?.clear()
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
  document.cookie = 'theme_preset=; Max-Age=0; path=/'
})

type LogResponse = {
  success: boolean
  data?: { items: UsageLog[]; total: number }
  message?: string
}

async function renderPage(
  preset: string,
  options: {
    initialEntry?: string
    role?: number
    loadLogs?: (type: string | null) => Promise<LogResponse>
  } = {}
) {
  document.cookie = `theme_preset=${preset}; path=/`
  vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
  useAuthStore.getState().auth.setUser({
    id: 1,
    username: 'preview',
    role: options.role ?? 1,
  })
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/group/') {
      return { data: { success: true, data: ['default'] } }
    }
    if (url === '/api/user/self/groups') {
      return {
        data: { success: true, data: { default: { desc: '', ratio: 1 } } },
      }
    }
    if (url.includes('/stat')) {
      return { data: { success: true, data: { rpm: 0, tpm: 0, quota: 0 } } }
    }
    if (
      options.loadLogs &&
      (url.startsWith('/api/log/?') || url.startsWith('/api/log/self?'))
    ) {
      return {
        data: await options.loadLogs(
          new URL(url, 'http://localhost').searchParams.get('type')
        ),
      }
    }
    return { data: { success: true, data: { items: [], total: 0 } } }
  })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  client.setQueryData(['status'], {})
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const logs = createRoute({
    getParentRoute: () => auth,
    path: '/usage-logs/$section',
    component: UsageLogs,
    validateSearch: UsageLogsRoute.options.validateSearch,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([logs])]),
    history: createMemoryHistory({
      initialEntries: [
        options.initialEntry ?? '/usage-logs/common?model=production-model',
      ],
    }),
  })
  await router.load()
  render(
    <QueryClientProvider client={client}>
      <ThemeCustomizationProvider>
        <RouterProvider router={router} />
      </ThemeCustomizationProvider>
    </QueryClientProvider>
  )
  await screen.findByRole('button', { name: 'Search' })
  return router
}

it.each(['signal-console', 'prism-console'])(
  '%s preserves bill download and URL filters when switching analytics and returning',
  async (preset) => {
    const router = await renderPage(preset)
    expect(screen.getByRole('button', { name: 'Download bill' })).toBeVisible()
    expect(screen.getByRole('tab', { name: 'Log records' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    expect(
      screen.queryByRole('region', { name: 'Usage analytics' })
    ).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('tab', { name: 'Usage analytics' }))
    expect(
      await screen.findByRole('region', { name: 'Usage analytics' })
    ).toBeVisible()
    expect(screen.getByRole('button', { name: 'Download bill' })).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Search' })
    ).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('tab', { name: 'Log records' }))
    expect(await screen.findByRole('button', { name: 'Search' })).toBeVisible()
    expect(screen.getByRole('button', { name: 'Download bill' })).toBeVisible()
    await waitFor(() =>
      expect(router.state.location.search).toMatchObject({
        model: 'production-model',
      })
    )
    expect(
      screen.getByRole('button', { name: 'Go to next page' })
    ).toBeVisible()
  }
)

it('default theme retains the original log list without workspace view tabs', async () => {
  await renderPage('default')
  expect(
    screen.queryByRole('tab', { name: 'Log records' })
  ).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Search' })).toBeVisible()
})

const consumeLog: UsageLog = {
  id: 1,
  user_id: 1,
  created_at: 1,
  type: 2,
  content: '',
  username: 'preview',
  token_name: '',
  model_name: 'consumed-model',
  quota: 100,
  prompt_tokens: 1,
  completion_tokens: 1,
  use_time: 1,
  is_stream: false,
  channel: 1,
  channel_name: '',
  token_id: 1,
  group: 'default',
  ip: '',
  request_id: '',
  upstream_request_id: '',
  other: '{}',
}

it.each([
  { role: 1, outcome: 'adjustment' },
  { role: 10, outcome: 'adjustment' },
  { role: 1, outcome: 'empty' },
  { role: 1, outcome: 'failure' },
])(
  'role $role querying quota adjustment replaces consumption ($outcome)',
  async ({ role, outcome }) => {
    let finishAdjustment!: (response: LogResponse) => void
    const adjustment = new Promise<LogResponse>((resolve) => {
      finishAdjustment = resolve
    })
    const loadLogs = vi.fn(
      async (type: string | null): Promise<LogResponse> =>
        type === '8'
          ? adjustment
          : { success: true, data: { items: [consumeLog], total: 101 } }
    )
    const router = await renderPage('default', {
      role,
      initialEntry: '/usage-logs/common?page=2&type=["2"]&group=default',
      loadLogs,
    })
    expect(await screen.findByText('consumed-model')).toBeVisible()
    await userEvent.click(screen.getByRole('combobox', { name: 'Type' }))
    await userEvent.click(
      screen.getByRole('option', { name: 'Quota Adjustment' })
    )
    await userEvent.click(screen.getByRole('button', { name: 'Search' }))
    await waitFor(() => expect(loadLogs).toHaveBeenCalledWith('8'))
    expect(router.state.location.search).toMatchObject({
      type: ['8'],
      page: 1,
      group: 'default',
    })
    expect(screen.queryByText('consumed-model')).not.toBeInTheDocument()
    await act(async () => {
      if (outcome === 'failure') {
        finishAdjustment({ success: false, message: 'Adjustment query failed' })
      } else {
        const items =
          outcome === 'empty'
            ? []
            : [
                {
                  ...consumeLog,
                  id: 2,
                  type: 1,
                  display_type: 8,
                  model_name: '',
                  content: 'Historical quota adjustment',
                },
              ]
        finishAdjustment({
          success: true,
          data: { items, total: items.length },
        })
      }
    })
    expect(screen.queryByText('consumed-model')).not.toBeInTheDocument()
    if (outcome === 'adjustment') {
      expect(
        await within(screen.getByRole('table')).findByText('Quota Adjustment')
      ).toBeVisible()
      expect(screen.getByText('Historical quota adjustment')).toBeVisible()
    } else {
      expect(await screen.findByText('No Logs Found')).toBeVisible()
    }
  }
)

it.each(['8', '["8"]'])(
  'loading a quota-adjustment URL with type=%s preserves the filter',
  async (type) => {
    const loadLogs = vi.fn(
      async (): Promise<LogResponse> => ({
        success: true,
        data: { items: [], total: 0 },
      })
    )
    const router = await renderPage('default', {
      initialEntry: `/usage-logs/common?type=${type}`,
      loadLogs,
    })
    await waitFor(() => expect(loadLogs).toHaveBeenCalledWith('8'))
    expect(router.state.location.search.type).toEqual(['8'])
    expect(screen.getByRole('combobox', { name: 'Type' })).toHaveTextContent(
      'Quota Adjustment'
    )
  }
)
