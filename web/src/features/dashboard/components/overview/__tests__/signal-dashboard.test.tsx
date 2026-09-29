/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
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
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ThemeCustomizationProvider } from '@/context/theme-customization-provider'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { OverviewThemeView } from '../overview-theme-view'

let client: QueryClient
let fail = false
let empty = false
let truncated = false
const now = Math.floor(Date.now() / 1000)
beforeEach(() => {
  fail = false
  empty = false
  truncated = false
  document.cookie = 'theme_preset=signal-console; path=/'
  useAuthStore.getState().auth.setUser({
    id: 1,
    username: 'tester',
    role: 1,
    quota: 10000,
    used_quota: 700,
    request_count: 99,
  })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    const path = String(url)
    if (path === '/api/data/self') {
      return {
        data: {
          success: true,
          data: [
            {
              created_at: now,
              count: 12400,
              token_used: 8800000,
              quota: 64000,
            },
          ],
        },
      }
    }
    if (path === '/api/status') {
      return {
        data: {
          data: {
            api_info_enabled: false,
            announcements_enabled: false,
            faq_enabled: false,
            uptime_kuma_enabled: false,
          },
        },
      }
    }
    if (path.includes('/api/token/')) {
      return { data: { success: true, data: { total: 3, items: [] } } }
    }
    if (path.includes('/stat?')) {
      return { data: { success: true, data: { rpm: 2, tpm: 40, quota: 30 } } }
    }
    if (path.startsWith('/api/log/self?')) {
      if (fail) throw new Error('Unavailable')
      let total = 1
      if (empty) total = 0
      else if (truncated) total = 9000
      return {
        data: {
          success: true,
          data: {
            total,
            items: empty
              ? []
              : [
                  {
                    id: 1,
                    user_id: 1,
                    created_at: now,
                    type: 2,
                    content: '',
                    model_name: 'model-a',
                    token_name: 'production',
                    group: 'default',
                    prompt_tokens: 100,
                    completion_tokens: 20,
                    quota: 30,
                    use_time: 2,
                  },
                ],
          },
        },
      }
    }
    throw new Error(`Unexpected URL: ${path}`)
  })
})
afterEach(() => {
  cleanup()
  client.clear()
  vi.restoreAllMocks()
  document.cookie = 'theme_preset=; Max-Age=0; path=/'
  useAuthStore.setState(useAuthStore.getInitialState(), true)
})
async function mount() {
  const router = createRouter({
    routeTree: createRootRoute({ component: OverviewThemeView }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  await router.load()
  render(
    <QueryClientProvider client={client}>
      <ThemeCustomizationProvider>
        <RouterProvider router={router} />
      </ThemeCustomizationProvider>
    </QueryClientProvider>
  )
  await screen.findByTestId('signal-overview')
}

describe('Signal dashboard', () => {
  it('Prism retains the complete overview and recent usage instead of switching layouts', async () => {
    document.cookie = 'theme_preset=prism-console; path=/'
    await mount()
    expect(document.body).toHaveAttribute('data-theme-preset', 'prism-console')
    const recent = await screen.findByRole('region', { name: 'Recent usage' })
    expect(await within(recent).findByText('model-a')).toBeVisible()
    const table = within(recent).getByRole('table', { name: 'Recent usage' })
    expect(
      within(table)
        .getAllByRole('columnheader')
        .map((cell) => cell.textContent)
    ).toEqual(['Model', 'API Key', 'Tokens', 'Usage'])
    expect(within(table).getByText('production')).toBeVisible()
    expect(within(table).getByText('Group: default')).toBeVisible()
    expect(within(table).getByText('120')).toBeVisible()
    expect(
      screen
        .getByTestId('signal-overview')
        .querySelectorAll('dl > .signal-metric')
    ).toHaveLength(8)
  })
  it('shows eight metrics and recent self usage without requesting admin logs', async () => {
    await mount()
    const recent = await screen.findByRole('region', { name: 'Recent usage' })
    expect(await within(recent).findByText('model-a')).toBeVisible()
    expect(screen.getByText('Total keys')).toBeVisible()
    expect(await screen.findByText('12,400')).toBeVisible()
    expect(
      screen.getByRole('navigation', { name: 'Quick actions' })
    ).toBeVisible()
    expect(api.get).not.toHaveBeenCalledWith(
      expect.stringMatching(/^\/api\/log\?/)
    )
    expect(
      screen
        .getByTestId('signal-overview')
        .querySelectorAll('dl > .signal-metric')
    ).toHaveLength(8)
  })
  it('changing the period refetches self logs and selects the requested period', async () => {
    await mount()
    await userEvent.click(screen.getByRole('tab', { name: 'Today' }))
    expect(screen.getByRole('tab', { name: 'Today' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    const midnight = Math.floor(
      new Date(new Date().setHours(0, 0, 0, 0)).getTime() / 1000
    )
    await waitFor(() =>
      expect(api.get).toHaveBeenCalledWith(
        expect.stringContaining(`start_timestamp=${midnight}`)
      )
    )
  })
  it('failed log queries show retry instead of invented zero metrics', async () => {
    fail = true
    await mount()
    expect(await screen.findByText('Failed to load')).toBeVisible()
    expect(screen.queryByText('No recent usage')).not.toBeInTheDocument()
    fail = false
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(
      await screen.findByRole('region', { name: 'Recent usage' })
    ).toBeVisible()
  })
  it('an empty period keeps charts and recent activity in explicit empty states', async () => {
    empty = true
    await mount()
    expect(await screen.findByText('No recent usage')).toBeVisible()
    expect(screen.queryByText('model-a')).not.toBeInTheDocument()
  })
  it('an incomplete data set is explicitly labeled as limited rather than full-period totals', async () => {
    truncated = true
    await mount()
    expect(
      await screen.findByText(
        'Analysis is limited to the latest 2,000 matching records. Narrow the time range for complete totals.'
      )
    ).toBeVisible()
  })
})
