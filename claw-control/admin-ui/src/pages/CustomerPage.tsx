import { useCallback, useState } from 'react'

import { adminApi } from '../api/client'
import type { AppConfigInput, CustomerDetail, CustomerMember, CustomerRole } from '../api/contracts'
import { LimitsFields, defaultLimits, limitsFromForm } from '../components/LimitsFields'
import { Button, DataState, DialogForm, Field, PageHeader, Status, formatDate, toISO, toNumber } from '../components/ui'
import { useResource } from '../hooks/useResource'
import { useI18n } from '../i18n'

const capabilities = ['chat', 'files', 'web_search', 'tools', 'connectors', 'oauth', 'scheduled_tasks', 'sandbox', 'catalog_models', 'catalog_skills', 'catalog_plugins']

export default function CustomerPage({ customerId }: { customerId: number }) {
  const { locale, t } = useI18n()
  const load = useCallback(async (signal: AbortSignal) => {
    const [detail, credentials, plans, invoices, margin] = await Promise.all([
      adminApi.customer(customerId, signal),
      adminApi.credentials(signal, customerId),
      adminApi.plans(signal),
      adminApi.invoices(customerId, signal),
	  adminApi.marginReport(customerId, signal),
    ])
    return { detail, credentials, plans, invoices, margin }
  }, [customerId])
  const resource = useResource(load)
  const detail = resource.data?.detail

  async function addMember(form: FormData) {
    await adminApi.addMember(customerId, { new_api_user_id: toNumber(form, 'new_api_user_id'), role: roleFromForm(form, t('customers.invalidRole')) })
    resource.refresh()
  }

  async function updateCustomer(form: FormData) {
    await adminApi.updateCustomer(customerId, {
      expected_version: toNumber(form, 'expected_version'),
      display_name: String(form.get('display_name') ?? '').trim(),
      billing_user_id: String(form.get('billing_user_id') ?? '').trim() === '' ? null : toNumber(form, 'billing_user_id'),
    })
    resource.refresh()
  }

  async function updateMember(form: FormData) {
    const userId = toNumber(form, 'new_api_user_id')
    await adminApi.updateMember(customerId, userId, {
      role: roleFromForm(form, t('customers.invalidRole')),
      expected_auth_epoch: toNumber(form, 'expected_auth_epoch'),
    })
    resource.refresh()
  }

  async function disableMember(form: FormData) {
    await adminApi.disableMember(customerId, toNumber(form, 'new_api_user_id'), String(form.get('reason') ?? '').trim())
    resource.refresh()
  }

  async function saveApp(form: FormData) {
    const input: AppConfigInput = {
      expected_version: toNumber(form, 'expected_version'),
      provider_environment: String(form.get('provider_environment') ?? ''),
      region: String(form.get('region') ?? '').trim(),
      space_id: String(form.get('space_id') ?? '').trim(),
      app_id: String(form.get('app_id') ?? '').trim(),
      template_agent_id: String(form.get('template_agent_id') ?? '').trim(),
      credential_profile_id: toNumber(form, 'credential_profile_id'),
      app_key_secret_ref: String(form.get('app_key_secret_ref') ?? '').trim(),
      app_key_fingerprint: String(form.get('app_key_fingerprint') ?? '').trim(),
      display_name: String(form.get('display_name') ?? '').trim(),
      limits: limitsFromForm(form),
      capabilities: capabilities.filter((capability) => form.getAll('capabilities').includes(capability)),
    }
    await adminApi.saveApp(customerId, input)
    resource.refresh()
  }

  async function verify(form: FormData) {
    await adminApi.verifyApp(customerId, {
      expected_version: toNumber(form, 'expected_version'),
      config_version: toNumber(form, 'config_version'),
    })
    resource.refresh()
  }

  async function requestCredentialChange(form: FormData) {
    await adminApi.requestApproval({
      action_type: 'app_credential_change',
      customer_id: customerId,
      resource_id: toNumber(form, 'pending_config_id'),
      request_key: String(form.get('request_key') ?? '').trim(),
      reason: String(form.get('reason') ?? '').trim(),
      ttl_seconds: toNumber(form, 'ttl_seconds'),
    })
    resource.refresh()
  }

  async function transition(form: FormData) {
    await adminApi.transitionApp(customerId, String(form.get('action') ?? ''), { expected_version: toNumber(form, 'expected_version'), reason: String(form.get('reason') ?? '').trim() })
    resource.refresh()
  }

  async function createPeriod(form: FormData) {
    await adminApi.createPeriod(customerId, {
      plan_version_id: toNumber(form, 'plan_version_id'),
      period_start: toISO(form.get('period_start')),
      period_end: toISO(form.get('period_end')),
    })
    resource.refresh()
  }

  async function confirmPayment(form: FormData) {
    await adminApi.confirmPayment(toNumber(form, 'period_id'), { expected_version: toNumber(form, 'expected_version'), payment_evidence_ref: String(form.get('payment_evidence_ref') ?? '').trim() })
    resource.refresh()
  }

  async function cancelPeriod(form: FormData) {
    await adminApi.cancelPeriod(toNumber(form, 'period_id'), {
      expected_version: toNumber(form, 'expected_version'),
      reason: String(form.get('reason') ?? '').trim(),
    })
    resource.refresh()
  }

  async function voidInvoice(form: FormData) {
    await adminApi.voidInvoice(toNumber(form, 'invoice_id'), {
      reason: String(form.get('reason') ?? '').trim(),
    })
    resource.refresh()
  }

  return <>
    <a className="back-link" href="#/customers">← {t('action.back')}</a>
    <PageHeader title={detail?.customer.display_name ?? t('common.details')} actions={<Button tone="secondary" onClick={resource.refresh}>{t('action.refresh')}</Button>} />
    <DataState loading={resource.loading} error={resource.error} onRetry={resource.refresh}>
      {detail && <CustomerSections
        detail={detail}
        credentials={resource.data?.credentials ?? []}
        plans={resource.data?.plans ?? []}
        invoices={resource.data?.invoices ?? []}
		margin={resource.data?.margin}
        locale={locale}
        handlers={{ updateCustomer, addMember, updateMember, disableMember, saveApp, requestCredentialChange, verify, transition, createPeriod, confirmPayment, cancelPeriod, voidInvoice }}
      />}
    </DataState>
  </>
}

