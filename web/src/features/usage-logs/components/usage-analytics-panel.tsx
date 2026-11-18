/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { hashKey, useQuery } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { useAuthStore } from '@/stores/auth-store'

import {
  fetchUsageAnalytics,
  type UsageAnalyticsResult,
} from '../lib/usage-analytics'
import { buildApiParams } from '../lib/utils'
import { UsageAnalyticsView } from './usage-analytics-view'
import {
  useLogsSearchParams,
  useLogsViewScope,
  useUsageLogsContext,
} from './usage-logs-provider'

const route = getRouteApi('/_authenticated/usage-logs/$section')

export function UsageAnalyticsPanel() {
  const { t } = useTranslation()
  const search = useLogsSearchParams(route.useSearch())
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
  const queryKey = ['logs', 'analytics', userId, viewAccess, params]
  const progressKey = hashKey(queryKey)
  const [progress, setProgress] = useState<{
    key: string
    data: UsageAnalyticsResult
  } | null>(null)
  const query = useQuery({
    queryKey,
    queryFn: ({ signal }) => {
      setProgress(null)
      return fetchUsageAnalytics(params, isAdminView, signal, (data) =>
        setProgress({ key: progressKey, data })
      )
    },
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
  if (query.isError) {
    return (
      <ErrorState
        title={t('Failed to load')}
        onRetry={() => void query.refetch()}
      />
    )
  }
  const data =
    query.data ?? (progress?.key === progressKey ? progress.data : null)
  if (!data) {
    return (
      <div role='status' aria-label={t('Loading...')}>
        <LoadingState className='min-h-96' />
      </div>
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
          {query.isFetching && (
            <LoadingState
              inline
              size='sm'
              message={t('Loading...')}
              className='mr-3'
            />
          )}
          {t('Analyzed {{count}} of {{total}} records', {
            count: data.rows.length,
            total: data.total,
          })}
        </p>
      </div>
      {query.isSuccess && data.truncated && (
        <p className='text-sm text-amber-700 dark:text-amber-300'>
          {t(
            'Analysis is limited to the latest 2,000 matching records. Narrow the time range for complete totals.'
          )}
        </p>
      )}
      <UsageAnalyticsView
        rows={data.rows}
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
