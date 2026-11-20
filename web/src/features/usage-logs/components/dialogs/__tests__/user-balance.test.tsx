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
import { afterEach, expect, it, vi } from 'vitest'

import type { OPSWalletFields } from '@/features/ops-wallet/types'
import { api } from '@/lib/api'

import type { UserInfo } from '../../../types'
import { UserInfoDialog } from '../user-info-dialog'

afterEach(cleanup)

const alice: UserInfo & OPSWalletFields = {
  id: 1,
  username: 'alice',
  quota: 500000000,
  used_quota: 0,
  request_count: 0,
  wallet_authority: 'ops',
  ops_balance: {
    status: 'available',
    amount: '12.345678',
    quota: 6172839,
    currency: 'USD',
    stale: false,
  },
}

function mountDialog() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const tree = (id: number) => (
    <QueryClientProvider client={client}>
      <UserInfoDialog userId={id} open onOpenChange={() => undefined} />
    </QueryClientProvider>
  )
  return { ...render(tree(1)), tree }
}

it('shows the OPS wallet in a usage log user detail instead of local quota', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: alice },
  })
  mountDialog()

  expect(await screen.findByLabelText('OPS balance')).toHaveTextContent(
    '$12.345678'
  )
})

it('does not retain another user balance while the selected user is loading', async () => {
  vi.spyOn(api, 'get')
    .mockResolvedValueOnce({ data: { success: true, data: alice } })
    .mockImplementationOnce(() => new Promise(() => undefined))
  const view = mountDialog()
  await screen.findByText('alice')
  view.rerender(view.tree(2))

  await waitFor(() =>
    expect(screen.queryByText('alice')).not.toBeInTheDocument()
  )
  expect(screen.queryByText('$12.345678')).not.toBeInTheDocument()
})

it('rejects a user detail response whose identity does not match the selection', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { ...alice, id: 2 } },
  })
  mountDialog()

  await screen.findByText('No user information available')
  expect(screen.queryByText('alice')).not.toBeInTheDocument()
})
