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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
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
import { toast } from 'sonner'

import { StatusBadge as SemanticStatusBadge } from '@/components/status-badge'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import {
  deleteOpsConcurrencyLimit,
  getOpsSettings,
  listOpsConcurrencyLimits,
  updateOpsSettings,
  upsertOpsConcurrencyLimit,
} from '../api'
import type {
  ConcurrencyChannel,
  OpsConcurrencyLimit,
  OpsSettings,
} from '../types'

function NumberField({
  id,
  label,
  value,
  onChange,
  min,
  max,
  step,
}: {
  id: string
  label: string
  value: number
  onChange: (value: number) => void
  min: number
  max: number
  step?: number
}) {
  return (
    <div className='flex flex-col gap-2'>
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        type='number'
        value={value}
        min={min}
        max={max}
        step={step}
        onChange={(event) => onChange(Number(event.target.value))}
      />
    </div>
  )
}

function SettingsForm({ settings }: { settings: OpsSettings }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [form, setForm] = useState(settings)
  const mutation = useMutation({
    mutationFn: async () => {
      const response = await updateOpsSettings(form)
      if (!response.success || !response.data) {
        throw new Error(response.message || t('Save failed'))
      }
      return response.data
    },
    onSuccess: async (data) => {
      setForm(data)
      toast.success(t('Operations settings saved'))
      await queryClient.invalidateQueries({ queryKey: ['ops'] })
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : t('Save failed'))
    },
  })

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('Monitoring and threshold settings')}</CardTitle>
        <CardDescription>
          {t(
            'Observation is enabled by default; concurrency enforcement is disabled by default'
          )}
        </CardDescription>
      </CardHeader>
      <CardContent className='flex flex-col gap-6'>
        <div className='grid gap-4 sm:grid-cols-2'>
          <div className='flex items-center justify-between gap-4 rounded-lg border p-4'>
            <div>
              <Label htmlFor='ops-enabled'>{t('Operations monitoring')}</Label>
              <p className='text-muted-foreground text-xs'>
                {t('Collect normalized request and system metrics')}
              </p>
            </div>
            <Switch
              id='ops-enabled'
              checked={form.enabled}
              onCheckedChange={(enabled) => setForm({ ...form, enabled })}
            />
          </div>
          <div className='flex items-center justify-between gap-4 rounded-lg border p-4'>
            <div>
              <Label htmlFor='ops-enforcement'>
                {t('Concurrency enforcement')}
              </Label>
              <p className='text-muted-foreground text-xs'>
                {t(
                  'When disabled, limits are observed but never reject requests'
                )}
              </p>
            </div>
            <Switch
              id='ops-enforcement'
              checked={form.concurrency_enforcement_enabled}
              onCheckedChange={(concurrency_enforcement_enabled) =>
                setForm({ ...form, concurrency_enforcement_enabled })
              }
            />
          </div>
          <div className='flex items-center justify-between gap-4 rounded-lg border p-4'>
            <div>
              <Label htmlFor='ops-fail-open'>{t('Fail open')}</Label>
              <p className='text-muted-foreground text-xs'>
                {t('Continue requests if Redis coordination is unavailable')}
              </p>
            </div>
            <Switch
              id='ops-fail-open'
              checked={form.concurrency_fail_open}
              onCheckedChange={(concurrency_fail_open) =>
                setForm({ ...form, concurrency_fail_open })
              }
            />
          </div>
        </div>

        <div className='grid gap-4 sm:grid-cols-2 xl:grid-cols-4'>
          <NumberField
            id='raw-retention'
            label={t('Raw retention days')}
            value={form.raw_retention_days}
            min={1}
            max={365}
            onChange={(raw_retention_days) =>
              setForm({ ...form, raw_retention_days })
            }
          />
          <NumberField
            id='aggregate-retention'
            label={t('Aggregate retention days')}
            value={form.aggregate_retention_days}
            min={form.raw_retention_days}
            max={3650}
            onChange={(aggregate_retention_days) =>
              setForm({ ...form, aggregate_retention_days })
            }
          />
          <NumberField
            id='system-retention'
            label={t('System retention days')}
            value={form.system_retention_days}
            min={1}
            max={3650}
            onChange={(system_retention_days) =>
              setForm({ ...form, system_retention_days })
            }
          />
          <NumberField
            id='collection-interval'
            label={t('Collection interval (seconds)')}
            value={form.system_collection_interval_secs}
            min={15}
            max={3600}
            onChange={(system_collection_interval_secs) =>
              setForm({ ...form, system_collection_interval_secs })
            }
          />
          <NumberField
            id='sla-threshold'
            label={t('Effective request success rate threshold')}
            value={form.sla_threshold * 100}
            min={0.01}
            max={100}
            step={0.01}
            onChange={(value) =>
              setForm({ ...form, sla_threshold: value / 100 })
            }
          />
          <NumberField
            id='request-latency-threshold'
            label={t('Request P99 latency threshold (ms)')}
            value={form.request_p99_threshold_ms}
            min={1}
            max={600000}
            onChange={(request_p99_threshold_ms) =>
              setForm({ ...form, request_p99_threshold_ms })
            }
          />
          <NumberField
            id='ttft-threshold'
            label={t('TTFT P99 threshold (ms)')}
            value={form.ttft_p99_threshold_ms}
            min={1}
            max={600000}
            onChange={(ttft_p99_threshold_ms) =>
              setForm({ ...form, ttft_p99_threshold_ms })
            }
          />
          <NumberField
            id='request-error-threshold'
            label={t('Request error threshold')}
            value={form.request_error_rate_threshold * 100}
            min={0.01}
            max={100}
            step={0.01}
            onChange={(value) =>
              setForm({ ...form, request_error_rate_threshold: value / 100 })
            }
          />
          <NumberField
            id='upstream-error-threshold'
            label={t('Upstream error threshold')}
            value={form.upstream_error_rate_threshold * 100}
            min={0.01}
            max={100}
            step={0.01}
            onChange={(value) =>
              setForm({ ...form, upstream_error_rate_threshold: value / 100 })
            }
          />
          <NumberField
            id='default-lease'
            label={t('Default lease seconds')}
            value={form.default_lease_seconds}
            min={30}
            max={86400}
            onChange={(default_lease_seconds) =>
              setForm({ ...form, default_lease_seconds })
            }
          />
          <NumberField
            id='rate-limit-cooldown'
            label={t('Rate-limit cooldown seconds')}
            value={form.rate_limit_cooldown_seconds}
            min={1}
            max={86400}
            onChange={(rate_limit_cooldown_seconds) =>
              setForm({ ...form, rate_limit_cooldown_seconds })
            }
          />
          <NumberField
            id='overload-cooldown'
            label={t('Overload cooldown seconds')}
            value={form.overload_cooldown_seconds}
            min={1}
            max={86400}
            onChange={(overload_cooldown_seconds) =>
              setForm({ ...form, overload_cooldown_seconds })
            }
          />
          <NumberField
            id='temporary-cooldown'
            label={t('Temporary cooldown seconds')}
            value={form.temporary_unschedulable_seconds}
            min={1}
            max={3600}
            onChange={(temporary_unschedulable_seconds) =>
              setForm({ ...form, temporary_unschedulable_seconds })
            }
          />
        </div>
      </CardContent>
      <CardFooter>
        <Button disabled={mutation.isPending} onClick={() => mutation.mutate()}>
          {mutation.isPending ? t('Saving...') : t('Save settings')}
        </Button>
      </CardFooter>
    </Card>
  )
}

