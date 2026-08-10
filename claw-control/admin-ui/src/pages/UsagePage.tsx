import { useCallback, useState } from 'react'

import { adminApi } from '../api/client'
import type { EvidenceMetadata } from '../api/contracts'
import { Button, DataState, DialogForm, Field, PageHeader, Status, formatDate, toISO, toOptionalNumber } from '../components/ui'
import { useResource } from '../hooks/useResource'
import { useI18n } from '../i18n'

export default function UsagePage() {
  const { locale, t } = useI18n()
  const load = useCallback(async (signal: AbortSignal) => {
    const [audits, evidence, margin, billingImports] = await Promise.all([
      adminApi.usageAudits(signal), adminApi.evidence(signal), adminApi.marginReport(undefined, signal), adminApi.billingImports(signal),
    ])
    return { audits, evidence, margin, billingImports }
  }, [])
  const resource = useResource(load)

  async function create(form: FormData) {
    const usage = parseUsage(form, t('usage.invalidJson'))
    await adminApi.createUsageAudit({
      customer_id: toOptionalNumber(form, 'customer_id'), customer_app_id: toOptionalNumber(form, 'customer_app_id'),
      plan_period_id: toOptionalNumber(form, 'plan_period_id'), period_start: toISO(form.get('period_start')),
      period_end: toISO(form.get('period_end')), source: String(form.get('source') ?? ''),
      allocation_confidence: String(form.get('allocation_confidence') ?? ''),
      resource_identifier: String(form.get('resource_identifier') ?? '').trim(),
      allocation_method: String(form.get('allocation_method') ?? '').trim(),
      upstream_cost_cny: String(form.get('upstream_cost_cny') ?? '').trim(), usage,
      note: String(form.get('note') ?? '').trim(),
    })
    resource.refresh()
  }

  async function upload(form: FormData) {
    const file = form.get('file')
    if (!(file instanceof File) || file.size === 0) throw new Error(t('evidence.fileRequired'))
    await adminApi.uploadEvidence(file, toOptionalNumber(form, 'customer_id'))
    resource.refresh()
  }

  async function review(form: FormData) {
    const evidenceRef = String(form.get('evidence_ref') ?? '')
    const selectedEvidence = resource.data?.evidence.find((item) => item.evidence_ref === evidenceRef)
    if (!selectedEvidence) throw new Error(t('evidence.selectUploaded'))
    await adminApi.reviewUsageAudit(String(form.get('audit_id') ?? ''), {
      expected_version: Number(form.get('expected_version')),
      customer_id: toOptionalNumber(form, 'customer_id'),
      customer_app_id: toOptionalNumber(form, 'customer_app_id'),
      plan_period_id: toOptionalNumber(form, 'plan_period_id'),
      allocation_confidence: String(form.get('allocation_confidence') ?? ''),
      resource_identifier: String(form.get('resource_identifier') ?? '').trim(),
      allocation_method: String(form.get('allocation_method') ?? '').trim(),
      evidence_ref: selectedEvidence.evidence_ref,
      evidence_hash: selectedEvidence.content_sha256,
      note: String(form.get('note') ?? '').trim(),
    })
    resource.refresh()
  }

  async function revise(form: FormData) {
    const evidenceRef = String(form.get('evidence_ref') ?? '')
    const selectedEvidence = resource.data?.evidence.find((item) => item.evidence_ref === evidenceRef)
    if (!selectedEvidence) throw new Error(t('evidence.selectUploaded'))
    await adminApi.reviseUsageAudit(String(form.get('audit_id') ?? ''), {
      expected_version: Number(form.get('expected_version')),
      customer_id: toOptionalNumber(form, 'customer_id'),
      customer_app_id: toOptionalNumber(form, 'customer_app_id'),
      plan_period_id: toOptionalNumber(form, 'plan_period_id'),
      allocation_confidence: String(form.get('allocation_confidence') ?? ''),
      resource_identifier: String(form.get('resource_identifier') ?? '').trim(),
      allocation_method: String(form.get('allocation_method') ?? '').trim(),
      upstream_cost_cny: String(form.get('upstream_cost_cny') ?? '').trim(),
      usage: parseUsage(form, t('usage.invalidJson')),
      evidence_ref: selectedEvidence.evidence_ref,
      evidence_hash: selectedEvidence.content_sha256,
      note: String(form.get('note') ?? '').trim(),
      reason: String(form.get('reason') ?? '').trim(),
    })
    resource.refresh()
  }

  async function createBillingImport(form: FormData) {
    await adminApi.createBillingImport({
      month: String(form.get('month') ?? ''),
      business_code: String(form.get('business_code') ?? '').trim(),
    })
    resource.refresh()
  }

  async function retryBillingImport(importId: string) {
    await adminApi.retryBillingImport(importId)
    resource.refresh()
  }

  return <>
    <PageHeader title={t('usage.title')} actions={<>
      <Button tone="secondary" onClick={resource.refresh}>{t('action.refresh')}</Button>
      <DialogForm title={t('billingImport.create')} trigger={t('billingImport.create')} submitLabel={t('action.create')} onSubmit={createBillingImport}>
        <Field label={t('billingImport.month')} hint={t('billingImport.monthHint')}><input name="month" type="month" required /></Field>
        <Field label={t('billingImport.businessCode')} hint={t('billingImport.businessCodeHint')}><input name="business_code" required pattern="[A-Za-z0-9_-]+" /></Field>
        <p className="security-note">{t('billingImport.boundary')}</p>
      </DialogForm>
      <DialogForm title={t('evidence.upload')} trigger={t('evidence.upload')} submitLabel={t('evidence.upload')} onSubmit={upload}>
        <Field label={t('usage.customerId')} hint={t('evidence.customerHint')}><input name="customer_id" type="number" min="1" /></Field>
        <Field label={t('evidence.file')} hint={t('evidence.fileHint')}><input name="file" type="file" required accept=".pdf,.png,.jpg,.jpeg,.webp,.txt,.csv,.json" /></Field>
      </DialogForm>
      <DialogForm title={t('usage.create')} trigger={t('usage.create')} submitLabel={t('action.create')} onSubmit={create}><UsageCreateFields /></DialogForm>
      <DialogForm title={t('action.review')} trigger={t('action.review')} submitLabel={t('action.review')} onSubmit={review}>
        <Field label={t('usage.auditId')}><select name="audit_id" required>{resource.data?.audits.filter((audit) => audit.status === 'draft').map((audit) => { const id = audit.audit_id ?? audit.public_id ?? String(audit.id); return <option key={audit.id} value={id}>{id}</option> })}</select></Field>
        <Field label={t('app.expectedVersion')}><input name="expected_version" type="number" min="1" required defaultValue="1" /></Field>
        <Field label={t('usage.customerId')}><input name="customer_id" type="number" min="1" /></Field>
        <Field label={t('usage.appId')}><input name="customer_app_id" type="number" min="1" /></Field>
        <Field label={t('usage.periodId')}><input name="plan_period_id" type="number" min="1" /></Field>
        <Field label={t('usage.confidence')}><select name="allocation_confidence" defaultValue="app_exact"><option value="app_exact">app_exact</option><option value="estimated_allocation">estimated_allocation</option><option value="account_only">account_only</option></select></Field>
        <Field label={t('usage.resource')}><input name="resource_identifier" /></Field>
        <Field label={t('usage.method')}><input name="allocation_method" /></Field>
        <Field label={t('usage.evidenceRef')} hint={t('evidence.bindingHint')}><select name="evidence_ref" required>{resource.data?.evidence.map((item) => <option key={item.evidence_ref} value={item.evidence_ref}>{item.filename} · {item.customer_id ?? 'account_only'} · {item.content_sha256.slice(0, 18)}…</option>)}</select></Field>
        <Field label={t('usage.note')}><textarea name="note" rows={3} /></Field>
      </DialogForm>
      <DialogForm title={t('usage.revise')} trigger={t('usage.revise')} submitLabel={t('usage.revise')} onSubmit={revise}>
        <UsageRevisionFields audits={resource.data?.audits.filter((audit) => audit.status === 'locked') ?? []} evidence={resource.data?.evidence ?? []} />
      </DialogForm>
    </>} />
    <DataState loading={resource.loading} error={resource.error} onRetry={resource.refresh}>
      {resource.data && <div className="stack">
        <MarginPanel margin={resource.data.margin} />
        <section className="panel"><div className="section-title"><h2>{t('billingImport.title')}</h2><span>{t('billingImport.readOnly')}</span></div><p className="security-note">{t('billingImport.boundary')}</p>{!resource.data.billingImports.length ? <p>{t('common.empty')}</p> : <div className="table-wrap"><table><thead><tr><th>ID</th><th>{t('billingImport.scope')}</th><th>{t('billingImport.progress')}</th><th>{t('usage.cost')}</th><th>{t('billingImport.review')}</th><th>{t('common.status')}</th><th>{t('common.actions')}</th></tr></thead><tbody>{resource.data.billingImports.map((item) => <tr key={item.import_id}><td><code>{item.import_id}</code><br /><small>{formatDate(item.created_at, locale)}</small></td><td>{item.month}<br /><code>{item.business_code}</code></td><td>{item.detail_page_count} {t('billingImport.pages')} · {item.detail_record_count} {t('billingImport.records')}<br /><small>{item.attempt_count}/{item.max_attempts}</small></td><td>¥{item.upstream_cost_cny}</td><td>{item.manual_review_required ? item.review_reasons.join(', ') || t('billingImport.required') : t('billingImport.required')}<br /><small>{item.usage_audit_id ?? '—'}</small></td><td><Status value={item.status} />{item.error_code && <><br /><code>{item.error_code}</code></>}</td><td>{item.status === 'failed' && <Button tone="secondary" onClick={() => void retryBillingImport(item.import_id)}>{t('action.retry')}</Button>}</td></tr>)}</tbody></table></div>}</section>
        <section className="panel"><h2>{t('evidence.title')}</h2>{!resource.data.evidence.length ? <p>{t('common.empty')}</p> : <EvidenceTable evidence={resource.data.evidence} locale={locale} />}</section>
        <section className="panel"><h2>{t('usage.title')}</h2>{!resource.data.audits.length ? <p>{t('common.empty')}</p> : <div className="table-wrap"><table><thead><tr><th>ID</th><th>{t('usage.customerId')}</th><th>{t('periods.start')}</th><th>{t('periods.end')}</th><th>{t('usage.confidence')}</th><th>{t('usage.cost')}</th><th>{t('common.status')}</th></tr></thead><tbody>{resource.data.audits.map((audit) => <tr key={audit.id}><td><code>{audit.audit_id ?? audit.public_id ?? audit.id}</code></td><td>{audit.customer_id ?? '—'}</td><td>{formatDate(audit.period_start, locale)}</td><td>{formatDate(audit.period_end, locale)}</td><td>{audit.allocation_confidence}</td><td>¥{audit.upstream_cost_cny}</td><td><Status value={audit.status} /></td></tr>)}</tbody></table></div>}</section>
      </div>}
    </DataState>
  </>
}

