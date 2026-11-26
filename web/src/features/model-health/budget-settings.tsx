import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { getServerErrorMessage } from '@/lib/server-error-message'

import { loadHealth, money, saveHealth, when, type Selection } from './api'
import { cycleBudgetSchema, type CycleBudgetValues } from './lib/budget-schema'

export type CycleBudgetStatus = {
  scope: string
  override: boolean
  configured: boolean
  source: string
  amount_micro: number
  run_micro: number
  interval_seconds: number
  next_interval_seconds: number
  start: number
  end: number
  used_micro: number
  reserved_micro: number
  remaining_micro: number
  global_remaining_micro: number
  recommended_micro: number
  recommended_run_micro: number
  estimated_round_micro: number
  targets: number
  missing_templates: number
  over_run: number
  warnings: string[]
}

function BudgetForm(props: {
  status: CycleBudgetStatus
  scope: string
  selection?: Selection
}) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const form = useForm<CycleBudgetValues>({
    resolver: zodResolver(cycleBudgetSchema),
    defaultValues: {
      override: props.scope === 'global' || props.status.override,
      amount: String(props.status.amount_micro / 1_000_000),
      run: String(props.status.run_micro / 1_000_000),
      days: String(
        (props.status.next_interval_seconds || props.status.interval_seconds) /
          86400
      ),
    },
  })
  const override = form.watch('override')
  const mutation = useMutation({
    mutationFn: (values: CycleBudgetValues) =>
      saveHealth('/cycle-budget', {
        ...props.selection,
        scope: props.scope,
        override: values.override,
        amount_micro: Math.round(Number(values.amount) * 1_000_000),
        run_micro: Math.round(Number(values.run) * 1_000_000),
        interval_seconds: Math.round(Number(values.days) * 86400),
      }),
    onSuccess: () => {
      toast.success(t('Saved successfully'))
      void client.invalidateQueries({ queryKey: ['model-health'] })
    },
  })
  const fields = [
    ['amount', 'Cycle budget (CNY)', '0.01', 0.01, 1_000_000],
    ['run', 'Per-detection limit (CNY)', '0.01', 0.01, 5],
    ['days', 'Budget cycle (days)', '1', 1, 7],
  ] as const
  return (
    <form
      noValidate
      className='space-y-3'
      onSubmit={form.handleSubmit((values) => mutation.mutate(values))}
    >
      <FieldGroup className='gap-3'>
        {props.scope !== 'global' && (
          <Field orientation='horizontal'>
            <Checkbox
              id='budget-override'
              checked={override}
              onCheckedChange={(v) => form.setValue('override', Boolean(v))}
            />
            <FieldLabel htmlFor='budget-override'>
              {t('Set a separate budget for this scope')}
            </FieldLabel>
          </Field>
        )}
        <div className='grid gap-3 sm:grid-cols-3'>
          {fields.map(([key, label, step, min, max]) => (
            <Field
              key={key}
              data-invalid={Boolean(form.formState.errors[key])}
              data-disabled={!override}
            >
              <FieldLabel htmlFor={`budget-${key}`}>{t(label)}</FieldLabel>
              <Input
                id={`budget-${key}`}
                type='number'
                step={step}
                min={min}
                max={max}
                disabled={!override || mutation.isPending}
                aria-invalid={Boolean(form.formState.errors[key])}
                {...form.register(key)}
              />
              {form.formState.errors[key] && (
                <FieldDescription role='alert'>
                  {t(
                    'Enter a valid budget; the per-detection limit must not exceed CNY 5 or the cycle budget.'
                  )}
                </FieldDescription>
              )}
            </Field>
          ))}
        </div>
      </FieldGroup>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Model overrides type; type overrides global. Every detection also shares the overall cycle cap. Editing an amount does not reset spending; cycle length changes at the next boundary.'
        )}
      </p>
      <div className='flex flex-wrap gap-2'>
        <Button type='submit' disabled={mutation.isPending}>
          {t('Save cycle budget')}
        </Button>
        <Button
          type='button'
          variant='outline'
          disabled={!override || mutation.isPending}
          onClick={() => {
            form.setValue(
              'amount',
              String(props.status.recommended_micro / 1_000_000)
            )
            form.setValue(
              'run',
              String(props.status.recommended_run_micro / 1_000_000)
            )
          }}
        >
          {t('Use recommended amounts')}
        </Button>
      </div>
      {mutation.isError && (
        <Alert variant='destructive'>
          <AlertDescription>
            {getServerErrorMessage(mutation.error)}
          </AlertDescription>
        </Alert>
      )}
    </form>
  )
}

export function HealthCycleBudget(props: {
  scope: string
  selection?: Selection
}) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['model-health', 'cycle-budget', props.scope, props.selection],
    queryFn: () =>
      loadHealth<CycleBudgetStatus>('/cycle-budget', {
        scope: props.scope,
        ...props.selection,
      }),
    staleTime: 30_000,
    refetchInterval: 60_000,
    retry: false,
    meta: { errorToast: false },
  })
  const status = query.data
  let sourceLabel = status?.source ?? ''
  if (sourceLabel === 'global') sourceLabel = 'Global defaults'
  if (sourceLabel === 'image') sourceLabel = 'Image'
  return (
    <section
      aria-label={t('Detection cycle budget')}
      className='space-y-3 border-t pt-4'
    >
      <h3 className='font-medium'>{t('Detection cycle budget')}</h3>
      {query.isPending && <LoadingState />}
      {query.isError && (
        <ErrorState
          description={getServerErrorMessage(query.error)}
          onRetry={() => void query.refetch()}
        />
      )}
      {status && (
        <>
          <p className='text-muted-foreground text-sm'>
            {t('Budget source')}: {t(sourceLabel)} ·{' '}
            {t('Recommended cycle budget')}: {money(status.recommended_micro)}
          </p>
          <dl className='grid grid-cols-2 gap-3 text-sm sm:grid-cols-4'>
            {(
              [
                ['Used and reserved', status.used_micro],
                ['Awaiting fee confirmation', status.reserved_micro],
                ['Remaining cycle budget', status.remaining_micro],
                ['Overall remaining budget', status.global_remaining_micro],
              ] as const
            ).map(([label, value]) => (
              <div key={label}>
                <dt className='text-muted-foreground'>{t(label)}</dt>
                <dd className='font-medium'>{money(value)}</dd>
              </div>
            ))}
          </dl>
          <p className='text-muted-foreground text-sm'>
            {t('Current budget period')}: {when(status.start)} –{' '}
            {when(status.end)} · {t('Estimated scheduled ceiling')}:{' '}
            {money(status.estimated_round_micro)} · {t('Detection targets')}:{' '}
            {status.targets}
          </p>
          {status.warnings.length > 0 && (
            <Alert>
              <AlertTitle>{t('Detection budget notice')}</AlertTitle>
              <AlertDescription>
                <ul className='list-disc space-y-1 pl-4'>
                  {status.warnings.map((warning) => (
                    <li key={warning}>{t(warning)}</li>
                  ))}
                </ul>
                <p>
                  {t(
                    'Insufficient budget defers new detections; it does not increase the budget automatically or interrupt customer API calls.'
                  )}
                </p>
              </AlertDescription>
            </Alert>
          )}
          <BudgetForm
            key={`${props.scope}-${JSON.stringify(props.selection)}-${status.override}-${status.amount_micro}-${status.run_micro}-${status.interval_seconds}-${status.next_interval_seconds}`}
            status={status}
            scope={props.scope}
            selection={props.selection}
          />
        </>
      )}
    </section>
  )
}
