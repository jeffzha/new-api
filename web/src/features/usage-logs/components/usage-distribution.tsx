/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { useTranslation } from 'react-i18next'
import { Cell, Pie, PieChart } from 'recharts'

import { StaticDataTable } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { Button } from '@/components/ui/button'
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from '@/components/ui/chart'
import { formatNumber, formatQuota } from '@/lib/format'

import type { summarizeUsage } from '../lib/usage-analytics'

type DistributionRow = ReturnType<typeof summarizeUsage>['models'][number]

export function UsageDistribution(props: {
  title: string
  rows: DistributionRow[]
  metric: 'tokens' | 'quota'
  showTable?: boolean
  filter?: 'model' | 'group'
  onFilter?: (key: 'model' | 'group', value: string) => void
}) {
  const { t } = useTranslation()
  const ordered = [...props.rows].sort(
    (a, b) => b[props.metric] - a[props.metric]
  )
  const total = ordered.reduce((sum, row) => sum + row[props.metric], 0)
  const formatValue = props.metric === 'quota' ? formatQuota : formatNumber
  const metricLabel = t(props.metric === 'quota' ? 'Usage' : 'Tokens')
  const shareLabel = t(props.metric === 'quota' ? 'Cost share' : 'Token share')
  const filter = props.filter
  const otherValue = ordered
    .slice(5)
    .reduce((sum, row) => sum + row[props.metric], 0)
  const plotted = ordered.slice(0, 5).map((row, index) => ({
    ...row,
    sliceKey: `item:${row.name}`,
    color: `var(--chart-${index + 1})`,
  }))
  if (ordered.length > 5) {
    plotted.push({
      sliceKey: 'aggregate:other',
      name: t('Other'),
      tokens: ordered.slice(5).reduce((sum, row) => sum + row.tokens, 0),
      quota: ordered.slice(5).reduce((sum, row) => sum + row.quota, 0),
      requests: ordered.slice(5).reduce((sum, row) => sum + row.requests, 0),
      standardQuota: null,
      color: 'var(--muted-foreground)',
    })
  }

  return (
    <section
      className='console-distribution min-w-0 border-b pb-4'
      aria-label={props.title}
    >
      <header className='console-chart-heading'>
        <h3>{props.title}</h3>
        <span>{shareLabel}</span>
      </header>
      {!ordered.length ? (
        <EmptyState title={t('No data')} className='min-h-56' />
      ) : (
        <>
          <div className='console-distribution-summary'>
            <figure className='min-w-0'>
              <div className='console-donut'>
                {total > 0 && (
                  <ChartContainer
                    config={{ value: { label: metricLabel } }}
                    className='aspect-square h-full w-full'
                  >
                    <PieChart accessibilityLayer>
                      <Pie
                        data={plotted}
                        dataKey={props.metric}
                        nameKey='name'
                        innerRadius='73%'
                        outerRadius='94%'
                        startAngle={90}
                        endAngle={-270}
                        paddingAngle={2}
                        stroke='var(--background)'
                        strokeWidth={2}
                        isAnimationActive={false}
                      >
                        {plotted.map((row) => (
                          <Cell key={row.sliceKey} fill={row.color} />
                        ))}
                      </Pie>
                      <ChartTooltip
                        wrapperStyle={{ zIndex: 10 }}
                        content={
                          <ChartTooltipContent
                            hideLabel
                            formatter={(value, name) => (
                              <div className='grid gap-1.5'>
                                <strong className='max-w-64 break-all'>
                                  {name === 'Unknown' ? t('Unknown') : name}
                                </strong>
                                <span>
                                  {metricLabel}: {formatValue(Number(value))}
                                </span>
                                <span>
                                  {shareLabel}:{' '}
                                  {((Number(value) / total) * 100).toFixed(1)}%
                                </span>
                              </div>
                            )}
                          />
                        }
                      />
                    </PieChart>
                  </ChartContainer>
                )}
                <div
                  className='console-donut-total'
                  aria-label={t('Distribution total')}
                >
                  <strong title={formatValue(total)}>
                    {formatValue(total)}
                  </strong>
                  <span>{metricLabel}</span>
                </div>
              </div>
              {ordered.length > 5 && (
                <figcaption className='text-muted-foreground mt-3 flex items-center justify-center gap-2 text-center text-xs tabular-nums'>
                  <span
                    className='console-chart-swatch bg-muted-foreground'
                    aria-hidden
                  />
                  <span>
                    {t('Other')}: {formatValue(otherValue)} (
                    {(total > 0 ? (otherValue / total) * 100 : 0).toFixed(1)}%)
                  </span>
                </figcaption>
              )}
            </figure>
            <ul
              className='console-distribution-legend'
              aria-label={t('Distribution details')}
            >
              {ordered.map((row, index) => {
                const share = total > 0 ? (row[props.metric] / total) * 100 : 0
                const color =
                  index < 5
                    ? `var(--chart-${index + 1})`
                    : 'var(--muted-foreground)'
                const name = row.name === 'Unknown' ? t('Unknown') : row.name
                return (
                  <li key={row.name}>
                    <div className='console-legend-name'>
                      <span
                        className='console-chart-swatch'
                        style={{ background: color }}
                        aria-hidden
                      />
                      {props.onFilter &&
                      filter &&
                      !props.showTable &&
                      row.name !== 'Unknown' ? (
                        <Button
                          variant='link'
                          size='sm'
                          aria-label={name}
                          title={name}
                          onClick={() => props.onFilter?.(filter, row.name)}
                          className='min-w-0 justify-start p-0'
                        >
                          <span className='truncate'>{name}</span>
                        </Button>
                      ) : (
                        <span className='truncate' title={name}>
                          {name}
                        </span>
                      )}
                      <span className='console-legend-share'>
                        {share.toFixed(1)}%
                      </span>
                    </div>
                    <div className='console-legend-value'>
                      <div className='console-share-track' aria-hidden>
                        <span
                          style={{ width: `${share}%`, background: color }}
                        />
                      </div>
                      <span>{formatValue(row[props.metric])}</span>
                    </div>
                  </li>
                )
              })}
            </ul>
          </div>
          {props.showTable && (
            <StaticDataTable
              className='console-distribution-table console-report-table'
              tableProps={{ 'aria-label': props.title }}
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
                        className='max-w-44 justify-start p-0 text-xs'
                        title={row.name}
                        onClick={() => props.onFilter?.('model', row.name)}
                      >
                        <span className='truncate'>{row.name}</span>
                      </Button>
                    ) : (
                      <span
                        className='block max-w-44 truncate font-medium'
                        title={row.name}
                      >
                        {row.name === 'Unknown' ? t('Unknown') : row.name}
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
          )}
        </>
      )}
    </section>
  )
}
