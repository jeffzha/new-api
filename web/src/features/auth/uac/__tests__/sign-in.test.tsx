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
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { render, screen, waitFor } from '@testing-library/react'
import { StrictMode } from 'react'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { ProjectSignIn } from '../project-sign-in'
import { UACSignIn } from '../sign-in'

function renderSignIn(project = false) {
  useAuthStore.getState().auth.reset('complete')
  const root = createRootRoute({ component: Outlet })
  const signIn = createRoute({
    getParentRoute: () => root,
    path: '/sign-in',
    component: project ? ProjectSignIn : UACSignIn,
  })
  const otp = createRoute({
    getParentRoute: () => root,
    path: '/otp',
    component: () => <p>Complete local verification</p>,
  })
  const router = createRouter({
    routeTree: root.addChildren([signIn, otp]),
    basepath: '/test',
    history: createMemoryHistory({ initialEntries: ['/test/sign-in'] }),
  })
  render(
    <StrictMode>
      <QueryClientProvider client={new QueryClient()}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </StrictMode>
  )
  return router
}

afterEach(() => {
  window.history.replaceState({}, '', '/')
  useAuthStore.getState().auth.reset('idle')
})

it('offers unified sign-in without asking for a local password', async () => {
  renderSignIn()
  expect(
    await screen.findByRole('button', {
      name: 'Sign in with unified authentication',
    })
  ).toBeEnabled()
  expect(screen.queryByLabelText(/password/i)).not.toBeInTheDocument()
})

it('shows the project platform identity with one keyboard-accessible unified sign-in action', async () => {
  renderSignIn(true)
  expect(await screen.findByRole('heading', { level: 1 })).toHaveTextContent(
    'Unified sign-in'
  )
  expect(screen.getByRole('main')).toBeVisible()
  const button = screen.getByRole('button', {
    name: 'Sign in with unified authentication',
  })
  button.focus()
  expect(button).toHaveFocus()
  expect(button).toBeEnabled()
  expect(screen.queryByLabelText(/password/i)).not.toBeInTheDocument()
})

it('keeps the project sign-in action available after an authentication error', async () => {
  window.history.replaceState({}, '', '/test/sign-in?uac_error=account')
  renderSignIn(true)
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Contact the platform administrator.'
  )
  expect(screen.getByRole('main')).toBeVisible()
  expect(
    screen.getByRole('button', { name: 'Sign in with unified authentication' })
  ).toBeEnabled()
})

it('shows an actionable message when the local account is unavailable', async () => {
  window.history.replaceState({}, '', '/test/sign-in?uac_error=account')
  renderSignIn()
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Contact the platform administrator.'
  )
  expect(
    screen.getByRole('button', { name: 'Sign in with unified authentication' })
  ).toBeEnabled()
})

it('continues an enrolled local factor inside the test instance without issuing a second handoff', async () => {
  window.history.replaceState({}, '', '/test/sign-in?uac_complete=1')
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: {
        require_verification: true,
        flow_token: 'uac-local-factor',
        expires_at: Math.floor(Date.now() / 1000) + 300,
        methods: [{ method: '2fa', available: true }],
      },
    },
  })
  const router = renderSignIn()
  expect(await screen.findByText('Complete local verification')).toBeVisible()
  expect(router.history.location.pathname).toBe('/test/otp')
  expect(useAuthStore.getState().auth.user).toBeNull()
  expect(post).toHaveBeenCalledTimes(1)
})

it('lets the operator retry sign-in when a handoff request fails', async () => {
  window.history.replaceState({}, '', '/test/sign-in?uac_complete=1')
  vi.spyOn(api, 'post').mockRejectedValue(new Error('network unavailable'))
  renderSignIn()
  await waitFor(() =>
    expect(screen.getByRole('alert')).toHaveTextContent('Try again')
  )
  expect(
    screen.getByRole('button', { name: 'Sign in with unified authentication' })
  ).toBeEnabled()
})
