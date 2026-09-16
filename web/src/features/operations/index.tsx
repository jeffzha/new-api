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
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
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
  model: string
  group: string
  endpoint: string
  node: string
  channelId: number
}

const emptyFilters: FilterState = {
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
      })
      if (!response.success || !response.data) {
        throw new Error(response.message || t('Could not load operations data'))
      }
      return normalizeOpsSnapshot(response.data)
    },
    refetchInterval: 30 * 1000,
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
      </Alert>
    )
  }

  const snapshot = snapshotQuery.data
  const channels = snapshot.concurrency.channels

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
      <CardContent className='flex flex-col gap-4'>
        <div className='flex flex-wrap gap-2'>
          <NativeSelect
            size='sm'
            value={filters.model}
            onChange={(event) =>
              setFilters({ ...filters, model: event.target.value })
            }
          >
            <NativeSelectOption value=''>{t('All models')}</NativeSelectOption>
            {snapshot.options.models.map((model) => (
              <NativeSelectOption key={model} value={model}>
                {model}
              </NativeSelectOption>
            ))}
          </NativeSelect>
          <NativeSelect
            size='sm'
            value={filters.group}
            onChange={(event) =>
              setFilters({ ...filters, group: event.target.value })
            }
          >
            <NativeSelectOption value=''>{t('All groups')}</NativeSelectOption>
            {snapshot.options.groups.map((group) => (
              <NativeSelectOption key={group} value={group}>
                {group}
              </NativeSelectOption>
            ))}
          </NativeSelect>
          <NativeSelect
            size='sm'
            value={filters.channelId}
            onChange={(event) =>
              setFilters({ ...filters, channelId: Number(event.target.value) })
            }
          >
            <NativeSelectOption value={0}>
              {t('All channels')}
            </NativeSelectOption>
            {channels.map((channel) => (
              <NativeSelectOption
                key={channel.channel_id}
                value={channel.channel_id}
              >
                {channel.channel_name}
              </NativeSelectOption>
            ))}
          </NativeSelect>
          <NativeSelect
            size='sm'
            value={filters.endpoint}
            onChange={(event) =>
              setFilters({ ...filters, endpoint: event.target.value })
            }
          >
            <NativeSelectOption value=''>
              {t('All endpoints')}
            </NativeSelectOption>
            {snapshot.options.endpoint_types.map((endpoint) => (
              <NativeSelectOption key={endpoint} value={endpoint}>
                {endpoint}
              </NativeSelectOption>
            ))}
          </NativeSelect>
          <NativeSelect
            size='sm'
            value={filters.node}
            onChange={(event) =>
              setFilters({ ...filters, node: event.target.value })
            }
          >
            <NativeSelectOption value=''>{t('All nodes')}</NativeSelectOption>
            {snapshot.options.nodes.map((node) => (
              <NativeSelectOption key={node} value={node}>
                {node}
              </NativeSelectOption>
            ))}
          </NativeSelect>
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

        <Tabs defaultValue='overview' className='min-w-0 gap-4'>
          <TabsList className='max-w-full overflow-x-auto'>
            <TabsTrigger value='overview'>{t('Overview')}</TabsTrigger>
            <TabsTrigger value='infrastructure'>
              {t('Infrastructure')}
            </TabsTrigger>
            <TabsTrigger value='concurrency'>{t('Concurrency')}</TabsTrigger>
            <TabsTrigger value='tasks'>{t('Tasks and jobs')}</TabsTrigger>
            <TabsTrigger value='errors'>{t('Errors')}</TabsTrigger>
            <TabsTrigger value='settings'>{t('Settings')}</TabsTrigger>
          </TabsList>
          <TabsContent value='overview'>
            <OverviewPanel snapshot={snapshot} settings={settingsQuery.data} />
          </TabsContent>
          <TabsContent value='infrastructure'>
            <InfrastructurePanel snapshot={snapshot} />
          </TabsContent>
          <TabsContent value='concurrency'>
            <ConcurrencyPanel snapshot={snapshot} />
          </TabsContent>
          <TabsContent value='tasks'>
            <TaskPanel snapshot={snapshot} settings={settingsQuery.data} />
          </TabsContent>
          <TabsContent value='errors'>
            <ErrorsPanel snapshot={snapshot} settings={settingsQuery.data} />
          </TabsContent>
          <TabsContent value='settings'>
            <SettingsPanel channels={channels} />
          </TabsContent>
        </Tabs>
      </CardContent>
    </Card>
  )
}
