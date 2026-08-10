import { useCallback, useState, type FormEvent } from 'react'

import { adminApi } from '../api/client'
import type { CredentialProfile } from '../api/contracts'
import { Button, DataState, DialogForm, Field, PageHeader, Status, formatDate, toNumber } from '../components/ui'
import { useResource } from '../hooks/useResource'
import { useI18n } from '../i18n'

export default function CredentialsPage() {
  const { locale, t } = useI18n()
  const [customerFilter, setCustomerFilter] = useState('')
  const [appliedCustomerFilter, setAppliedCustomerFilter] = useState<number>()
  const load = useCallback((signal: AbortSignal) => adminApi.credentials(signal, appliedCustomerFilter), [appliedCustomerFilter])
  const resource = useResource(load)

  function applyFilter(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const parsed = Number(customerFilter)
    setAppliedCustomerFilter(customerFilter === '' || !Number.isInteger(parsed) || parsed <= 0 ? undefined : parsed)
  }

  async function create(form: FormData) {
    const scope = String(form.get('scope') ?? 'platform')
    const customerId = scope === 'customer' ? toNumber(form, 'customer_id') : undefined
    await adminApi.createCredential({
      owner_scope: customerId === undefined ? 'platform' : `customer:${customerId}`,
      ...(customerId === undefined ? {} : { customer_id: customerId }),
      provider_environment: String(form.get('provider_environment') ?? ''),
      name: String(form.get('name') ?? '').trim(),
      secret_id_ref: String(form.get('secret_id_ref') ?? '').trim(),
      secret_key_ref: String(form.get('secret_key_ref') ?? '').trim(),
      fingerprint: String(form.get('fingerprint') ?? '').trim(),
    })
    resource.refresh()
  }

  async function rotate(profile: CredentialProfile, form: FormData) {
    await adminApi.stageCredentialRotation(profile.id, {
      expected_version: profile.row_version,
      secret_id_ref: String(form.get('secret_id_ref') ?? '').trim(),
      secret_key_ref: String(form.get('secret_key_ref') ?? '').trim(),
      fingerprint: String(form.get('fingerprint') ?? '').trim(),
    })
    resource.refresh()
  }

  async function retire(profile: CredentialProfile, form: FormData) {
    await adminApi.retireCredential(profile.id, {
      expected_version: profile.row_version,
      reason: String(form.get('reason') ?? '').trim(),
    })
    resource.refresh()
  }

  return <>
    <PageHeader title={t('credentials.title')} actions={<>
      <Button tone="secondary" onClick={resource.refresh}>{t('action.refresh')}</Button>
      <DialogForm title={t('credentials.create')} trigger={t('credentials.create')} submitLabel={t('action.create')} onSubmit={create}>
        <p className="form-wide security-note">{t('credentials.security')}</p>
        <CredentialScopeFields />
        <Field label={t('app.provider')}><select name="provider_environment" defaultValue="china_tencent_cloud"><option value="china_tencent_cloud">china_tencent_cloud</option><option value="china_tencent_adp">china_tencent_adp</option></select></Field>
        <Field label={t('credentials.name')}><input name="name" required maxLength={120} /></Field>
        <SecretReferenceFields />
      </DialogForm>
    </>} />
    <section className="panel">
      <form className="inline-form" onSubmit={applyFilter}><Field label={t('credentials.customerFilter')}><input type="number" min="1" value={customerFilter} onChange={(event) => setCustomerFilter(event.target.value)} placeholder={t('credentials.customerFilterPlaceholder')} /></Field><Button type="submit">{t('credentials.applyFilter')}</Button><Button type="button" tone="secondary" onClick={() => { setCustomerFilter(''); setAppliedCustomerFilter(undefined) }}>{t('credentials.clearFilter')}</Button></form>
      <p className="security-note">{t('credentials.filterHelp')}</p>
    </section>
    <DataState loading={resource.loading} error={resource.error} empty={resource.data?.length === 0} onRetry={resource.refresh}>
      <div className="table-wrap"><table><thead><tr><th>{t('credentials.name')}</th><th>{t('credentials.scope')}</th><th>{t('app.provider')}</th><th>{t('credentials.fingerprint')}</th><th>{t('common.status')}</th><th>{t('common.created')}</th><th>{t('common.actions')}</th></tr></thead><tbody>{resource.data?.map((profile) => <tr key={profile.id}><td>{profile.name} <small>v{profile.version}</small></td><td><code>{profile.owner_scope}</code></td><td><code>{profile.provider_environment}</code></td><td><code className="fingerprint">{profile.fingerprint}</code></td><td><Status value={profile.status} /></td><td>{formatDate(profile.created_at, locale)}</td><td><div className="actions actions--compact">{profile.status === 'active' && <DialogForm title={t('credentials.rotate')} trigger={t('credentials.rotate')} submitLabel={t('credentials.stageRotation')} onSubmit={(form) => rotate(profile, form)}><p className="form-wide security-note">{t('credentials.rotationHelp')}</p><SecretReferenceFields /></DialogForm>}{profile.status === 'retiring' && <DialogForm title={t('credentials.retire')} trigger={t('credentials.retire')} submitLabel={t('credentials.retire')} onSubmit={(form) => retire(profile, form)}><p className="form-wide security-note">{t('credentials.retireHelp')}</p><Field label={t('customers.reason')}><input name="reason" required maxLength={300} /></Field></DialogForm>}{profile.status !== 'active' && profile.status !== 'retiring' && '—'}</div></td></tr>)}</tbody></table></div>
    </DataState>
  </>
}

function SecretReferenceFields() {
  const { t } = useI18n()
  return <>
    <Field label={t('credentials.secretIdRef')}><input name="secret_id_ref" required pattern="env://WORKBENCH_PROVIDER_[A-Z0-9_]+" placeholder="env://WORKBENCH_PROVIDER_CUSTOMER_SECRET_ID" autoComplete="off" /></Field>
    <Field label={t('credentials.secretKeyRef')}><input name="secret_key_ref" required pattern="env://WORKBENCH_PROVIDER_[A-Z0-9_]+" placeholder="env://WORKBENCH_PROVIDER_CUSTOMER_SECRET_KEY" autoComplete="off" /></Field>
    <Field label={t('credentials.fingerprint')}><input name="fingerprint" required pattern="sha256:[a-f0-9]{64}" placeholder="sha256:…" /></Field>
  </>
}

function CredentialScopeFields() {
  const { t } = useI18n()
  const [scope, setScope] = useState('platform')
  return <>
    <Field label={t('credentials.scope')}><select name="scope" value={scope} onChange={(event) => setScope(event.target.value)}><option value="platform">{t('credentials.platformScope')}</option><option value="customer">{t('credentials.customerScope')}</option></select></Field>
    {scope === 'customer' && <Field label={t('credentials.customerId')}><input name="customer_id" type="number" min="1" required /></Field>}
    <p className="form-wide security-note">{scope === 'platform' ? t('credentials.platformScopeHelp') : t('credentials.customerScopeHelp')}</p>
  </>
}
