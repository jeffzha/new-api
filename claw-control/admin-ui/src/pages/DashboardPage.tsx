import { useCallback } from 'react'

import { adminApi } from '../api/client'
import { DataState, PageHeader, Status, formatDate } from '../components/ui'
import { useResource } from '../hooks/useResource'
import { useI18n } from '../i18n'

export default function DashboardPage() {
  const { locale, t } = useI18n()
  const load = useCallback((signal: AbortSignal) => adminApi.dashboard(signal), [])
  const resource = useResource(load)
  const cards = resource.data ? [
    [t('dashboard.customersTotal'), resource.data.customers_total],
    [t('dashboard.customersActive'), resource.data.customers_active],
    [t('dashboard.appsActive'), resource.data.apps_active],
    [t('dashboard.plansExpiring'), resource.data.plans_expiring],
    [t('dashboard.usagePending'), resource.data.usage_audits_pending],
  ] : []

  return <>
    <PageHeader title={t('dashboard.title')} />
    <DataState loading={resource.loading} error={resource.error} onRetry={resource.refresh}>
      <div className="metric-grid">{cards.map(([label, value]) => <article className="metric" key={label}><span>{label}</span><strong>{value}</strong></article>)}</div>
      <section className="panel"><h2>{t('dashboard.recent')}</h2>
        {!resource.data?.recent_audits?.length ? <p>{t('common.empty')}</p> : <div className="table-wrap"><table><thead><tr><th>{t('audits.actor')}</th><th>{t('audits.action')}</th><th>{t('audits.resource')}</th><th>{t('common.status')}</th><th>{t('common.created')}</th></tr></thead><tbody>{resource.data.recent_audits.map((item) => <tr key={item.id}><td>{item.actor}</td><td><code>{item.action}</code></td><td>{item.resource_type} / {item.resource_id}</td><td><Status value={item.result ?? 'recorded'} /></td><td>{formatDate(item.created_at, locale)}</td></tr>)}</tbody></table></div>}
      </section>
    </DataState>
  </>
}
