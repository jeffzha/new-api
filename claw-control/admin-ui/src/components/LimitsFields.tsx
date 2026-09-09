import type { Limits } from '../api/contracts'
import { Field } from './ui'
import { useI18n } from '../i18n'

export const defaultLimits: Limits = {
  customer_concurrency: 10,
  user_concurrency: 1,
  max_runtime_seconds: 900,
  max_reasoning_rounds: 20,
  max_output_tokens: 8192,
  web_search_per_turn: 0,
  max_file_bytes: 52428800,
}

export const limitKeys = Object.keys(defaultLimits) as (keyof Limits)[]

export function LimitsFields({ values = defaultLimits }: { values?: Limits }) {
  const { t } = useI18n()
  return <fieldset className="form-section"><legend>{t('app.limits')}</legend>{limitKeys.map((key) => <Field key={key} label={t(`limits.${key}`)}><input name={`limit_${key}`} type="number" min={key === 'web_search_per_turn' || key === 'max_file_bytes' ? 0 : 1} required defaultValue={values[key]} /></Field>)}</fieldset>
}

export function limitsFromForm(form: FormData): Limits {
  return Object.fromEntries(limitKeys.map((key) => [key, Number(form.get(`limit_${key}`))])) as unknown as Limits
}