function LimitEditor({ channels }: { channels: ConcurrencyChannel[] }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [channelId, setChannelId] = useState(channels[0]?.channel_id ?? 0)
  const selectedChannel =
    channels.find((channel) => channel.channel_id === channelId) ?? channels[0]
  const [keyIndex, setKeyIndex] = useState(-1)
  const [enabled, setEnabled] = useState(true)
  const [maxConcurrency, setMaxConcurrency] = useState(1)
  const [queueSize, setQueueSize] = useState(0)
  const [queueTimeoutMs, setQueueTimeoutMs] = useState(30000)
  const [leaseSeconds, setLeaseSeconds] = useState(300)
  const [deleteTarget, setDeleteTarget] = useState<OpsConcurrencyLimit | null>(
    null
  )

  const limitsQuery = useQuery({
    queryKey: ['ops', 'concurrency-limits'],
    queryFn: async () => {
      const response = await listOpsConcurrencyLimits()
      if (!response.success || !response.data) {
        throw new Error(response.message || t('Load failed'))
      }
      return response.data
    },
  })

  const saveMutation = useMutation({
    mutationFn: async () => {
      const response = await upsertOpsConcurrencyLimit({
        channel_id: channelId,
        key_index: keyIndex,
        enabled,
        max_concurrency: maxConcurrency,
        queue_size: queueSize,
        queue_timeout_ms: queueTimeoutMs,
        lease_seconds: leaseSeconds,
      })
      if (!response.success) {
        throw new Error(response.message || t('Save failed'))
      }
    },
    onSuccess: async () => {
      toast.success(t('Concurrency limit saved'))
      await queryClient.invalidateQueries({ queryKey: ['ops'] })
    },
    onError: (error) =>
      toast.error(error instanceof Error ? error.message : t('Save failed')),
  })

  const deleteMutation = useMutation({
    mutationFn: async (target: OpsConcurrencyLimit) => {
      const response = await deleteOpsConcurrencyLimit(
        target.channel_id,
        target.key_index
      )
      if (!response.success) {
        throw new Error(response.message || t('Delete failed'))
      }
    },
    onSuccess: async () => {
      setDeleteTarget(null)
      toast.success(t('Concurrency limit deleted'))
      await queryClient.invalidateQueries({ queryKey: ['ops'] })
    },
    onError: (error) =>
      toast.error(error instanceof Error ? error.message : t('Delete failed')),
  })

  return (
    <>
      <Card>
        <CardHeader>
          <CardTitle>{t('Channel concurrency limits')}</CardTitle>
          <CardDescription>
            {t('Limits take effect only when global enforcement is enabled')}
          </CardDescription>
        </CardHeader>
        <CardContent className='flex flex-col gap-6'>
          <div className='grid gap-4 sm:grid-cols-2 xl:grid-cols-4'>
            <div className='flex flex-col gap-2'>
              <Label htmlFor='limit-channel'>{t('Channel')}</Label>
              <NativeSelect
                id='limit-channel'
                value={channelId}
                className='w-full'
                onChange={(event) => {
                  setChannelId(Number(event.target.value))
                  setKeyIndex(-1)
                }}
              >
                {channels.map((channel) => (
                  <NativeSelectOption
                    key={channel.channel_id}
                    value={channel.channel_id}
                  >
                    {channel.channel_name} (ID {channel.channel_id})
                  </NativeSelectOption>
                ))}
              </NativeSelect>
            </div>
            <div className='flex flex-col gap-2'>
              <Label htmlFor='limit-key'>{t('Channel key')}</Label>
              <NativeSelect
                id='limit-key'
                value={keyIndex}
                className='w-full'
                onChange={(event) => setKeyIndex(Number(event.target.value))}
              >
                <NativeSelectOption value={-1}>
                  {t('All keys')}
                </NativeSelectOption>
                {Array.from(
                  { length: selectedChannel?.multi_key_size ?? 0 },
                  (_, index) => (
                    <NativeSelectOption key={index} value={index}>
                      {t('Key')} {index}
                    </NativeSelectOption>
                  )
                )}
              </NativeSelect>
            </div>
            <NumberField
              id='max-concurrency'
              label={t('Max concurrency')}
              value={maxConcurrency}
              min={1}
              max={100000}
              onChange={setMaxConcurrency}
            />
            <NumberField
              id='queue-size'
              label={t('Queue size')}
              value={queueSize}
              min={0}
              max={100000}
              onChange={setQueueSize}
            />
            <NumberField
              id='queue-timeout'
              label={t('Queue timeout (ms)')}
              value={queueTimeoutMs}
              min={0}
              max={3600000}
              onChange={setQueueTimeoutMs}
            />
            <NumberField
              id='lease-seconds'
              label={t('Lease seconds')}
              value={leaseSeconds}
              min={0}
              max={86400}
              onChange={setLeaseSeconds}
            />
            <div className='flex items-end'>
              <div className='flex h-9 w-full items-center justify-between rounded-lg border px-3'>
                <Label htmlFor='limit-enabled'>{t('Limit enabled')}</Label>
                <Switch
                  id='limit-enabled'
                  checked={enabled}
                  onCheckedChange={setEnabled}
                />
              </div>
            </div>
            <div className='flex items-end'>
              <Button
                className='w-full'
                disabled={!channelId || saveMutation.isPending}
                onClick={() => saveMutation.mutate()}
              >
                {saveMutation.isPending ? t('Saving...') : t('Save limit')}
              </Button>
            </div>
          </div>

          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('Channel')}</TableHead>
                <TableHead>{t('Key')}</TableHead>
                <TableHead>{t('Enabled')}</TableHead>
                <TableHead>{t('Capacity')}</TableHead>
                <TableHead>{t('Queue')}</TableHead>
                <TableHead>{t('Timeout')}</TableHead>
                <TableHead>{t('Actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(limitsQuery.data ?? []).map((limit) => (
                <TableRow key={`${limit.channel_id}-${limit.key_index}`}>
                  <TableCell>
                    {channels.find(
                      (channel) => channel.channel_id === limit.channel_id
                    )?.channel_name ?? `ID ${limit.channel_id}`}
                  </TableCell>
                  <TableCell>
                    {limit.key_index < 0 ? t('All keys') : limit.key_index}
                  </TableCell>
                  <TableCell>
                    <SemanticStatusBadge
                      variant={limit.enabled ? 'success' : 'neutral'}
                      label={limit.enabled ? t('Enabled') : t('Disabled')}
                      copyable={false}
                      showDot
                    />
                  </TableCell>
                  <TableCell>{limit.max_concurrency}</TableCell>
                  <TableCell>{limit.queue_size}</TableCell>
                  <TableCell>{limit.queue_timeout_ms} ms</TableCell>
                  <TableCell>
                    <Button
                      variant='destructive'
                      size='sm'
                      onClick={() => setDeleteTarget(limit)}
                    >
                      {t('Delete')}
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <AlertDialog
        open={deleteTarget !== null}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t('Delete concurrency limit?')}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(
                'The channel will return to observation-only behavior unless another matching limit exists.'
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteMutation.isPending}>
              {t('Cancel')}
            </AlertDialogCancel>
            <AlertDialogAction
              variant='destructive'
              disabled={deleteMutation.isPending}
              onClick={() =>
                deleteTarget && deleteMutation.mutate(deleteTarget)
              }
            >
              {deleteMutation.isPending ? t('Deleting...') : t('Delete')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

export function SettingsPanel({
  channels,
}: {
  channels: ConcurrencyChannel[]
}) {
  const { t } = useTranslation()
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
  })

  if (settingsQuery.isLoading) {
    return <Skeleton className='h-80 w-full' />
  }
  if (!settingsQuery.data) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>{t('Could not load operations settings')}</CardTitle>
        </CardHeader>
      </Card>
    )
  }

  return (
    <div className='flex flex-col gap-4'>
      <SettingsForm
        key={JSON.stringify(settingsQuery.data)}
        settings={settingsQuery.data}
      />
      <LimitEditor channels={channels} />
    </div>
  )
}
