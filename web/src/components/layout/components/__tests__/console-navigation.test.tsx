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
import { afterEach, describe, expect, it, vi } from 'vitest'

import { SidebarProvider, SidebarTrigger } from '@/components/ui/sidebar'
import { LayoutProvider } from '@/context/layout-provider'
import {
  ThemeCustomizationProvider,
  useThemeCustomization,
} from '@/context/theme-customization-provider'
import type { ThemePreset } from '@/lib/theme-customization'
import { useAuthStore } from '@/stores/auth-store'

import { AppSidebar } from '../app-sidebar'
import { ConsoleProductNav } from '../console-product-nav'

let client: QueryClient
function ThemeToggle() {
  const { setPreset } = useThemeCustomization()
  return (
    <>
      <button type='button' onClick={() => setPreset('prism-console')}>
        Prism
      </button>
      <button type='button' onClick={() => setPreset('default')}>
        Default theme
      </button>
    </>
  )
}

async function mount(
  options: {
    preset?: ThemePreset
    role?: number
    path?: string
    status?: object
  } = {}
) {
  document.cookie = `theme_preset=${options.preset ?? 'signal-console'}; path=/`
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'designer', role: options.role ?? 1 })
  client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  client.setQueryData(['status'], options.status ?? {})
  const navigate = vi.fn((event: React.MouseEvent) => event.preventDefault())
  function Shell() {
    return (
      <ThemeCustomizationProvider>
        <LayoutProvider>
          <SidebarProvider>
            <SidebarTrigger />
            <AppSidebar />
            <ThemeToggle />
            <ConsoleProductNav
              console
              onNavigate={navigate}
              links={[
                { title: 'Home', href: '/' },
                { title: 'Console', href: '/dashboard' },
                { title: 'Model Square', href: '/pricing' },
                { title: 'Rankings', href: '/rankings', disabled: true },
                {
                  title: 'Docs',
                  href: 'https://docs.example.com',
                  external: true,
                },
                { title: 'About', href: '/about' },
              ]}
            />
          </SidebarProvider>
        </LayoutProvider>
      </ThemeCustomizationProvider>
    )
  }
  const root = createRootRoute({ component: Shell })
  const routes = [
    '/dashboard/overview',
    '/keys',
    '/security',
    '/profile',
    '/wallet',
    '/usage-logs/audit',
    '/usage-logs/drawing',
    '/system-settings/site/system-info',
  ].map((path) => createRoute({ getParentRoute: () => root, path }))
  const router = createRouter({
    routeTree: root.addChildren(routes),
    history: createMemoryHistory({
      initialEntries: [options.path ?? '/dashboard/overview'],
    }),
  })
  await router.load()
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  await screen.findByRole('navigation', { name: 'Platform navigation' })
  return { router, navigate }
}

afterEach(() => {
  cleanup()
  client?.clear()
  useAuthStore.getState().auth.reset()
  for (const cookie of document.cookie.split(';')) {
    document.cookie = `${cookie.split('=')[0].trim()}=; max-age=0; path=/`
  }
  document.body.removeAttribute('data-theme-preset')
})

