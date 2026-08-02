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
      return response.data
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
              <Badge
                variant={
                  snapshot.concurrency.enforcement_enabled
                    ? 'default'
                    : 'outline'
                }
              >
                {snapshot.concurrency.enforcement_enabled
                  ? t('Concurrency enforcement enabled')
                  : t('Concurrency enforcement disabled')}
              </Badge>
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
            <TaskPanel snapshot={snapshot} />
          </TabsContent>
          <TabsContent value='errors'>
            <ErrorsPanel snapshot={snapshot} />
          </TabsContent>
          <TabsContent value='settings'>
            <SettingsPanel channels={channels} />
          </TabsContent>
        </Tabs>
      </CardContent>
    </Card>
  )
}
