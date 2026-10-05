/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
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
import i18next from 'i18next'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  ThemeCustomizationProvider,
  useThemeCustomization,
} from '@/context/theme-customization-provider'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { buildSignalUsage } from '../../../lib/signal-usage'
import { OverviewDashboard } from '../overview-dashboard'
import { SignalConsoleOverview } from '../signal-console-overview'

function LegacyOverview() {
  const { customization } = useThemeCustomization()
  return customization.preset === 'signal-console' ? (
    <SignalConsoleOverview />
  ) : (
    <OverviewDashboard />
  )
}

let client: QueryClient
let usageFailure = false
let empty = false
let hold = false
let performanceCalls = 0
const now = Math.floor(Date.now() / 3600000) * 3600
beforeEach(() => {
  usageFailure = false
  empty = false
  hold = false
  performanceCalls = 0
  document.cookie = 'theme_preset=signal-console; path=/'
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
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
    if (url === '/api/status') {
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
    if (url === '/api/data/self') {
      if (hold) return new Promise(() => {})
      if (usageFailure) throw new Error('Unavailable')
      return {
        data: {
          success: true,
          data: empty
            ? []
            : [
                {
                  created_at: now,
                  count: 12,
                  quota: 30,
                  token_used: 500,
                  model_name: 'model-a',
                },
              ],
        },
      }
    }
    if (url === '/api/perf-metrics/summary') {
      performanceCalls++
      return { data: { success: true, data: { models: [] } } }
    }
    if (url === '/api/token/?p=1&size=10') {
      return { data: { success: true, data: { items: [] } } }
    }
    if (url === '/api/user/models') return { data: { success: true, data: [] } }
    throw new Error(`Unexpected URL: ${url}`)
  })
})
afterEach(async () => {
  cleanup()
  await i18next.changeLanguage('en')
  client.clear()
  document.cookie = 'theme_preset=; Max-Age=0; path=/'
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
})
async function mountOverview() {
  const router = createRouter({
    routeTree: createRootRoute({ component: LegacyOverview }),
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

describe('signal overview', () => {
  it('shows model distribution from the selected reporting window', async () => {
    await mountOverview()
    const distribution = await screen.findByRole('region', {
      name: 'Model distribution',
    })
    expect(await within(distribution).findByText('model-a')).toBeVisible()
    expect(within(distribution).getByText('12 Requests')).toBeVisible()
    expect(within(distribution).getByText('500')).toBeVisible()
  })
  it.each(['zhCN', 'zhTW'])(
    'renders dates and source data in %s and survives switching language',
    async (language) => {
      await i18next.changeLanguage(language)
      await mountOverview()
      await userEvent.setup().click(await screen.findByText('signal.viewData'))
      expect(within(screen.getByRole('table')).getByText('12')).toBeVisible()
      await act(async () => {
        await i18next.changeLanguage(language === 'zhCN' ? 'zhTW' : 'zhCN')
      })
      expect(screen.getByTestId('signal-overview')).toBeInTheDocument()
      expect(within(screen.getByRole('table')).getByText('12')).toBeVisible()
    }
  )
  it('stacks primary content and shortcuts on narrow screens and preserves long account values', async () => {
    useAuthStore.getState().auth.setUser({
      id: 1,
      username: 'member',
      role: 1,
      quota: 9007199254740991,
    })
    await mountOverview()
    const account = screen.getByRole('complementary', {
      name: 'signal.account',
    })
    expect(account.parentElement).toHaveClass(
      'grid',
      'min-w-0',
      'xl:grid-cols-[minmax(0,1fr)_19rem]'
    )
    expect(
      screen.getByRole('navigation', { name: 'Quick actions' })
    ).toHaveClass('grid-cols-1', 'sm:grid-cols-2', 'xl:grid-cols-4')
    expect(account.querySelector('.tabular-nums')).toHaveClass('break-words')
  })
  it('shows period usage separately from lifetime totals and never requests admin metrics for a member', async () => {
    await mountOverview()
    expect(
      await screen.findByText('12', { selector: 'dd' })
    ).toBeInTheDocument()
    expect(screen.getByText('99')).toBeInTheDocument()
    expect(performanceCalls).toBe(0)
    expect(
      screen.getByRole('navigation', { name: 'Quick actions' })
    ).toBeInTheDocument()
  })
  it('changes the reporting window and exposes keyboard-accessible source data', async () => {
    await mountOverview()
    const user = userEvent.setup()
    await user.click(screen.getByRole('tab', { name: 'signal.7d' }))
    expect(screen.getByRole('tab', { name: 'signal.7d' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    await waitFor(() =>
      expect(api.get).toHaveBeenCalledWith(
        '/api/data/self',
        expect.objectContaining({
          params: expect.objectContaining({
            start_timestamp: now + 3600 - 7 * 86400,
          }),
        })
      )
    )
    await user.click(await screen.findByText('signal.viewData'))
    expect(
      within(screen.getByRole('table')).getByText('12')
    ).toBeInTheDocument()
  })
  it('restores the original overview and persists Default when restore is clicked', async () => {
    await mountOverview()
    await userEvent
      .setup()
      .click(screen.getByRole('button', { name: 'signal.restore' }))
    await waitFor(() =>
      expect(screen.queryByTestId('signal-overview')).not.toBeInTheDocument()
    )
    expect(document.cookie).toContain('theme_preset=default')
    expect(await screen.findByText('Usage at a glance')).toBeInTheDocument()
  })
  it('shows loading placeholders instead of zero usage while the request is pending', async () => {
    hold = true
    await mountOverview()
    expect(
      screen.getByRole('status', { name: 'Loading...' })
    ).toBeInTheDocument()
    expect(screen.queryByText('No recent usage')).not.toBeInTheDocument()
  })
  it('shows a helpful empty state when the selected period has no rows', async () => {
    empty = true
    await mountOverview()
    expect(await screen.findByText('No recent usage')).toBeInTheDocument()
    expect(screen.queryByText('signal.viewData')).not.toBeInTheDocument()
  })
  it('shows error and retries instead of claiming there is no traffic', async () => {
    usageFailure = true
    await mountOverview()
    expect(await screen.findByText('Failed to load')).toBeInTheDocument()
    expect(screen.queryByText('No recent usage')).not.toBeInTheDocument()
    usageFailure = false
    await userEvent.setup().click(screen.getByRole('button', { name: 'Retry' }))
    expect(
      await screen.findByText('12', { selector: 'dd' })
    ).toBeInTheDocument()
  })
  it('loads the existing performance panel for an administrator', async () => {
    useAuthStore
      .getState()
      .auth.setUser({ id: 1, username: 'admin', role: 10, quota: 10000 })
    await mountOverview()
    await waitFor(() => expect(performanceCalls).toBe(1))
  })
  it('aggregates shared timestamps and preserves missing buckets without invented traffic', () => {
    const result = buildSignalUsage([
      { created_at: 7200, count: 3, quota: 20 },
      { created_at: 0, count: 4, token_used: 100 },
      { created_at: 7200, count: 5, quota: 10 },
    ])
    expect(result.series).toEqual([
      { timestamp: 0, requests: 4 },
      { timestamp: 7200, requests: 8 },
    ])
    expect(result.requests).toBe(12)
    expect(result.quota).toBe(30)
    expect(result.tokens).toBe(100)
  })
})
