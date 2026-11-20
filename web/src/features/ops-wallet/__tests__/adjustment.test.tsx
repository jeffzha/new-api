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
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { OPSAdjustDialog } from '../adjust-dialog'

const balance = {
  status: 'available' as const,
  amount: '12.345678',
  quota: 6172839,
  currency: 'USD',
  stale: false,
}

function mountAdjustment(userId = 2) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const tree = (id: number) => (
    <QueryClientProvider client={client}>
      <OPSAdjustDialog
        open
        userId={id}
        balance={balance}
        onOpenChange={() => undefined}
        onSuccess={() => undefined}
      />
    </QueryClientProvider>
  )
  return { ...render(tree(userId)), tree }
}

beforeEach(() => {
  sessionStorage.clear()
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'operator', role: 100 })
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  sessionStorage.clear()
  useAuthStore.getState().auth.reset()
})

it('retries a timed-out adjustment after reopening with the same immutable operation', async () => {
  const post = vi
    .spyOn(api, 'post')
    .mockRejectedValueOnce(new Error('Timeout'))
    .mockResolvedValueOnce({ data: { success: true } })
  const first = mountAdjustment()
  expect(screen.getByText('$12.345678')).toBeVisible()
  await userEvent.type(screen.getByLabelText(/Amount/), '1.234567')
  await userEvent.type(screen.getByLabelText('Reason'), 'Opening correction')
  await userEvent.click(screen.getByRole('button', { name: 'Confirm' }))
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Confirm' })).toBeEnabled()
  )
  const operation = post.mock.calls[0][1]
  first.unmount()
  mountAdjustment()
  expect(screen.getByLabelText(/Amount/)).toBeDisabled()
  expect(screen.getByLabelText(/Amount/)).toHaveValue('1.234567')
  await userEvent.click(screen.getByRole('button', { name: 'Confirm' }))
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2))
  expect(post.mock.calls[1][1]).toEqual(operation)
  await waitFor(() =>
    expect(sessionStorage.getItem('ops-balance-change:1:2')).toBeNull()
  )
})

it('does not reuse another operator pending operation after the authenticated operator changes', async () => {
  sessionStorage.setItem(
    'ops-balance-change:1:2',
    JSON.stringify({
      request_id: 'adm-prior-operation',
      mode: 'add',
      ops_amount: '7',
      reason: 'Prior correction',
    })
  )
  const view = mountAdjustment()
  expect(screen.getByLabelText(/Amount/)).toHaveValue('7')
  useAuthStore
    .getState()
    .auth.setUser({ id: 3, username: 'other-operator', role: 100 })
  view.rerender(view.tree(2))
  await waitFor(() => expect(screen.getByLabelText(/Amount/)).toHaveValue(''))
  expect(screen.getByLabelText(/Amount/)).toBeEnabled()
})

it('keeps a malformed saved operation blocked instead of treating it as a new adjustment', async () => {
  sessionStorage.setItem('ops-balance-change:1:2', '{invalid')
  const post = vi.spyOn(api, 'post')
  mountAdjustment()
  await userEvent.type(screen.getByLabelText(/Amount/), '5')
  await userEvent.type(screen.getByLabelText('Reason'), 'Correction')
  expect(screen.getByRole('button', { name: 'Confirm' })).toBeDisabled()
  expect(post).not.toHaveBeenCalled()
})
