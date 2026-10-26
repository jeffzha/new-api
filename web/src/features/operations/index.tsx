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
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import {
  CircleAlert,
  Gauge,
  LayoutDashboard,
  ListTodo,
  Server,
  Settings2,
} from 'lucide-react'
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

import { StatusBadge as SemanticStatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Combobox } from '@/components/ui/combobox'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

import { getOpsSettings, getOpsSnapshot } from './api'
import {
  ConcurrencyPanel,
  ErrorsPanel,
  InfrastructurePanel,
  TaskPanel,
} from './components/details-panels'
import { OverviewPanel } from './components/overview-panel'
import { SettingsPanel } from './components/settings-panel'
import type { OpsSnapshot } from './types'

function arrayOrEmpty<T>(value: unknown): T[] {
  return Array.isArray(value) ? (value as T[]) : []
}

function recordOrEmpty(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === 'object'
    ? (value as Record<string, unknown>)
    : {}
}

function numericRecord(
  value: unknown,
  keys: readonly string[]
): Record<string, unknown> {
  const record = { ...recordOrEmpty(value) }
  for (const key of keys) {
    const numeric = record[key]
    record[key] =
      typeof numeric === 'number' && Number.isFinite(numeric) ? numeric : 0
  }
  return record
}

function normalizeOpsSnapshot(snapshot: OpsSnapshot): OpsSnapshot {
  const value = recordOrEmpty(snapshot)
  const overview = {
    request_count: 0,
    success_count: 0,
    error_count: 0,
    sla_error_count: 0,
    business_limit_count: 0,
    upstream_error_count: 0,
    sla: 0,
    request_error_rate: 0,
    upstream_error_rate: 0,
    input_tokens: 0,
    output_tokens: 0,
    cache_read_tokens: 0,
    cache_creation_tokens: 0,
    total_tokens: 0,
    average_qps: 0,
    average_tps: 0,
    average_switches: 0,
    success_rate: 0,
    failure_rate: 0,
    average_rpm: 0,
    average_tpm: 0,
    ...numericRecord(value.overview, [
      'request_count',
      'success_count',
      'error_count',
      'sla_error_count',
      'business_limit_count',
      'upstream_error_count',
      'sla',
      'request_error_rate',
      'upstream_error_rate',
      'input_tokens',
      'output_tokens',
      'cache_read_tokens',
      'cache_creation_tokens',
      'total_tokens',
      'average_qps',
      'average_tps',
      'average_switches',
      'success_rate',
      'failure_rate',
      'average_rpm',
      'average_tpm',
    ]),
  } as OpsSnapshot['overview']
  const realtime = {
    current_qps: 0,
    current_tps: 0,
    peak_qps: 0,
    peak_tps: 0,
    approximate: false,
    ...numericRecord(value.realtime, [
      'current_qps',
      'current_tps',
      'peak_qps',
      'peak_tps',
    ]),
  } as OpsSnapshot['realtime']
  const percentileDefaults = {
    p50: 0,
    p90: 0,
    p95: 0,
    p99: 0,
    avg: 0,
    max: 0,
    samples: 0,
  }
  const latencyValue = recordOrEmpty(value.latency)
  const latency = {
    duration: {
      ...percentileDefaults,
      ...numericRecord(latencyValue.duration, [
        'p50',
        'p90',
        'p95',
        'p99',
        'avg',
        'max',
        'samples',
      ]),
    },
    ttft: {
      ...percentileDefaults,
      ...numericRecord(latencyValue.ttft, [
        'p50',
        'p90',
        'p95',
        'p99',
        'avg',
        'max',
        'samples',
      ]),
    },
    histogram: arrayOrEmpty(latencyValue.histogram),
    approximate: Boolean(latencyValue.approximate),
  } as OpsSnapshot['latency']
  const health = {
    state: 'unknown',
    score: 0,
    business_score: 0,
    infra_score: 0,
    error_score: 0,
    ttft_score: 0,
    storage_score: 0,
    compute_score: 0,
    job_score: 0,
    ...numericRecord(value.health, [
      'score',
      'business_score',
      'infra_score',
      'error_score',
      'ttft_score',
      'storage_score',
      'compute_score',
      'job_score',
    ]),
  } as OpsSnapshot['health']
  const tasks = {
    submitted: 0,
    submit_succeeded: 0,
    terminal: 0,
    succeeded: 0,
    failed: 0,
    submit_success_rate: 0,
    generation_success_rate: 0,
    average_queue_ms: 0,
    average_generation_ms: 0,
    average_end_to_end_ms: 0,
    ...numericRecord(value.tasks, [
      'submitted',
      'submit_succeeded',
      'terminal',
      'succeeded',
      'failed',
      'submit_success_rate',
      'generation_success_rate',
      'average_queue_ms',
      'average_generation_ms',
      'average_end_to_end_ms',
    ]),
  } as OpsSnapshot['tasks']
  const telemetry = {
    request_queue_depth: 0,
    attempt_queue_depth: 0,
    dropped_requests: 0,
    dropped_attempts: 0,
    ...numericRecord(value.telemetry, [
      'request_queue_depth',
      'attempt_queue_depth',
      'dropped_requests',
      'dropped_attempts',
    ]),
  } as OpsSnapshot['telemetry']
  const concurrency = recordOrEmpty(value.concurrency)
  const system = recordOrEmpty(value.system)
  const errors = recordOrEmpty(value.errors)
  const options = recordOrEmpty(value.options)
  return {
    ...snapshot,
    overview,
    realtime,
    health,
    tasks,
    telemetry,
    throughput_trend: arrayOrEmpty(value.throughput_trend),
    switch_trend: arrayOrEmpty(value.switch_trend),
    error_trend: arrayOrEmpty(value.error_trend),
    model_token_stats: arrayOrEmpty(value.model_token_stats),
    recent_errors: arrayOrEmpty(value.recent_errors),
    jobs: arrayOrEmpty(value.jobs),
    diagnostics: arrayOrEmpty(value.diagnostics),
    system: {
      ...(system as OpsSnapshot['system']),
      latest_by_node: arrayOrEmpty(system.latest_by_node),
      trend: arrayOrEmpty(system.trend),
    },
    errors: {
      ...(errors as OpsSnapshot['errors']),
      distribution: arrayOrEmpty(errors.distribution),
    },
    concurrency: {
      ...(concurrency as OpsSnapshot['concurrency']),
      channels: arrayOrEmpty(concurrency.channels),
      platforms: arrayOrEmpty(concurrency.platforms),
      groups: arrayOrEmpty(concurrency.groups),
      users: arrayOrEmpty(concurrency.users),
    },
    options: {
      ...(options as OpsSnapshot['options']),
      models: arrayOrEmpty(options.models),
      groups: arrayOrEmpty(options.groups),
      endpoint_types: arrayOrEmpty(options.endpoint_types),
      nodes: arrayOrEmpty(options.nodes),
      users: arrayOrEmpty(options.users),
      tokens: arrayOrEmpty(options.tokens),
    },
    latency,
  } as OpsSnapshot
}

