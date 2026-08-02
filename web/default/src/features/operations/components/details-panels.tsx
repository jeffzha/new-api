import { useQuery } from '@tanstack/react-query'
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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { CartesianGrid, Line, LineChart, XAxis, YAxis } from 'recharts'

import { StatusBadge as SemanticStatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
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
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Progress } from '@/components/ui/progress'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { cn } from '@/lib/utils'

import { getOpsRequestDetail } from '../api'
import {
  formatBytesPerSecond,
  formatCompact,
  formatMilliseconds,
  formatPercent,
  formatTimestamp,
} from '../format'
import {
  availabilityTone,
  boundedUsageTone,
  connectionPoolTone,
  concurrencyLoadTone,
  healthScoreTone,
  higherIsBetterTone,
  lowerIsBetterTone,
  METRIC_TONE_PROGRESS_CLASS,
  METRIC_TONE_TEXT_CLASS,
  statusCodeTone,
  type MetricTone,
} from '../metric-status'
import type { OpsSettings, OpsSnapshot } from '../types'

function statusVariantFromTone(tone: MetricTone) {
  return tone === 'critical' ? ('danger' as const) : tone
}

function aggregateSystemTrend(snapshot: OpsSnapshot) {
  const buckets = new Map<
    number,
    {
      ts: number
      cpu: number
      memory: number
      disk: number
      receive: number
      transmit: number
      count: number
    }
  >()
  for (const row of snapshot.system.trend) {
    const bucket = buckets.get(row.bucket_ts) ?? {
      ts: row.bucket_ts,
      cpu: 0,
      memory: 0,
      disk: 0,
      receive: 0,
      transmit: 0,
      count: 0,
    }
    bucket.cpu += row.cpu_percent
    bucket.memory += row.memory_percent
    bucket.disk += row.disk_percent
    bucket.receive += row.network_receive_bps
    bucket.transmit += row.network_transmit_bps
    bucket.count += 1
    buckets.set(row.bucket_ts, bucket)
  }
  return [...buckets.values()]
    .sort((left, right) => left.ts - right.ts)
    .map((bucket) => ({
      ts: bucket.ts,
      cpu: bucket.cpu / bucket.count,
      memory: bucket.memory / bucket.count,
      disk: bucket.disk / bucket.count,
      receive: bucket.receive / bucket.count,
      transmit: bucket.transmit / bucket.count,
    }))
}

function HealthStatusBadge({
  healthy,
  label,
  unhealthyTone = 'critical',
}: {
  healthy: boolean
  label: string
  unhealthyTone?: 'warning' | 'critical'
}) {
  const variant = unhealthyTone === 'warning' ? 'warning' : 'danger'
  return (
    <SemanticStatusBadge
      variant={healthy ? 'success' : variant}
      label={label}
      copyable={false}
      showDot
    />
  )
}

