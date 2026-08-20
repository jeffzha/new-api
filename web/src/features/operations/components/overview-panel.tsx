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
import { useTranslation } from 'react-i18next'
import { CartesianGrid, Line, LineChart, XAxis, YAxis } from 'recharts'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from '@/components/ui/chart'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { cn } from '@/lib/utils'

import { formatCompact, formatMilliseconds, formatPercent } from '../format'
import {
  higherIsBetterTone,
  lowerIsBetterTone,
  METRIC_TONE_DOT_CLASS,
  METRIC_TONE_TEXT_CLASS,
  worstMetricTone,
  type MetricTone,
} from '../metric-status'
import type { OpsSettings, OpsSnapshot } from '../types'
import { MetricHelp, type MetricHelpId } from './metric-help'

function MetricCard({
  metric,
  value,
  description,
  tone = 'neutral',
}: {
  metric: MetricHelpId
  value: string
  description: string
  tone?: MetricTone
}) {
  return (
    <Card>
      <CardHeader className='pb-2'>
        <CardDescription className='flex items-center gap-1.5'>
          {tone !== 'neutral' && (
            <span
              className={cn(
                'size-1.5 shrink-0 rounded-full',
                METRIC_TONE_DOT_CLASS[tone]
              )}
              aria-hidden='true'
            />
          )}
          <MetricHelp metric={metric} />
        </CardDescription>
        <CardTitle
          className={cn('text-2xl tabular-nums', METRIC_TONE_TEXT_CLASS[tone])}
        >
          {value}
        </CardTitle>
      </CardHeader>
      <CardContent className='text-muted-foreground text-xs'>
        {description}
      </CardContent>
    </Card>
  )
}

function diagnosticMetricId(metric: string): MetricHelpId | undefined {
  if (metric === 'sla') return 'effectiveRequestSuccessRate'
  if (metric === 'request_p99') return 'requestLatencyP99'
  if (metric === 'ttft_p99') return 'ttftP99'
  if (metric === 'request_error_rate' || metric === 'upstream_error_rate') {
    return 'errorRates'
  }
  return undefined
}

function diagnosticTone(severity: string): MetricTone {
  if (severity === 'critical') return 'critical'
  if (severity === 'warning') return 'warning'
  return 'info'
}

function latencyPercentileTone(
  hasSamples: boolean,
  percentile: string,
  value: number,
  p99Threshold: number | undefined
): MetricTone {
  if (!hasSamples) return 'neutral'
  if (percentile === 'p99') {
    return lowerIsBetterTone(value, p99Threshold)
  }
  return 'info'
}