interface SectionsProps {
  detail: CustomerDetail
  credentials: Awaited<ReturnType<typeof adminApi.credentials>>
  plans: Awaited<ReturnType<typeof adminApi.plans>>
  invoices: Awaited<ReturnType<typeof adminApi.invoices>>
	margin?: Awaited<ReturnType<typeof adminApi.marginReport>>
  locale: string
  handlers: {
    updateCustomer: (form: FormData) => Promise<void>
    addMember: (form: FormData) => Promise<void>
    updateMember: (form: FormData) => Promise<void>
    disableMember: (form: FormData) => Promise<void>
    saveApp: (form: FormData) => Promise<void>
    requestCredentialChange: (form: FormData) => Promise<void>
    verify: (form: FormData) => Promise<void>
    transition: (form: FormData) => Promise<void>
    createPeriod: (form: FormData) => Promise<void>
    confirmPayment: (form: FormData) => Promise<void>
    cancelPeriod: (form: FormData) => Promise<void>
    voidInvoice: (form: FormData) => Promise<void>
  }
}

function CustomerSections({ detail, credentials, plans, invoices, margin, locale, handlers }: SectionsProps) {
  const { t } = useI18n()
  const app = detail.app
  const config = detail.current_config
  const pendingConfig = detail.pending_config
  const credentialChange = Boolean(config?.credential_profile_id && pendingConfig?.credential_profile_id && config.credential_profile_id !== pendingConfig.credential_profile_id)
  return <div className="stack">
    <section className="panel">
      <div className="section-title"><h2>{t('common.details')}</h2><div className="actions"><Status value={detail.customer.status} /><DialogForm title={t('customers.edit')} trigger={t('action.edit')} submitLabel={t('action.save')} onSubmit={handlers.updateCustomer}><Field label={t('app.expectedVersion')}><input name="expected_version" type="number" min="1" required defaultValue={detail.customer.row_version} /></Field><Field label={t('customers.name')}><input name="display_name" required maxLength={160} defaultValue={detail.customer.display_name} /></Field><Field label={t('customers.billingUser')}><input name="billing_user_id" type="number" min="1" defaultValue={detail.customer.billing_user_id} /></Field></DialogForm></div></div>
      <dl className="definition-grid"><div><dt>{t('customers.code')}</dt><dd><code>{detail.customer.customer_code}</code></dd></div><div><dt>{t('customers.billingUser')}</dt><dd>{detail.customer.billing_user_id ?? '—'}</dd></div><div><dt>{t('common.created')}</dt><dd>{formatDate(detail.customer.created_at, locale)}</dd></div></dl>
    </section>

    <section className="panel"><div className="section-title"><h2>{t('customers.members')}</h2><div className="actions"><DialogForm title={t('customers.addMember')} trigger={t('customers.addMember')} submitLabel={t('action.create')} onSubmit={handlers.addMember}><Field label={t('customers.userId')}><input name="new_api_user_id" type="number" min="1" required /></Field><Field label={t('customers.role')}><MemberRoleSelect /></Field></DialogForm><DialogForm title={t('customers.updateRole')} trigger={t('customers.updateRole')} submitLabel={t('action.save')} onSubmit={handlers.updateMember}><MemberRoleFields members={detail.members.filter((member) => member.status !== 'disabled')} /></DialogForm><DialogForm title={t('action.disable')} trigger={t('action.disable')} submitLabel={t('action.disable')} onSubmit={handlers.disableMember}><p className="form-wide">{t('customers.identityWarning')}</p><Field label={t('customers.userId')}><select name="new_api_user_id" required>{detail.members.filter((member) => member.status !== 'disabled').map((member) => <option key={member.id} value={member.new_api_user_id}>{member.new_api_user_id}</option>)}</select></Field><Field label={t('customers.reason')}><input name="reason" required maxLength={300} /></Field></DialogForm></div></div>
      {!detail.members.length ? <p>{t('common.empty')}</p> : <div className="table-wrap"><table><thead><tr><th>{t('customers.userId')}</th><th>{t('customers.role')}</th><th>{t('common.status')}</th><th>{t('customers.authEpoch')}</th></tr></thead><tbody>{detail.members.map((member) => <tr key={member.id}><td>{member.new_api_user_id}</td><td>{member.role}</td><td><Status value={member.status} /></td><td>{member.auth_epoch}</td></tr>)}</tbody></table></div>}
    </section>

    <section className="panel"><div className="section-title"><h2>{t('app.title')}</h2>{app && <Status value={app.status} />}</div>
      {!app && <p>{t('app.none')}</p>}
      {app && <dl className="definition-grid"><div><dt>{t('app.appId')}</dt><dd><code>{app.app_id}</code></dd></div><div><dt>{t('app.expectedVersion')}</dt><dd>{app.row_version}</dd></div><div><dt>{t('app.currentConfig')}</dt><dd>{config?.config_version ?? '—'}</dd></div><div><dt>{t('app.pendingConfig')}</dt><dd>{pendingConfig?.config_version ?? '—'}</dd></div></dl>}
      <div className="actions section-actions"><DialogForm title={t('app.title')} trigger={t('action.save')} submitLabel={t('action.save')} onSubmit={handlers.saveApp}><AppConfigFields app={app} config={config} credentials={credentials} /></DialogForm>{credentialChange && pendingConfig && <DialogForm title={t('app.credentialChangeApproval')} trigger={t('app.requestCredentialChangeApproval')} submitLabel={t('governance.request')} onSubmit={handlers.requestCredentialChange}><p className="form-wide security-note">{t('app.credentialChangeApprovalHelp')}</p><input type="hidden" name="pending_config_id" value={pendingConfig.id} /><Field label={t('governance.requestKey')}><input name="request_key" required maxLength={128} /></Field><Field label={t('governance.reason')}><input name="reason" required maxLength={1000} /></Field><Field label={t('governance.ttl')}><input name="ttl_seconds" type="number" min="300" max="86400" required defaultValue="3600" /></Field></DialogForm>}<DialogForm title={t('app.verification')} trigger={t('action.verify')} submitLabel={t('action.verify')} onSubmit={handlers.verify}><VerificationFields app={app} pendingConfig={pendingConfig} /></DialogForm><DialogForm title={t('common.actions')} trigger={t('common.actions')} submitLabel={t('action.save')} onSubmit={handlers.transition}><Field label={t('common.actions')}><select name="action" defaultValue="enable"><option value="enable">{t('action.enable')}</option><option value="suspend">{t('action.suspend')}</option><option value="disable">{t('action.disable')}</option><option value="prepare">{t('action.prepare')}</option></select></Field><Field label={t('app.expectedVersion')}><input name="expected_version" type="number" min="1" required defaultValue={app?.row_version ?? 1} /></Field><Field label={t('app.transitionReason')}><input name="reason" maxLength={300} /></Field></DialogForm></div>
    </section>

    <section className="panel"><div className="section-title"><h2>{t('periods.title')}</h2><div className="actions"><DialogForm title={t('periods.create')} trigger={t('periods.create')} submitLabel={t('action.create')} onSubmit={handlers.createPeriod}><Field label={t('periods.planVersion')}><select name="plan_version_id" required>{plans.map((plan) => <option key={plan.id} value={plan.id}>{plan.display_name} · ¥{plan.monthly_price_cny}</option>)}</select></Field><Field label={t('periods.start')} hint={t('common.beijingTime')}><input name="period_start" type="datetime-local" required /></Field><Field label={t('periods.end')} hint={t('common.beijingTime')}><input name="period_end" type="datetime-local" required /></Field></DialogForm><DialogForm title={t('action.confirmPayment')} trigger={t('action.confirmPayment')} submitLabel={t('action.confirmPayment')} onSubmit={handlers.confirmPayment}><PaymentFields periods={detail.plan_periods.filter((period) => period.payment_status !== 'paid' && period.status !== 'canceled')} /></DialogForm><DialogForm title={t('periods.cancel')} trigger={t('periods.cancel')} submitLabel={t('action.cancel')} onSubmit={handlers.cancelPeriod}><CancelPeriodFields periods={detail.plan_periods.filter((period) => period.status !== 'canceled' && period.status !== 'expired')} /></DialogForm></div></div>
      {!detail.plan_periods.length ? <p>{t('common.empty')}</p> : <div className="table-wrap"><table><thead><tr><th>ID</th><th>{t('periods.start')}</th><th>{t('periods.end')}</th><th>{t('invoices.amount')}</th><th>{t('common.status')}</th></tr></thead><tbody>{detail.plan_periods.map((period) => <tr key={period.id}><td>{period.id}</td><td>{formatDate(period.start_at, locale)}</td><td>{formatDate(period.end_at, locale)}</td><td>¥{period.amount_cny}</td><td><Status value={`${period.status} / ${period.payment_status}`} /></td></tr>)}</tbody></table></div>}
    </section>

    <section className="panel"><div className="section-title"><h2>{t('invoices.title')}</h2><DialogForm title={t('invoices.void')} trigger={t('invoices.void')} submitLabel={t('invoices.void')} onSubmit={handlers.voidInvoice}><InvoiceVoidFields invoices={invoices.filter((invoice) => invoice.status === 'paid')} /></DialogForm></div>{!invoices.length ? <p>{t('common.empty')}</p> : <div className="table-wrap"><table><thead><tr><th>{t('invoices.number')}</th><th>{t('invoices.amount')}</th><th>{t('common.status')}</th><th>{t('common.created')}</th></tr></thead><tbody>{invoices.map((invoice) => <tr key={invoice.id}><td><code>{invoice.invoice_number}</code></td><td>¥{invoice.amount_cny}</td><td><Status value={invoice.status} /></td><td>{formatDate(invoice.issued_at, locale)}</td></tr>)}</tbody></table></div>}</section>
	{margin && <section className="panel"><div className="section-title"><h2>{t('margin.title')}</h2><Status value={margin.confidence} /></div><dl className="definition-grid"><div><dt>{t('margin.revenue')}</dt><dd>¥{margin.invoiced_revenue_cny}</dd></div><div><dt>{t('margin.cost')}</dt><dd>¥{margin.reviewed_cost_cny}</dd></div><div><dt>{t('margin.margin')}</dt><dd>¥{margin.margin_cny}</dd></div><div><dt>{t('margin.unverified')}</dt><dd>¥{margin.breakdown.unverified_cost_cny}</dd></div></dl><p className="security-note">{t('margin.customerBoundary')}</p></section>}
  </div>
}

