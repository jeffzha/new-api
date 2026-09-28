/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { useQuery } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { Skeleton } from '@/components/ui/skeleton'
import { useAuthStore } from '@/stores/auth-store'

import { fetchUsageAnalytics } from '../lib/usage-analytics'
import { buildApiParams } from '../lib/utils'
import { UsageAnalyticsView } from './usage-analytics-view'
import { useLogsViewScope, useUsageLogsContext } from './usage-logs-provider'

const route = getRouteApi('/_authenticated/usage-logs/$section')

export function UsageAnalyticsPanel() {
  const { t } = useTranslation()
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const { isAdminView, viewAccess } = useLogsViewScope()
  const { sensitiveVisible } = useUsageLogsContext()
  const userId = useAuthStore((state) => state.auth.user?.id)
  const params = useMemo(
    () =>
      buildApiParams({
        page: 1,
        pageSize: 100,
        searchParams: search,
        isAdmin: isAdminView,
      }),
    [search, isAdminView]
  )
  const query = useQuery({
    queryKey: ['logs', 'analytics', userId, viewAccess, params],
    queryFn: ({ signal }) => fetchUsageAnalytics(params, isAdminView, signal),
    staleTime: 60_000,
    enabled: Boolean(userId) && sensitiveVisible,
  })
  if (!sensitiveVisible) {
    return (
      <p className='text-muted-foreground border-y py-6 text-sm'>
        {t('Analytics hidden')}
      </p>
    )
  }
  if (query.isPending) {
    return (
      <div role='status' aria-label={t('Loading...')}>
        <Skeleton className='h-96 w-full' />
      </div>
    )
  }
  if (query.isError) {
    return (
      <ErrorState
        title={t('Failed to load')}
        onRetry={() => void query.refetch()}
      />
    )
  }
  return (
    <section
      className='signal-analytics-band space-y-3'
      aria-label={t('Usage analytics')}
      aria-busy={query.isFetching}
    >
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <h3 className='text-sm font-semibold'>{t('Usage analytics')}</h3>
        <p className='text-muted-foreground text-xs' role='status'>
          {t('Analyzed {{count}} of {{total}} records', {
            count: query.data.rows.length,
            total: query.data.total,
          })}
        </p>
      </div>
      {query.data.truncated && (
        <p className='text-sm text-amber-700 dark:text-amber-300'>
          {t(
            'Analysis is limited to the latest 2,000 matching records. Narrow the time range for complete totals.'
          )}
        </p>
      )}
      <UsageAnalyticsView
        rows={query.data.rows}
        showMetrics
        showDimensions
        showModelTable
        onFilter={(key, value) =>
          void navigate({ search: { ...search, [key]: value, page: 1 } })
        }
      />
    </section>
  )
}
