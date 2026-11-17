/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { useQuery } from '@tanstack/react-query'
import { useMemo } from 'react'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import type { UsageLog } from '../data/schema'

type FeeRow = { request_id: string; ops_fee: UsageLog['ops_fee'] }

export function useLogFees<T extends object>(
  rows: T[],
  isAdmin: boolean,
  enabled: boolean
): T[] {
  const userId = useAuthStore((state) => state.auth.user?.id)
  const ids = useMemo(
    () =>
      [
        ...new Set(
          rows.flatMap((row) => {
            const log = row as Partial<UsageLog>
            return log.type === 2 && log.ops_fee && log.request_id
              ? [log.request_id]
              : []
          })
        ),
      ].slice(0, 100),
    [rows]
  )
  const query = useQuery({
    queryKey: ['usage-log-fees', userId, isAdmin, ids],
    enabled: enabled && ids.length > 0,
    queryFn: async ({ signal }) => {
      const res = await api.post<{ success: boolean; data: FeeRow[] }>(
        isAdmin ? '/api/log/ops-fees' : '/api/log/self/ops-fees',
        { request_ids: ids },
        { signal, skipErrorHandler: true }
      )
      if (!res.data.success) throw new Error('Fee refresh unavailable')
      return res.data.data
    },
    retry: false,
    meta: { errorToast: false },
    refetchInterval: (query) => {
      if (query.state.status === 'error') return 30_000
      const pending = query.state.data
        ? query.state.data.some((row) => row.ops_fee?.status === 'waiting')
        : rows.some(
            (row) => (row as Partial<UsageLog>).ops_fee?.status === 'waiting'
          )
      if (!pending) return 30_000
      return query.state.dataUpdateCount < 30 ? 2_000 : 10_000
    },
    refetchIntervalInBackground: false,
  })
  return useMemo(() => {
    if (!enabled || !query.data) return rows
    const fees = new Map(query.data.map((row) => [row.request_id, row.ops_fee]))
    return rows.map((row) => {
      const log = row as Partial<UsageLog>
      const fee = log.request_id ? fees.get(log.request_id) : undefined
      return log.type === 2 && fee ? { ...row, ops_fee: fee } : row
    })
  }, [rows, enabled, query.data])
}
