/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it } from 'vitest'

import { useAuthStore } from '@/stores/auth-store'

import { ApiScenarios } from '../components/sections/api-scenarios'
import { Hero } from '../components/sections/hero'
import { HomeFaq } from '../components/sections/home-faq'
import { ModelShowcase } from '../components/sections/model-showcase'

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
    component: () => (
      <>
        <Hero isAuthenticated={authenticated} />
        <ModelShowcase />
        <ApiScenarios />
        <HomeFaq />
      </>
    ),
  })
  const signIn = createRoute({
    getParentRoute: () => root,
    path: '/sign-in',
    validateSearch: (search: Record<string, unknown>) => ({
      redirect: String(search.redirect ?? ''),
    }),
    component: () => <h1>Sign in</h1>,
  })
  const destinations = ['/keys', '/pricing', '/docs'].map((path) =>
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
  render(
    <QueryClientProvider client={new QueryClient()}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  await screen.findByRole('heading', {
    level: 1,
    name: 'Large model Token aggregation and routing platform',
  })
  return router
}

afterEach(() => useAuthStore.getState().auth.reset())

describe('Homepage discovery and navigation', () => {
  it('sends visitors to sign-in with the API key destination preserved', async () => {
    const user = userEvent.setup()
    const router = await mount()
    await user.click(screen.getByRole('button', { name: 'Get API key' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/sign-in'))
    expect(router.state.location.search).toEqual({ redirect: '/keys' })
  })

  it('takes authenticated users directly to API keys', async () => {
    const user = userEvent.setup()
    const router = await mount(true)
    await user.click(screen.getByRole('button', { name: 'Get API key' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/keys'))
  })

  it('opens the live catalog from the model showcase instead of inventing prices', async () => {
    const user = userEvent.setup()
    const router = await mount()
    await user.click(screen.getByRole('button', { name: 'View all models' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/pricing'))
  })

  it('switches API scenarios by mouse and keyboard with matching selected states', async () => {
    const user = userEvent.setup()
    await mount()
    const chat = screen.getByRole('tab', { name: 'Chat API' })
    const image = screen.getByRole('tab', { name: 'Image API' })
    const video = screen.getByRole('tab', { name: 'Video API' })
    expect(chat).toHaveAttribute('aria-selected', 'true')
    await user.click(image)
    expect(image).toHaveAttribute('aria-selected', 'true')
    expect(
      screen.getByRole('heading', {
        name: 'Make creativity part of your workflow',
      })
    ).toBeVisible()
    await user.keyboard('{ArrowRight}{Enter}')
    expect(video).toHaveAttribute('aria-selected', 'true')
    expect(
      screen.getByRole('heading', {
        name: 'From a single idea to moving stories',
      })
    ).toBeVisible()
    expect(
      screen.queryByRole('heading', {
        name: 'Make creativity part of your workflow',
      })
    ).not.toBeInTheDocument()
  })

  it('expands and collapses FAQ answers using the keyboard', async () => {
    const user = userEvent.setup()
    await mount()
    const question = screen.getByRole('button', {
      name: /How should I protect my API key/,
    })
    expect(question).toHaveAttribute('aria-expanded', 'false')
    question.focus()
    await user.keyboard('{Enter}')
    expect(question).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByText(/Keep API keys on your server/)).toBeVisible()
    await user.keyboard('{Enter}')
    expect(question).toHaveAttribute('aria-expanded', 'false')
  })

  it('shows the domestic provider lineup in the model ecosystem strip', async () => {
    await mount()
    const ecosystem = screen.getByLabelText('Model ecosystem')
    for (const provider of [
      'GLM',
      'Kimi',
      'DeepSeek',
      'Tencent Hunyuan',
      'Qwen',
      'ByteDance',
    ]) {
      expect(ecosystem).toHaveTextContent(provider)
    }
    for (const legacy of ['OpenAI', 'Claude', 'Gemini']) {
      expect(ecosystem).not.toHaveTextContent(legacy)
    }
  })
})
