import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Archive, Download, RefreshCw, Upload } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { invalidateModelPricing } from '@/features/model-pricing/api'
import { handleServerError } from '@/lib/handle-server-error'

import {
  applyImport,
  bridgeAction,
  getBridgeStatus,
  previewImport,
} from './api'

export function OpsBridgeSection() {
  const { t } = useTranslation()
  const stateLabel = (state: string) => {
    const labels: Record<string, string> = {
      reserving: t('Reserving entitlement'),
      reserved: t('Entitlement reserved'),
      blocked: t('Request blocked'),
      usage_queued: t('Usage queued'),
      cancel_queued: t('Cancellation queued'),
      needs_usage_evidence: t('Usage evidence missing'),
      lease_lost: t('Reservation lease lost'),
      pending: t('Delivery pending'),
      delivered: t('Delivery completed'),
      needs_repair: t('Delivery requires repair'),
    }
    return labels[state] ?? state
  }
  const client = useQueryClient()
  const [selected, setSelected] = useState<string[]>([])
  const [overwrite, setOverwrite] = useState<string[]>([])
  const [confirm, setConfirm] = useState(false)
  const [applied, setApplied] = useState(false)
  const status = useQuery({
    queryKey: ['ops-bridge'],
    queryFn: getBridgeStatus,
  })
  const preview = useMutation({
    mutationFn: previewImport,
    onSuccess: (plan) => {
      setSelected(
        Object.keys(plan.after).filter(
          (name) => !plan.protected_models.includes(name)
        )
      )
      setOverwrite([])
      setApplied(false)
    },
    onError: (error) => handleServerError(error),
  })
  const apply = useMutation({
    mutationFn: async () => {
      if (!preview.data) return
      await applyImport(
        preview.data,
        selected,
        overwrite.filter((name) => selected.includes(name))
      )
    },
    onSuccess: async () => {
      setConfirm(false)
      setApplied(true)
      await invalidateModelPricing(client)
      await client.invalidateQueries({ queryKey: ['ops-bridge'] })
    },
    onError: (error) => handleServerError(error),
  })
  const action = useMutation({
    mutationFn: bridgeAction,
    onSuccess: () => client.invalidateQueries({ queryKey: ['ops-bridge'] }),
    onError: (error) => handleServerError(error),
  })
  if (status.isPending) return <LoadingState />
  if (status.isError) {
    return <ErrorState onRetry={() => void status.refetch()} />
  }
  if (!status.data.enabled) {
    return <p role='status'>{t('Ops connection disabled')}</p>
  }

  const plan = preview.data
  const names = plan
    ? [
        ...new Set([
          ...Object.keys(plan.after),
          ...Object.keys(plan.unsupported_models),
        ]),
      ].sort()
    : []
  const busy = preview.isPending || apply.isPending || action.isPending
  const canApply =
    !!plan &&
    selected.length > 0 &&
    selected.every(
      (name) =>
        !plan.protected_models.includes(name) || overwrite.includes(name)
    )
  const jobs = (status.data.deliveries ?? []).filter(
    (job) => job.status !== 'delivered'
  )
  return (
    <section
      className='flex min-w-0 flex-col gap-4'
      aria-label={t('TokenOps connection')}
    >
      <div className='flex flex-wrap items-center gap-2'>
        <Button
          disabled={busy || !!status.data.persistence_error}
          onClick={() => preview.mutate()}
        >
          <Download />
          {t('Preview official prices')}
        </Button>
        <Button
          variant='outline'
          disabled={busy}
          onClick={() => action.mutate('notifications/refresh')}
        >
          <RefreshCw />
          {t('Refresh entitlements')}
        </Button>
        <Button
          variant='outline'
          disabled={busy}
          onClick={() => action.mutate('objects/sync')}
        >
          <Upload />
          {t('Sync objects')}
        </Button>
        <Button
          variant='outline'
          disabled={busy}
          onClick={() => action.mutate('pricing/sync')}
        >
          <Upload />
          {t('Sync local prices to Ops')}
        </Button>
        <Button
          variant='outline'
          disabled={busy}
          onClick={() => action.mutate('archive')}
        >
          <Archive />
          {t('Archive delivered records')}
        </Button>
      </div>
      {status.data.persistence_error && (
        <p role='alert'>{status.data.persistence_error}</p>
      )}
      {plan && (
        <>
          <p className='text-sm break-all'>
            {t('Catalog version')}: {plan.catalog_version}
          </p>
          <StaticDataTable
            data={names}
            getRowKey={(name) => name}
            columns={[
              {
                id: 'select',
                header: t('Select'),
                cell: (name) => (
                  <Checkbox
                    aria-label={`${t('Select')} ${name}`}
                    checked={selected.includes(name)}
                    disabled={busy || applied || !plan.after[name]}
                    onCheckedChange={(checked) =>
                      setSelected((prior) =>
                        checked
                          ? [...prior, name]
                          : prior.filter((item) => item !== name)
                      )
                    }
                  />
                ),
              },
              {
                id: 'model',
                header: t('Model'),
                cell: (name) => <span className='break-all'>{name}</span>,
              },
              {
                id: 'before',
                header: t('Current price'),
                cell: (name) => (
                  <pre className='max-w-80 text-xs break-all whitespace-pre-wrap'>
                    {JSON.stringify(plan.before[name]?.configured ?? {})}
                  </pre>
                ),
              },
              {
                id: 'after',
                header: t('Official price'),
                cell: (name) => (
                  <pre className='max-w-80 text-xs break-all whitespace-pre-wrap'>
                    {plan.after[name]
                      ? String(plan.after[name]['billing_setting.billing_expr'])
                      : plan.unsupported_models[name]}
                  </pre>
                ),
              },
              {
                id: 'overwrite',
                header: t('Replace local price'),
                cell: (name) =>
                  plan.protected_models.includes(name) ? (
                    <Checkbox
                      aria-label={`${t('Replace local price')} ${name}`}
                      checked={overwrite.includes(name)}
                      disabled={busy || applied || !plan.after[name]}
                      onCheckedChange={(checked) =>
                        setOverwrite((prior) =>
                          checked
                            ? [...prior, name]
                            : prior.filter((item) => item !== name)
                        )
                      }
                    />
                  ) : null,
              },
            ]}
          />
          <Button
            className='self-start'
            disabled={busy || applied || !canApply}
            onClick={() => setConfirm(true)}
          >
            <Download />
            {t('Import selected prices')}
          </Button>
          {applied && (
            <p role='status'>{t('Prices imported; confirmation queued')}</p>
          )}
        </>
      )}
      <div className='flex flex-wrap gap-2'>
        {Object.entries(status.data.request_states ?? {}).map(
          ([name, count]) => (
            <Badge key={name} variant='secondary'>
              {stateLabel(name)}: {count}
            </Badge>
          )
        )}
      </div>
      <StaticDataTable
        data={status.data.requests ?? []}
        getRowKey={(request) => request.id}
        columns={[
          {
            id: 'request',
            header: t('Request ID'),
            cell: (request) => <span className='break-all'>{request.id}</span>,
          },
          {
            id: 'state',
            header: t('Status'),
            cell: (request) => stateLabel(request.status),
          },
        ]}
      />
      <StaticDataTable
        data={jobs}
        getRowKey={(job) => job.id}
        columns={[
          { id: 'path', header: t('Delivery'), cell: (job) => job.path },
          {
            id: 'status',
            header: t('Status'),
            cell: (job) => `${stateLabel(job.status)} (${job.attempts})`,
          },
          {
            id: 'error',
            header: t('Error'),
            cell: (job) => job.last_error ?? '',
          },
          {
            id: 'retry',
            header: t('Actions'),
            cell: (job) => (
              <Button
                variant='outline'
                disabled={busy}
                onClick={() =>
                  action.mutate(
                    `deliveries/${encodeURIComponent(job.id)}/retry`
                  )
                }
              >
                <RefreshCw />
                {t('Retry')}
              </Button>
            ),
          },
        ]}
      />
      <ConfirmDialog
        open={confirm}
        onOpenChange={setConfirm}
        title={t('Import selected prices')}
        desc={`${t('Replace selected local prices with this catalog version?')} (${selected.length})`}
        confirmText={t('Import')}
        isLoading={apply.isPending}
        disabled={!canApply}
        handleConfirm={() => apply.mutate()}
      />
    </section>
  )
}
