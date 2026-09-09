import { useEffect, useRef, useState } from 'react'

import { adminApi } from '../api/client'
import type {
  AgentStoreEntitlement,
  AgentStoreEntitlementInput,
  Customer,
  CustomerApp,
} from '../api/contracts'
import { Button, Field, toBeijingDateTimeLocal, toISO } from '../components/ui'
import { useI18n } from '../i18n'

interface EntitlementRow {
  key: number
  subject_type: AgentStoreEntitlement['subject_type']
  subject_ref: string
  valid_from?: string
  valid_until?: string
}

export function AgentStoreDeploymentFields(props: { customers: Customer[] }) {
  const { t } = useI18n()
  const [customerId, setCustomerId] = useState(0)
  const [customerAppId, setCustomerAppId] = useState(0)
  const [apps, setApps] = useState<CustomerApp[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string>()

  useEffect(() => {
    if (customerId !== 0 || props.customers.length === 0) return
    const firstCustomer = props.customers[0]
    if (firstCustomer != null) setCustomerId(firstCustomer.id)
  }, [customerId, props.customers])

  useEffect(() => {
    setCustomerAppId(0)
    if (customerId === 0) {
      setApps([])
      return
    }
    const controller = new AbortController()
    setLoading(true)
    setError(undefined)
    void adminApi.customerApps(customerId, controller.signal)
      .then((loadedApps) => {
        const eligibleApps = loadedApps.filter((app) => app.current_config_version_id != null && app.status !== 'archived')
        setApps(eligibleApps)
        const firstApp = eligibleApps[0]
        if (firstApp != null) setCustomerAppId(firstApp.id)
      })
      .catch((caught: unknown) => {
        if (!controller.signal.aborted) setError(caught instanceof Error ? caught.message : t('common.error'))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [customerId, t])

  return <>
    <Field label={t('agentStore.customerId')}>
      <select name="customer_id" required value={customerId || ''} onChange={(event) => setCustomerId(Number(event.target.value))}>
        <option value="" disabled>{t('agentStore.chooseCustomer')}</option>
        {props.customers.map((customer) => <option key={customer.id} value={customer.id}>{customer.display_name} ({customer.customer_code})</option>)}
      </select>
    </Field>
    <Field label={t('agentStore.customerAppId')} hint={error ?? t('agentStore.appHint')}>
      <select name="customer_app_id" required disabled={loading || apps.length === 0} value={customerAppId || ''} onChange={(event) => setCustomerAppId(Number(event.target.value))}>
        <option value="" disabled>{loading ? t('common.loading') : t('agentStore.chooseApp')}</option>
        {apps.map((app) => <option key={app.id} value={app.id}>{app.display_name} | {app.app_id} | {app.status}</option>)}
      </select>
    </Field>
  </>
}

export function EntitlementEditor(props: { entitlements?: AgentStoreEntitlement[] }) {
  const { t } = useI18n()
  const nextKey = useRef(0)
  const [rows, setRows] = useState<EntitlementRow[]>(() => (props.entitlements ?? []).map((entitlement) => ({
    key: nextKey.current++,
    subject_type: entitlement.subject_type,
    subject_ref: entitlement.subject_ref,
    valid_from: toDateTimeLocal(entitlement.valid_from),
    valid_until: toDateTimeLocal(entitlement.valid_until),
  })))

  function addRow() {
    setRows((current) => [...current, { key: nextKey.current++, subject_type: 'user', subject_ref: '' }])
  }

  function removeRow(key: number) {
    setRows((current) => current.filter((row) => row.key !== key))
  }

  return <fieldset className="form-section entitlement-editor">
    <legend>{t('agentStore.entitlements')}</legend>
    <p className="form-wide entitlement-help">{t('agentStore.entitlementsHint')}</p>
    {rows.map((row, index) => <div className="entitlement-row form-wide" key={row.key}>
      <Field label={t('agentStore.subjectType')}>
        <select name="entitlement_subject_type" required defaultValue={row.subject_type}>
          <option value="customer">{t('agentStore.subjectCustomer')}</option>
          <option value="user">{t('agentStore.subjectUser')}</option>
          <option value="role">{t('agentStore.subjectRole')}</option>
          <option value="plan">{t('agentStore.subjectPlan')}</option>
        </select>
      </Field>
      <Field label={t('agentStore.subjectRef')}><input name="entitlement_subject_ref" required maxLength={191} defaultValue={row.subject_ref} /></Field>
      <Field label={t('agentStore.validFrom')} hint={t('common.beijingTime')}><input name="entitlement_valid_from" type="datetime-local" defaultValue={row.valid_from} /></Field>
      <Field label={t('agentStore.validUntil')} hint={t('common.beijingTime')}><input name="entitlement_valid_until" type="datetime-local" defaultValue={row.valid_until} /></Field>
      <Button type="button" tone="secondary" onClick={() => removeRow(row.key)} aria-label={`${t('agentStore.removeEntitlement')} ${index + 1}`}>{t('agentStore.removeEntitlement')}</Button>
    </div>)}
    <div className="form-wide"><Button type="button" tone="secondary" onClick={addRow}>{t('agentStore.addEntitlement')}</Button></div>
  </fieldset>
}

export function parseEntitlements(form: FormData): AgentStoreEntitlementInput[] {
  const types = form.getAll('entitlement_subject_type')
  const refs = form.getAll('entitlement_subject_ref')
  const validFromValues = form.getAll('entitlement_valid_from')
  const validUntilValues = form.getAll('entitlement_valid_until')

  return refs.map((ref, index) => {
    const validFrom = toISO(validFromValues[index] ?? null)
    const validUntil = toISO(validUntilValues[index] ?? null)
    return {
      subject_type: String(types[index] ?? 'customer') as AgentStoreEntitlementInput['subject_type'],
      subject_ref: String(ref).trim(),
      ...(validFrom ? { valid_from: validFrom } : {}),
      ...(validUntil ? { valid_until: validUntil } : {}),
    }
  })
}

function toDateTimeLocal(value?: string): string | undefined {
  if (!value) return undefined
  const parsed = new Date(value)
  return Number.isNaN(parsed.valueOf()) ? undefined : toBeijingDateTimeLocal(parsed)
}
