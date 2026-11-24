import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, Save, Send } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { NativeSelect } from '@/components/ui/native-select'
import { Textarea } from '@/components/ui/textarea'
import {
  getServerErrorMessage,
  getServerErrorStatus,
} from '@/lib/server-error-message'

import {
  loadHealth,
  saveHealth,
  when,
  type Delivery,
  type Destination,
  type Page,
  type Plan,
  type Selection,
} from './api'
import { Pages, Status } from './status'

type Values = Record<string, unknown>
type Field = {
  key: string
  label: string
  type: 'text' | 'number' | 'checkbox' | 'json' | 'list' | 'select' | 'interval'
  options?: string[]
  readOnly?: boolean
  wholeDays?: boolean
}
const modalities = [
  'llm',
  'video',
  'image',
  'embedding',
  'audio',
  'tts',
  'speech2text',
  'rerank',
]

const ruleReadOptions = {
  staleTime: 60_000,
  retry: false,
  meta: { errorToast: false },
}

function RuleReadError(props: { error: unknown; onRetry: () => void }) {
  const { t } = useTranslation()
  const rateLimited = getServerErrorStatus(props.error) === 429
  return (
    <ErrorState
      title={rateLimited ? t('Too many requests') : undefined}
      description={
        rateLimited
          ? t('Please try again later')
          : getServerErrorMessage(props.error)
      }
      onRetry={props.onRetry}
    />
  )
}

const intervalUnits = [
  [86400, 'Probe interval days'],
  [3600, 'Probe interval hours'],
  [60, 'Probe interval minutes'],
  [1, 'Probe interval seconds'],
] as const

function IntervalInput(props: {
  label: string
  seconds: number
  onChange: (seconds: number) => void
  wholeDays?: boolean
}) {
  const { t } = useTranslation()
  const [unit, setUnit] = useState<number>(
    () =>
      intervalUnits.find(
        ([n]) => props.seconds >= n && props.seconds % n === 0
      )?.[0] ?? 1
  )
  return (
    <div className='min-w-0 space-y-1 text-sm'>
      <div>{props.label}</div>
      <div className='flex gap-2'>
        <Input
          aria-label={props.label}
          type='number'
          min={props.wholeDays ? 4 : 1}
          max={props.wholeDays ? 7 : undefined}
          step={1}
          required
          value={props.seconds ? props.seconds / unit : ''}
          onChange={(e) => props.onChange(Number(e.target.value) * unit)}
        />
        <NativeSelect
          aria-label={t('Cycle unit')}
          disabled={props.wholeDays}
          value={unit}
          onChange={(e) => {
            const next = Number(e.target.value)
            props.onChange(Math.max(1, Math.round(props.seconds / next)) * next)
            setUnit(next)
          }}
        >
          {intervalUnits.map(([value, label]) => (
            <option key={value} value={value}>
              {t(label)}
            </option>
          ))}
        </NativeSelect>
      </div>
    </div>
  )
}

