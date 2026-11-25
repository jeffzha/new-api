import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Search, Settings2, Zap } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { NativeSelect } from '@/components/ui/native-select'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'

import {
  loadHealth,
  money,
  saveHealth,
  when,
  type Access,
  type Endpoint,
  type Overview,
  type Plan,
  type Row,
  type Selection,
} from './api'
import { HealthCharts } from './health-charts'
import { EvidenceButton } from './records'
import { Pages, Status } from './status'

const filters = {
  q: '',
  channel: '',
  group: '',
  endpoint: '',
  type: '',
  state: '',
  range: '24',
}

export function HealthOverview(props: {
  access: Access
  onRules: (selection: Selection) => void
}) {
  const { t } = useTranslation()
  const [form, setForm] = useState(filters)
  const [params, setParams] = useState(filters)
  const [page, setPage] = useState(1)
  const [selected, setSelected] = useState<{ row: Row; endpoint: Endpoint }>()
  const [probe, setProbe] = useState<{ row: Row; endpoint: string }>()
  const query = useQuery({
    queryKey: ['model-health', 'overview', params, page],
    queryFn: ({ signal }) => {
      const to = Math.floor(Date.now() / 1000) + 1
      return loadHealth<Overview>(
        '/overview',
        {
          ...params,
          page,
          from: to - Number(params.range) * 3600,
          to,
        },
        signal
      )
    },
    refetchInterval: 20000,
  })
  const row =
    query.data?.rows.find((r) => r.id === selected?.row.id) ?? selected?.row
  const detail =
    row?.details.find((e) => e.name === selected?.endpoint.name) ??
    selected?.endpoint
  const options = [
    [
      'type',
      t('Model type'),
      [
        'llm',
        'video',
        'image',
        'embedding',
        'audio',
        'tts',
        'speech2text',
        'rerank',
      ],
    ],
    ['state', t('Health status'), ['healthy', 'abnormal', 'slow', 'waiting']],
    ['range', t('Time range'), ['24', '168', '720']],
  ] as const
  return (
    <div className='space-y-4 pt-3'>
      <form
        className='grid grid-cols-2 gap-2 md:grid-cols-4 xl:flex xl:flex-wrap xl:items-end'
        onSubmit={(event) => {
          event.preventDefault()
          setPage(1)
          setParams(form)
        }}
      >
        {(
          [
            ['q', t('Channel or model')],
            ['channel', t('Channel ID')],
            ['group', t('Group')],
            ['endpoint', t('Endpoint')],
          ] as const
        ).map(([key, label]) => (
          <label key={key} className='min-w-0 space-y-1 text-xs'>
            {label}
            <Input
              value={form[key]}
              onChange={(e) => setForm({ ...form, [key]: e.target.value })}
              type={key === 'channel' ? 'number' : 'text'}
            />
          </label>
        ))}
        {options.map(([key, label, values]) => (
          <label key={key} className='min-w-0 space-y-1 text-xs'>
            {label}
            <NativeSelect
              className='w-full'
              value={form[key]}
              onChange={(e) => setForm({ ...form, [key]: e.target.value })}
            >
              {key !== 'range' && <option value=''>{t('All')}</option>}
              {values.map((v) => (
                <option key={v} value={v}>
                  {key === 'range'
                    ? t('{{hours}} hours', { hours: v })
                    : t(v === 'image' ? 'Image' : v)}
                </option>
              ))}
            </NativeSelect>
          </label>
        ))}
        <Button type='submit'>
          <Search aria-hidden='true' />
          {t('Search')}
        </Button>
      </form>
      {query.isPending && <LoadingState />}
      {query.isError && <ErrorState onRetry={() => void query.refetch()} />}
      {query.data && (
        <>
          <HealthCharts data={query.data} page={page} hours={params.range} />
          <StaticDataTable
            data={query.data.rows}
            className='overflow-x-auto'
            tableClassName='min-w-[760px]'
            getRowKey={(r) => r.id}
            emptyContent={t('No matching models')}
            columns={[
              {
                id: 'model',
                header: t('Channel and model'),
                cell: (r) => (
                  <div className='max-w-64 min-w-44 py-1 break-words whitespace-normal'>
                    <div className='font-semibold'>{r.model}</div>
                    <div className='text-muted-foreground text-xs'>
                      {r.channel}
                    </div>
                  </div>
                ),
              },
              {
                id: 'endpoints',
                header: t('Endpoint health'),
                cell: (r) => (
                  <div className='grid justify-items-start gap-1'>
                    {r.details.map((ep) => (
                      <Button
                        key={ep.name}
                        variant='ghost'
                        size='sm'
                        onClick={() => setSelected({ row: r, endpoint: ep })}
                      >
                        <span className='font-mono text-xs'>{ep.name}</span>
                        <Status state={ep.state} />
                      </Button>
                    ))}
                  </div>
                ),
              },
              {
                id: 'probe',
                header: t('Last detection success'),
                cell: (r) =>
                  when(Math.max(0, ...r.details.map((ep) => ep.last_probe))),
              },
              {
                id: 'user',
                header: t('Last user success'),
                cell: (r) =>
                  when(Math.max(0, ...r.details.map((ep) => ep.last_user))),
              },
              {
                id: 'latest',
                header: t('Latest failure'),
                cell: (r) => {
                  const fault = r.details
                    .flatMap((ep) => ep.recent)
                    .filter((s) => ['failed', 'uncertain'].includes(s.state))
                    .sort((a, b) => b.at - a.at)[0]
                  return (
                    <div className='max-w-44 text-xs break-words whitespace-normal'>
                      {fault
                        ? `${fault.status_code || ''} ${t(fault.reason || fault.stage)}`.trim()
                        : '\u2014'}
                    </div>
                  )
                },
              },
              {
                id: 'actions',
                header: <div className='text-right'>{t('Actions')}</div>,
                cell: (r) => (
                  <div className='flex justify-end'>
                    {props.access.probe && r.details[0] && (
                      <Button
                        variant='outline'
                        size='sm'
                        onClick={() =>
                          setProbe({ row: r, endpoint: r.details[0].name })
                        }
                      >
                        <Zap aria-hidden='true' />
                        {t('Run detection')}
                      </Button>
                    )}
                  </div>
                ),
              },
            ]}
          />
          <Pages
            page={page}
            hasMore={page * 20 < query.data.total}
            onPage={setPage}
          />
        </>
      )}
      <Sheet
        open={Boolean(row && detail)}
        onOpenChange={(open) => {
          if (!open) setSelected(undefined)
        }}
      >
        {row && detail && (
          <SheetContent className='w-full overflow-y-auto sm:max-w-lg'>
            <SheetHeader>
              <SheetTitle className='break-all'>{row.model}</SheetTitle>
              <SheetDescription>
                {row.channel} #{row.channel_id} · {detail.name}
              </SheetDescription>
            </SheetHeader>
            <div className='space-y-4 px-4 pb-4'>
              <div className='flex flex-wrap items-center gap-2'>
                <Status state={detail.state} />
                {row.paused && (
                  <Badge variant='outline'>{t('Monitoring paused')}</Badge>
                )}
                {detail.stale && (
                  <Badge variant='outline'>{t('Evidence expired')}</Badge>
                )}
                {!detail.configured && (
                  <Badge variant='outline'>
                    {t(
                      detail.template_state === 'draft'
                        ? 'Template needs confirmation'
                        : 'Template required'
                    )}
                  </Badge>
                )}
                {detail.run && <Status state={detail.run.state} />}
              </div>
              <dl className='grid grid-cols-2 gap-3 text-sm'>
                <div>
                  <dt className='text-muted-foreground'>
                    {t('Last user success')}
                  </dt>
                  <dd>{when(detail.last_user)}</dd>
                </div>
                <div>
                  <dt className='text-muted-foreground'>
                    {t('Last detection success')}
                  </dt>
                  <dd>
                    {when(detail.last_probe)}{' '}
                    {detail.last_probe_source && t(detail.last_probe_source)}
                  </dd>
                </div>
                <div>
                  <dt className='text-muted-foreground'>
                    {t('User successes / eligible samples')}
                  </dt>
                  <dd>
                    {detail.user_ok} / {detail.user_count} · {detail.excluded}{' '}
                    {t('excluded')}
                  </dd>
                </div>
                <div>
                  <dt className='text-muted-foreground'>
                    {t('Detection successes / samples')}
                  </dt>
                  <dd>
                    {detail.probe_ok} / {detail.probe_count}
                  </dd>
                </div>
                <div>
                  <dt className='text-muted-foreground'>
                    {t('Next detection')}
                  </dt>
                  <dd>{when(detail.next_at)}</dd>
                </div>
              </dl>
              <div className='flex flex-wrap gap-2'>
                {props.access.probe && (
                  <Button
                    onClick={() =>
                      setProbe({
                        row,
                        endpoint: detail.name,
                      })
                    }
                  >
                    <Zap aria-hidden='true' />
                    {t('Run detection')}
                  </Button>
                )}
                {props.access.rules && (
                  <Button
                    variant='outline'
                    onClick={() => {
                      props.onRules({
                        channel_id: row.channel_id,
                        model: row.model,
                        endpoint: detail.name,
                      })
                      setSelected(undefined)
                    }}
                  >
                    <Settings2 aria-hidden='true' />
                    {t('Configure rule')}
                  </Button>
                )}
              </div>
              <div className='space-y-2 border-t pt-3'>
                <h3 className='font-semibold'>{t('Recent evidence')}</h3>
                {detail.recent.length === 0 && (
                  <p className='text-muted-foreground'>{t('No Data')}</p>
                )}
                {detail.recent.map((sample) => (
                  <div
                    key={`${sample.source}-${sample.evidence_id ?? sample.run_id}`}
                    className='grid grid-cols-[minmax(0,1fr)_auto] items-start gap-2 border-b py-2'
                  >
                    <div className='min-w-0 space-y-1 text-xs break-words'>
                      <div className='flex flex-wrap items-center gap-2'>
                        <Status state={sample.state} />
                        {t(sample.source)} · {when(sample.at)}
                      </div>
                      <div className='text-muted-foreground'>
                        {t(sample.stage)} · {sample.status_code || '\u2014'} ·{' '}
                        {sample.duration_ms} ms{' '}
                        {sample.reason && `· ${t(sample.reason)}`}
                      </div>
                      {sample.request_id && (
                        <div className='font-mono break-all'>
                          {sample.request_id}
                        </div>
                      )}
                      {sample.source === 'user' && sample.key_index >= 0 && (
                        <div className='text-muted-foreground'>
                          {t('Channel key #{{number}}', {
                            number: sample.key_index + 1,
                          })}
                        </div>
                      )}
                    </div>
                    {props.access.export &&
                      Boolean(sample.run_id || sample.evidence_id) && (
                        <EvidenceButton
                          id={sample.run_id || sample.evidence_id || 0}
                          user={sample.source === 'user'}
                        />
                      )}
                  </div>
                ))}
              </div>
            </div>
          </SheetContent>
        )}
      </Sheet>
      {probe && (
        <ProbeDialog
          row={probe.row}
          endpoint={probe.endpoint}
          onEndpoint={(endpoint) => setProbe({ ...probe, endpoint })}
          onClose={() => setProbe(undefined)}
        />
      )}
    </div>
  )
}

