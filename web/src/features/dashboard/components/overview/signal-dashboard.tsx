/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import {
  Activity,
  ArrowRight,
  CalendarDays,
  CalendarRange,
  CalendarClock,
  Coins,
  Database,
  Gauge,
  KeyRound,
  RefreshCw,
  Timer,
  WalletCards,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { getApiKeys } from '@/features/keys/api'
import { getUserLogStats } from '@/features/usage-logs/api'
import { UsageAnalyticsView } from '@/features/usage-logs/components/usage-analytics-view'
import {
  fetchUsageAnalytics,
  summarizeUsage,
} from '@/features/usage-logs/lib/usage-analytics'
import { formatNumber, formatQuota, formatUseTime } from '@/lib/format'
import { ROLE } from '@/lib/roles'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { getUserQuotaDates } from '../../api'
import { useDashboardContentVisibility } from '../../hooks/use-status-data'
import { buildSignalUsage } from '../../lib/signal-usage'
import { AnnouncementsPanel } from './announcements-panel'
import { ApiInfoPanel } from './api-info-panel'
import { FAQPanel } from './faq-panel'
import { PerformanceHealthPanel } from './performance-health-panel'
import { UptimePanel } from './uptime-panel'

export function SignalDashboard() {
  const { t } = useTranslation()
  const user = useAuthStore((s) => s.auth.user)
  const visibility = useDashboardContentVisibility()
  const [days, setDays] = useState('7')
  const [anchor, setAnchor] = useState(() => Date.now())
  const end = Math.floor(anchor / 1000)
  const start =
    Math.floor(
      new Date(new Date(anchor).setHours(0, 0, 0, 0)).getTime() / 1000
    ) -
    (Number(days) - 1) * 86400
  const query = useQuery({
    queryKey: ['dashboard', 'signal-records', user?.id, start, end, anchor],
    queryFn: ({ signal }) =>
      fetchUsageAnalytics(
        { type: 2, start_timestamp: start, end_timestamp: end },
        false,
        signal
      ),
    enabled: Boolean(user?.id),
    staleTime: 60_000,
  })
  const keys = useQuery({
    queryKey: ['dashboard', 'key-count', user?.id],
    queryFn: async () =>
      requireServerSuccess(await getApiKeys({ p: 1, size: 1 })),
    enabled: Boolean(user?.id),
  })
  const totals = useQuery({
    queryKey: ['dashboard', 'signal-totals', user?.id, start, end, anchor],
    queryFn: async () =>
      requireServerSuccess(
        await getUserQuotaDates(
          { start_timestamp: start, end_timestamp: end, default_time: 'hour' },
          false
        )
      ),
    enabled: Boolean(user?.id),
  })
  const periodTotals = useMemo(
    () => buildSignalUsage(totals.data?.data ?? []),
    [totals.data]
  )
  const rates = useQuery({
    queryKey: ['dashboard', 'rates', user?.id, start, end],
    queryFn: async () =>
      requireServerSuccess(
        await getUserLogStats({
          type: 2,
          start_timestamp: start,
          end_timestamp: end,
        })
      ),
    enabled: Boolean(user?.id),
  })
  const summary = useMemo(
    () => summarizeUsage(query.data?.rows ?? []),
    [query.data]
  )
  const ready = query.isSuccess
  const periodLabel = query.data?.truncated
    ? t('Analyzed records only')
    : t('Selected period')
  const metrics = [
    {
      label: t('Credit remaining'),
      value: user ? formatQuota(user.quota ?? 0) : '-',
      hint: t('Available balance'),
      icon: WalletCards,
    },
    {
      label: t('API Keys'),
      value: keys.isSuccess ? String(keys.data.data?.total ?? 0) : '-',
      hint: t('Total keys'),
      icon: KeyRound,
    },
    {
      label: t('Requests'),
      value: totals.isSuccess ? formatNumber(periodTotals.requests) : '-',
      hint: t('Selected period'),
      icon: Activity,
    },
    {
      label: t('Usage'),
      value: totals.isSuccess ? formatQuota(periodTotals.quota) : '-',
      hint: t('Selected period'),
      icon: Coins,
    },
    {
      label: t('Tokens'),
      value: totals.isSuccess ? formatNumber(periodTotals.tokens) : '-',
      hint: t('Selected period'),
      icon: Database,
    },
    {
      label: t('Total Usage'),
      value: user ? formatQuota(user.used_quota ?? 0) : '-',
      hint: t('Lifetime'),
      icon: Coins,
    },
    {
      label: t('RPM'),
      value: rates.isSuccess ? String(rates.data.data?.rpm ?? 0) : '-',
      hint: rates.isSuccess
        ? `${formatNumber(rates.data.data?.tpm ?? 0)} TPM`
        : t('N/A'),
      icon: Gauge,
    },
    {
      label: t('Average duration'),
      value:
        ready && summary.averageDuration !== null
          ? formatUseTime(summary.averageDuration)
          : '-',
      hint: periodLabel,
      icon: Timer,
    },
  ]
  const actions = [
    { to: '/keys', title: t('API Keys'), icon: KeyRound },
    { to: '/usage-logs', title: t('Usage logs'), icon: Activity },
    { to: '/wallet', title: t('Wallet'), icon: WalletCards },
    { to: '/pricing', title: t('Model Square'), icon: Database },
  ] as const
  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('Overview')}</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Button render={<Link to='/keys' />}>
          <KeyRound aria-hidden />
          {t('Create API Key')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div
          className='signal-overview min-w-0 space-y-6'
          data-testid='signal-overview'
        >
          <div className='flex flex-wrap items-center justify-between gap-3'>
            <Tabs
              value={days}
              onValueChange={(value) => {
                setDays(String(value))
                setAnchor(Date.now())
              }}
            >
              <TabsList aria-label={t('Reporting period')}>
                <TabsTrigger value='1' icon={<CalendarClock />}>
                  {t('Today')}
                </TabsTrigger>
                <TabsTrigger value='7' icon={<CalendarDays />}>
                  {t('Last 7 days')}
                </TabsTrigger>
                <TabsTrigger value='30' icon={<CalendarRange />}>
                  {t('Last 30 days')}
                </TabsTrigger>
              </TabsList>
            </Tabs>
            <Button
              variant='outline'
              size='sm'
              disabled={query.isFetching}
              onClick={() => {
                setAnchor(Date.now())
                void keys.refetch()
                void rates.refetch()
              }}
            >
              <RefreshCw
                aria-hidden
                className={query.isFetching ? 'animate-spin' : ''}
              />
              {t('Refresh')}
            </Button>
          </div>
          <dl className='grid min-w-0 grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4'>
            {metrics.map((item) => (
              <div
                key={item.label}
                className='signal-metric flex min-w-0 items-start gap-3 rounded-lg border p-5'
              >
                <item.icon
                  aria-hidden
                  className='text-primary mt-1 size-5 shrink-0'
                />
                <div className='min-w-0'>
                  <dt className='text-muted-foreground text-xs'>
                    {item.label}
                  </dt>
                  <dd className='my-2 text-2xl font-semibold break-words tabular-nums'>
                    {item.value}
                  </dd>
                  <p className='text-muted-foreground text-xs'>{item.hint}</p>
                </div>
              </div>
            ))}
          </dl>
          {query.isPending && (
            <div role='status' aria-label={t('Loading...')}>
              <Skeleton className='h-72 w-full' />
            </div>
          )}
          {query.isError && (
            <ErrorState
              title={t('Failed to load')}
              onRetry={() => void query.refetch()}
            />
          )}
          {totals.isError && (
            <ErrorState
              title={t('Failed to load period totals')}
              onRetry={() => void totals.refetch()}
            />
          )}
          {query.isSuccess && (
            <>
              <p className='text-muted-foreground text-xs'>
                {t('Analyzed {{count}} of {{total}} records', {
                  count: query.data.rows.length,
                  total: query.data.total,
                })}
              </p>
              {query.data.truncated && (
                <p className='text-sm text-amber-700 dark:text-amber-300'>
                  {t(
                    'Analysis is limited to the latest 2,000 matching records. Narrow the time range for complete totals.'
                  )}
                </p>
              )}
              <UsageAnalyticsView rows={query.data.rows} showModelTable />
            </>
          )}
          <div className='console-activity-grid grid min-w-0 gap-8 xl:grid-cols-2'>
            {query.isSuccess && (
              <section className='min-w-0' aria-label={t('Recent usage')}>
                <div className='mb-3 flex items-center justify-between'>
                  <h3 className='font-semibold'>{t('Recent usage')}</h3>
                  <Button
                    variant='link'
                    size='sm'
                    render={<Link to='/usage-logs' />}
                  >
                    {t('View all')}
                    <ArrowRight aria-hidden />
                  </Button>
                </div>
                {!query.data.rows.length ? (
                  <EmptyState title={t('No recent usage')} />
                ) : (
                  <StaticDataTable
                    className='console-report-table console-recent-table'
                    tableProps={{ 'aria-label': t('Recent usage') }}
                    data={[...query.data.rows]
                      .sort(
                        (a, b) => b.created_at - a.created_at || b.id - a.id
                      )
                      .slice(0, 5)}
                    getRowKey={(row) => row.id}
                    columns={[
                      {
                        id: 'model',
                        header: t('Model'),
                        cell: (row) => (
                          <div className='grid max-w-52 gap-1'>
                            <span
                              className='truncate font-medium'
                              title={row.model_name}
                            >
                              {row.model_name || t('Unknown')}
                            </span>
                            <time
                              className='text-muted-foreground text-xs'
                              dateTime={new Date(
                                row.created_at * 1000
                              ).toISOString()}
                            >
                              {new Date(row.created_at * 1000).toLocaleString()}
                            </time>
                          </div>
                        ),
                      },
                      {
                        id: 'key',
                        header: t('API Key'),
                        cell: (row) => (
                          <div className='grid max-w-36 gap-1'>
                            <span className='truncate' title={row.token_name}>
                              {row.token_name || t('Not provided')}
                            </span>
                            <span
                              className='text-muted-foreground truncate text-xs'
                              title={row.group}
                            >
                              {t('Group')}: {row.group || t('Unknown')}
                            </span>
                          </div>
                        ),
                      },
                      {
                        id: 'tokens',
                        header: t('Tokens'),
                        className: 'text-right',
                        cellClassName: 'text-right tabular-nums',
                        cell: (row) =>
                          formatNumber(
                            row.prompt_tokens + row.completion_tokens
                          ),
                      },
                      {
                        id: 'usage',
                        header: t('Usage'),
                        className: 'text-right',
                        cellClassName: 'text-right tabular-nums',
                        cell: (row) => (
                          <div className='grid gap-1'>
                            <span className='text-primary font-medium'>
                              {formatQuota(row.quota)}
                            </span>
                            <span className='text-muted-foreground text-xs'>
                              {formatUseTime(row.use_time)}
                            </span>
                          </div>
                        ),
                      },
                    ]}
                  />
                )}
              </section>
            )}
            <nav
              className='console-quick-actions'
              aria-label={t('Quick actions')}
            >
              <h3 className='mb-3 font-semibold'>{t('Quick actions')}</h3>
              <div className='divide-y'>
                {actions.map((action) => (
                  <Button
                    key={action.to}
                    variant='ghost'
                    render={<Link to={action.to} />}
                    className='h-12 w-full justify-start gap-3 rounded-md'
                  >
                    <action.icon aria-hidden className='text-primary' />
                    <span className='flex-1 text-left'>{action.title}</span>
                    <ArrowRight aria-hidden />
                  </Button>
                ))}
              </div>
            </nav>
          </div>
          {(user?.role ?? 0) >= ROLE.ADMIN && <PerformanceHealthPanel />}
          <div className='grid gap-6 xl:grid-cols-2'>
            {visibility.apiInfo && <ApiInfoPanel />}
            {visibility.uptimeKuma && <UptimePanel />}
            {visibility.announcements && <AnnouncementsPanel />}
            {visibility.faq && <FAQPanel />}
          </div>
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
