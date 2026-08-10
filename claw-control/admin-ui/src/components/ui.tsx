import { type FormEvent, type PropsWithChildren, type ReactNode, useRef } from 'react'

import { ApiError } from '../api/client'
import { useI18n } from '../i18n'
import {
  formatBeijingDate,
  parseBeijingDateTimeLocal,
  toBeijingDateTimeLocal,
} from './beijing-time'

export { toBeijingDateTimeLocal }

export function PageHeader({ title, actions }: { title: string; actions?: ReactNode }) {
  return <header className="page-header"><h1>{title}</h1><div className="actions">{actions}</div></header>
}

export function Button({ children, tone = 'primary', ...props }: PropsWithChildren<React.ButtonHTMLAttributes<HTMLButtonElement> & { tone?: 'primary' | 'secondary' | 'danger' }>) {
  return <button className={`button button--${tone}`} {...props}>{children}</button>
}

export function Status({ value }: { value: string }) {
  const normalized = value.toLowerCase()
  const tone = ['active', 'paid', 'verified', 'locked', 'available', 'approved', 'executed', 'completed', 'read'].includes(normalized)
    ? 'good'
    : ['disabled', 'invalid', 'failed', 'expired', 'rejected'].includes(normalized) ? 'bad' : 'neutral'
  return <span className={`status status--${tone}`}>{value}</span>
}

export function DataState({ loading, error, empty, onRetry, children }: PropsWithChildren<{ loading: boolean; error?: unknown; empty?: boolean; onRetry: () => void }>) {
  const { t } = useI18n()
  if (loading) return <p className="state" role="status">{t('common.loading')}</p>
  if (error) {
    const message = error instanceof Error ? error.message : t('common.error')
    const requestId = error instanceof ApiError ? error.requestId : undefined
    return <div className="state state--error" role="alert"><p>{message}</p>{requestId && <p>{t('common.requestId')}: <code>{requestId}</code></p>}<Button onClick={onRetry}>{t('action.retry')}</Button></div>
  }
  if (empty) return <p className="state">{t('common.empty')}</p>
  return children
}

export function Field({ label, children, hint }: PropsWithChildren<{ label: string; hint?: string }>) {
  return <label className="field"><span>{label}</span>{children}{hint && <small>{hint}</small>}</label>
}

export function DialogForm({ title, trigger, children, onSubmit, submitLabel }: PropsWithChildren<{
  title: string
  trigger: string
  submitLabel: string
  onSubmit: (form: FormData) => Promise<void>
}>) {
  const dialogRef = useRef<HTMLDialogElement>(null)
  const { t } = useI18n()
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = event.currentTarget
    const submitter = form.querySelector<HTMLButtonElement>('button[type="submit"]')
    if (submitter) submitter.disabled = true
    try {
      await onSubmit(new FormData(form))
      dialogRef.current?.close()
      form.reset()
    } catch (error) {
      const output = form.querySelector<HTMLOutputElement>('output')
      if (output) output.value = error instanceof Error ? error.message : t('common.error')
    } finally {
      if (submitter) submitter.disabled = false
    }
  }
  return <>
    <Button tone="secondary" onClick={() => dialogRef.current?.showModal()}>{trigger}</Button>
    <dialog ref={dialogRef} className="dialog" onCancel={() => dialogRef.current?.close()}>
      <form onSubmit={submit}>
        <header><h2>{title}</h2><Button type="button" tone="secondary" onClick={() => dialogRef.current?.close()} aria-label={t('action.close')}>×</Button></header>
        <div className="form-grid">{children}</div>
        <output className="form-error" aria-live="polite" />
        <footer><Button type="button" tone="secondary" onClick={() => dialogRef.current?.close()}>{t('action.cancel')}</Button><Button type="submit">{submitLabel}</Button></footer>
      </form>
    </dialog>
  </>
}

export function toNumber(form: FormData, name: string, fallback = 0) {
  const value = Number(form.get(name))
  return Number.isFinite(value) ? value : fallback
}

export function toOptionalNumber(form: FormData, name: string) {
  const raw = String(form.get(name) ?? '').trim()
  return raw === '' ? undefined : toNumber(form, name)
}

export function toISO(value: FormDataEntryValue | null) {
  return parseBeijingDateTimeLocal(value)
}

export function formatDate(value?: string, locale = 'zh') {
  return formatBeijingDate(value, locale)
}
