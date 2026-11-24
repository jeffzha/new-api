import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { useOPSBalance } from '@/features/ops-wallet/use-ops-balance'
import { getSelf } from '@/lib/api'
import { formatQuota } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'
import type { AuthUser } from '@/stores/auth-store'

// The OPS snapshot has its own authority. Missing/stale snapshots never fall
// back to the unrelated product wallet in the shared user table.
export function useWalletBalance(user: AuthUser | null) {
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
    }
  }
  return { external, ...result }
}
