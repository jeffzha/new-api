/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
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
import { afterEach, describe, expect, it } from 'vitest'

import { useAuthStore } from '@/stores/auth-store'

import { HowItWorks } from '../components/sections/how-it-works'

async function mount(authenticated = false) {
  useAuthStore
    .getState()
    .auth.setUser(
      authenticated ? { id: 1, username: 'customer', role: 1 } : null
    )
  const root = createRootRoute({ component: Outlet })
  const home = createRoute({
    getParentRoute: () => root,
    path: '/',
    component: HowItWorks,
  })
  const signIn = createRoute({
    getParentRoute: () => root,
    path: '/sign-in',
    validateSearch: (search: Record<string, unknown>) => ({
      redirect: String(search.redirect ?? ''),
    }),
    component: () => <h1>Sign in</h1>,
  })
  const destinations = ['/wallet', '/keys', '/docs'].map((path) =>
    createRoute({
      getParentRoute: () => root,
      path,
      component: () => <h1>{path}</h1>,
    })
  )
  const router = createRouter({
    routeTree: root.addChildren([home, signIn, ...destinations]),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  await router.load()
  render(<RouterProvider router={router} />)
  await screen.findByRole('heading', { name: 'Three steps to get started' })
  return router
}

afterEach(() => {
  cleanup()
  useAuthStore.getState().auth.reset()
})

const destinations = [
  ['Go to recharge', '/wallet'],
  ['Configure API keys', '/keys'],
  ['Open API documentation', '/docs'],
] as const

describe('Homepage quick start navigation', () => {
  it.each(destinations)(
    'prompts before %s and retains %s for sign-in',
    async (action, destination) => {
      const user = userEvent.setup()
      const router = await mount()
      await user.click(screen.getByRole('button', { name: action }))
      const dialog = await screen.findByRole('dialog', {
        name: 'Sign in required',
      })
      expect(router.state.location.pathname).toBe('/')
      await user.click(
        within(dialog).getByRole('button', { name: 'Sign in now' })
      )
      await waitFor(() =>
        expect(router.state.location.pathname).toBe('/sign-in')
      )
      expect(router.state.location.search).toEqual({ redirect: destination })
    }
  )

  it('lets a visitor cancel without leaving the homepage', async () => {
    const user = userEvent.setup()
    const router = await mount()
    await user.click(screen.getByRole('button', { name: 'Go to recharge' }))
    const dialog = await screen.findByRole('dialog', {
      name: 'Sign in required',
    })
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    expect(router.state.location.pathname).toBe('/')
  })

  it.each(destinations)(
    'takes a signed-in user from %s directly to %s',
    async (action, destination) => {
      const user = userEvent.setup()
      const router = await mount(true)
      await user.click(screen.getByRole('button', { name: action }))
      await waitFor(() =>
        expect(router.state.location.pathname).toBe(destination)
      )
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    }
  )
})
