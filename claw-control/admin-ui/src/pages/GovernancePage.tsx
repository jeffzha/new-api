import { useCallback, useEffect, useMemo, useState, type FormEvent } from 'react'

import { adminApi, ApiError } from '../api/client'
import type {
  AppConfigInput,
  AppDraftResult,
  ApprovalAction,
  CredentialProfile,
  Customer,
  CustomerApp,
  EvidenceMetadata,
  GovernanceApproval,
  RetentionPolicy,
  RetentionRunView,
} from '../api/contracts'
import { LimitsFields, limitsFromForm } from '../components/LimitsFields'
import { Button, DataState, DialogForm, Field, PageHeader, Status, formatDate, toBeijingDateTimeLocal, toISO, toNumber, toOptionalNumber } from '../components/ui'
import { useResource } from '../hooks/useResource'
import { useI18n } from '../i18n'

const capabilities = ['chat', 'files', 'web_search', 'tools', 'connectors', 'oauth', 'scheduled_tasks', 'sandbox', 'catalog_models', 'catalog_skills', 'catalog_plugins']

export default function GovernancePage() {
  const { t } = useI18n()
  const load = useCallback(async (signal: AbortSignal) => {
    const [customers, credentials, evidence, notifications, approvals] = await Promise.all([
      adminApi.customers(signal),
      adminApi.credentials(signal),
      adminApi.evidence(signal),
      adminApi.notifications(signal),
      adminApi.approvals(undefined, signal),
    ])
    return { customers, credentials, evidence, notifications, approvals }
  }, [])
  const resource = useResource(load)

  return <>
    <PageHeader title={t('governance.title')} actions={<Button tone="secondary" onClick={resource.refresh}>{t('action.refresh')}</Button>} />
    <DataState loading={resource.loading} error={resource.error} onRetry={resource.refresh}>
      {resource.data && <div className="stack">
        <NotificationsPanel notifications={resource.data.notifications} onChanged={resource.refresh} />
        <ApprovalsPanel approvals={resource.data.approvals} customers={resource.data.customers} onChanged={resource.refresh} />
        <AuditExportPanel customers={resource.data.customers} />
        <RetentionPanel customers={resource.data.customers} />
        <MigrationPanel customers={resource.data.customers} credentials={resource.data.credentials} evidence={resource.data.evidence} onChanged={resource.refresh} />
        <CredentialGovernancePanel credentials={resource.data.credentials} onChanged={resource.refresh} />
      </div>}
    </DataState>
  </>
}

function NotificationsPanel({ notifications, onChanged }: {
  notifications: Awaited<ReturnType<typeof adminApi.notifications>>
  onChanged: () => void
}) {
  const { locale, t } = useI18n()
  const [error, setError] = useState<unknown>()
  async function markRead(notificationId: string) {
    setError(undefined)
    try {
      await adminApi.markNotificationRead(notificationId)
      onChanged()
    } catch (cause) {
      setError(cause)
    }
  }
  return <section className="panel" aria-labelledby="governance-notifications">
    <div className="section-title"><h2 id="governance-notifications">{t('governance.notifications')}</h2><span className="count-badge">{notifications.filter((item) => item.status === 'unread').length} {t('governance.unread')}</span></div>
    {error !== undefined && <InlineError error={error} />}
    {!notifications.length ? <p className="empty-inline">{t('governance.noNotifications')}</p> : <div className="table-wrap"><table><thead><tr><th>{t('common.created')}</th><th>{t('governance.notification')}</th><th>{t('governance.customer')}</th><th>{t('common.status')}</th><th>{t('common.actions')}</th></tr></thead><tbody>{notifications.map((item) => <tr key={item.notification_id}><td>{formatDate(item.occurred_at, locale)}</td><td><strong>{item.title}</strong><br /><small>{item.message}</small><br /><code>{item.resource_type}/{item.resource_id}</code></td><td>{item.customer_id}</td><td><Status value={item.status} /></td><td>{item.status === 'unread' ? <Button tone="secondary" onClick={() => markRead(item.notification_id)}>{t('governance.markRead')}</Button> : '—'}</td></tr>)}</tbody></table></div>}
  </section>
}