const WINDOW_SECONDS = {
  '5m': 5 * 60,
  '1h': 60 * 60,
  '24h': 24 * 60 * 60,
} as const

const LOADING_METRIC_IDS = [
  'sla',
  'requests',
  'traffic',
  'errors',
  'latency',
  'ttft',
  'switches',
  'telemetry',
] as const

type WindowKey = keyof typeof WINDOW_SECONDS

type FilterState = {
  userId: number
  tokenId: number
  model: string
  group: string
  endpoint: string
  node: string
  channelId: number
}

const emptyFilters: FilterState = {
  userId: 0,
  tokenId: 0,
  model: '',
  group: '',
  endpoint: '',
  node: '',
  channelId: 0,
}

export function OperationsDashboard() {
  const { t } = useTranslation()
  const [windowKey, setWindowKey] = useState<WindowKey>('1h')
  const [filters, setFilters] = useState<FilterState>(emptyFilters)
  const snapshotQuery = useQuery({
    queryKey: ['ops', 'snapshot', windowKey, filters],
    queryFn: async () => {
      const end = Math.floor(Date.now() / 1000)
      const response = await getOpsSnapshot({
        start: end - WINDOW_SECONDS[windowKey],
        end,
        model: filters.model || undefined,
        group: filters.group || undefined,
        channel_id: filters.channelId || undefined,
        endpoint_type: filters.endpoint || undefined,
        node_name: filters.node || undefined,
        user_id: filters.userId || undefined,
        token_id: filters.tokenId || undefined,
      })
      if (!response.success || !response.data) {
        throw new Error(response.message || t('Could not load operations data'))
      }
      return normalizeOpsSnapshot(response.data)
    },
    refetchInterval: 30 * 1000,
    placeholderData: keepPreviousData,
    staleTime: 10 * 1000,
    retry: false,
  })
  const settingsQuery = useQuery({
    queryKey: ['ops', 'settings'],
    queryFn: async () => {
      const response = await getOpsSettings()
      if (!response.success || !response.data) {
        throw new Error(response.message || t('Load failed'))
      }
      return response.data
    },
    staleTime: 30 * 1000,
    retry: false,
  })

  if (snapshotQuery.isLoading) {
    return (
      <Card>
        <CardHeader>
          <Skeleton className='h-6 w-56' />
          <Skeleton className='h-4 w-96 max-w-full' />
        </CardHeader>
        <CardContent className='grid gap-4 sm:grid-cols-2 xl:grid-cols-4'>
          {LOADING_METRIC_IDS.map((metric) => (
            <Skeleton key={metric} className='h-28' />
          ))}
        </CardContent>
      </Card>
    )
  }

  if (!snapshotQuery.data) {
    return (
      <Alert variant='destructive'>
        <AlertTitle>{t('Could not load operations data')}</AlertTitle>
        <AlertDescription>
          {snapshotQuery.error instanceof Error
            ? snapshotQuery.error.message
            : t('Please try again later')}
        </AlertDescription>
        <Button variant='outline' onClick={() => setFilters(emptyFilters)}>
          {t('Clear filters')}
        </Button>
      </Alert>
    )
  }

  const snapshot = snapshotQuery.data
  const channels = snapshot.concurrency.channels
  const userOptions = snapshot.options.users ?? []
  const tokenOptions = snapshot.options.tokens ?? []

  return (
    <Card>
      <CardHeader>
        <div className='flex flex-wrap items-start justify-between gap-4'>
          <div>
            <div className='flex flex-wrap items-center gap-2'>
              <CardTitle>{t('Operations monitoring')}</CardTitle>
              <Badge variant='outline'>Root</Badge>
              <SemanticStatusBadge
                variant={
                  snapshot.concurrency.enforcement_enabled
                    ? 'success'
                    : 'neutral'
                }
                label={
                  snapshot.concurrency.enforcement_enabled
                    ? t('Concurrency enforcement enabled')
                    : t('Concurrency enforcement disabled')
                }
                copyable={false}
                showDot
              />
            </div>
            <CardDescription>
              {t(
                'Requests, latency, errors, concurrency, resources, jobs, and asynchronous tasks'
              )}
            </CardDescription>
          </div>
          <div className='flex gap-2'>
            {(['5m', '1h', '24h'] as const).map((value) => (
              <Button
                key={value}
                size='sm'
                variant={windowKey === value ? 'default' : 'outline'}
                onClick={() => setWindowKey(value)}
              >
                {value}
              </Button>
            ))}
            <Button
              size='sm'
              variant='outline'
              disabled={snapshotQuery.isFetching}
              onClick={() => snapshotQuery.refetch()}
            >
              {snapshotQuery.isFetching ? t('Refreshing...') : t('Refresh')}
            </Button>
          </div>
        </div>
      </CardHeader>
      <CardContent
        className='flex min-w-0 flex-col gap-4'
        aria-busy={snapshotQuery.isFetching}
      >
        {snapshotQuery.isError && (
          <Alert variant='destructive'>
            <AlertDescription>{snapshotQuery.error.message}</AlertDescription>
          </Alert>
        )}
        {snapshotQuery.isPlaceholderData && (
          <Alert role='status'>
            <AlertDescription>
              {t('Loading results for the selected filters...')}
            </AlertDescription>
          </Alert>
        )}
        {snapshot.raw_details_complete === false && (
          <Alert>
            <AlertDescription>
              {t(
                'Detailed performance and task results are limited to retained raw telemetry.'
              )}
            </AlertDescription>
          </Alert>
        )}
        <div className='grid min-w-0 grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4'>
          <Combobox
            aria-label={t('User')}
            value={String(filters.userId)}
            onValueChange={(value) =>
              setFilters({
                ...filters,
                userId: Number(value),
                tokenId: 0,
              })
            }
            options={[
              { value: '0', label: t('All users') },
              ...userOptions.map((user) => ({
                value: String(user.id),
                label: `${user.name} (#${user.id})`,
              })),
              ...(filters.userId > 0 &&
              !userOptions.some((user) => user.id === filters.userId)
                ? [
                    {
                      value: String(filters.userId),
                      label: `#${filters.userId}`,
                    },
                  ]
                : []),
            ]}
          />
          <Combobox
            aria-label={t('API Key')}
            value={String(filters.tokenId)}
            onValueChange={(value) =>
              setFilters({ ...filters, tokenId: Number(value) })
            }
            options={[
              { value: '0', label: t('All API Keys') },
              ...tokenOptions.map((token) => ({
                value: String(token.id),
                label: `${token.name} (#${token.id})`,
              })),
              ...(filters.tokenId > 0 &&
              !tokenOptions.some((token) => token.id === filters.tokenId)
                ? [
                    {
                      value: String(filters.tokenId),
                      label: `#${filters.tokenId}`,
                    },
                  ]
                : []),
            ]}
          />
          <Combobox
            aria-label={t('Model')}
            value={filters.model}
            onValueChange={(value) =>
              setFilters({ ...filters, model: value ?? '' })
            }
            options={[
              { value: '', label: t('All models') },
              ...[
                ...new Set(
                  [...snapshot.options.models, filters.model].filter(Boolean)
                ),
              ].map((model) => ({ value: model, label: model })),
            ]}
          />
          <Combobox
            value={filters.group}
            aria-label={t('Group')}
            onValueChange={(value) =>
              setFilters({ ...filters, group: value ?? '' })
            }
            options={[
              { value: '', label: t('All groups') },
              ...[
                ...new Set(
                  [...snapshot.options.groups, filters.group].filter(Boolean)
                ),
              ].map((group) => ({ value: group, label: group })),
            ]}
          />
          <Combobox
            value={String(filters.channelId)}
            aria-label={t('Channel')}
            onValueChange={(value) =>
              setFilters({ ...filters, channelId: Number(value) })
            }
            options={[
              { value: '0', label: t('All channels') },
              ...channels.map((channel) => ({
                value: String(channel.channel_id),
                label: `${channel.channel_name} (#${channel.channel_id})`,
              })),
              ...(filters.channelId > 0 &&
              !channels.some(
                (channel) => channel.channel_id === filters.channelId
              )
                ? [
                    {
                      value: String(filters.channelId),
                      label: `#${filters.channelId}`,
                    },
                  ]
                : []),
            ]}
          />
          <Combobox
            value={filters.endpoint}
            aria-label={t('Endpoint')}
            onValueChange={(value) =>
              setFilters({ ...filters, endpoint: value ?? '' })
            }
            options={[
              { value: '', label: t('All endpoints') },
              ...[
                ...new Set(
                  [...snapshot.options.endpoint_types, filters.endpoint].filter(
                    Boolean
                  )
                ),
              ].map((endpoint) => ({ value: endpoint, label: endpoint })),
            ]}
          />
          <Combobox
            value={filters.node}
            aria-label={t('Node')}
            onValueChange={(value) =>
              setFilters({ ...filters, node: value ?? '' })
            }
            options={[
              { value: '', label: t('All nodes') },
              ...[
                ...new Set(
                  [...snapshot.options.nodes, filters.node].filter(Boolean)
                ),
              ].map((node) => ({ value: node, label: node })),
            ]}
          />
          {Object.values(filters).some(Boolean) && (
            <Button
              size='sm'
              variant='ghost'
              onClick={() => setFilters(emptyFilters)}
            >
              {t('Clear filters')}
            </Button>
          )}
        </div>

        {!snapshotQuery.isPlaceholderData && !snapshotQuery.isError && (
          <Tabs defaultValue='overview' className='min-w-0 gap-4'>
            <TabsList className='max-w-full overflow-x-auto'>
              <TabsTrigger value='overview' icon={<LayoutDashboard />}>
                {t('Overview')}
              </TabsTrigger>
              <TabsTrigger value='infrastructure' icon={<Server />}>
                {t('Infrastructure')}
              </TabsTrigger>
              <TabsTrigger value='concurrency' icon={<Gauge />}>
                {t('Concurrency')}
              </TabsTrigger>
              <TabsTrigger value='tasks' icon={<ListTodo />}>
                {t('Tasks and jobs')}
              </TabsTrigger>
              <TabsTrigger value='errors' icon={<CircleAlert />}>
                {t('Errors')}
              </TabsTrigger>
              <TabsTrigger value='settings' icon={<Settings2 />}>
                {t('Settings')}
              </TabsTrigger>
            </TabsList>
            <TabsContent value='overview'>
              <OverviewPanel
                snapshot={snapshot}
                settings={settingsQuery.data}
              />
            </TabsContent>
            <TabsContent value='infrastructure'>
              <Badge variant='outline'>
                {t('Node resources · not filtered by user or API Key')}
              </Badge>
              <InfrastructurePanel snapshot={snapshot} />
            </TabsContent>
            <TabsContent value='concurrency'>
              <Badge variant='outline'>{t('Live global concurrency')}</Badge>
              <ConcurrencyPanel snapshot={snapshot} />
            </TabsContent>
            <TabsContent value='tasks'>
              <Badge variant='outline'>
                {t('Filtered submission cohort · global background jobs')}
              </Badge>
              <TaskPanel snapshot={snapshot} settings={settingsQuery.data} />
            </TabsContent>
            <TabsContent value='errors'>
              <ErrorsPanel snapshot={snapshot} settings={settingsQuery.data} />
            </TabsContent>
            <TabsContent value='settings'>
              <SettingsPanel channels={channels} />
            </TabsContent>
          </Tabs>
        )}
      </CardContent>
    </Card>
  )
}