export function InfrastructurePanel({ snapshot }: { snapshot: OpsSnapshot }) {
  const { t } = useTranslation()
  const trend = aggregateSystemTrend(snapshot)
  const overallHealthTone = healthScoreTone(
    snapshot.health.score,
    snapshot.health.state
  )
  const resourceConfig = {
    cpu: { label: 'CPU', color: 'var(--chart-1)' },
    memory: { label: t('Memory'), color: 'var(--chart-2)' },
    disk: { label: t('Disk'), color: 'var(--chart-3)' },
  } satisfies ChartConfig
  const networkConfig = {
    receive: { label: t('Receive'), color: 'var(--chart-4)' },
    transmit: { label: t('Transmit'), color: 'var(--chart-5)' },
  } satisfies ChartConfig

  return (
    <div className='flex flex-col gap-4'>
      <div className='grid gap-4 xl:grid-cols-[minmax(0,2fr)_minmax(20rem,1fr)]'>
        <Card>
          <CardHeader>
            <CardTitle>{t('System resources by node')}</CardTitle>
            <CardDescription>
              {t('Container-aware CPU and memory, network, storage, and pools')}
            </CardDescription>
          </CardHeader>
          <CardContent className='overflow-auto'>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('Node')}</TableHead>
                  <TableHead>CPU</TableHead>
                  <TableHead>{t('Memory')}</TableHead>
                  <TableHead>{t('Disk')}</TableHead>
                  <TableHead>{t('Network')}</TableHead>
                  <TableHead>DB</TableHead>
                  <TableHead>Redis</TableHead>
                  <TableHead>{t('Goroutines')}</TableHead>
                  <TableHead>{t('Queue')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {snapshot.system.latest_by_node.map((node) => (
                  <TableRow key={node.node_name}>
                    <TableCell className='font-medium'>
                      {node.node_name}
                    </TableCell>
                    <TableCell className='min-w-28'>
                      <div className='flex flex-col gap-1'>
                        <span
                          className={cn(
                            'tabular-nums',
                            METRIC_TONE_TEXT_CLASS[
                              boundedUsageTone(node.cpu_percent, 80, 95)
                            ]
                          )}
                        >
                          {node.cpu_percent.toFixed(1)}%
                        </span>
                        <Progress
                          value={node.cpu_percent}
                          className={
                            METRIC_TONE_PROGRESS_CLASS[
                              boundedUsageTone(node.cpu_percent, 80, 95)
                            ]
                          }
                        />
                      </div>
                    </TableCell>
                    <TableCell className='min-w-28'>
                      <div className='flex flex-col gap-1'>
                        <span
                          className={cn(
                            'tabular-nums',
                            METRIC_TONE_TEXT_CLASS[
                              boundedUsageTone(node.memory_percent, 85, 95)
                            ]
                          )}
                        >
                          {node.memory_percent.toFixed(1)}%
                        </span>
                        <Progress
                          value={node.memory_percent}
                          className={
                            METRIC_TONE_PROGRESS_CLASS[
                              boundedUsageTone(node.memory_percent, 85, 95)
                            ]
                          }
                        />
                      </div>
                    </TableCell>
                    <TableCell
                      className={
                        METRIC_TONE_TEXT_CLASS[
                          boundedUsageTone(node.disk_percent, 80, 95)
                        ]
                      }
                    >
                      {node.disk_percent.toFixed(1)}%
                    </TableCell>
                    <TableCell className='text-info whitespace-nowrap'>
                      ↓ {formatBytesPerSecond(node.network_receive_bps)}
                      <br />↑ {formatBytesPerSecond(node.network_transmit_bps)}
                    </TableCell>
                    <TableCell className='whitespace-nowrap'>
                      <HealthStatusBadge
                        healthy={node.db_healthy}
                        label={node.db_healthy ? t('Healthy') : t('Down')}
                      />
                      <div
                        className={cn(
                          'mt-1 text-xs tabular-nums',
                          METRIC_TONE_TEXT_CLASS[
                            connectionPoolTone(
                              node.db_healthy,
                              node.db_open_connections,
                              node.db_max_open_connections
                            )
                          ]
                        )}
                      >
                        {node.db_in_use_connections}/{node.db_open_connections}
                        {node.db_max_open_connections > 0
                          ? `/${node.db_max_open_connections}`
                          : ''}
                      </div>
                    </TableCell>
                    <TableCell className='whitespace-nowrap'>
                      {node.redis_enabled ? (
                        <>
                          <HealthStatusBadge
                            healthy={node.redis_healthy}
                            label={
                              node.redis_healthy ? t('Healthy') : t('Down')
                            }
                          />
                          <div
                            className={cn(
                              'mt-1 text-xs tabular-nums',
                              METRIC_TONE_TEXT_CLASS[
                                connectionPoolTone(
                                  node.redis_healthy,
                                  node.redis_total_connections,
                                  node.redis_max_connections
                                )
                              ]
                            )}
                          >
                            {node.redis_total_connections -
                              node.redis_idle_connections}
                            /{node.redis_total_connections}
                            {node.redis_max_connections > 0
                              ? `/${node.redis_max_connections}`
                              : ''}
                          </div>
                        </>
                      ) : (
                        <SemanticStatusBadge
                          variant='neutral'
                          label={t('Disabled')}
                          copyable={false}
                          showDot
                        />
                      )}
                    </TableCell>
                    <TableCell
                      className={
                        METRIC_TONE_TEXT_CLASS[
                          boundedUsageTone(node.goroutines, 8_000, 15_000)
                        ]
                      }
                    >
                      {formatCompact(node.goroutines)}
                    </TableCell>
                    <TableCell
                      className={
                        METRIC_TONE_TEXT_CLASS[
                          node.queue_depth > 0 ? 'warning' : 'success'
                        ]
                      }
                    >
                      {formatCompact(node.queue_depth)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>{t('Composite health score')}</CardTitle>
            <CardDescription>
              {t('Business 70%, infrastructure 30%')}
            </CardDescription>
          </CardHeader>
          <CardContent className='flex flex-col gap-4'>
            <div
              className={cn(
                'text-4xl font-semibold tabular-nums',
                METRIC_TONE_TEXT_CLASS[overallHealthTone]
              )}
            >
              {snapshot.health.score}
            </div>
            {[
              [t('Business'), snapshot.health.business_score],
              [t('Error score'), snapshot.health.error_score],
              [t('TTFT score'), snapshot.health.ttft_score],
              [t('Storage'), snapshot.health.storage_score],
              [t('Compute'), snapshot.health.compute_score],
              [t('Jobs'), snapshot.health.job_score],
            ].map(([label, value]) => (
              <div key={label as string} className='flex flex-col gap-1'>
                <div className='flex justify-between text-sm'>
                  <span>{label as string}</span>
                  <span
                    className={cn(
                      'tabular-nums',
                      METRIC_TONE_TEXT_CLASS[
                        healthScoreTone(value as number, snapshot.health.state)
                      ]
                    )}
                  >
                    {(value as number).toFixed(0)}
                  </span>
                </div>
                <Progress
                  value={value as number}
                  className={
                    METRIC_TONE_PROGRESS_CLASS[
                      healthScoreTone(value as number, snapshot.health.state)
                    ]
                  }
                />
              </div>
            ))}
          </CardContent>
        </Card>
      </div>

      <div className='grid gap-4 xl:grid-cols-2'>
        <Card>
          <CardHeader>
            <CardTitle>{t('System resource trend')}</CardTitle>
            <CardDescription>
              {t('Average across nodes for each collection bucket')}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <ChartContainer
              config={resourceConfig}
              className='aspect-auto h-64 w-full'
            >
              <LineChart data={trend}>
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
                <YAxis width={45} domain={[0, 100]} />
                <ChartTooltip content={<ChartTooltipContent />} />
                <Line dataKey='cpu' stroke='var(--color-cpu)' dot={false} />
                <Line
                  dataKey='memory'
                  stroke='var(--color-memory)'
                  dot={false}
                />
                <Line dataKey='disk' stroke='var(--color-disk)' dot={false} />
              </LineChart>
            </ChartContainer>
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>{t('Network throughput trend')}</CardTitle>
            <CardDescription>{t('Bytes per second')}</CardDescription>
          </CardHeader>
          <CardContent>
            <ChartContainer
              config={networkConfig}
              className='aspect-auto h-64 w-full'
            >
              <LineChart data={trend}>
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
                <YAxis
                  width={70}
                  tickFormatter={(value) => formatCompact(value)}
                />
                <ChartTooltip content={<ChartTooltipContent />} />
                <Line
                  dataKey='receive'
                  stroke='var(--color-receive)'
                  dot={false}
                />
                <Line
                  dataKey='transmit'
                  stroke='var(--color-transmit)'
                  dot={false}
                />
              </LineChart>
            </ChartContainer>
          </CardContent>
        </Card>
      </div>
    </div>
  )
}

export function ConcurrencyPanel({ snapshot }: { snapshot: OpsSnapshot }) {
  const { t } = useTranslation()
  const concurrency = snapshot.concurrency

  return (
    <div className='flex flex-col gap-4'>
      <Card>
        <CardHeader>
          <div className='flex flex-wrap items-center justify-between gap-2'>
            <div>
              <CardTitle>{t('Channel concurrency')}</CardTitle>
              <CardDescription>
                {t('Real-time occupancy, queues, and runtime availability')}
              </CardDescription>
            </div>
            <div className='flex gap-2'>
              <SemanticStatusBadge
                variant={
                  concurrency.enforcement_enabled ? 'success' : 'neutral'
                }
                label={
                  concurrency.enforcement_enabled
                    ? t('Enforcement enabled')
                    : t('Observation only')
                }
                copyable={false}
                showDot
              />
              <SemanticStatusBadge
                variant={concurrency.redis_backed ? 'success' : 'neutral'}
                label={
                  concurrency.redis_backed
                    ? t('Redis coordinated')
                    : t('Local process only')
                }
                copyable={false}
                showDot
              />
            </div>
          </div>
        </CardHeader>
        <CardContent className='overflow-auto'>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('Channel')}</TableHead>
                <TableHead>{t('Platform')}</TableHead>
                <TableHead>{t('Groups')}</TableHead>
                <TableHead>{t('In use')}</TableHead>
                <TableHead>{t('Capacity')}</TableHead>
                <TableHead>{t('Waiting')}</TableHead>
                <TableHead>{t('Load')}</TableHead>
                <TableHead>{t('Availability')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {concurrency.channels.map((channel) => (
                <TableRow key={channel.channel_id}>
                  <TableCell>
                    <div className='font-medium'>{channel.channel_name}</div>
                    <div className='text-muted-foreground text-xs'>
                      ID {channel.channel_id}
                    </div>
                  </TableCell>
                  <TableCell>{channel.platform}</TableCell>
                  <TableCell>{channel.groups.join(', ') || '-'}</TableCell>
                  <TableCell className='text-info tabular-nums'>
                    {channel.in_use}
                  </TableCell>
                  <TableCell
                    className={
                      channel.capacity > 0
                        ? 'text-info tabular-nums'
                        : 'text-muted-foreground'
                    }
                  >
                    {channel.capacity || '-'}
                  </TableCell>
                  <TableCell
                    className={
                      METRIC_TONE_TEXT_CLASS[
                        channel.waiting > 0 ? 'warning' : 'success'
                      ]
                    }
                  >
                    {channel.waiting}
                  </TableCell>
                  <TableCell className='min-w-32'>
                    {channel.capacity > 0 ? (
                      <div className='flex flex-col gap-1'>
                        <span
                          className={
                            METRIC_TONE_TEXT_CLASS[
                              concurrencyLoadTone(channel.load_percent)
                            ]
                          }
                        >
                          {channel.load_percent.toFixed(0)}%
                        </span>
                        <Progress
                          value={Math.min(channel.load_percent, 100)}
                          className={
                            METRIC_TONE_PROGRESS_CLASS[
                              concurrencyLoadTone(channel.load_percent)
                            ]
                          }
                        />
                      </div>
                    ) : (
                      '-'
                    )}
                  </TableCell>
                  <TableCell>
                    <HealthStatusBadge
                      healthy={channel.available}
                      label={
                        channel.available
                          ? t('Available')
                          : channel.unavailable_reason || t('Unavailable')
                      }
                    />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <div className='grid gap-4 xl:grid-cols-2'>
        {[
          { title: t('Platform concurrency'), rows: concurrency.platforms },
          { title: t('Group concurrency'), rows: concurrency.groups },
        ].map(({ title, rows }) => (
          <Card key={title}>
            <CardHeader>
              <CardTitle>{title}</CardTitle>
              <CardDescription>
                {t('Aggregated occupancy, capacity, queue, and availability')}
              </CardDescription>
            </CardHeader>
            <CardContent>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t('Name')}</TableHead>
                    <TableHead>{t('In use')}</TableHead>
                    <TableHead>{t('Capacity')}</TableHead>
                    <TableHead>{t('Waiting')}</TableHead>
                    <TableHead>{t('Load')}</TableHead>
                    <TableHead>{t('Available')}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {rows.map((row) => (
                    <TableRow key={row.name}>
                      <TableCell>{row.name}</TableCell>
                      <TableCell className='text-info tabular-nums'>
                        {row.in_use}
                      </TableCell>
                      <TableCell
                        className={
                          row.capacity > 0
                            ? 'text-info tabular-nums'
                            : 'text-muted-foreground'
                        }
                      >
                        {row.capacity || '-'}
                      </TableCell>
                      <TableCell
                        className={
                          METRIC_TONE_TEXT_CLASS[
                            row.waiting > 0 ? 'warning' : 'success'
                          ]
                        }
                      >
                        {row.waiting}
                      </TableCell>
                      <TableCell
                        className={
                          METRIC_TONE_TEXT_CLASS[
                            concurrencyLoadTone(row.load_percent)
                          ]
                        }
                      >
                        {row.capacity > 0
                          ? `${row.load_percent.toFixed(0)}%`
                          : '-'}
                      </TableCell>
                      <TableCell
                        className={
                          METRIC_TONE_TEXT_CLASS[
                            availabilityTone(row.available, row.total)
                          ]
                        }
                      >
                        {row.available}/{row.total}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </CardContent>
          </Card>
        ))}
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t('Active user concurrency')}</CardTitle>
          <CardDescription>
            {t('Only users with active leases are shown')}
          </CardDescription>
        </CardHeader>
        <CardContent className='flex flex-wrap gap-2'>
          {concurrency.users.length ? (
            concurrency.users.map((user) => (
              <SemanticStatusBadge
                key={user.user_id}
                variant='info'
                label={`${t('User')} ${user.user_id}: ${user.in_use}`}
                copyable={false}
                showDot
              />
            ))
          ) : (
            <span className='text-muted-foreground text-sm'>
              {t('No active concurrency')}
            </span>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

export function TaskPanel({
  snapshot,
  settings,
}: {
  snapshot: OpsSnapshot
  settings?: OpsSettings
}) {
  const { t } = useTranslation()
  const tasks = snapshot.tasks
  const submitTone = tasks.submitted
    ? higherIsBetterTone(
        tasks.submit_success_rate,
        settings?.sla_threshold,
        0.001
      )
    : 'neutral'
  const generationTone = tasks.terminal
    ? higherIsBetterTone(
        tasks.generation_success_rate,
        settings?.sla_threshold,
        0.001
      )
    : 'neutral'
  let failureTone: MetricTone = 'neutral'
  if (tasks.terminal > 0) {
    failureTone = tasks.failed > 0 ? 'critical' : 'success'
  }

  return (
    <div className='grid gap-4 xl:grid-cols-2'>
      <Card>
        <CardHeader>
          <CardTitle>{t('Asynchronous task lifecycle')}</CardTitle>
          <CardDescription>
            {t('Submission and generation are measured separately')}
          </CardDescription>
        </CardHeader>
        <CardContent className='grid gap-4 sm:grid-cols-2'>
          <div>
            <div className='text-muted-foreground text-sm'>
              {t('Submit success rate')}
            </div>
            <div
              className={cn(
                'text-2xl font-semibold tabular-nums',
                METRIC_TONE_TEXT_CLASS[submitTone]
              )}
            >
              {tasks.submitted ? formatPercent(tasks.submit_success_rate) : '-'}
            </div>
            <div className='text-muted-foreground text-xs'>
              {tasks.submit_succeeded}/{tasks.submitted}
            </div>
          </div>
          <div>
            <div className='text-muted-foreground text-sm'>
              {t('Generation success rate')}
            </div>
            <div
              className={cn(
                'text-2xl font-semibold tabular-nums',
                METRIC_TONE_TEXT_CLASS[generationTone]
              )}
            >
              {tasks.terminal
                ? formatPercent(tasks.generation_success_rate)
                : '-'}
            </div>
            <div className='text-muted-foreground text-xs'>
              {tasks.succeeded}/{tasks.terminal}
            </div>
          </div>
          <div>
            <div className='text-muted-foreground text-sm'>
              {t('Average queue time')}
            </div>
            <div
              className={cn(
                'text-xl font-medium',
                tasks.submitted ? 'text-info' : 'text-muted-foreground'
              )}
            >
              {tasks.submitted
                ? formatMilliseconds(tasks.average_queue_ms)
                : '-'}
            </div>
          </div>
          <div>
            <div className='text-muted-foreground text-sm'>
              {t('Average generation time')}
            </div>
            <div
              className={cn(
                'text-xl font-medium',
                tasks.terminal ? 'text-info' : 'text-muted-foreground'
              )}
            >
              {tasks.terminal
                ? formatMilliseconds(tasks.average_generation_ms)
                : '-'}
            </div>
          </div>
          <div>
            <div className='text-muted-foreground text-sm'>
              {t('Average end-to-end time')}
            </div>
            <div
              className={cn(
                'text-xl font-medium',
                tasks.terminal ? 'text-info' : 'text-muted-foreground'
              )}
            >
              {tasks.terminal
                ? formatMilliseconds(tasks.average_end_to_end_ms)
                : '-'}
            </div>
          </div>
          <div>
            <div className='text-muted-foreground text-sm'>{t('Failures')}</div>
            <div
              className={cn(
                'text-xl font-medium tabular-nums',
                METRIC_TONE_TEXT_CLASS[failureTone]
              )}
            >
              {tasks.failed}
            </div>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t('Background job heartbeat')}</CardTitle>
          <CardDescription>
            {t('Latest success and failure for each system task type')}
          </CardDescription>
        </CardHeader>
        <CardContent className='overflow-auto'>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('Job')}</TableHead>
                <TableHead>{t('Status')}</TableHead>
                <TableHead>{t('Last success')}</TableHead>
                <TableHead>{t('Last error')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {snapshot.jobs.map((job) => (
                <TableRow key={job.type}>
                  <TableCell>{job.type}</TableCell>
                  <TableCell>
                    <HealthStatusBadge
                      healthy={job.status === 'healthy'}
                      label={job.status}
                      unhealthyTone='warning'
                    />
                  </TableCell>
                  <TableCell>{formatTimestamp(job.last_success_at)}</TableCell>
                  <TableCell>{formatTimestamp(job.last_error_at)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </div>
  )
}

export function ErrorsPanel({
  snapshot,
  settings,
}: {
  snapshot: OpsSnapshot
  settings?: OpsSettings
}) {
  const { t } = useTranslation()
  const [requestId, setRequestId] = useState('')
  const detailQuery = useQuery({
    queryKey: ['ops', 'request-detail', requestId],
    queryFn: async () => {
      const response = await getOpsRequestDetail(requestId)
      if (!response.success || !response.data) {
        throw new Error(response.message || t('Load failed'))
      }
      return response.data
    },
    enabled: requestId !== '',
    staleTime: 5 * 60 * 1000,
  })

  return (
    <>
      <div className='flex flex-col gap-4'>
        <Card>
          <CardHeader>
            <CardTitle>{t('Error status distribution')}</CardTitle>
            <CardDescription>
              {t('Top 20 effective status codes in the selected window')}
            </CardDescription>
          </CardHeader>
          <CardContent className='flex flex-wrap gap-2'>
            {snapshot.errors.distribution.map((row) => (
              <SemanticStatusBadge
                key={row.status_code}
                variant={statusVariantFromTone(statusCodeTone(row.status_code))}
                label={`${row.status_code}: ${row.total}`}
                copyable={false}
                showDot
              />
            ))}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>{t('Recent errors')}</CardTitle>
            <CardDescription>
              {t('Normalized errors without request or response bodies')}
            </CardDescription>
          </CardHeader>
          <CardContent className='overflow-auto'>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('Time')}</TableHead>
                  <TableHead>{t('Request ID')}</TableHead>
                  <TableHead>{t('Endpoint')}</TableHead>
                  <TableHead>{t('Model')}</TableHead>
                  <TableHead>{t('Status')}</TableHead>
                  <TableHead>{t('Owner')}</TableHead>
                  <TableHead>{t('Error')}</TableHead>
                  <TableHead>{t('Duration')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {snapshot.recent_errors.map((error) => (
                  <TableRow
                    key={`${error.request_id}-${error.occurred_at_ms}-${error.endpoint_type}-${error.status_code}`}
                  >
                    <TableCell className='whitespace-nowrap'>
                      {formatTimestamp(error.occurred_at_ms)}
                    </TableCell>
                    <TableCell>
                      {error.request_id ? (
                        <Button
                          size='sm'
                          variant='ghost'
                          className='max-w-40 font-mono text-xs'
                          onClick={() => setRequestId(error.request_id)}
                        >
                          <span className='truncate'>{error.request_id}</span>
                        </Button>
                      ) : (
                        '-'
                      )}
                    </TableCell>
                    <TableCell>
                      <div>{error.endpoint_type}</div>
                      <div className='text-muted-foreground max-w-56 truncate text-xs'>
                        {error.method} {error.path}
                      </div>
                    </TableCell>
                    <TableCell>{error.model_name || '-'}</TableCell>
                    <TableCell>
                      <SemanticStatusBadge
                        variant={statusVariantFromTone(
                          statusCodeTone(
                            error.upstream_status_code || error.status_code
                          )
                        )}
                        label={String(
                          error.upstream_status_code || error.status_code
                        )}
                        copyable={false}
                        showDot
                      />
                    </TableCell>
                    <TableCell>{error.error_owner || '-'}</TableCell>
                    <TableCell className='max-w-80'>
                      <div
                        className={cn(
                          'font-medium',
                          METRIC_TONE_TEXT_CLASS[
                            statusCodeTone(
                              error.upstream_status_code || error.status_code
                            )
                          ]
                        )}
                      >
                        {error.error_code || '-'}
                      </div>
                      <div className='text-muted-foreground truncate text-xs'>
                        {error.error_summary}
                      </div>
                    </TableCell>
                    <TableCell
                      className={
                        METRIC_TONE_TEXT_CLASS[
                          lowerIsBetterTone(
                            error.duration_ms,
                            settings?.request_p99_threshold_ms
                          )
                        ]
                      }
                    >
                      {formatMilliseconds(error.duration_ms)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      </div>

      <Dialog
        open={requestId !== ''}
        onOpenChange={(open) => !open && setRequestId('')}
      >
        <DialogContent className='max-w-4xl'>
          <DialogHeader>
            <DialogTitle>{t('Request attempt details')}</DialogTitle>
            <DialogDescription className='font-mono break-all'>
              {requestId}
            </DialogDescription>
          </DialogHeader>
          {detailQuery.isLoading && <Skeleton className='h-48 w-full' />}
          {!detailQuery.isLoading && detailQuery.data && (
            <div className='flex max-h-[70vh] flex-col gap-4 overflow-auto'>
              <div className='grid gap-3 sm:grid-cols-3'>
                <div>
                  <div className='text-muted-foreground text-xs'>
                    {t('Endpoint')}
                  </div>
                  <div>{detailQuery.data.request.endpoint_type}</div>
                </div>
                <div>
                  <div className='text-muted-foreground text-xs'>
                    {t('Model')}
                  </div>
                  <div>{detailQuery.data.request.model_name || '-'}</div>
                </div>
                <div>
                  <div className='text-muted-foreground text-xs'>
                    {t('Total duration')}
                  </div>
                  <div
                    className={
                      METRIC_TONE_TEXT_CLASS[
                        lowerIsBetterTone(
                          detailQuery.data.request.duration_ms,
                          settings?.request_p99_threshold_ms
                        )
                      ]
                    }
                  >
                    {formatMilliseconds(detailQuery.data.request.duration_ms)}
                  </div>
                </div>
              </div>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t('Attempt')}</TableHead>
                    <TableHead>{t('Channel')}</TableHead>
                    <TableHead>{t('Key')}</TableHead>
                    <TableHead>{t('Status')}</TableHead>
                    <TableHead>{t('Duration')}</TableHead>
                    <TableHead>{t('Concurrency wait')}</TableHead>
                    <TableHead>{t('Switched')}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {detailQuery.data.attempts.map((attempt) => (
                    <TableRow
                      key={`${attempt.attempt_index}-${attempt.channel_id}-${attempt.channel_key_index}`}
                    >
                      <TableCell>{attempt.attempt_index + 1}</TableCell>
                      <TableCell>{attempt.channel_id}</TableCell>
                      <TableCell>{attempt.channel_key_index}</TableCell>
                      <TableCell>
                        <SemanticStatusBadge
                          variant={statusVariantFromTone(
                            statusCodeTone(attempt.status_code)
                          )}
                          label={String(attempt.status_code)}
                          copyable={false}
                          showDot
                        />
                      </TableCell>
                      <TableCell
                        className={
                          METRIC_TONE_TEXT_CLASS[
                            lowerIsBetterTone(
                              attempt.duration_ms,
                              settings?.request_p99_threshold_ms
                            )
                          ]
                        }
                      >
                        {formatMilliseconds(attempt.duration_ms)}
                      </TableCell>
                      <TableCell
                        className={
                          METRIC_TONE_TEXT_CLASS[
                            attempt.concurrency_wait_ms > 0
                              ? 'warning'
                              : 'success'
                          ]
                        }
                      >
                        {formatMilliseconds(attempt.concurrency_wait_ms)}
                      </TableCell>
                      <TableCell
                        className={
                          METRIC_TONE_TEXT_CLASS[
                            attempt.switched_channel ? 'warning' : 'success'
                          ]
                        }
                      >
                        {attempt.switched_channel ? t('Yes') : t('No')}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
          {!detailQuery.isLoading && !detailQuery.data && (
            <div className='text-destructive text-sm'>
              {detailQuery.error instanceof Error
                ? detailQuery.error.message
                : t('Load failed')}
            </div>
          )}
        </DialogContent>
      </Dialog>
    </>
  )
}
