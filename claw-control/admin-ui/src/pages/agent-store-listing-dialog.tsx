import { type FormEvent, useMemo, useRef, useState } from 'react'

import { adminApi } from '../api/client'
import type { AgentStoreItem, CredentialProfile, Customer, UnifiedAgentStoreListingPreview } from '../api/contracts'
import { defaultLimits, LimitsFields, limitsFromForm } from '../components/LimitsFields'
import { Button, Field, Status, toNumber } from '../components/ui'
import { useI18n } from '../i18n'

const capabilities = ['chat', 'files', 'web_search', 'tools', 'connectors', 'oauth', 'scheduled_tasks', 'sandbox', 'catalog_models', 'catalog_skills', 'catalog_plugins']

export function AgentStoreListingDialog(props: {
  customers: Customer[]
  credentials: CredentialProfile[]
  credentialsLoading: boolean
  credentialsError?: unknown
  onPublished: (item: AgentStoreItem) => void
}) {
  const { t } = useI18n()
  const dialogRef = useRef<HTMLDialogElement>(null)
  const [audience, setAudience] = useState<'all_customers' | 'selected_customers'>('all_customers')
  const [preview, setPreview] = useState<UnifiedAgentStoreListingPreview>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const platformCredentials = useMemo(() => activePlatformCredentials(props.credentials), [props.credentials])
  const credentialsUnavailable = props.credentialsLoading || props.credentialsError !== undefined || platformCredentials.length === 0

  function open() {
    setPreview(undefined)
    setError(undefined)
    setAudience('all_customers')
    dialogRef.current?.showModal()
  }

  async function verify(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const formElement = event.currentTarget
    const form = new FormData(formElement)
    setBusy(true)
    setError(undefined)
    try {
      const result = await adminApi.verifyUnifiedAgentStoreListing({
        slug: String(form.get('slug') ?? '').trim().toLowerCase(),
        display_name: String(form.get('display_name') ?? '').trim(),
        summary: String(form.get('summary') ?? '').trim(),
        description: String(form.get('description') ?? '').trim(),
        avatar_url: String(form.get('avatar_url') ?? '').trim(),
        category: String(form.get('category') ?? '').trim().toLowerCase(),
        tags: Array.from(new Set(String(form.get('tags') ?? '').split(',').map((tag) => tag.trim().toLowerCase()).filter(Boolean))),
        sort_order: toNumber(form, 'sort_order'),
        featured: form.get('featured') === 'on',
        audience_scope: audience,
        selected_customer_ids: audience === 'selected_customers' ? form.getAll('selected_customer_ids').map(Number).filter((id) => Number.isInteger(id) && id > 0) : [],
        provider_environment: String(form.get('provider_environment') ?? 'china_tencent_adp') as 'china_tencent_cloud' | 'china_tencent_adp',
        region: String(form.get('region') ?? '').trim(),
        space_id: String(form.get('space_id') ?? '').trim(),
        app_id: String(form.get('app_id') ?? '').trim(),
        app_key: String(form.get('app_key') ?? ''),
        template_agent_id: String(form.get('template_agent_id') ?? '').trim(),
        credential_profile_id: toNumber(form, 'credential_profile_id'),
        limits: limitsFromForm(form),
        capabilities: capabilities.filter((capability) => form.getAll('capabilities').includes(capability)),
      })
      setPreview(result)
      if (result.verification.result !== 'verified') {
        setError(result.verification.error_message || result.verification.error_code || t('agentStore.verificationRejected'))
      }
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : t('common.error'))
    } finally {
      const appKeyInput = formElement.elements.namedItem('app_key')
      if (appKeyInput instanceof HTMLInputElement) appKeyInput.value = ''
      setBusy(false)
    }
  }

  async function publish() {
    const item = preview?.item
    const deployment = item?.deployments[0]
    if (!item || !deployment || preview.verification.result !== 'verified') return
    setBusy(true)
    setError(undefined)
    try {
      const published = await adminApi.publishUnifiedAgentStoreListing(item.item_id, item.row_version, deployment.row_version)
      dialogRef.current?.close()
      props.onPublished(published)
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : t('common.error'))
    } finally {
      setBusy(false)
    }
  }

  return <>
    <Button tone="secondary" onClick={open}>{t('agentStore.listApplication')}</Button>
    <dialog ref={dialogRef} className="dialog dialog--wide" onCancel={() => dialogRef.current?.close()}>
      <form onSubmit={verify}>
        <header><h2>{t('agentStore.listApplication')}</h2><Button type="button" tone="secondary" onClick={() => dialogRef.current?.close()} aria-label={t('action.close')}>×</Button></header>
        <div className="form-grid">
          <fieldset className="form-wide listing-inputs" disabled={preview?.verification.result === 'verified'}><div className="form-grid">
          <fieldset className="form-section form-wide"><legend>{t('agentStore.catalogInformation')}</legend>
            <div className="form-grid">
              <Field label={t('agentStore.slug')} hint={t('agentStore.slugHint')}><input name="slug" required minLength={3} maxLength={64} pattern="[a-z0-9][a-z0-9-]*[a-z0-9]" /></Field>
              <Field label={t('agentStore.name')}><input name="display_name" required maxLength={160} /></Field>
              <Field label={t('agentStore.summary')}><textarea name="summary" required maxLength={500} rows={3} /></Field>
              <Field label={t('agentStore.description')}><textarea name="description" maxLength={20000} rows={5} /></Field>
              <Field label={t('agentStore.avatar')}><input name="avatar_url" type="url" maxLength={1024} /></Field>
              <Field label={t('agentStore.category')}><input name="category" required maxLength={96} /></Field>
              <Field label={t('agentStore.tags')} hint={t('agentStore.tagsHint')}><input name="tags" maxLength={512} /></Field>
              <Field label={t('agentStore.sortOrder')}><input name="sort_order" type="number" defaultValue="0" /></Field>
              <label className="check-field"><input name="featured" type="checkbox" /> {t('agentStore.featured')}</label>
            </div>
          </fieldset>
          <fieldset className="form-section form-wide"><legend>{t('agentStore.adpConfiguration')}</legend><div className="form-grid">
            <Field label={t('app.provider')}><select name="provider_environment" defaultValue="china_tencent_adp"><option value="china_tencent_adp">china_tencent_adp</option><option value="china_tencent_cloud">china_tencent_cloud</option></select></Field>
            <Field label={t('app.region')}><input name="region" required defaultValue="ap-guangzhou" /></Field>
            <Field label={t('app.spaceId')}><input name="space_id" required defaultValue="default_space" /></Field>
            <Field label={t('app.appId')}><input name="app_id" required maxLength={128} /></Field>
            <Field label={t('app.templateAgentId')} hint={t('app.templateAgentHint')}><input name="template_agent_id" /></Field>
            <Field label={t('app.credentialProfile')} hint={t('agentStore.platformCredentialHint')}><select name="credential_profile_id" required defaultValue="" disabled={credentialsUnavailable}><option value="" disabled>{props.credentialsLoading ? t('common.loading') : t('agentStore.choosePlatformCredential')}</option>{platformCredentials.map((profile) => <option key={profile.id} value={profile.id}>{profile.name} · {profile.provider_environment}</option>)}</select></Field>
            <Field label={t('app.appKey')} hint={t('agentStore.appKeyWriteOnlyHint')}><input name="app_key" type="password" required minLength={16} maxLength={4096} autoComplete="new-password" /></Field>
          </div></fieldset>
          {props.credentialsLoading && <p className="form-wide security-note" role="status">{t('agentStore.loadingPlatformCredentials')}</p>}
          {props.credentialsError !== undefined && <p className="form-wide inline-error" role="alert">{t('agentStore.platformCredentialLoadFailed')} <a href="#/credentials">{t('agentStore.openCredentialSettings')}</a></p>}
          {!props.credentialsLoading && props.credentialsError === undefined && platformCredentials.length === 0 && <p className="form-wide inline-error" role="alert">{t('agentStore.platformCredentialRequired')} <a href="#/credentials">{t('agentStore.openCredentialSettings')}</a></p>}
          <fieldset className="form-section form-wide"><legend>{t('agentStore.audience')}</legend>
            <label className="check-field"><input type="radio" name="audience_scope" checked={audience === 'all_customers'} onChange={() => setAudience('all_customers')} /> {t('agentStore.allCustomers')}</label>
            <p className="security-note">{t('agentStore.allCustomersHint')}</p>
            <label className="check-field"><input type="radio" name="audience_scope" checked={audience === 'selected_customers'} onChange={() => setAudience('selected_customers')} /> {t('agentStore.selectedCustomers')}</label>
            {audience === 'selected_customers' && <div className="checkbox-grid">{props.customers.map((customer) => <label key={customer.id}><input type="checkbox" name="selected_customer_ids" value={customer.id} /> {customer.display_name} ({customer.customer_code})</label>)}</div>}
          </fieldset>
          <fieldset className="form-section"><legend>{t('app.capabilities')}</legend><div className="checkbox-grid">{capabilities.map((capability) => <label key={capability}><input type="checkbox" name="capabilities" value={capability} defaultChecked={capability === 'chat' || capability === 'files'} /> {capability}</label>)}</div></fieldset>
          <LimitsFields values={defaultLimits} />
          </div></fieldset>
          {preview && <section className="form-section form-wide verification-preview" aria-live="polite"><h3>{t('agentStore.verificationPreview')}</h3><dl className="definition-grid">
            <div><dt>{t('common.status')}</dt><dd><Status value={preview.verification.result} /></dd></div>
            <div><dt>{t('agentStore.appMode')}</dt><dd>{preview.verification.app_mode ?? '-'}</dd></div>
            <div><dt>{t('agentStore.runtime')}</dt><dd>{preview.item?.deployments[0]?.runtime_profile || '-'}</dd></div>
            <div><dt>{t('agentStore.providerReleaseStatus')}</dt><dd>{preview.verification.release_status || '-'}</dd></div>
            <div><dt>{t('agentStore.providerApplicationName')}</dt><dd>{preview.item?.deployments[0]?.provider_display_name || '-'}</dd></div>
          </dl></section>}
        </div>
        {error && <output className="form-error" aria-live="polite">{error}</output>}
        <footer><Button type="button" tone="secondary" onClick={() => dialogRef.current?.close()}>{t('action.cancel')}</Button>{!preview || preview.verification.result !== 'verified' ? <Button type="submit" disabled={busy || credentialsUnavailable} aria-busy={busy || props.credentialsLoading}>{busy || props.credentialsLoading ? t('common.loading') : t('agentStore.verifyConfiguration')}</Button> : <Button type="button" disabled={busy} aria-busy={busy} onClick={() => void publish()}>{busy ? t('common.loading') : t('agentStore.confirmListing')}</Button>}</footer>
      </form>
    </dialog>
  </>
}

export function activePlatformCredentials(credentials: CredentialProfile[]): CredentialProfile[] {
  return credentials.filter((profile) => profile.owner_scope === 'platform' && profile.status === 'active')
}
