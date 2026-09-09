import { useCallback } from 'react'

import { adminApi } from '../api/client'
import { LimitsFields, limitsFromForm } from '../components/LimitsFields'
import { Button, DataState, DialogForm, Field, PageHeader, Status, formatDate, toISO } from '../components/ui'
import { useResource } from '../hooks/useResource'
import { useI18n } from '../i18n'

const capabilities = ['chat', 'files', 'web_search', 'tools', 'connectors', 'oauth', 'scheduled_tasks', 'sandbox', 'catalog_models', 'catalog_skills', 'catalog_plugins']

export default function PlansPage() {
  const { locale, t } = useI18n()
  const load = useCallback((signal: AbortSignal) => adminApi.plans(signal), [])
  const resource = useResource(load)
  async function create(form: FormData) {
    await adminApi.createPlan({
      plan_code: String(form.get('plan_code') ?? '').trim(),
      display_name: String(form.get('display_name') ?? '').trim(),
      monthly_price_cny: String(form.get('monthly_price_cny') ?? '').trim(),
      capabilities: capabilities.filter((capability) => form.getAll('capabilities').includes(capability)),
      limits: limitsFromForm(form),
      valid_from: toISO(form.get('valid_from')),
      valid_to: toISO(form.get('valid_to')),
    })
    resource.refresh()
  }
  return <>
    <PageHeader title={t('plans.title')} actions={<><Button tone="secondary" onClick={resource.refresh}>{t('action.refresh')}</Button><DialogForm title={t('plans.create')} trigger={t('plans.create')} submitLabel={t('action.create')} onSubmit={create}><Field label={t('plans.code')}><input name="plan_code" required maxLength={80} /></Field><Field label={t('plans.name')}><input name="display_name" required maxLength={160} /></Field><Field label={t('plans.price')}><input name="monthly_price_cny" required inputMode="decimal" pattern="\d+(\.\d{1,2})?" /></Field><Field label={t('plans.validFrom')} hint={t('common.beijingTime')}><input name="valid_from" type="datetime-local" required /></Field><Field label={t('plans.validTo')} hint={t('common.beijingTime')}><input name="valid_to" type="datetime-local" /></Field><fieldset className="form-section"><legend>{t('app.capabilities')}</legend><div className="checkbox-grid">{capabilities.map((capability) => <label key={capability}><input type="checkbox" name="capabilities" value={capability} defaultChecked={['chat', 'files'].includes(capability)} /> {capability}</label>)}</div></fieldset><LimitsFields /></DialogForm></>} />
    <DataState loading={resource.loading} error={resource.error} empty={resource.data?.length === 0} onRetry={resource.refresh}><div className="card-grid">{resource.data?.map((plan) => <article className="plan-card" key={plan.id}><div className="section-title"><div><code>{plan.plan_code}</code><h2>{plan.display_name}</h2></div><Status value={plan.valid_to && new Date(plan.valid_to) < new Date() ? 'expired' : 'published'} /></div><p className="price">¥{plan.monthly_price_cny}<small>/ {t('plans.perMonth')}</small></p><dl><div><dt>ID</dt><dd>{plan.id}</dd></div><div><dt>{t('plans.validFrom')}</dt><dd>{formatDate(plan.valid_from, locale)}</dd></div><div><dt>{t('plans.validTo')}</dt><dd>{formatDate(plan.valid_to, locale)}</dd></div></dl><div className="tag-list">{plan.capabilities?.map((value) => <span key={value}>{value}</span>)}</div></article>)}</div></DataState>
  </>
}