function MarginPanel({ margin }: { margin: Awaited<ReturnType<typeof adminApi.marginReport>> }) {
  const { t } = useI18n()
  return <section className="panel"><div className="section-title"><h2>{t('margin.title')}</h2><Status value={margin.confidence} /></div><dl className="definition-grid"><div><dt>{t('margin.revenue')}</dt><dd>¥{margin.invoiced_revenue_cny}</dd></div><div><dt>{t('margin.cost')}</dt><dd>¥{margin.reviewed_cost_cny}</dd></div><div><dt>{t('margin.margin')}</dt><dd>¥{margin.margin_cny}</dd></div><div><dt>{t('margin.accountOnly')}</dt><dd>¥{margin.breakdown.account_only_cost_cny}</dd></div><div><dt>{t('margin.unverified')}</dt><dd>¥{margin.breakdown.unverified_cost_cny}</dd></div></dl><p className="security-note">{t('margin.boundary')}</p></section>
}

function EvidenceTable({ evidence, locale }: { evidence: EvidenceMetadata[]; locale: string }) {
  const { t } = useI18n()
  const [downloadError, setDownloadError] = useState('')
  async function download(item: EvidenceMetadata) {
    setDownloadError('')
    try { await adminApi.downloadEvidence(item.evidence_ref, item.filename) } catch (error) { setDownloadError(error instanceof Error ? error.message : t('common.error')) }
  }
  return <>{downloadError && <p className="form-error" role="alert">{downloadError}</p>}<div className="table-wrap"><table><thead><tr><th>{t('evidence.file')}</th><th>{t('usage.customerId')}</th><th>{t('evidence.size')}</th><th>SHA-256</th><th>{t('common.created')}</th><th>{t('common.actions')}</th></tr></thead><tbody>{evidence.map((item) => <tr key={item.evidence_ref}><td>{item.filename}<br /><code>{item.evidence_ref}</code></td><td>{item.customer_id ?? 'account_only'}</td><td>{item.size_bytes}</td><td><code>{item.content_sha256}</code></td><td>{formatDate(item.created_at, locale)}</td><td><Button tone="secondary" onClick={() => void download(item)}>{t('evidence.download')}</Button></td></tr>)}</tbody></table></div></>
}

