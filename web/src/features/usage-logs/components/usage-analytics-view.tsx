/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { Activity, Coins, Database, Timer } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Area,
  AreaChart,
  CartesianGrid,
  Cell,
  Pie,
  PieChart,
  XAxis,
  YAxis,
} from 'recharts'

import { StaticDataTable } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { Button } from '@/components/ui/button'
import {
  ChartContainer,
  ChartLegend,
  ChartLegendContent,
  ChartTooltip,
  ChartTooltipContent,
} from '@/components/ui/chart'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { formatNumber, formatQuota, formatUseTime } from '@/lib/format'

import type { UsageLog } from '../data/schema'
import { summarizeUsage } from '../lib/usage-analytics'

export function UsageAnalyticsView(props: {
  rows: UsageLog[]
  showMetrics?: boolean
  showDimensions?: boolean
  showModelTable?: boolean
  onFilter?: (key: 'model' | 'group', value: string) => void
}) {
  const { t, i18n } = useTranslation()
  const [metric, setMetric] = useState<'tokens' | 'quota'>('tokens')
  const [granularity, setGranularity] = useState('hour')
  const summary = useMemo(
    () => summarizeUsage(props.rows, granularity === 'hour' ? 3600 : 86400),
    [props.rows, granularity]
  )
  const metrics = [
    {
      label: t('Requests'),
      value: formatNumber(summary.requests),
      icon: Activity,
    },
    { label: t('Tokens'), value: formatNumber(summary.tokens), icon: Database },
    { label: t('Usage'), value: formatQuota(summary.quota), icon: Coins },
    {
      label: t('Average duration'),
      value:
        summary.averageDuration === null
          ? t('N/A')
          : formatUseTime(summary.averageDuration),
      icon: Timer,
    },
  ]
  const dimensions = [
    {
      title: t('Model distribution'),
      rows: summary.models,
      filter: 'model' as const,
    },
  ]
  const extraDimensions = [
    {
      title: t('Group distribution'),
      rows: summary.groups,
      filter: 'group' as const,
    },
    {
      title: t('Endpoint distribution'),
      rows: summary.endpoints,
      filter: undefined,
    },
  ]
  const distributions = props.showDimensions
    ? [...dimensions, ...extraDimensions]
    : dimensions
  return (
    <div className='space-y-5' data-testid='usage-analytics'>
      {props.showMetrics && (
        <dl className='grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4'>
          {metrics.map((item) => (
            <div
              key={item.label}
              className='signal-metric flex min-w-0 items-center gap-3 rounded-lg border p-4'
            >
              <item.icon aria-hidden className='text-primary size-5 shrink-0' />
              <div className='min-w-0'>
                <dt className='text-muted-foreground text-xs'>{item.label}</dt>
                <dd className='mt-2 text-2xl font-semibold break-words tabular-nums'>
                  {item.value}
                </dd>
              </div>
            </div>
          ))}
        </dl>
      )}
      <div className='flex flex-wrap items-center justify-between gap-3 border-b pb-3'>
        <Tabs
          value={metric}
          onValueChange={(value) =>
            setMetric(value === 'quota' ? 'quota' : 'tokens')
          }
        >
          <TabsList aria-label={t('Distribution metric')}>
            <TabsTrigger value='tokens'>{t('Tokens')}</TabsTrigger>
            <TabsTrigger value='quota'>{t('Usage')}</TabsTrigger>
          </TabsList>
        </Tabs>
        <Tabs
          value={granularity}
          onValueChange={(value) => setGranularity(String(value))}
        >
          <TabsList aria-label={t('Time granularity')}>
            <TabsTrigger value='hour'>{t('Hourly')}</TabsTrigger>
            <TabsTrigger value='day'>{t('Daily')}</TabsTrigger>
          </TabsList>
        </Tabs>
      </div>
      <div className='console-analysis-grid grid min-w-0 gap-x-8 gap-y-6 xl:grid-cols-2'>
        {distributions.map((dimension) => {
          const ordered = [...dimension.rows].sort(
            (a, b) => b[metric] - a[metric]
          )
          const filter = dimension.filter
          const showTable = filter === 'model' && props.showModelTable
          const hasChartValues = ordered.some((row) => row[metric] > 0)
          // Keep the chart legible while retaining the full distribution in the scrollable legend.
          const plotted = ordered.slice(0, 7)
          if (ordered.length > 7) {
            plotted.push({
              name: t('Other'),
              tokens: ordered.slice(7).reduce((n, row) => n + row.tokens, 0),
              quota: ordered.slice(7).reduce((n, row) => n + row.quota, 0),
              requests: 0,
              standardQuota: null,
            })
          }
          return (
            <section
              key={dimension.title}
              className='min-w-0 border-b pb-4'
              aria-label={dimension.title}
            >
              <h3 className='mb-4 text-sm font-semibold'>{dimension.title}</h3>
              {!ordered.length || (!showTable && !hasChartValues) ? (
                <EmptyState title={t('No data')} className='min-h-56' />
              ) : (
                <div
                  className={
                    showTable
                      ? 'console-model-distribution grid min-w-0 grid-cols-1 items-center gap-4 sm:grid-cols-[160px_minmax(0,1fr)]'
                      : 'grid min-w-0 grid-cols-1 items-center gap-3 sm:grid-cols-[minmax(0,0.8fr)_minmax(0,1fr)]'
                  }
                >
                  {hasChartValues ? (
                    <ChartContainer
                      config={{
                        value: {
                          label: t(metric === 'tokens' ? 'Tokens' : 'Usage'),
                        },
                      }}
                      className='aspect-auto h-56 w-full min-w-0'
                    >
                      <PieChart accessibilityLayer>
                        <Pie
                          data={plotted}
                          dataKey={metric}
                          nameKey='name'
                          innerRadius='58%'
                          outerRadius='85%'
                          strokeWidth={2}
                          isAnimationActive={false}
                        >
                          {plotted.map((row, i) => (
                            <Cell
                              key={row.name}
                              fill={`var(--chart-${(i % 5) + 1})`}
                            />
                          ))}
                        </Pie>
                        <ChartTooltip
                          content={
                            <ChartTooltipContent
                              formatter={(value, name) => (
                                <span>
                                  {name}:{' '}
                                  {metric === 'quota'
                                    ? formatQuota(Number(value))
                                    : formatNumber(Number(value))}
                                </span>
                              )}
                            />
                          }
                        />
                      </PieChart>
                    </ChartContainer>
                  ) : (
                    <EmptyState title={t('No data')} className='min-h-40' />
                  )}
                  {showTable ? (
                    <StaticDataTable
                      className='console-distribution-table'
                      tableProps={{ 'aria-label': t('Model distribution') }}
                      data={ordered}
                      getRowKey={(row) => row.name}
                      columns={[
                        {
                          id: 'model',
                          header: t('Model'),
                          cell: (row) =>
                            props.onFilter && row.name !== 'Unknown' ? (
                              <Button
                                variant='link'
                                size='sm'
                                className='max-w-40 justify-start p-0 text-xs'
                                title={row.name}
                                onClick={() =>
                                  props.onFilter?.('model', row.name)
                                }
                              >
                                <span className='truncate'>{row.name}</span>
                              </Button>
                            ) : (
                              <span
                                className='block max-w-40 truncate'
                                title={row.name}
                              >
                                {row.name === 'Unknown'
                                  ? t('Unknown')
                                  : row.name}
                              </span>
                            ),
                        },
                        {
                          id: 'requests',
                          header: t('Requests'),
                          className: 'text-right',
                          cellClassName: 'text-right tabular-nums',
                          cell: (row) => formatNumber(row.requests),
                        },
                        {
                          id: 'tokens',
                          header: t('Tokens'),
                          className: 'text-right',
                          cellClassName: 'text-right tabular-nums',
                          cell: (row) => formatNumber(row.tokens),
                        },
                        {
                          id: 'actual',
                          header: t('Actual'),
                          className: 'text-right',
                          cellClassName: 'text-right tabular-nums text-primary',
                          cell: (row) => formatQuota(row.quota),
                        },
                        {
                          id: 'standard',
                          header: t('Standard'),
                          className: 'text-right',
                          cellClassName:
                            'text-right tabular-nums text-muted-foreground',
                          cell: (row) =>
                            row.standardQuota === null
                              ? t('Not provided')
                              : formatQuota(row.standardQuota),
                        },
                      ]}
                    />
                  ) : (
                    <ul className='max-h-56 min-w-0 overflow-y-auto'>
                      {ordered.map((row) => (
                        <li
                          key={row.name}
                          className='flex min-w-0 items-center justify-between gap-3 border-b py-2 text-xs'
                        >
                          {props.onFilter &&
                          filter &&
                          row.name !== 'Unknown' ? (
                            <Button
                              variant='link'
                              size='sm'
                              className='min-h-9 min-w-0 justify-start p-0 text-xs'
                              onClick={() => props.onFilter?.(filter, row.name)}
                              title={row.name}
                            >
                              <span className='truncate'>{row.name}</span>
                            </Button>
                          ) : (
                            <span className='min-w-0 truncate' title={row.name}>
                              {row.name === 'Unknown' ? t('Unknown') : row.name}
                            </span>
                          )}
                          <span className='shrink-0 tabular-nums'>
                            {metric === 'quota'
                              ? formatQuota(row.quota)
                              : formatNumber(row.tokens)}
                          </span>
                        </li>
                      ))}
                    </ul>
                  )}
                </div>
              )}
            </section>
          )
        })}
        <section
          className='min-w-0 border-b pb-4'
          aria-label={t('Token usage trend')}
        >
          <h3 className='mb-4 text-sm font-semibold'>
            {t('Token usage trend')}
          </h3>
          {!summary.series.length ? (
            <EmptyState title={t('No data')} className='min-h-56' />
          ) : (
            <>
              <ChartContainer
                config={{
                  input: { label: t('Input tokens'), color: 'var(--chart-1)' },
                  output: {
                    label: t('Output tokens'),
                    color: 'var(--chart-2)',
                  },
                  cache: {
                    label: t('Cache read tokens'),
                    color: 'var(--chart-3)',
                  },
                }}
                className='aspect-auto h-56 w-full min-w-0'
              >
                <AreaChart
                  accessibilityLayer
                  data={summary.series}
                  margin={{ left: 0, right: 12, top: 12, bottom: 0 }}
                >
                  <CartesianGrid vertical={false} />
                  <XAxis
                    dataKey='timestamp'
                    tickFormatter={(n: number) =>
                      new Date(n * 1000).toLocaleString(
                        i18n.language.startsWith('zh') ? 'zh-CN' : 'en-US',
                        { month: '2-digit', day: '2-digit', hour: '2-digit' }
                      )
                    }
                    minTickGap={44}
                    tickLine={false}
                    axisLine={false}
                  />
                  <YAxis
                    width={48}
                    tickFormatter={(value: number) => formatNumber(value)}
                    tickLine={false}
                    axisLine={false}
                  />
                  <ChartTooltip
                    content={<ChartTooltipContent />}
                    labelFormatter={(_, payload) =>
                      new Date(
                        Number(payload[0]?.payload.timestamp) * 1000
                      ).toLocaleString()
                    }
                  />
                  <ChartLegend
                    content={<ChartLegendContent className='flex-wrap' />}
                  />
                  {(['input', 'output', 'cache'] as const).map((key) => (
                    <Area
                      key={key}
                      type='linear'
                      dataKey={key}
                      stroke={`var(--color-${key})`}
                      fill={`var(--color-${key})`}
                      fillOpacity={0.06}
                      isAnimationActive={false}
                      dot={summary.series.length === 1}
                    />
                  ))}
                </AreaChart>
              </ChartContainer>
              <p className='text-muted-foreground mt-2 text-xs'>
                {t('Cache reads are included in input tokens.')}
              </p>
            </>
          )}
        </section>
      </div>
    </div>
  )
}
