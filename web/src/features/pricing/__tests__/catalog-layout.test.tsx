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
import { useAuthStore } from '@/stores/auth-store'

import { Pricing } from '..'
import { CustomerPricingNotice } from '../components/customer-pricing-status'

let client: QueryClient

// The model-detail canvas is a browser boundary, not part of catalog navigation.
vi.mock('@visactor/react-vchart', () => ({ VChart: () => null }))

afterEach(() => {
  cleanup()
  client?.clear()
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
  document.cookie = 'theme_preset=; Max-Age=0; path=/'
})

it.each(['default', 'signal-console', 'prism-console'])(
  '%s shows agency pricing notice and discount details',
  async (preset) => {
    document.cookie = `theme_preset=${preset}; path=/`
    render(
      <ThemeCustomizationProvider>
        <CustomerPricingNotice
          models={[
            {
              id: 1,
              model_name: 'gpt-4.1',
              quota_type: 0,
              model_ratio: 1,
              completion_ratio: 2,
              sales_bps: 9000,
              enable_groups: ['default'],
            },
          ]}
        />
      </ThemeCustomizationProvider>
    )

    expect(screen.getByRole('alert')).toHaveTextContent(
      'You are using agency-exclusive pricing.'
    )
    await userEvent.click(
      screen.getByRole('button', { name: 'View exclusive pricing' })
    )
    expect(screen.getByRole('dialog')).toHaveTextContent('90.00%')
    expect(screen.getByRole('dialog')).toHaveTextContent('Save 10.00%')
  }
)

it.each(['signal-console', 'prism-console'])(
  '%s keeps search and provider tabs outside the scrollable model results',
  async (preset) => {
    document.cookie = `theme_preset=${preset}; path=/`
    vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
    vi.spyOn(api, 'get').mockImplementation(async (url) => {
      if (url === '/api/pricing') {
        return {
          data: {
            success: true,
            data: [
              {
                id: 1,
                model_name: 'gpt-4.1',
                vendor_id: 1,
                quota_type: 0,
                model_ratio: 1,
                completion_ratio: 2,
                sales_bps: 9000,
                enable_groups: ['default'],
              },
              {
                id: 2,
                model_name: 'claude-sonnet-4',
                vendor_id: 2,
                quota_type: 0,
                model_ratio: 1,
                completion_ratio: 2,
                sales_bps: 8000,
                enable_groups: ['default'],
              },
            ],
            vendors: [
              { id: 1, name: 'OpenAI' },
              { id: 2, name: 'Anthropic' },
            ],
            usable_group: { default: { desc: '', ratio: 1 } },
            group_ratio: { default: 1 },
            pricing_scope: 'agency',
          },
        }
      }
      if (url === '/api/notice') {
        return { data: { success: true, data: '' } }
      }
      if (url === '/api/status') {
        return { data: { success: true, data: {} } }
      }
      throw new Error(`Unexpected GET ${url}`)
    })
    client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    client.setQueryData(['status'], {})
    const root = createRootRoute()
    const pricing = createRoute({
      getParentRoute: () => root,
      path: '/pricing/',
      component: Pricing,
    })
    const router = createRouter({
      routeTree: root.addChildren([pricing]),
      history: createMemoryHistory({ initialEntries: ['/pricing'] }),
    })
    await router.load()
    render(
      <QueryClientProvider client={client}>
        <ThemeCustomizationProvider>
          <RouterProvider router={router} />
        </ThemeCustomizationProvider>
      </QueryClientProvider>
    )
    const providers = await screen.findByRole('tablist', { name: 'Providers' })
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'You are using agency-exclusive pricing.'
    )
    expect(
      await screen.findByRole('button', { name: 'View exclusive pricing' })
    ).toBeVisible()
    const results = screen.getByRole('region', { name: 'Models' })
    expect(results).toHaveClass('min-h-0', 'overflow-y-auto')
    expect(results).toHaveAttribute('tabindex', '0')
    expect(results).not.toContainElement(providers)
    expect(results).not.toContainElement(
      screen.getByRole('textbox', { name: 'Search models' })
    )
    await userEvent.click(
      await within(providers).findByRole('tab', { name: 'Anthropic' })
    )
    expect(
      within(providers).getByRole('tab', { name: 'Anthropic' })
    ).toHaveAttribute('aria-selected', 'true')
    expect(within(results).getByText('claude-sonnet-4')).toBeVisible()
    expect(within(results).queryByText('gpt-4.1')).not.toBeInTheDocument()
    await userEvent.type(
      screen.getByRole('textbox', { name: 'Search models' }),
      'unavailable-model'
    )
    await waitFor(() =>
      expect(
        within(results).queryByText('claude-sonnet-4')
      ).not.toBeInTheDocument()
    )
    expect(
      within(providers).getByRole('tab', { name: 'Anthropic' })
    ).toBeVisible()
  }
)