function UsageCreateFields() {
  const { t } = useI18n()
  return <><Field label={t('usage.customerId')}><input name="customer_id" type="number" min="1" /></Field><Field label={t('usage.appId')}><input name="customer_app_id" type="number" min="1" /></Field><Field label={t('usage.periodId')}><input name="plan_period_id" type="number" min="1" /></Field><Field label={t('periods.start')} hint={t('common.beijingTime')}><input name="period_start" type="datetime-local" required /></Field><Field label={t('periods.end')} hint={t('common.beijingTime')}><input name="period_end" type="datetime-local" required /></Field><Field label={t('usage.source')}><input name="source" required defaultValue="tencent_console_manual" /></Field><Field label={t('usage.confidence')}><select name="allocation_confidence" defaultValue="app_exact"><option value="app_exact">app_exact</option><option value="estimated_allocation">estimated_allocation</option><option value="account_only">account_only</option><option value="unverified">unverified</option></select></Field><Field label={t('usage.cost')}><input name="upstream_cost_cny" required inputMode="decimal" pattern="\d+(\.\d+)?" /></Field><Field label={t('usage.resource')}><input name="resource_identifier" /></Field><Field label={t('usage.method')}><input name="allocation_method" /></Field><Field label={t('usage.metrics')}><textarea name="usage" rows={5} required defaultValue={'{\n  "runtime_minutes": "0",\n  "input_tokens": "0",\n  "output_tokens": "0"\n}'} /></Field><Field label={t('usage.note')}><textarea name="note" rows={3} /></Field></>
}