function HealthForm(props: {
  fields: Field[]
  initial: Values
  save: (data: Values) => Promise<void>
  submit?: string
}) {
  const { t } = useTranslation()
  const [values, setValues] = useState<Values>(() =>
    Object.fromEntries(
      props.fields.map((field) => {
        const value = props.initial[field.key]
        if (field.type === 'json') {
          return [field.key, JSON.stringify(value ?? {}, null, 2)]
        }
        if (field.type === 'list') {
          return [field.key, Array.isArray(value) ? value.join(', ') : '']
        }
        return [field.key, value ?? (field.type === 'checkbox' ? false : '')]
      })
    )
  )
  const [invalid, setInvalid] = useState('')
  const client = useQueryClient()
  const mutation = useMutation({
    mutationFn: props.save,
    onSuccess: () => {
      toast.success(t('Saved successfully'))
      void client.invalidateQueries({ queryKey: ['model-health'] })
    },
  })
  return (
    <form
      className='grid gap-3 sm:grid-cols-2 xl:grid-cols-3'
      onSubmit={(event) => {
        event.preventDefault()
        setInvalid('')
        const body: Values = {}
        for (const field of props.fields) {
          const value = values[field.key]
          if (field.type === 'json') {
            try {
              body[field.key] = JSON.parse(String(value))
            } catch {
              setInvalid(field.key)
              return
            }
          } else if (field.type === 'list') {
            body[field.key] = String(value)
              .split(',')
              .map((v) => v.trim())
              .filter(Boolean)
          } else if (field.type === 'number') body[field.key] = Number(value)
          else body[field.key] = value
        }
        mutation.mutate(body)
      }}
    >
      {props.fields.map((field) => {
        const set = (value: unknown) =>
          setValues({ ...values, [field.key]: value })
        if (field.type === 'interval') {
          return (
            <IntervalInput
              key={field.key}
              label={field.label}
              seconds={Number(values[field.key])}
              onChange={set}
              wholeDays={field.wholeDays}
            />
          )
        }
        if (field.type === 'checkbox') {
          return (
            <label
              key={field.key}
              className='flex min-h-8 items-center gap-2 self-end text-sm'
            >
              <Checkbox
                checked={Boolean(values[field.key])}
                onCheckedChange={(v) => set(v)}
              />
              {field.label}
            </label>
          )
        }
        if (field.type === 'select') {
          return (
            <label key={field.key} className='min-w-0 space-y-1 text-sm'>
              {field.label}
              <NativeSelect
                className='w-full'
                value={String(values[field.key])}
                onChange={(e) => set(e.target.value)}
              >
                {field.options?.map((v) => (
                  <option key={v} value={v}>
                    {v ? t(v === 'image' ? 'Image' : v) : t('All')}
                  </option>
                ))}
              </NativeSelect>
            </label>
          )
        }
        return (
          <label
            key={field.key}
            className={
              field.type === 'json'
                ? 'space-y-1 text-sm sm:col-span-2 xl:col-span-3'
                : 'min-w-0 space-y-1 text-sm'
            }
          >
            {field.label}
            {field.type === 'json' ? (
              <Textarea
                rows={6}
                className='font-mono'
                aria-invalid={invalid === field.key}
                value={String(values[field.key])}
                onChange={(e) => set(e.target.value)}
              />
            ) : (
              <Input
                type={field.type === 'number' ? 'number' : 'text'}
                step={field.type === 'number' ? '1' : undefined}
                readOnly={field.readOnly}
                value={String(values[field.key])}
                onChange={(e) => set(e.target.value)}
              />
            )}
            {invalid === field.key && (
              <span role='alert' className='text-destructive'>
                {t('Invalid JSON')}
              </span>
            )}
          </label>
        )
      })}
      <div className='sm:col-span-2 xl:col-span-3'>
        <Button type='submit' disabled={mutation.isPending}>
          <Save aria-hidden='true' />
          {props.submit ?? t('Save')}
        </Button>
      </div>
    </form>
  )
}