export function OverviewPanel({
  snapshot,
  settings,
}: {
  snapshot: OpsSnapshot
  settings?: OpsSettings
}) {
  const { t } = useTranslation()
  const { overview, realtime, latency } = snapshot
  const effectiveSuccessTone = overview.request_count
    ? higherIsBetterTone(overview.sla, settings?.sla_threshold, 0.001)
    : 'neutral'
  const requestErrorTone = overview.request_count
    ? lowerIsBetterTone(
        overview.request_error_rate,
        settings?.request_error_rate_threshold
      )
    : 'neutral'
  const upstreamErrorTone = overview.request_count
    ? lowerIsBetterTone(
        overview.upstream_error_rate,
        settings?.upstream_error_rate_threshold
      )
    : 'neutral'
  const errorTone = worstMetricTone(requestErrorTone, upstreamErrorTone)
  const requestLatencyTone = latency.duration.samples
    ? lowerIsBetterTone(
        latency.duration.p99,
        settings?.request_p99_threshold_ms
      )
    : 'neutral'
  const ttftTone = latency.ttft.samples
    ? lowerIsBetterTone(latency.ttft.p99, settings?.ttft_p99_threshold_ms)
    : 'neutral'
  const telemetryDrops =
    snapshot.telemetry.dropped_requests + snapshot.telemetry.dropped_attempts
  const telemetryQueue =
    snapshot.telemetry.request_queue_depth +
    snapshot.telemetry.attempt_queue_depth
  let telemetryTone: MetricTone = 'success'
  if (telemetryDrops > 0) {
    telemetryTone = 'critical'
  } else if (telemetryQueue > 0) {
    telemetryTone = 'warning'
  }
  const throughputConfig = {
    qps: { label: 'QPS', color: 'var(--chart-1)' },
    tps: { label: 'TPS', color: 'var(--chart-2)' },
  } satisfies ChartConfig
  const errorConfig = {
    sla_errors: {
      label: t('Effective request failures'),
      color: 'var(--destructive)',
    },
    upstream_errors: {
      label: t('Upstream errors'),
      color: 'var(--chart-3)',
    },
    business_limits: {
      label: t('Business limits'),
      color: 'var(--chart-4)',
    },
  } satisfies ChartConfig
  const switchConfig = {
    average: {
      label: t('Average account switches'),
      color: 'var(--chart-5)',
    },
  } satisfies ChartConfig
  const latencyRows = [
    {
      metric: 'requestDuration' as const,
      values: latency.duration,
      p99Threshold: settings?.request_p99_threshold_ms,
    },
    {
      metric: 'ttft' as const,
      values: latency.ttft,
      p99Threshold: settings?.ttft_p99_threshold_ms,
    },
  ]

  return (
    <div className='flex flex-col gap-4'>
      {snapshot.diagnostics.length > 0 && (
        <div className='grid gap-3 lg:grid-cols-2'>
          {snapshot.diagnostics.map((diagnostic) => {
            const tone = diagnosticTone(diagnostic.severity)
            const metricId = diagnosticMetricId(diagnostic.metric)
            return (
              <Alert
                key={`${diagnostic.metric}-${diagnostic.severity}-${diagnostic.value}-${diagnostic.threshold}`}
                variant={tone === 'critical' ? 'destructive' : 'default'}
                className={cn(
                  tone === 'warning' &&
                    'border-warning/50 text-warning [&>svg]:text-warning',
                  tone === 'info' &&
                    'border-info/50 text-info [&>svg]:text-info'
                )}
              >
                <AlertTitle>{t('Operations diagnosis')}</AlertTitle>
                <AlertDescription>
                  {t('Metric')}:{' '}
                  {metricId ? (
                    <MetricHelp metric={metricId} />
                  ) : (
                    diagnostic.metric
                  )}
                  {diagnostic.threshold > 0
                    ? ` · ${t('Value')}: ${diagnostic.value.toFixed(3)} · ${t('Threshold')}: ${diagnostic.threshold.toFixed(3)}`
                    : ''}
                </AlertDescription>
              </Alert>
            )
          })}
        </div>
      )}

      <div className='grid gap-4 sm:grid-cols-2 xl:grid-cols-4'>
        <MetricCard
          metric='effectiveRequestSuccessRate'
          value={overview.request_count ? formatPercent(overview.sla, 3) : '-'}
          description={`${t('Effective request failures')}: ${formatCompact(overview.sla_error_count)} · ${t('Business limits')}: ${formatCompact(overview.business_limit_count)}`}
          tone={effectiveSuccessTone}
        />
        <MetricCard
          metric='requestsAndTokens'
          value={formatCompact(overview.request_count)}
          description={`${formatCompact(overview.total_tokens)} ${t('tokens')}`}
          tone='info'
        />
        <MetricCard
          metric='realtimeThroughput'
          value={`${realtime.current_qps.toFixed(2)} / ${realtime.current_tps.toFixed(1)}`}
          description={`${t('Peak')}: ${realtime.peak_qps.toFixed(2)} / ${realtime.peak_tps.toFixed(1)}`}
          tone='info'
        />
        <MetricCard
          metric='errorRates'
          value={`${formatPercent(overview.request_error_rate)} / ${formatPercent(overview.upstream_error_rate)}`}
          description={`${t('Upstream errors')}: ${formatCompact(overview.upstream_error_count)}`}
          tone={errorTone}
        />
        <MetricCard
          metric='requestLatencyP99'
          value={
            latency.duration.samples
              ? formatMilliseconds(latency.duration.p99)
              : '-'
          }
          description={`${t('Average')}: ${latency.duration.samples ? formatMilliseconds(latency.duration.avg) : '-'} · ${t('Samples')}: ${formatCompact(latency.duration.samples)}`}
          tone={requestLatencyTone}
        />
        <MetricCard
          metric='ttftP99'
          value={
            latency.ttft.samples ? formatMilliseconds(latency.ttft.p99) : '-'
          }
          description={`${t('Average')}: ${latency.ttft.samples ? formatMilliseconds(latency.ttft.avg) : '-'} · ${t('Samples')}: ${formatCompact(latency.ttft.samples)}`}
          tone={ttftTone}
        />
        <MetricCard
          metric='averageAccountSwitches'
          value={overview.average_switches.toFixed(3)}
          description={t('Average switches per downstream request')}
          tone='info'
        />
        <MetricCard
          metric='telemetryHealth'
          value={
            snapshot.telemetry.dropped_requests +
              snapshot.telemetry.dropped_attempts ===
            0
              ? t('Healthy')
              : t('Degraded')
          }
          description={`${t('Queue depth')}: ${snapshot.telemetry.request_queue_depth + snapshot.telemetry.attempt_queue_depth}`}
          tone={telemetryTone}
        />
      </div>

      <div className='grid gap-4 xl:grid-cols-3'>
        <Card>
          <CardHeader>
            <CardTitle>
              <MetricHelp
                metric='realtimeThroughput'
                label={t('Throughput trend')}
              />
            </CardTitle>
            <CardDescription>
              {t('QPS and token throughput for the selected window')}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <ChartContainer
              config={throughputConfig}
              className='aspect-auto h-72 w-full'
            >
              <LineChart data={snapshot.throughput_trend}>
                <CartesianGrid vertical={false} />
                <XAxis
                  dataKey='ts'
                  tickFormatter={(value) =>
                    new Date(value * 1000).toLocaleTimeString([], {
                      hour: '2-digit',
                      minute: '2-digit',
                    })
                  }
                />
                <YAxis yAxisId='qps' width={45} />
                <YAxis yAxisId='tps' orientation='right' width={55} />
                <ChartTooltip content={<ChartTooltipContent />} />
                <Line
                  yAxisId='qps'
                  dataKey='qps'
                  stroke='var(--color-qps)'
                  dot={false}
                />
                <Line
                  yAxisId='tps'
                  dataKey='tps'
                  stroke='var(--color-tps)'
                  dot={false}
                />
              </LineChart>
            </ChartContainer>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>
              <MetricHelp metric='errorRates' label={t('Error trend')} />
            </CardTitle>
            <CardDescription>
              {t(
                'Effective request failures, upstream errors, and business limits'
              )}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <ChartContainer
              config={errorConfig}
              className='aspect-auto h-72 w-full'
            >
              <LineChart data={snapshot.error_trend}>
                <CartesianGrid vertical={false} />
                <XAxis
                  dataKey='ts'
                  tickFormatter={(value) =>
                    new Date(value * 1000).toLocaleTimeString([], {
                      hour: '2-digit',
                      minute: '2-digit',
                    })
                  }
                />
                <YAxis width={45} allowDecimals={false} />
                <ChartTooltip content={<ChartTooltipContent />} />
                <Line
                  dataKey='sla_errors'
                  stroke='var(--color-sla_errors)'
                  dot={false}
                />
                <Line
                  dataKey='upstream_errors'
                  stroke='var(--color-upstream_errors)'
                  dot={false}
                />
                <Line
                  dataKey='business_limits'
                  stroke='var(--color-business_limits)'
                  dot={false}
                />
              </LineChart>
            </ChartContainer>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>
              <MetricHelp
                metric='averageAccountSwitches'
                label={t('Account switch trend')}
              />
            </CardTitle>
            <CardDescription>
              {t('Average switches per downstream request')}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <ChartContainer
              config={switchConfig}
              className='aspect-auto h-72 w-full'
            >
              <LineChart data={snapshot.switch_trend}>
                <CartesianGrid vertical={false} />
                <XAxis
                  dataKey='ts'
                  tickFormatter={(value) =>
                    new Date(value * 1000).toLocaleTimeString([], {
                      hour: '2-digit',
                      minute: '2-digit',
                    })
                  }
                />
                <YAxis width={45} />
                <ChartTooltip content={<ChartTooltipContent />} />
                <Line
                  dataKey='average'
                  stroke='var(--color-average)'
                  dot={false}
                />
              </LineChart>
            </ChartContainer>
          </CardContent>
        </Card>
      </div>

      <div className='grid gap-4 xl:grid-cols-2'>
        <Card>
          <CardHeader>
            <CardTitle>{t('Latency distribution')}</CardTitle>
            <CardDescription>
              {latency.approximate
                ? t('Long-window percentile values are approximate')
                : t('Calculated from raw request samples')}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('Metric')}</TableHead>
                  {['P50', 'P90', 'P95', 'P99'].map((percentile) => (
                    <TableHead key={percentile}>
                      <MetricHelp
                        metric='latencyPercentile'
                        label={percentile}
                      />
                    </TableHead>
                  ))}
                  <TableHead>
                    <MetricHelp metric='latencyPercentile' label={t('Max')} />
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {latencyRows.map(({ metric, values, p99Threshold }) => (
                  <TableRow key={metric}>
                    <TableCell>
                      <MetricHelp metric={metric} />
                    </TableCell>
                    {[
                      { percentile: 'p50', value: values.p50 },
                      { percentile: 'p90', value: values.p90 },
                      { percentile: 'p95', value: values.p95 },
                      { percentile: 'p99', value: values.p99 },
                      { percentile: 'max', value: values.max },
                    ].map((percentile) => (
                      <TableCell
                        key={percentile.percentile}
                        className={cn(
                          'tabular-nums',
                          METRIC_TONE_TEXT_CLASS[
                            latencyPercentileTone(
                              values.samples > 0,
                              percentile.percentile,
                              percentile.value,
                              p99Threshold
                            )
                          ]
                        )}
                      >
                        {values.samples
                          ? formatMilliseconds(percentile.value)
                          : '-'}
                      </TableCell>
                    ))}
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>{t('OpenAI token request statistics')}</CardTitle>
            <CardDescription>
              {t('Models whose names start with gpt, over the last 30 days')}
            </CardDescription>
          </CardHeader>
          <CardContent className='max-h-72 overflow-auto'>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('Model')}</TableHead>
                  <TableHead>
                    <MetricHelp metric='modelRequests' />
                  </TableHead>
                  <TableHead>
                    <MetricHelp metric='tokensPerSecond' />
                  </TableHead>
                  <TableHead>
                    <MetricHelp metric='averageTtft' />
                  </TableHead>
                  <TableHead>
                    <MetricHelp metric='averageDuration' />
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {snapshot.model_token_stats.map((row) => (
                  <TableRow key={row.model_name}>
                    <TableCell>
                      <Badge variant='outline'>{row.model_name}</Badge>
                    </TableCell>
                    <TableCell className='text-info'>
                      {formatCompact(row.request_count)}
                    </TableCell>
                    <TableCell className='text-info'>
                      {row.average_tokens_per_second.toFixed(2)}
                    </TableCell>
                    <TableCell
                      className={
                        row.ttft_samples ? 'text-info' : 'text-muted-foreground'
                      }
                    >
                      {row.ttft_samples
                        ? formatMilliseconds(row.average_ttft_ms)
                        : '-'}
                    </TableCell>
                    <TableCell className='text-info'>
                      {formatMilliseconds(row.average_duration_ms)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      </div>
    </div>
  )
}
