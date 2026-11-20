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
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import type { OPSWalletFields } from '@/features/ops-wallet/types'
import { useOPSBalance } from '@/features/ops-wallet/use-ops-balance'
import { getSelf } from '@/lib/api'
import { formatQuota } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'
import type { AuthUser } from '@/stores/auth-store'

// The OPS snapshot has its own authority. Missing/stale snapshots never fall
// back to the unrelated product wallet in the shared user table.
export function useWalletBalance(
  user: (OPSWalletFields & { id: number; quota?: number }) | null
) {
  const { t } = useTranslation()
  const external = user?.wallet_authority === 'ops'
  const query = useQuery({
    queryKey: ['dashboard', 'wallet-balance', user?.id],
    queryFn: async () => {
      const result = requireServerSuccess(await getSelf())
      const data = result.data as AuthUser
      if (data.id !== user?.id) {
        throw new Error('Wallet identity changed')
      }
      return data
    },
    enabled: external,
    staleTime: 10_000,
    refetchInterval: external ? 15_000 : false,
    retry: 1,
  })
  const result = useOPSBalance(
    query.data?.ops_balance ?? user?.ops_balance,
    query.isError
  )
  if (!external) {
    return {
      external,
      text: user ? formatQuota(user.quota ?? 0) : '—',
      hint: t('Available balance'),
      quota: user?.quota ?? 0,
      failed: false,
    }
  }
  return {
    external,
    ...result,
    balance: query.data?.ops_balance ?? user?.ops_balance,
    failed: query.isError,
  }
}