function ApprovalsPanel({ approvals, customers, onChanged }: {
  approvals: GovernanceApproval[]
  customers: Customer[]
  onChanged: () => void
}) {
  const { locale, t } = useI18n()
  async function decide(item: GovernanceApproval, action: 'approve' | 'reject' | 'execute', form: FormData) {
    await adminApi.decideApproval(item.approval_id, action, {
      expected_version: item.row_version,
      reason: String(form.get('reason') ?? '').trim() || undefined,
    })
    onChanged()
  }
  async function requestDisable(form: FormData) {
    await adminApi.requestApproval({
      action_type: 'app_disable',
      customer_id: toNumber(form, 'customer_id'),
      request_key: String(form.get('request_key') ?? '').trim(),
      reason: String(form.get('reason') ?? '').trim(),
      ttl_seconds: toNumber(form, 'ttl_seconds', 900),
    })
    onChanged()
  }
  return <section className="panel" aria-labelledby="governance-approvals">
    <div className="section-title"><h2 id="governance-approvals">{t('governance.approvals')}</h2>{customers.length > 0 && <DialogForm title={t('governance.requestDisable')} trigger={t('governance.requestDisable')} submitLabel={t('governance.request')} onSubmit={requestDisable}><Field label={t('governance.customer')}><select name="customer_id" required>{customers.map((customer) => <option key={customer.id} value={customer.id}>{customer.display_name} · {customer.customer_code}</option>)}</select></Field><ApprovalRequestFields /></DialogForm>}</div>
    <p className="security-note">{t('governance.twoPerson')}</p>
    {!approvals.length ? <p className="empty-inline">{t('governance.noApprovals')}</p> : <div className="table-wrap"><table><thead><tr><th>{t('common.created')}</th><th>{t('governance.actionType')}</th><th>{t('governance.requestedBy')}</th><th>{t('governance.expires')}</th><th>{t('common.status')}</th><th>{t('common.actions')}</th></tr></thead><tbody>{approvals.map((item) => <tr key={item.approval_id}><td>{formatDate(item.created_at, locale)}<br /><code>{item.approval_id}</code></td><td><code>{item.action_type}</code><br /><small>{item.reason}</small></td><td>{item.requested_by}{item.approved_by && <><br /><small>{t('governance.approvedBy')}: {item.approved_by}</small></>}</td><td>{formatDate(item.expires_at, locale)}</td><td><Status value={item.status} /></td><td><div className="actions actions--compact">{item.status === 'pending' && <><DecisionForm item={item} action="approve" label={t('governance.approve')} onSubmit={decide} /><DecisionForm item={item} action="reject" label={t('governance.reject')} onSubmit={decide} requireReason /></>}{item.status === 'approved' && <DecisionForm item={item} action="execute" label={t('governance.execute')} onSubmit={decide} />}{!['pending', 'approved'].includes(item.status) && '—'}</div></td></tr>)}</tbody></table></div>}
  </section>
}

function DecisionForm({ item, action, label, onSubmit, requireReason = false }: {
  item: GovernanceApproval
  action: 'approve' | 'reject' | 'execute'
  label: string
  requireReason?: boolean
  onSubmit: (item: GovernanceApproval, action: 'approve' | 'reject' | 'execute', form: FormData) => Promise<void>
}) {
  const { t } = useI18n()
  return <DialogForm title={label} trigger={label} submitLabel={label} onSubmit={(form) => onSubmit(item, action, form)}><p className="form-wide">{t('governance.approvalId')}: <code>{item.approval_id}</code><br />{t('governance.expectedVersion')}: {item.row_version}</p><Field label={t('governance.decisionReason')}><textarea name="reason" rows={3} required={requireReason} maxLength={1000} /></Field></DialogForm>
}

function ApprovalRequestFields() {
  const { t } = useI18n()
  return <>
    <Field label={t('governance.requestKey')} hint={t('governance.requestKeyHint')}><input name="request_key" required maxLength={128} /></Field>
    <Field label={t('governance.reason')}><textarea name="reason" rows={3} required maxLength={1000} /></Field>
    <Field label={t('governance.ttl')}><input name="ttl_seconds" type="number" min="300" max="86400" step="60" required defaultValue="900" /></Field>
  </>
}