describe('Shared console navigation', () => {
  it.each(['signal-console', 'prism-console'] as const)(
    '%s preserves original groups and selects the clicked destination',
    async (preset) => {
      const { router } = await mount({ preset, role: 100 })
      expect(
        within(screen.getByRole('navigation', { name: 'General' }))
          .getAllByRole('link')
          .map((link) => link.textContent)
      ).toEqual([
        'Overview',
        'Dashboard',
        'API Keys',
        'Usage Logs',
        'Audit Logs',
        'Task Logs',
      ])
      expect(
        within(screen.getByRole('navigation', { name: 'Personal' }))
          .getAllByRole('link')
          .map((link) => link.textContent)
      ).toEqual(['Wallet', 'Profile', 'Security & Access'])
      expect(
        within(screen.getByRole('navigation', { name: 'Admin' }))
          .getAllByRole('link')
          .map((link) => link.textContent)
      ).toEqual([
        'Channels',
        'Models',
        'Users',
        'Redemption Codes',
        'Subscriptions',
        'Model mock scheduling',
        'System Info',
        'Workbench administration',
        'Task Plugins',
        'System Settings',
      ])
      expect(screen.getByRole('navigation', { name: 'Chat' })).toBeVisible()
      expect(
        screen.queryByRole('tablist', { name: 'Workspace areas' })
      ).not.toBeInTheDocument()
      expect(screen.getByRole('link', { name: 'Overview' })).toHaveAttribute(
        'aria-current',
        'page'
      )
      await userEvent.click(screen.getByRole('link', { name: 'API Keys' }))
      await waitFor(() => expect(router.state.location.pathname).toBe('/keys'))
      expect(screen.getByRole('link', { name: 'API Keys' })).toHaveAttribute(
        'aria-current',
        'page'
      )
      expect(screen.getByRole('link', { name: 'API Keys' })).toHaveAttribute(
        'data-active'
      )
      expect(
        screen.getByRole('link', { name: 'Overview' })
      ).not.toHaveAttribute('aria-current')
    }
  )

  it('keyboard collapse hides a group and navigation into it reopens it', async () => {
    const { router } = await mount()
    const group = screen.getByRole('button', { name: 'Personal' })
    group.focus()
    await userEvent.keyboard('{Enter}')
    expect(group).toHaveAttribute('aria-expanded', 'false')
    expect(
      screen.queryByRole('link', { name: 'Profile' })
    ).not.toBeInTheDocument()
    await act(() => router.navigate({ to: '/profile' }))
    expect(
      await screen.findByRole('link', { name: 'Profile' })
    ).toHaveAttribute('aria-current', 'page')
    expect(group).toHaveAttribute('aria-expanded', 'true')
  })

  it('theme switching and browser back preserve the current destination', async () => {
    const { router } = await mount()
    await userEvent.click(screen.getByRole('link', { name: 'API Keys' }))
    await userEvent.click(screen.getByRole('button', { name: 'Prism' }))
    expect(screen.getByRole('link', { name: 'API Keys' })).toHaveAttribute(
      'aria-current',
      'page'
    )
    await act(() => router.history.back())
    await waitFor(() =>
      expect(screen.getByRole('link', { name: 'Overview' })).toHaveAttribute(
        'aria-current',
        'page'
      )
    )
    await userEvent.click(screen.getByRole('button', { name: 'Default theme' }))
    expect(screen.getByRole('link', { name: 'Overview' })).toBeVisible()
    expect(screen.getByRole('link', { name: 'API Keys' })).toBeVisible()
  })

  it('normal users and disabled modules do not reappear in compact navigation', async () => {
    await mount({
      status: {
        SidebarModulesAdmin: JSON.stringify({
          console: { enabled: true, token: false },
          chat: { enabled: false },
        }),
      },
    })
    expect(
      screen.queryByRole('link', { name: 'API Keys' })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('navigation', { name: 'Chat' })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('navigation', { name: 'Admin' })
    ).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Wallet' })).toBeVisible()
    expect(
      screen.queryByRole('link', { name: 'Subscriptions' })
    ).not.toBeInTheDocument()
  })

  it('admins keep billing management in Admin while super-admin tools stay hidden', async () => {
    await mount({ role: 10 })
    const admin = screen.getByRole('navigation', { name: 'Admin' })
    expect(within(admin).getByRole('link', { name: 'Channels' })).toBeVisible()
    expect(
      within(admin).getByRole('link', { name: 'Subscriptions' })
    ).toBeVisible()
    expect(
      within(admin).queryByRole('link', { name: 'System Info' })
    ).not.toBeInTheDocument()
    expect(
      within(screen.getByRole('navigation', { name: 'Personal' })).queryByRole(
        'link',
        { name: 'Subscriptions' }
      )
    ).not.toBeInTheDocument()
  })

  it('icon mode retains direct links even when their group was collapsed', async () => {
    await mount()
    await userEvent.click(screen.getByRole('button', { name: 'General' }))
    await userEvent.click(
      screen.getAllByRole('button', { name: 'Toggle Sidebar' })[0]
    )
    expect(screen.getByRole('link', { name: 'API Keys' })).toBeVisible()
    await userEvent.click(screen.getByRole('link', { name: 'API Keys' }))
    expect(screen.getByRole('link', { name: 'API Keys' })).toHaveAttribute(
      'aria-current',
      'page'
    )
    await userEvent.click(
      screen.getAllByRole('button', { name: 'Toggle Sidebar' })[0]
    )
    expect(screen.getByRole('button', { name: 'General' })).toHaveAttribute(
      'aria-expanded',
      'true'
    )
  })

  it.each([
    ['/usage-logs/audit?view=all', 'Audit Logs'],
    ['/usage-logs/drawing', 'Task Logs'],
    ['/system-settings/site/system-info', 'System Information'],
  ])('direct entry to %s selects %s', async (path, title) => {
    await mount({ role: 100, path })
    expect(screen.getByRole('link', { name: title })).toHaveAttribute(
      'aria-current',
      'page'
    )
    expect(
      screen.queryByRole('link', { name: 'Usage Logs', current: 'page' })
    ).not.toBeInTheDocument()
  })

  it('platform navigation shows each destination once without a nested launcher', async () => {
    const { navigate } = await mount()
    const nav = screen.getByRole('navigation', { name: 'Platform navigation' })
    expect(within(nav).queryByRole('button')).not.toBeInTheDocument()
    expect(
      within(nav).getAllByRole('link', { name: 'Model Square' })
    ).toHaveLength(1)
    expect(within(nav).getByRole('link', { name: 'Docs' })).toHaveAttribute(
      'rel',
      'noopener noreferrer'
    )
    expect(within(nav).getByText('Rankings')).toHaveAttribute(
      'aria-disabled',
      'true'
    )
    expect(within(nav).getByRole('link', { name: 'Home' })).toHaveAttribute(
      'href',
      '/'
    )
    expect(within(nav).getByRole('link', { name: 'About' })).toHaveAttribute(
      'href',
      '/about'
    )
    expect(within(nav).getByRole('link', { name: 'Console' })).toHaveAttribute(
      'aria-current',
      'page'
    )
    expect(within(nav).getByRole('link', { name: 'Home' })).not.toHaveAttribute(
      'aria-current'
    )
    await userEvent.click(
      within(nav).getByRole('link', { name: 'Model Square' })
    )
    expect(navigate).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({ href: '/pricing' })
    )
  })
})