function ProbeDialog(props: {
  row: Row
  endpoint: string
  onEndpoint: (value: string) => void
  onClose: () => void
}) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const selection = {
    channel_id: props.row.channel_id,
    model: props.row.model,
    endpoint: props.endpoint,
  }
  const plan = useQuery({
    queryKey: ['model-health', 'plan', selection],
    queryFn: ({ signal }) => loadHealth<Plan>('/plan', selection, signal),
    retry: false,
  })
  const mutation = useMutation({
    mutationFn: (p: Plan) =>
      saveHealth(
        '/probe',
        {
          ...selection,
          revision: p.revision,
          max_micro: p.template.max_micro,
        },
        'post'
      ),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['model-health'] })
      props.onClose()
    },
  })
  return (
    <ConfirmDialog
      open
      onOpenChange={(open) => {
        if (!open && !mutation.isPending) props.onClose()
      }}
      title={t('Run detection')}
      desc={`${props.row.model} · ${props.row.channel}`}
      confirmText={t('Run detection')}
      disabled={!plan.data?.ready || plan.isError}
      isLoading={mutation.isPending}
      handleConfirm={() => {
        if (plan.data) mutation.mutate(plan.data)
      }}
    >
      <label className='flex items-center gap-2 text-sm'>
        {t('Endpoint')}
        <NativeSelect
          value={props.endpoint}
          disabled={mutation.isPending}
          onChange={(e) => props.onEndpoint(e.target.value)}
        >
          {props.row.details.map((ep) => (
            <option key={ep.name} value={ep.name}>
              {ep.name}
            </option>
          ))}
        </NativeSelect>
      </label>
      {plan.isPending && <p className='text-sm'>{t('Loading...')}</p>}
      {plan.isError && <ErrorState onRetry={() => void plan.refetch()} />}
      {plan.data && !plan.data.ready && (
        <div className='text-sm'>
          <p className='font-medium'>{t('Detection not ready')}</p>
          <ul className='mt-1 list-disc space-y-1 ps-5'>
            {plan.data.blocked.map((reason) => (
              <li key={reason}>{t(reason)}</li>
            ))}
          </ul>
        </div>
      )}
      {plan.data?.ready && (
        <p className='text-sm'>
          {t('Cost ceiling')}: {money(plan.data.template.max_micro)}
        </p>
      )}
      {plan.data?.template.body && (
        <pre className='bg-muted max-h-52 overflow-auto rounded-md p-2 text-xs break-all whitespace-pre-wrap'>
          {JSON.stringify(plan.data.template.body, null, 2)}
        </pre>
      )}
    </ConfirmDialog>
  )
}