function AuditExportPanel({ customers }: { customers: Customer[] }) {
  const { t } = useI18n()
  const [message, setMessage] = useState('')
  const [error, setError] = useState<unknown>()
  const [busy, setBusy] = useState(false)
  const now = useMemo(() => new Date(), [])
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError(undefined)
    setMessage('')
    const form = new FormData(event.currentTarget)
    try {
      await adminApi.downloadAuditExport({
        format: String(form.get('format')) === 'json' ? 'json' : 'csv',
        start: toISO(form.get('start')) ?? '',
        end: toISO(form.get('end')) ?? '',
        customer_id: toOptionalNumber(form, 'customer_id'),
        limit: toNumber(form, 'limit', 100),
      })
      setMessage(t('governance.exportReady'))
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }
  return <section className="panel" aria-labelledby="governance-export"><h2 id="governance-export">{t('governance.auditExport')}</h2><p className="security-note">{t('governance.exportSession')}</p><form className="inline-form" onSubmit={submit}><Field label={t('governance.start')} hint={t('common.beijingTime')}><input name="start" type="datetime-local" required defaultValue={toBeijingDateTimeLocal(new Date(now.valueOf() - 24 * 60 * 60 * 1000))} /></Field><Field label={t('governance.end')} hint={t('common.beijingTime')}><input name="end" type="datetime-local" required defaultValue={toBeijingDateTimeLocal(now)} /></Field><Field label={t('governance.customerOptional')}><select name="customer_id" defaultValue=""><option value="">{t('governance.allCustomers')}</option>{customers.map((customer) => <option key={customer.id} value={customer.id}>{customer.display_name}</option>)}</select></Field><Field label={t('governance.format')}><select name="format" defaultValue="csv"><option value="csv">CSV</option><option value="json">JSON</option></select></Field><Field label={t('governance.rowLimit')}><input name="limit" type="number" min="1" max="1000" defaultValue="100" required /></Field><Button type="submit" disabled={busy}>{busy ? t('common.loading') : t('governance.download')}</Button></form>{message && <p className="inline-success" role="status">{message}</p>}{error !== undefined && <InlineError error={error} />}</section>
}

