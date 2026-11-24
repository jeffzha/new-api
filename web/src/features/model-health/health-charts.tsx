import { Activity, CircleGauge } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import {
  Bar,
  BarChart,
  CartesianGrid,
  Pie,
  PieChart,
  XAxis,
  YAxis,
} from 'recharts'

import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from '@/components/ui/chart'

import type { Overview } from './api'

const healthColors = {
  healthy: 'var(--success)',
  abnormal: 'var(--destructive)',
  slow: 'var(--warning)',
  waiting: 'var(--muted-foreground)',
}

export function HealthCharts(props: {
  data: Overview
  page: number
  hours: string
}) {
  const { t } = useTranslation()
  const health = (
    Object.keys(healthColors) as (keyof typeof healthColors)[]
  ).map((state) => ({
    state,
    name: t(state),
    count: props.data.counts[state] ?? 0,
    fill: healthColors[state],
  }))
  const user = { name: t('User calls'), success: 0, incomplete: 0 }
  const probe = { name: t('Active detections'), success: 0, incomplete: 0 }
  for (const row of props.data.rows) {
    for (const endpoint of row.details) {
      user.success += endpoint.user_ok
      user.incomplete += endpoint.user_count - endpoint.user_ok
      probe.success += endpoint.probe_ok
      probe.incomplete += endpoint.probe_count - endpoint.probe_ok
    }
  }
  const outcomes = [user, probe]
  const outcomeConfig = {
    success: { label: t('succeeded'), color: 'var(--success)' },
    incomplete: {
      label: t('Failed or unconfirmed'),
      color: 'var(--destructive)',
    },
  } satisfies ChartConfig

  return (
    <div className='space-y-2'>
      <div className='text-muted-foreground flex flex-wrap justify-between gap-x-4 gap-y-1 text-xs tabular-nums'>
        <span>
          {t('Monitored models')}:{' '}
          <strong className='text-foreground'>
            {props.data.total.toLocaleString()}
          </strong>
        </span>
        <span>
          {t('Page {{page}} · {{hours}} hours', {
            page: props.page,
            hours: props.hours,
          })}
        </span>
      </div>
      <div className='grid min-w-0 gap-x-6 divide-y border-y xl:grid-cols-[minmax(0,2fr)_minmax(0,3fr)] xl:divide-y-0'>
        <section
          aria-label={t('Health distribution')}
          className='min-w-0 py-3 xl:border-e xl:pe-6'
        >
          <h2 className='flex items-center gap-2 text-sm font-semibold'>
            <CircleGauge
              aria-hidden='true'
              className='text-muted-foreground size-4'
            />
            {t('Health distribution')}
          </h2>
          {props.data.rows.length ? (
            <div className='grid min-w-0 grid-cols-[minmax(0,1fr)_minmax(0,1fr)] items-center gap-4 py-2'>
              <ChartContainer
                config={{}}
                className='aspect-auto h-40 w-full min-w-0'
              >
                <PieChart accessibilityLayer>
                  <Pie
                    data={health.filter((item) => item.count > 0)}
                    dataKey='count'
                    nameKey='name'
                    innerRadius='67%'
                    outerRadius='92%'
                    startAngle={90}
                    endAngle={-270}
                    stroke='var(--background)'
                    strokeWidth={2}
                    isAnimationActive={false}
                  />
                  <ChartTooltip content={<ChartTooltipContent hideLabel />} />
                </PieChart>
              </ChartContainer>
              <dl className='space-y-2.5 text-xs'>
                {health.map((item) => (
                  <div
                    key={item.state}
                    className='flex items-center justify-between gap-2'
                  >
                    <dt className='flex min-w-0 items-center gap-2 break-words'>
                      <span
                        aria-hidden='true'
                        className='size-2 shrink-0 rounded-sm'
                        style={{ backgroundColor: item.fill }}
                      />
                      {item.name}
                    </dt>
                    <dd className='font-medium tabular-nums'>{item.count}</dd>
                  </div>
                ))}
              </dl>
            </div>
          ) : (
            <p className='text-muted-foreground grid min-h-44 place-items-center text-sm'>
              {t('No models on this page')}
            </p>
          )}
        </section>
        <section aria-label={t('Call outcomes')} className='min-w-0 py-3'>
          <h2 className='flex items-center gap-2 text-sm font-semibold'>
            <Activity
              aria-hidden='true'
              className='text-muted-foreground size-4'
            />
            {t('Call outcomes')}
          </h2>
          <p className='text-muted-foreground mt-1 text-xs'>
            {t('Completed samples on this page')}
          </p>
          {outcomes.some((item) => item.success + item.incomplete > 0) ? (
            <>
              <ChartContainer
                config={outcomeConfig}
                className='mt-2 aspect-auto h-28 w-full'
              >
                <BarChart
                  accessibilityLayer
                  data={outcomes}
                  margin={{ top: 4, right: 8, bottom: 0, left: 0 }}
                  barSize={28}
                >
                  <CartesianGrid vertical={false} />
                  <XAxis dataKey='name' axisLine={false} tickLine={false} />
                  <YAxis
                    allowDecimals={false}
                    tickLine={false}
                    axisLine={false}
                    width={36}
                  />
                  <ChartTooltip content={<ChartTooltipContent />} />
                  <Bar
                    dataKey='success'
                    stackId='outcome'
                    fill='var(--color-success)'
                    isAnimationActive={false}
                  />
                  <Bar
                    dataKey='incomplete'
                    stackId='outcome'
                    fill='var(--color-incomplete)'
                    isAnimationActive={false}
                  />
                </BarChart>
              </ChartContainer>
              <dl className='mt-2 flex flex-wrap gap-x-5 gap-y-1 text-xs tabular-nums'>
                {outcomes.map((item) => (
                  <div
                    key={item.name}
                    className='flex flex-wrap items-center gap-x-2'
                  >
                    <dt className='font-medium'>{item.name}</dt>
                    <dd className='text-muted-foreground'>
                      {t('succeeded')}: {item.success} ·{' '}
                      {t('Failed or unconfirmed')}: {item.incomplete}
                    </dd>
                  </div>
                ))}
              </dl>
            </>
          ) : (
            <p className='text-muted-foreground grid min-h-36 place-items-center text-sm'>
              {t('No completed samples')}
            </p>
          )}
        </section>
      </div>
    </div>
  )
}