function AppConfigFields({ app, config, credentials }: { app?: CustomerDetail['app']; config?: CustomerDetail['current_config']; credentials: SectionsProps['credentials'] }) {
  const { t } = useI18n()
  const configuredCapabilities = config?.capabilities ?? ['chat', 'files']
  return <>
    <Field label={t('app.expectedVersion')}><input name="expected_version" type="number" min="0" required defaultValue={app?.row_version ?? 0} /></Field>
    <Field label={t('app.provider')}><select name="provider_environment" defaultValue={app?.provider_environment ?? 'china_tencent_cloud'}><option value="china_tencent_cloud">china_tencent_cloud</option><option value="china_tencent_adp">china_tencent_adp</option></select></Field>
    <Field label={t('app.region')}><input name="region" required defaultValue={config?.region ?? 'ap-guangzhou'} /></Field>
    <Field label={t('app.spaceId')}><input name="space_id" required defaultValue={config?.space_id ?? ''} /></Field>
    <Field label={t('app.appId')}><input name="app_id" required defaultValue={app?.app_id ?? ''} /></Field>
    <Field label={t('app.templateAgentId')} hint={t('app.templateAgentHint')}><input name="template_agent_id" defaultValue={config?.template_agent_id ?? ''} /></Field>
    <Field label={t('app.credentialProfile')}><select name="credential_profile_id" required defaultValue={config?.credential_profile_id}>{credentials.map((profile) => <option key={profile.id} value={profile.id}>{profile.name} · {profile.owner_scope} · {profile.provider_environment}</option>)}</select></Field>
    <p className="form-wide security-note">{t('app.additionalCredentialConstraint')}</p>
    <Field label={t('app.secretRef')}><input name="app_key_secret_ref" required pattern="env://WORKBENCH_PROVIDER_[A-Z0-9_]+" placeholder="env://WORKBENCH_PROVIDER_CUSTOMER_APP_KEY" autoComplete="off" /></Field>
    <Field label={t('app.fingerprint')}><input name="app_key_fingerprint" required pattern="sha256:[a-f0-9]{64}" placeholder="sha256:…" /></Field>
    <Field label={t('app.displayName')}><input name="display_name" required defaultValue={app?.display_name ?? ''} /></Field>
    <fieldset className="form-section"><legend>{t('app.capabilities')}</legend><div className="checkbox-grid">{capabilities.map((capability) => <label key={capability}><input type="checkbox" name="capabilities" value={capability} defaultChecked={configuredCapabilities.includes(capability)} /> {capability}</label>)}</div></fieldset>
    <LimitsFields values={config?.limits ?? defaultLimits} />
  </>
}

