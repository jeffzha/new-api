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
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'

import type { OPSWalletFields } from '@/features/ops-wallet/types'
import { api } from '@/lib/api'

import { ProfileHeader } from '../components/profile-header'
import type { UserProfile } from '../types'

afterEach(cleanup)

it('shows the OPS wallet in the profile instead of the historical local balance', async () => {
  const profile: UserProfile & OPSWalletFields = {
    id: 1,
    username: 'alice',
    display_name: 'Alice',
    role: 1,
    group: 'default',
    quota: 500000000,
    used_quota: 0,
    request_count: 0,
    status: 1,
    aff_count: 0,
    aff_quota: 0,
    aff_history_quota: 0,
    created_time: 0,
    wallet_authority: 'ops',
    ops_balance: {
      status: 'available',
      amount: '12.345678',
      quota: 6172839,
      currency: 'USD',
      stale: false,
    },
  }
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: profile },
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ProfileHeader profile={profile} loading={false} />
    </QueryClientProvider>
  )

  expect(await screen.findByText('$12.345678')).toBeVisible()
  expect(screen.queryByText('$1,000.00')).not.toBeInTheDocument()
})
