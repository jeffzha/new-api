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
import { CalendarDays, Clock3, RefreshCw } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Area, AreaChart, CartesianGrid, XAxis, YAxis } from 'recharts'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from '@/components/ui/chart'
import { Skeleton } from '@/components/ui/skeleton'
import { Table } from '@/components/ui/table'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { getUserQuotaDates } from '@/features/dashboard/api'
import { buildSignalUsage } from '@/features/dashboard/lib/signal-usage'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber, formatQuota } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

export function SignalUsagePanel() {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.language)
  const userId = useAuthStore((state) => state.auth.user?.id)
  const [days, setDays] = useState(1)
  const [anchor, setAnchor] = useState(() => Math.floor(Date.now() / 1000))
  // Whole hourly buckets match the server's aggregated data. The current hour is partial.
  const end = Math.floor(anchor / 3600) * 3600 + 3599
  const start = end + 1 - days * 86400
  const query = useQuery({
    queryKey: ['dashboard', 'signal-usage', userId, start, end],
    enabled: Boolean(userId),
    queryFn: async () =>
      requireServerSuccess(
        await getUserQuotaDates({
          start_timestamp: start,
          end_timestamp: end,
          default_time: 'hour',
        })
      ),
    staleTime: 60_000,
  })
  const usage = useMemo(
    () => buildSignalUsage(query.data?.data ?? []),
    [query.data]
  )
  const metrics = [
    {
      label: t('Models'),
      value: String(
        new Set(
          (query.data?.data ?? []).map((row) => row.model_name).filter(Boolean)
        ).size
      ),
    },
    { label: t('Requests'), value: formatNumber(usage.requests) },
    { label: t('Usage'), value: formatQuota(usage.quota) },
    { label: t('Tokens'), value: formatNumber(usage.tokens) },
  ]
  const timeFormat = new Intl.DateTimeFormat(locale, {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  })
  const tickFormat = new Intl.DateTimeFormat(
    locale,
    days === 1
      ? { hour: '2-digit', minute: '2-digit' }
      : { month: '2-digit', day: '2-digit' }
  )
  const series = usage.series.map((point) => ({
    ...point,
    label: timeFormat.format(point.timestamp * 1000),
  }))
  let content = (
    <ChartContainer
      config={{ requests: { label: t('Requests'), color: 'var(--primary)' } }}
      className='h-52 w-full sm:h-60'
    >
      <AreaChart
        accessibilityLayer
        data={series}
        margin={{ left: 0, right: 12, top: 12, bottom: 0 }}
      >
        <CartesianGrid vertical={false} strokeDasharray='3 5' />
        <XAxis
          dataKey='timestamp'
          type='number'
          scale='time'
          domain={[start, end]}
          tickFormatter={(value: number) => tickFormat.format(value * 1000)}
          tickLine={false}
          axisLine={false}
          minTickGap={44}
        />
        <YAxis
          width={44}
          tickFormatter={(value: number) => formatNumber(value)}
          tickLine={false}
          axisLine={false}
          allowDecimals={false}
        />
        <ChartTooltip
          content={<ChartTooltipContent />}
          labelFormatter={(_, payload) => payload[0]?.payload.label ?? ''}
        />
        <Area
          type='linear'
          dataKey='requests'
          stroke='var(--color-requests)'
          fill='var(--color-requests)'
          fillOpacity={0.09}
          strokeWidth={2.5}
          dot={series.length === 1}
          isAnimationActive={false}
        />
      </AreaChart>
    </ChartContainer>
  )
  if (query.isPending) {
    content = (
      <div role='status' aria-label={t('Loading...')}>
        <Skeleton className='h-52 w-full sm:h-60' />
      </div>
    )
  } else if (query.isError) {
    content = (
      <ErrorState
        title={t('Failed to load')}
        description={t('signal.errorHint')}
        onRetry={() => void query.refetch()}
        className='min-h-52'
      />
    )
  } else if (!series.length) {
    content = (
      <EmptyState
        title={t('No recent usage')}
        description={t('signal.emptyHint')}
        className='min-h-52'
      />
    )
  }
  return (
    <section
      className='signal-traffic bg-card flex min-w-0 flex-col overflow-hidden rounded-2xl border'
      aria-label={t('signal.traffic')}
    >
      <div className='flex flex-wrap items-center justify-between gap-3 px-5 pt-5'>
        <div className='flex flex-col gap-1'>
          <h3 className='font-semibold'>{t('signal.traffic')}</h3>
          <p className='text-muted-foreground text-xs'>{t('signal.scope')}</p>
        </div>
        <div className='flex flex-wrap items-center gap-2'>
          <Tabs
            value={String(days)}
            onValueChange={(value) => {
              setDays(Number(value))
              setAnchor(Math.floor(Date.now() / 1000))
            }}
          >
            <TabsList aria-label={t('signal.period')}>
              <TabsTrigger value='1' icon={<Clock3 />}>
                {t('signal.24h')}
              </TabsTrigger>
              <TabsTrigger value='7' icon={<CalendarDays />}>
                {t('signal.7d')}
              </TabsTrigger>
            </TabsList>
          </Tabs>
          <Button
            variant='ghost'
            size='icon'
            aria-label={t('Refresh')}
            disabled={query.isFetching}
            onClick={() => {
              const now = Math.floor(Date.now() / 1000)
              if (Math.floor(now / 3600) === Math.floor(anchor / 3600)) {
                void query.refetch()
              } else setAnchor(now)
            }}
          >
            <RefreshCw className='size-4' aria-hidden='true' />
          </Button>
        </div>
      </div>
      <dl className='signal-metrics grid grid-cols-1 gap-4 px-5 py-6 sm:grid-cols-4'>
        {metrics.map((metric) => (
          <div key={metric.label} className='flex min-w-0 flex-col gap-2'>
            <dt className='text-muted-foreground text-xs'>{metric.label}</dt>
            <dd className='text-2xl font-semibold tracking-tight break-words tabular-nums'>
              {query.isPending || query.isError ? '—' : metric.value}
            </dd>
          </div>
        ))}
      </dl>
      <div className='min-w-0 px-3 pb-3'>{content}</div>
      {!query.isPending && !query.isError && (
        <section
          aria-label={t('Model distribution')}
          className='border-t px-5 py-4'
        >
          <h3 className='mb-3 text-sm font-semibold'>
            {t('Model distribution')}
          </h3>
          <div className='grid gap-3 sm:grid-cols-2'>
            {[
              ...new Set((query.data?.data ?? []).map((row) => row.model_name)),
            ].map((model) => {
              const rows = (query.data?.data ?? []).filter(
                (row) => row.model_name === model
              )
              const total = buildSignalUsage(rows)
              return (
                <div key={model ?? 'unknown'} className='min-w-0 border-b py-3'>
                  <p className='truncate text-sm font-medium'>
                    {model || t('Unknown')}
                  </p>
                  <dl className='mt-2 flex flex-wrap gap-x-5 gap-y-2 text-xs'>
                    <div>
                      <dt className='text-muted-foreground'>{t('Requests')}</dt>
                      <dd>
                        {total.requests.toLocaleString(locale)} {t('Requests')}
                      </dd>
                    </div>
                    <div>
                      <dt className='text-muted-foreground'>{t('Tokens')}</dt>
                      <dd>{formatNumber(total.tokens)}</dd>
                    </div>
                    <div>
                      <dt className='text-muted-foreground'>{t('Usage')}</dt>
                      <dd>{formatQuota(total.quota)}</dd>
                    </div>
                  </dl>
                </div>
              )
            })}
          </div>
        </section>
      )}
      <div className='text-muted-foreground mt-auto flex flex-wrap items-center justify-between gap-2 border-t px-5 py-3 text-xs'>
        <span>{t('signal.hourly')}</span>
        <span>
          {timeFormat.format(start * 1000)} – {timeFormat.format(anchor * 1000)}
        </span>
      </div>
      {!query.isError && !query.isPending && series.length > 0 && (
        <details className='border-t px-5 py-3 text-xs'>
          <summary className='text-muted-foreground focus-visible:outline-ring cursor-pointer rounded-sm py-1 focus-visible:outline-2 focus-visible:outline-offset-4'>
            {t('signal.viewData')}
          </summary>
          <div className='console-table-frame mt-3 max-h-48 overflow-auto'>
            <Table className='w-full text-left tabular-nums'>
              <caption className='sr-only'>{t('signal.traffic')}</caption>
              <thead>
                <tr>
                  <th scope='col' className='py-2'>
                    {t('Time')}
                  </th>
                  <th scope='col' className='py-2 text-right'>
                    {t('Requests')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {series.map((point) => (
                  <tr key={point.timestamp}>
                    <td className='py-1'>{point.label}</td>
                    <td className='text-right'>
                      {point.requests.toLocaleString(locale)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </Table>
          </div>
        </details>
      )}
    </section>
  )
}