function RetentionPanel({ customers }: { customers: Customer[] }) {
  const { locale, t } = useI18n()
  const [customerId, setCustomerId] = useState(customers[0]?.id ?? 0)
  const [policy, setPolicy] = useState<RetentionPolicy>()
  const [policyAbsent, setPolicyAbsent] = useState(false)
  const [run, setRun] = useState<RetentionRunView>()
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<unknown>()
  const customer = customers.find((item) => item.id === customerId)
  const loadPolicy = useCallback(async () => {
    if (!customerId) return
    setLoading(true)
    setError(undefined)
    setRun(undefined)
    try {
      setPolicy(await adminApi.retentionPolicy(customerId))
      setPolicyAbsent(false)
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 404) {
        setPolicy(undefined)
        setPolicyAbsent(true)
      } else {
        setError(cause)
      }
    } finally {
      setLoading(false)
    }
  }, [customerId])
  useEffect(() => { void loadPolicy() }, [loadPolicy])
  async function save(form: FormData) {
    setError(undefined)
    const result = await adminApi.setRetentionPolicy(customerId, {
      expected_version: policy?.row_version ?? 0,
      retention_days: toNumber(form, 'retention_days'),
      legal_hold: form.get('legal_hold') === 'on',
    })
    setPolicy(result)
    setPolicyAbsent(false)
    setRun(undefined)
  }
  async function dryRun() {
    setError(undefined)
    try { setRun(await adminApi.dryRunRetention(customerId)) } catch (cause) { setError(cause) }
  }
  async function execute() {
    if (!run) return
    setError(undefined)
    try { setRun(await adminApi.executeRetention(run.run.retention_run_id, run.run.row_version)) } catch (cause) { setError(cause) }
  }
  const executable = customer?.status === 'archived' && policy && !policy.legal_hold && run?.run.status === 'planned'
  if (!customers.length) return <section className="panel"><h2>{t('governance.retention')}</h2><p className="empty-inline">{t('common.empty')}</p></section>
  return <section className="panel" aria-labelledby="governance-retention"><div className="section-title"><h2 id="governance-retention">{t('governance.retention')}</h2><Button tone="secondary" onClick={loadPolicy}>{t('action.refresh')}</Button></div><div className="inline-form"><Field label={t('governance.customer')}><select value={customerId} onChange={(event) => setCustomerId(Number(event.target.value))}>{customers.map((item) => <option key={item.id} value={item.id}>{item.display_name} · {item.status}</option>)}</select></Field></div>{loading ? <p role="status">{t('common.loading')}</p> : <><form className="inline-form" onSubmit={(event) => { event.preventDefault(); void save(new FormData(event.currentTarget)).catch(setError) }}><Field label={t('governance.retentionDays')}><input name="retention_days" type="number" min="30" max="3650" required defaultValue={policy?.retention_days ?? 365} key={`${customerId}-${policy?.row_version ?? 0}`} /></Field><label className="check-field"><input name="legal_hold" type="checkbox" defaultChecked={policy?.legal_hold ?? false} key={`hold-${customerId}-${policy?.row_version ?? 0}`} /> {t('governance.legalHold')}</label><Button type="submit">{policyAbsent ? t('action.create') : t('action.save')}</Button><Button type="button" tone="secondary" onClick={dryRun} disabled={!policy || policy.legal_hold || customer?.status !== 'archived'}>{t('governance.dryRun')}</Button></form>{policy && <p><Status value={policy.status} /> · {t('governance.expectedVersion')}: {policy.row_version} · {t('common.created')}: {formatDate(policy.updated_at, locale)}</p>}{policyAbsent && <p className="empty-inline">{t('governance.noRetentionPolicy')}</p>}{customer?.status !== 'archived' && <p className="security-note">{t('governance.archivedOnly')}</p>}{policy?.legal_hold && <p className="security-note">{t('governance.legalHoldBlocks')}</p>}{run && <div className="retention-result"><div className="section-title"><h3>{t('governance.retentionRun')}</h3><Status value={run.run.status} /></div><p><code>{run.run.retention_run_id}</code> · {formatDate(run.run.cutoff_at, locale)}</p><dl className="definition-grid">{Object.entries(run.counts).map(([key, value]) => <div key={key}><dt>{key}</dt><dd>{value}</dd></div>)}</dl>{run.run.status === 'planned' && executable && <DialogForm title={t('governance.executeCleanup')} trigger={t('governance.executeCleanup')} submitLabel={t('governance.executeCleanup')} onSubmit={execute}><p className="form-wide security-note">{t('governance.cleanupWarning')}</p><p className="form-wide"><code>{run.run.retention_run_id}</code> · {t('governance.expectedVersion')}: {run.run.row_version}</p></DialogForm>}</div>}</>}{error !== undefined && <InlineError error={error} />}</section>
}

