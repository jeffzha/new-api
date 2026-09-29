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
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { ThemeCustomizationProvider } from '@/context/theme-customization-provider'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { UsageLogs } from '..'

let client: QueryClient

afterEach(() => {
  cleanup()
  client?.clear()
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
  document.cookie = 'theme_preset=; Max-Age=0; path=/'
})

async function renderPage(preset: string) {
  document.cookie = `theme_preset=${preset}; path=/`
  vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
  useAuthStore.getState().auth.setUser({ id: 1, username: 'preview', role: 1 })
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/user/self/groups') {
      return {
        data: { success: true, data: { default: { desc: '', ratio: 1 } } },
      }
    }
    if (url.includes('/stat')) {
      return { data: { success: true, data: { rpm: 0, tpm: 0, quota: 0 } } }
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
    validateSearch: (search: Record<string, unknown>) => search,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([logs])]),
    history: createMemoryHistory({
      initialEntries: ['/usage-logs/common?model=production-model'],
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
    expect(
      screen.getByRole('button', { name: 'Download usage bill' })
    ).toBeVisible()
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
    expect(
      screen.getByRole('button', { name: 'Download usage bill' })
    ).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Search' })
    ).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('tab', { name: 'Log records' }))
    expect(await screen.findByRole('button', { name: 'Search' })).toBeVisible()
    expect(
      screen.getByRole('button', { name: 'Download usage bill' })
    ).toBeVisible()
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