function VerificationFields({ app, pendingConfig }: { app?: CustomerDetail['app']; pendingConfig?: CustomerDetail['pending_config'] }) {
  const { t } = useI18n()
  return <>
    <p className="form-wide security-note">{t('app.verificationHelp')}</p>
    <Field label={t('app.expectedVersion')}><input name="expected_version" type="number" min="1" required defaultValue={app?.row_version ?? 1} /></Field>
    <Field label={t('app.configVersion')}><input name="config_version" type="number" min="1" required readOnly value={pendingConfig?.config_version ?? ''} /></Field>
  </>
}

function MemberRoleSelect() {
  return <select name="role" defaultValue="member"><option value="owner">owner</option><option value="admin">admin</option><option value="member">member</option><option value="viewer">viewer</option></select>
}

function roleFromForm(form: FormData, invalidMessage: string): CustomerRole {
  const role = String(form.get('role') ?? '')
  if (role !== 'owner' && role !== 'admin' && role !== 'member' && role !== 'viewer') throw new Error(invalidMessage)
  return role
}

function MemberRoleFields({ members }: { members: CustomerMember[] }) {
  const { t } = useI18n()
  const [selectedUserId, setSelectedUserId] = useState(() => members[0]?.new_api_user_id ?? 0)
  const selected = members.find((member) => member.new_api_user_id === selectedUserId)
  return <>
    <Field label={t('customers.userId')}><select name="new_api_user_id" required value={selectedUserId || ''} onChange={(event) => setSelectedUserId(Number(event.target.value))}>{members.map((member) => <option key={member.id} value={member.new_api_user_id}>{member.new_api_user_id} · {member.role}</option>)}</select></Field>
    <Field label={t('customers.role')}><MemberRoleSelect /></Field>
    <Field label={t('customers.expectedAuthEpoch')}><input name="expected_auth_epoch" type="number" min="1" required readOnly value={selected?.auth_epoch ?? ''} /></Field>
  </>
}