export function HealthRules(props: { selection?: Selection }) {
  const { t } = useTranslation()
  const [selectedScope, setScope] = useState<string>()
  const scope = selectedScope ?? (props.selection ? 'model' : 'global')
  const target = scope === 'model' ? props.selection : undefined
  const [templateOpen, setTemplateOpen] = useState(false)
  const rules = useQuery({
    ...ruleReadOptions,
    queryKey: ['model-health', 'rule', scope, target],
    queryFn: () =>
      target
        ? loadHealth<Plan>('/template', target)
        : loadHealth<Values>('/rule', { scope }),
  })
  const plan = target ? (rules.data as Plan | undefined) : undefined
  const rule = target ? plan?.rule : (rules.data as Values | undefined)
  const kind = plan?.kind ?? scope
  const paidMedia = kind === 'video' || kind === 'image'
  const ruleFields: Field[] = [
    {
      key: 'enabled',
      label: t('Enable scheduled detection'),
      type: 'checkbox',
    },
    {
      key: 'interval_seconds',
      label: t('Detection interval'),
      type: 'interval',
      wholeDays: paidMedia,
    },
    {
      key: 'per_cycle',
      label: t('Runs per cycle'),
      type: 'number',
      readOnly: paidMedia,
    },
    {
      key: 'per_day',
      label: t('Runs per day'),
      type: 'number',
      readOnly: paidMedia,
    },
    { key: 'slow_ms', label: t('Slow threshold (ms)'), type: 'number' },
    { key: 'fail_after', label: t('Failures before alerting'), type: 'number' },
    {
      key: 'recover_after',
      label: t('Successes before recovery'),
      type: 'number',
    },
    { key: 'start_hour', label: t('Start hour (UTC+8)'), type: 'number' },
    { key: 'end_hour', label: t('End hour (UTC+8)'), type: 'number' },
  ]
  return (
    <div className='space-y-5 pt-3'>
      <section className='space-y-3 border-b pb-4'>
        <h2 className='font-semibold'>{t('Schedule')}</h2>
        <label className='flex flex-wrap items-center gap-2'>
          {t('Rule scope')}
          <NativeSelect
            value={scope}
            onChange={(e) => setScope(e.target.value)}
          >
            <option value='global'>{t('Global defaults')}</option>
            {modalities.map((kind) => (
              <option key={kind} value={kind}>
                {t(kind === 'image' ? 'Image' : kind)}
              </option>
            ))}
            {props.selection && (
              <option value='model'>
                {props.selection.model} · #{props.selection.channel_id} ·{' '}
                {props.selection.endpoint}
              </option>
            )}
          </NativeSelect>
        </label>
        {paidMedia && (
          <p className='text-muted-foreground text-sm'>
            {t(
              'Paid media: once every 4 to 7 days, using the minimum specification. Recommended: {{days}} days.',
              { days: kind === 'video' ? 7 : 5 }
            )}
          </p>
        )}
        {kind === 'llm' && (
          <p className='text-muted-foreground text-sm'>
            {t('Recommended for text: one detection per day.')}
          </p>
        )}
        {rules.isPending && <LoadingState />}
        {rules.isError && (
          <RuleReadError
            error={rules.error}
            onRetry={() => void rules.refetch()}
          />
        )}
        {rule && (
          <HealthForm
            key={`${scope}-${target?.channel_id}-${target?.model}-${target?.endpoint}`}
            fields={ruleFields}
            initial={rule}
            save={(data) =>
              saveHealth('/rule', { ...target, scope, rule: data })
            }
          />
        )}
      </section>
      {target && plan && (
        <section className='space-y-3 border-b pb-4'>
          <div className='flex flex-wrap items-center justify-between gap-2'>
            <h2 className='font-semibold'>{t('Provider probe template')}</h2>
            <Button
              variant='outline'
              onClick={() => setTemplateOpen(!templateOpen)}
              aria-expanded={templateOpen}
            >
              {t('Edit template')}
            </Button>
          </div>
          {templateOpen && (
            <HealthForm
              key={`${target.channel_id}-${target.model}-${target.endpoint}`}
              initial={{
                ...plan.template,
                auth: plan.template.auth || 'bearer',
                timeout_seconds: plan.template.timeout_seconds || 30,
                max_tokens: plan.template.max_tokens || 128,
              }}
              save={(data) => saveHealth('/template', { ...data, ...target })}
              fields={[
                { key: 'path', label: t('Upstream POST path'), type: 'text' },
                {
                  key: 'credential',
                  label: t('Credential environment reference'),
                  type: 'text',
                },
                {
                  key: 'auth',
                  label: t('Authentication type'),
                  type: 'select',
                  options: ['bearer', 'anthropic', 'gemini'],
                },
                {
                  key: 'max_micro',
                  label: t('Cost ceiling (millionths of CNY)'),
                  type: 'number',
                },
                { key: 'max_tokens', label: t('Token budget'), type: 'number' },
                {
                  key: 'timeout_seconds',
                  label: t('Timeout (seconds)'),
                  type: 'number',
                },
                {
                  key: 'poll_path',
                  label: t('Polling path with {task_id}'),
                  type: 'text',
                },
                { key: 'task_field', label: t('Task ID field'), type: 'text' },
                {
                  key: 'status_field',
                  label: t('Task status field'),
                  type: 'text',
                },
                {
                  key: 'success_values',
                  label: t('Success statuses (comma separated)'),
                  type: 'list',
                },
                {
                  key: 'pending_values',
                  label: t('Pending statuses (comma separated)'),
                  type: 'list',
                },
                {
                  key: 'failure_values',
                  label: t('Failure statuses (comma separated)'),
                  type: 'list',
                },
                { key: 'result_field', label: t('Result field'), type: 'text' },
                {
                  key: 'cost_field',
                  label: t('Actual cost field (millionths of CNY)'),
                  type: 'text',
                },
                {
                  key: 'dimension',
                  label: t('Expected embedding dimension'),
                  type: 'number',
                },
                {
                  key: 'body',
                  label: t('Public probe request JSON'),
                  type: 'json',
                },
                {
                  key: 'minimum_confirmed',
                  label: t(
                    'Minimum supported specification and cost ceiling confirmed'
                  ),
                  type: 'checkbox',
                },
              ]}
            />
          )}
        </section>
      )}
    </div>
  )
}

