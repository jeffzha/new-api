import { useCallback } from 'react'

import { adminApi } from '../api/client'
import { Button, DataState, PageHeader, Status, formatDate } from '../components/ui'
import { useResource } from '../hooks/useResource'
import { useI18n } from '../i18n'

export default function AuditsPage() {
  const { locale, t } = useI18n()
  const load = useCallback((signal: AbortSignal) => adminApi.audits(signal), [])
  const resource = useResource(load)
  return <>
    <PageHeader title={t('audits.title')} actions={<Button tone="secondary" onClick={resource.refresh}>{t('action.refresh')}</Button>} />
    <DataState loading={resource.loading} error={resource.error} empty={resource.data?.length === 0} onRetry={resource.refresh}><div className="table-wrap"><table><thead><tr><th>{t('common.created')}</th><th>{t('audits.actor')}</th><th>{t('audits.action')}</th><th>{t('audits.resource')}</th><th>{t('common.status')}</th><th>{t('audits.reason')}</th><th>{t('common.requestId')}</th></tr></thead><tbody>{resource.data?.map((audit) => <tr key={audit.id}><td>{formatDate(audit.created_at, locale)}</td><td>{audit.actor}</td><td><code>{audit.action}</code></td><td>{audit.resource_type} / {audit.resource_id}</td><td><Status value={audit.result ?? 'recorded'} /></td><td>{audit.reason || '—'}</td><td><code>{audit.request_id || '—'}</code></td></tr>)}</tbody></table></div></DataState>
  </>
}