function PaymentFields({ periods }: { periods: CustomerDetail['plan_periods'] }) {
  const { t } = useI18n()
  const [selectedPeriodId, setSelectedPeriodId] = useState(() => periods[0]?.id ?? 0)
  const selected = periods.find((period) => period.id === selectedPeriodId)
  return <>
    <Field label={t('periods.title')}><select name="period_id" required value={selectedPeriodId || ''} onChange={(event) => setSelectedPeriodId(Number(event.target.value))}>{periods.map((period) => <option key={period.id} value={period.id}>{period.id} · {period.start_at.slice(0, 10)}</option>)}</select></Field>
    <Field label={t('app.expectedVersion')}><input name="expected_version" type="number" min="1" required readOnly value={selected?.row_version ?? ''} /></Field>
    <Field label={t('periods.paymentEvidence')}><input name="payment_evidence_ref" required maxLength={512} /></Field>
  </>
}

function CancelPeriodFields({ periods }: { periods: CustomerDetail['plan_periods'] }) {
  const { t } = useI18n()
  const [selectedPeriodId, setSelectedPeriodId] = useState(() => periods[0]?.id ?? 0)
  const selected = periods.find((period) => period.id === selectedPeriodId)
  return <>
    <p className="form-wide security-note">{t('periods.cancelHelp')}</p>
    <Field label={t('periods.title')}><select name="period_id" required value={selectedPeriodId || ''} onChange={(event) => setSelectedPeriodId(Number(event.target.value))}>{periods.map((period) => <option key={period.id} value={period.id}>{period.id} · {period.start_at.slice(0, 10)}</option>)}</select></Field>
    <Field label={t('app.expectedVersion')}><input name="expected_version" type="number" min="1" required readOnly value={selected?.row_version ?? ''} /></Field>
    <Field label={t('customers.reason')}><input name="reason" required maxLength={1000} /></Field>
  </>
}

function InvoiceVoidFields({ invoices }: { invoices: SectionsProps['invoices'] }) {
  const { t } = useI18n()
  return <>
    <p className="form-wide security-note">{t('invoices.voidHelp')}</p>
    <Field label={t('invoices.title')}><select name="invoice_id" required>{invoices.map((invoice) => <option key={invoice.id} value={invoice.id}>{invoice.invoice_number} · ¥{invoice.amount_cny}</option>)}</select></Field>
    <Field label={t('customers.reason')}><input name="reason" required maxLength={1000} /></Field>
  </>
}