function UsageRevisionFields({ audits, evidence }: { audits: Awaited<ReturnType<typeof adminApi.usageAudits>>; evidence: EvidenceMetadata[] }) {
  const { t } = useI18n()
  const [selectedAuditId, setSelectedAuditId] = useState(() => audits[0]?.audit_id ?? audits[0]?.public_id ?? '')
  const selected = audits.find((audit) => (audit.audit_id ?? audit.public_id) === selectedAuditId)
  return <>
    <p className="form-wide security-note">{t('usage.revisionHelp')}</p>
    <Field label={t('usage.auditId')}><select name="audit_id" required value={selectedAuditId} onChange={(event) => setSelectedAuditId(event.target.value)}>{audits.map((audit) => { const id = audit.audit_id ?? audit.public_id ?? String(audit.id); return <option key={audit.id} value={id}>{id}</option> })}</select></Field>
    <Field label={t('app.expectedVersion')}><input name="expected_version" type="number" min="1" required readOnly value={selected?.row_version ?? ''} /></Field>
    <Field label={t('usage.customerId')}><input name="customer_id" type="number" min="1" defaultValue={selected?.customer_id} /></Field>
    <Field label={t('usage.appId')}><input name="customer_app_id" type="number" min="1" defaultValue={selected?.customer_app_id} /></Field>
    <Field label={t('usage.periodId')}><input name="plan_period_id" type="number" min="1" defaultValue={selected?.plan_period_id} /></Field>
    <Field label={t('usage.confidence')}><select name="allocation_confidence" defaultValue={selected?.allocation_confidence ?? 'app_exact'}><option value="app_exact">app_exact</option><option value="estimated_allocation">estimated_allocation</option><option value="account_only">account_only</option></select></Field>
    <Field label={t('usage.cost')}><input name="upstream_cost_cny" required inputMode="decimal" pattern="\d+(\.\d+)?" /></Field>
    <Field label={t('usage.resource')}><input name="resource_identifier" /></Field>
    <Field label={t('usage.method')}><input name="allocation_method" /></Field>
    <Field label={t('usage.metrics')}><textarea name="usage" rows={5} required defaultValue={'{\n  "runtime_minutes": "0",\n  "input_tokens": "0",\n  "output_tokens": "0"\n}'} /></Field>
    <Field label={t('usage.evidenceRef')} hint={t('evidence.bindingHint')}><select name="evidence_ref" required>{evidence.map((item) => <option key={item.evidence_ref} value={item.evidence_ref}>{item.filename} · {item.customer_id ?? 'account_only'} · {item.content_sha256.slice(0, 18)}…</option>)}</select></Field>
    <Field label={t('usage.note')}><textarea name="note" rows={3} /></Field>
    <Field label={t('usage.revisionReason')}><textarea name="reason" rows={3} required maxLength={1000} /></Field>
  </>
}

function parseUsage(form: FormData, invalidMessage: string): Record<string, string> {
  try {
    const parsed = JSON.parse(String(form.get('usage') ?? '{}')) as unknown
    if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object' || Object.values(parsed).some((value) => typeof value !== 'string')) throw new Error(invalidMessage)
    return parsed as Record<string, string>
  } catch {
    throw new Error(invalidMessage)
  }
}