export function HealthNotifications(props: { worker: boolean }) {
  const { t } = useTranslation()
  const [edit, setEdit] = useState<Values>()
  const [test, setTest] = useState<Destination>()
  const [targetPage, setTargetPage] = useState(1)
  const [deliveryPage, setDeliveryPage] = useState(1)
  const client = useQueryClient()
  const targets = useQuery({
    queryKey: ['model-health', 'notify', targetPage],
    queryFn: ({ signal }) =>
      loadHealth<Page<Destination>>(
        '/notifications',
        { page: targetPage },
        signal
      ),
  })
  const deliveries = useQuery({
    queryKey: ['model-health', 'deliveries', deliveryPage],
    queryFn: ({ signal }) =>
      loadHealth<Page<Delivery>>('/deliveries', { page: deliveryPage }, signal),
    refetchInterval: 15000,
  })
  const send = useMutation({
    mutationFn: (d: Destination) =>
      saveHealth('/notifications/test', { id: d.id }, 'post'),
    onSuccess: () => {
      setTest(undefined)
      void client.invalidateQueries({
        queryKey: ['model-health', 'deliveries'],
      })
    },
  })
  return (
    <div className='space-y-4 pt-3'>
      <div className='flex items-center justify-between gap-2'>
        <h2 className='font-semibold'>{t('Destinations')}</h2>
        <Button
          onClick={() =>
            setEdit({
              kind: 'feishu',
              interval_seconds: 600,
              max_per_incident: 2,
              max_per_day: 20,
              start_hour: 0,
              end_hour: 24,
              recovery: true,
            })
          }
        >
          <Plus aria-hidden='true' />
          {t('Add destination')}
        </Button>
      </div>
      {targets.isPending && <LoadingState />}
      {targets.isError && <ErrorState onRetry={() => void targets.refetch()} />}
      {targets.data && (
        <div role='region' aria-label={t('Destinations')}>
          <StaticDataTable
            data={targets.data.items}
            getRowKey={(d) => d.id}
            emptyContent={t('No Data')}
            columns={[
              { id: 'name', header: t('Name'), cell: (d) => d.name },
              { id: 'kind', header: t('Type'), cell: (d) => t(d.kind) },
              {
                id: 'enabled',
                header: t('Status'),
                cell: (d) => (d.enabled ? t('Enabled') : t('Disabled')),
              },
              {
                id: 'actions',
                header: t('Actions'),
                cell: (d) => (
                  <div className='flex flex-wrap gap-2'>
                    <Button
                      size='sm'
                      variant='outline'
                      onClick={() => setEdit(d)}
                    >
                      {t('Edit')}
                    </Button>
                    <Button
                      size='sm'
                      variant='outline'
                      disabled={!props.worker}
                      onClick={() => setTest(d)}
                    >
                      <Send aria-hidden='true' />
                      {t('Test send')}
                    </Button>
                  </div>
                ),
              },
            ]}
          />
          <Pages
            page={targetPage}
            hasMore={targets.data.has_more}
            onPage={setTargetPage}
          />
        </div>
      )}
      {edit && (
        <section className='space-y-3 border-y py-4'>
          <div className='flex items-center justify-between'>
            <h3 className='font-semibold'>{t('Destination settings')}</h3>
            <Button
              size='sm'
              variant='ghost'
              onClick={() => setEdit(undefined)}
            >
              {t('Cancel')}
            </Button>
          </div>
          <HealthForm
            key={String(edit.id ?? 'new')}
            initial={edit}
            save={async (data) => {
              await saveHealth('/notifications', data)
              setEdit(undefined)
            }}
            fields={[
              { key: 'id', label: t('Destination identifier'), type: 'text' },
              { key: 'name', label: t('Name'), type: 'text' },
              {
                key: 'kind',
                label: t('Type'),
                type: 'select',
                options: ['feishu', 'dingtalk', 'webhook'],
              },
              {
                key: 'url_ref',
                label: t('Webhook environment reference'),
                type: 'text',
              },
              {
                key: 'sign_ref',
                label: t('Signature environment reference'),
                type: 'text',
              },
              {
                key: 'channel_id',
                label: t('Channel ID (0 for all)'),
                type: 'number',
              },
              {
                key: 'type',
                label: t('Model type'),
                type: 'select',
                options: ['', ...modalities],
              },
              {
                key: 'source',
                label: t('Detection source'),
                type: 'select',
                options: ['', 'user', 'manual', 'scheduled'],
              },
              {
                key: 'interval_seconds',
                label: t('Reminder interval (seconds)'),
                type: 'number',
              },
              {
                key: 'max_per_incident',
                label: t('Reminders per incident'),
                type: 'number',
              },
              {
                key: 'max_per_day',
                label: t('Messages per day'),
                type: 'number',
              },
              {
                key: 'start_hour',
                label: t('Start hour (UTC+8)'),
                type: 'number',
              },
              { key: 'end_hour', label: t('End hour (UTC+8)'), type: 'number' },
              { key: 'enabled', label: t('Enabled'), type: 'checkbox' },
              {
                key: 'recovery',
                label: t('Notify on recovery'),
                type: 'checkbox',
              },
            ]}
          />
        </section>
      )}
      <h2 className='font-semibold'>{t('Delivery history')}</h2>
      {deliveries.isPending && <LoadingState />}
      {deliveries.isError && (
        <ErrorState onRetry={() => void deliveries.refetch()} />
      )}
      {deliveries.data && (
        <div role='region' aria-label={t('Delivery history')}>
          <StaticDataTable
            data={deliveries.data.items}
            getRowKey={(d) => d.id}
            emptyContent={t('No Data')}
            tableClassName='min-w-[600px]'
            columns={[
              {
                id: 'target',
                header: t('Destinations'),
                cell: (d) => d.target,
              },
              { id: 'event', header: t('Event'), cell: (d) => t(d.event) },
              {
                id: 'time',
                header: t('Time'),
                cell: (d) => when(d.created_at),
              },
              {
                id: 'state',
                header: t('Status'),
                cell: (d) => <Status state={d.state} />,
              },
              {
                id: 'attempts',
                header: t('Attempts'),
                cell: (d) => `${d.attempts} · ${d.status_code || '\u2014'}`,
              },
              {
                id: 'retry',
                header: t('Next retry'),
                cell: (d) => when(d.next_at),
              },
            ]}
          />
          <Pages
            page={deliveryPage}
            hasMore={deliveries.data.has_more}
            onPage={setDeliveryPage}
          />
        </div>
      )}
      <ConfirmDialog
        open={Boolean(test)}
        onOpenChange={(open) => {
          if (!open && !send.isPending) setTest(undefined)
        }}
        title={t('Test send')}
        desc={test?.name ?? ''}
        isLoading={send.isPending}
        handleConfirm={() => {
          if (test) send.mutate(test)
        }}
      />
    </div>
  )
}
