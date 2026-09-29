/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { Activity, Coins, Database, Timer } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Area, AreaChart, CartesianGrid, XAxis, YAxis } from 'recharts'

import { EmptyState } from '@/components/empty-state'
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
import { UsageDistribution } from './usage-distribution'

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
        {distributions.map((dimension) => (
          <UsageDistribution
            key={dimension.title}
            title={dimension.title}
            rows={dimension.rows}
            metric={metric}
            filter={dimension.filter}
            showTable={dimension.filter === 'model' && props.showModelTable}
            onFilter={props.onFilter}
          />
        ))}
        <section
          className='min-w-0 border-b pb-4'
          aria-label={t('Token usage trend')}
        >
          <header className='console-chart-heading'>
            <h3>{t('Token usage trend')}</h3>
            <span>{t(granularity === 'hour' ? 'Hourly' : 'Daily')}</span>
          </header>
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
                className='aspect-auto h-64 w-full min-w-0'
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
