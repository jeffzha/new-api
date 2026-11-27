import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Download } from 'lucide-react'
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import {
  Field,
  FieldError,
  FieldGroup,
  FieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { NativeSelect } from '@/components/ui/native-select'
import { TableScrollArea } from '@/components/ui/table-scroll-area'

import {
  downloadEvidence,
  loadHealth,
  money,
  saveHealth,
  when,
  type Access,
  type OpsCosts,
  type Run,
} from './api'
import { HealthModelName } from './model-name'
import { Pages, Status } from './status'

export function EvidenceButton(props: { id: number; user?: boolean }) {
  const { t } = useTranslation()
  const download = useMutation({
    mutationFn: () => downloadEvidence(props.id, props.user),
  })
  return (
    <Button
      size='icon'
      variant='outline'
      title={t('Download evidence MD')}
      aria-label={t('Download evidence MD')}
      disabled={download.isPending}
      onClick={() => download.mutate()}
    >
      <Download aria-hidden='true' />
    </Button>
  )
}

export function HealthRecords(props: { access: Access }) {
  const { t } = useTranslation()
  const [source, setSource] = useState('')
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(20)
  const [reconcile, setReconcile] = useState<Run>()
  const [cost, setCost] = useState('')
  const costId = useId()
  const [whole, fraction = ''] = cost.split('.')
  const actualMicro =
    Number(whole) * 1_000_000 + Number(fraction.padEnd(6, '0'))
  const validCost =
    /^\d+(?:\.\d{1,6})?$/.test(cost) &&
    Number.isSafeInteger(actualMicro) &&
    actualMicro <= 1e12
  const client = useQueryClient()
  const history = useQuery({
    queryKey: ['model-health', 'runs', source, page, pageSize],
    queryFn: ({ signal }) =>
      loadHealth<Run[]>('/runs', { source, page, limit: pageSize }, signal),
    refetchInterval: 10000,
  })
  const costs = useQuery({
    queryKey: ['model-health', 'run-costs', source, page, pageSize],
    queryFn: ({ signal }) =>
      loadHealth<OpsCosts>(
        '/run-costs',
        { source, page, limit: pageSize },
        signal
      ),
    staleTime: 60_000,
    refetchInterval: 60_000,
    retry: false,
    meta: { errorToast: false },
  })
  const totals = costs.data
  let pendingCost = t('OPS costs unavailable')
  if (costs.isPending) {
    pendingCost = t('Loading OPS costs')
  } else if (totals?.state === 'confirmed') {
    pendingCost = t('Awaiting OPS receipt')
  } else if (totals?.state === 'unconfigured') {
    pendingCost = t('OPS costs not connected')
  }
  const settle = useMutation({
    mutationFn: (r: Run) =>
      saveHealth('/reconcile', { id: r.id, actual_micro: actualMicro }, 'post'),
    onSuccess: () => {
      setReconcile(undefined)
      void client.invalidateQueries({ queryKey: ['model-health'] })
    },
  })
  return (
    <div className='flex h-full min-h-0 flex-col gap-3 pt-3'>
      <dl className='bg-card grid shrink-0 grid-cols-2 divide-x rounded-xl border'>
        {[
          [t('OPS confirmed this week (Beijing time)'), totals?.week_micro],
          [t('OPS confirmed all time'), totals?.total_micro],
        ].map(([label, value]) => (
          <div key={label} className='min-w-0 px-3 py-3'>
            <dt className='text-muted-foreground text-xs'>{label}</dt>
            <dd className='mt-1 font-semibold break-words tabular-nums'>
              {typeof value === 'number' ? money(value) : pendingCost}
            </dd>
          </div>
        ))}
      </dl>
      <label className='inline-flex shrink-0 items-center gap-2'>
        {t('Detection source')}
        <NativeSelect
          value={source}
          onChange={(e) => {
            setSource(e.target.value)
            setPage(1)
          }}
        >
          <option value=''>{t('All')}</option>
          <option value='manual'>{t('manual')}</option>
          <option value='scheduled'>{t('scheduled')}</option>
        </NativeSelect>
      </label>
      {history.isPending && <LoadingState />}
      {history.isError && <ErrorState onRetry={() => void history.refetch()} />}
      {history.data && (
        <>
          <div className='min-h-0 flex-1 overflow-hidden'>
            <TableScrollArea className='table-scroll-area-fill h-full max-h-none'>
              <StaticDataTable
                data={history.data.slice(0, pageSize)}
                tableClassName='min-w-[720px] [&_thead]:sticky [&_thead]:top-0 [&_thead]:z-10 [&_thead]:bg-muted'
                className='bg-card contents!'
                tableProps={{ withContainer: false }}
                getRowKey={(r) => r.id}
                emptyContent={t('No Data')}
                columns={[
                  {
                    id: 'model',
                    header: t('Channel and model'),
                    cell: (r) => (
                      <div className='max-w-48 break-words whitespace-normal'>
                        <HealthModelName model={r.model} />
                        <div className='text-muted-foreground text-xs'>
                          #{r.channel_id} · {r.endpoint}
                        </div>
                      </div>
                    ),
                  },
                  {
                    id: 'time',
                    header: t('Time'),
                    cell: (r) => when(r.created_at),
                  },
                  {
                    id: 'source',
                    header: t('Detection source'),
                    cell: (r) => t(r.source),
                  },
                  {
                    id: 'result',
                    header: t('Status'),
                    cell: (r) => (
                      <div className='space-y-1'>
                        <Status state={r.state} />
                        <div className='text-muted-foreground max-w-52 text-xs break-words whitespace-normal'>
                          {t(r.stage)} · {r.status_code || '\u2014'}{' '}
                          {r.reason && t(r.reason)}
                        </div>
                      </div>
                    ),
                  },
                  {
                    id: 'cost',
                    header: t('OPS confirmed cost'),
                    cell: (r) => {
                      const amount = totals?.amounts[r.id]
                      return typeof amount === 'number'
                        ? money(amount)
                        : pendingCost
                    },
                  },
                  {
                    id: 'action',
                    header: t('Actions'),
                    cell: (r) => (
                      <div className='flex items-center gap-2'>
                        {props.access.export && <EvidenceButton id={r.id} />}{' '}
                        {props.access.rules &&
                          r.actual_micro === null &&
                          ['uncertain', 'failed', 'succeeded'].includes(
                            r.state
                          ) && (
                            <Button
                              size='sm'
                              variant='outline'
                              onClick={() => {
                                setCost('')
                                setReconcile(r)
                              }}
                            >
                              {t('Verify detection cost')}
                            </Button>
                          )}
                      </div>
                    ),
                  },
                ]}
              />
            </TableScrollArea>
          </div>
          <div className='flex shrink-0 items-center justify-between gap-3 border-t'>
            <label className='flex items-center gap-2 text-sm'>
              <span className='text-muted-foreground'>
                {t('Rows per page')}
              </span>
              <NativeSelect
                value={pageSize}
                onChange={(event) => {
                  setPageSize(Number(event.target.value))
                  setPage(1)
                }}
              >
                {[10, 20, 30, 40, 50, 100].map((size) => (
                  <option key={size} value={size}>
                    {size}
                  </option>
                ))}
              </NativeSelect>
            </label>
            <Pages
              page={page}
              hasMore={history.data.length > pageSize}
              onPage={setPage}
            />
          </div>
        </>
      )}
      <ConfirmDialog
        open={Boolean(reconcile)}
        onOpenChange={(open) => {
          if (!open && !settle.isPending) setReconcile(undefined)
        }}
        title={t('Verify detection cost')}
        desc={t(
          'Confirm the original request has finished and verify its final charge before releasing the detection budget. This only reconciles the internal detection budget; it does not change wallet balances or OPS fees.'
        )}
        confirmText={t('Reconcile')}
        disabled={!validCost}
        isLoading={settle.isPending}
        handleConfirm={() => {
          if (reconcile && validCost) settle.mutate(reconcile)
        }}
      >
        <FieldGroup>
          <Field data-invalid={cost !== '' && !validCost}>
            <FieldLabel htmlFor={costId}>{t('Verified cost (CNY)')}</FieldLabel>
            <Input
              id={costId}
              type='number'
              min='0'
              max='1000000'
              step='0.000001'
              value={cost}
              aria-invalid={cost !== '' && !validCost}
              aria-describedby={
                cost !== '' && !validCost ? `${costId}-error` : undefined
              }
              onChange={(event) => setCost(event.target.value)}
            />
            {cost !== '' && !validCost && (
              <FieldError id={`${costId}-error`}>
                {t(
                  'Enter an amount from 0 to 1000000 CNY with at most six decimal places'
                )}
              </FieldError>
            )}
          </Field>
        </FieldGroup>
      </ConfirmDialog>
    </div>
  )
}