function MigrationPanel({ customers, credentials, evidence, onChanged }: {
  customers: Customer[]
  credentials: CredentialProfile[]
  evidence: EvidenceMetadata[]
  onChanged: () => void
}) {
  const { t } = useI18n()
  const [customerId, setCustomerId] = useState(customers[0]?.id ?? 0)
  const [apps, setApps] = useState<CustomerApp[]>([])
  const [draft, setDraft] = useState<AppDraftResult>()
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<unknown>()
  const [message, setMessage] = useState('')
  const primary = apps.find((item) => item.slot === 'primary')
  const candidates = apps.filter((item) => item.slot?.startsWith('migration:'))
  const customerEvidence = evidence.filter((item) => item.customer_id === customerId)
  const loadApps = useCallback(async () => {
    if (!customerId) return
    setLoading(true)
    setError(undefined)
    try { setApps(await adminApi.customerApps(customerId)) } catch (cause) { setError(cause) } finally { setLoading(false) }
  }, [customerId])
  useEffect(() => { setDraft(undefined); void loadApps() }, [loadApps])
  async function prepare(form: FormData) {
    const body: AppConfigInput = {
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
    setDraft(await adminApi.prepareAppMigration(customerId, body))
    setMessage(t('governance.migrationPrepared'))
    await loadApps()
  }
  async function verify(item: CustomerApp) {
    if (!draft || draft.app.id !== item.id) throw new Error(t('governance.verificationUnavailable'))
    await adminApi.verifyAppMigration(customerId, item.id, { expected_version: item.row_version, config_version: draft.config_version.config_version })
    setMessage(t('governance.migrationVerified'))
    await loadApps()
  }
  async function requestMigration(item: CustomerApp, form: FormData) {
    await adminApi.requestApproval({
      action_type: 'app_id_migration', customer_id: customerId, resource_id: item.id,
      evidence_ref: String(form.get('evidence_ref') ?? ''), request_key: String(form.get('request_key') ?? '').trim(),
      reason: String(form.get('reason') ?? '').trim(), ttl_seconds: toNumber(form, 'ttl_seconds', 900),
    })
    setMessage(t('governance.approvalRequested'))
    onChanged()
  }
  if (!customers.length) return <section className="panel"><h2>{t('governance.appMigration')}</h2><p className="empty-inline">{t('common.empty')}</p></section>
  return <section className="panel" aria-labelledby="governance-migration"><div className="section-title"><h2 id="governance-migration">{t('governance.appMigration')}</h2><Button tone="secondary" onClick={loadApps}>{t('action.refresh')}</Button></div><p className="security-note">{t('governance.migrationSafety')}</p><div className="inline-form"><Field label={t('governance.customer')}><select value={customerId} onChange={(event) => setCustomerId(Number(event.target.value))}>{customers.map((item) => <option key={item.id} value={item.id}>{item.display_name}</option>)}</select></Field>{primary ? <DialogForm title={t('governance.prepareMigration')} trigger={t('governance.prepareMigration')} submitLabel={t('governance.prepareMigration')} onSubmit={prepare}><MigrationConfigFields primary={primary} credentials={credentials} customerId={customerId} /></DialogForm> : <span className="security-note">{t('governance.noPrimaryApp')}</span>}</div>{loading && <p role="status">{t('common.loading')}</p>}{message && <p className="inline-success" role="status">{message}</p>}{error !== undefined && <InlineError error={error} />}{!loading && !candidates.length ? <p className="empty-inline">{t('common.empty')}</p> : <div className="table-wrap"><table><thead><tr><th>ID</th><th>{t('app.appId')}</th><th>{t('common.status')}</th><th>{t('app.configVersion')}</th><th>{t('common.actions')}</th></tr></thead><tbody>{candidates.map((item) => <tr key={item.id}><td>{item.id}</td><td><code>{item.app_id}</code><br /><small>{item.slot}</small></td><td><Status value={item.status} /></td><td>{draft?.app.id === item.id ? draft.config_version.config_version : '—'}</td><td><div className="actions actions--compact">{draft?.app.id === item.id && item.status !== 'verified' && <DialogForm title={t('action.verify')} trigger={t('action.verify')} submitLabel={t('action.verify')} onSubmit={() => verify(item)}><p className="form-wide">{t('governance.trustedVerification')}</p></DialogForm>}{item.status === 'verified' && customerEvidence.length > 0 && <DialogForm title={t('governance.requestMigrationApproval')} trigger={t('governance.requestMigrationApproval')} submitLabel={t('governance.request')} onSubmit={(form) => requestMigration(item, form)}><Field label={t('usage.evidenceRef')}><select name="evidence_ref" required>{customerEvidence.map((entry) => <option key={entry.evidence_ref} value={entry.evidence_ref}>{entry.filename} · {entry.evidence_ref}</option>)}</select></Field><ApprovalRequestFields /></DialogForm>}{item.status === 'verified' && !customerEvidence.length && <span>{t('governance.evidenceRequired')}</span>}{draft?.app.id !== item.id && item.status !== 'verified' && <span>{t('governance.verificationUnavailable')}</span>}</div></td></tr>)}</tbody></table></div>}</section>
}

function MigrationConfigFields({ primary, credentials, customerId }: { primary: CustomerApp; credentials: CredentialProfile[]; customerId: number }) {
  const { t } = useI18n()
  return <>
    <Field label={t('app.expectedVersion')}><input name="expected_version" type="number" readOnly required value={primary.row_version} /></Field>
    <Field label={t('app.provider')}><select name="provider_environment" defaultValue={primary.provider_environment}><option value="china_tencent_cloud">china_tencent_cloud</option><option value="china_tencent_adp">china_tencent_adp</option></select></Field>
    <Field label={t('app.region')}><input name="region" required defaultValue="ap-guangzhou" /></Field>
    <Field label={t('app.spaceId')}><input name="space_id" required /></Field>
    <Field label={t('app.appId')}><input name="app_id" required /></Field>
    <Field label={t('app.templateAgentId')}><input name="template_agent_id" required /></Field>
    <Field label={t('app.credentialProfile')}><select name="credential_profile_id" required>{credentials.filter((item) => item.status === 'active' && (item.owner_scope === 'platform' || item.customer_id === customerId)).map((item) => <option key={item.id} value={item.id}>{item.name} v{item.version} · {item.owner_scope}</option>)}</select></Field>
    <Field label={t('app.secretRef')}><input name="app_key_secret_ref" required pattern="env://WORKBENCH_PROVIDER_[A-Z0-9_]+" autoComplete="off" placeholder="env://WORKBENCH_PROVIDER_CUSTOMER_APP_KEY" /></Field>
    <Field label={t('app.fingerprint')}><input name="app_key_fingerprint" required pattern="sha256:[a-f0-9]{64}" placeholder="sha256:…" /></Field>
    <Field label={t('app.displayName')}><input name="display_name" required maxLength={160} /></Field>
    <fieldset className="form-section"><legend>{t('app.capabilities')}</legend><div className="checkbox-grid">{capabilities.map((capability) => <label key={capability}><input type="checkbox" name="capabilities" value={capability} defaultChecked={['chat', 'files', 'catalog_models', 'catalog_skills', 'catalog_plugins'].includes(capability)} /> {capability}</label>)}</div></fieldset>
    <LimitsFields />
  </>
}

function CredentialGovernancePanel({ credentials, onChanged }: { credentials: CredentialProfile[]; onChanged: () => void }) {
  const { t } = useI18n()
  const [message, setMessage] = useState('')
  async function request(item: CredentialProfile, action: ApprovalAction, form: FormData) {
    await adminApi.requestApproval({
      action_type: action,
      customer_id: item.customer_id,
      resource_id: item.id,
      request_key: String(form.get('request_key') ?? '').trim(),
      reason: String(form.get('reason') ?? '').trim(),
      ttl_seconds: toNumber(form, 'ttl_seconds', 900),
    })
    setMessage(t('governance.approvalRequested'))
    onChanged()
  }
  return <section className="panel" aria-labelledby="governance-credentials"><h2 id="governance-credentials">{t('governance.credentialGovernance')}</h2><p className="security-note">{t('governance.credentialContractGap')}</p>{message && <p className="inline-success" role="status">{message}</p>}{!credentials.length ? <p className="empty-inline">{t('common.empty')}</p> : <div className="table-wrap"><table><thead><tr><th>{t('credentials.name')}</th><th>{t('credentials.scope')}</th><th>{t('credentials.fingerprint')}</th><th>{t('common.status')}</th><th>{t('common.actions')}</th></tr></thead><tbody>{credentials.map((item) => <tr key={item.id}><td>{item.name} · v{item.version}<br /><small>ID {item.id}</small></td><td><code>{item.owner_scope}</code></td><td><code className="fingerprint">{item.fingerprint}</code></td><td><Status value={item.status} /></td><td><div className="actions actions--compact">{item.status === 'staged' && <DialogForm title={t('governance.requestRotation')} trigger={t('governance.requestRotation')} submitLabel={t('governance.request')} onSubmit={(form) => request(item, 'credential_rotation', form)}><ApprovalRequestFields /></DialogForm>}{item.status === 'active' && item.version > 1 && <DialogForm title={t('governance.requestRollback')} trigger={t('governance.requestRollback')} submitLabel={t('governance.request')} onSubmit={(form) => request(item, 'credential_rollback', form)}><ApprovalRequestFields /></DialogForm>}{item.status !== 'staged' && !(item.status === 'active' && item.version > 1) && '—'}</div></td></tr>)}</tbody></table></div>}</section>
}

function InlineError({ error }: { error: unknown }) {
  const { t } = useI18n()
  const message = error instanceof Error ? error.message : t('common.error')
  const requestId = error instanceof ApiError ? error.requestId : undefined
  return <div className="inline-error" role="alert"><span>{message}</span>{requestId && <small>{t('common.requestId')}: <code>{requestId}</code></small>}</div>
}
