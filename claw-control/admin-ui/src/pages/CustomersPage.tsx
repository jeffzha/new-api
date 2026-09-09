import { useCallback } from 'react'

import { adminApi } from '../api/client'
import { Button, DataState, DialogForm, Field, PageHeader, Status, formatDate, toOptionalNumber } from '../components/ui'
import { useResource } from '../hooks/useResource'
import { useI18n } from '../i18n'

export default function CustomersPage() {
  const { locale, t } = useI18n()
  const load = useCallback((signal: AbortSignal) => adminApi.customers(signal), [])
  const resource = useResource(load)

  async function create(form: FormData) {
    await adminApi.createCustomer({
      customer_code: String(form.get('customer_code') ?? '').trim(),
      display_name: String(form.get('display_name') ?? '').trim(),
      billing_user_id: toOptionalNumber(form, 'billing_user_id'),
    })
    resource.refresh()
  }

  return <>
    <PageHeader title={t('customers.title')} actions={<><Button tone="secondary" onClick={resource.refresh}>{t('action.refresh')}</Button><DialogForm title={t('customers.create')} trigger={t('customers.create')} submitLabel={t('action.create')} onSubmit={create}><Field label={t('customers.code')}><input name="customer_code" required maxLength={80} autoComplete="off" /></Field><Field label={t('customers.name')}><input name="display_name" required maxLength={160} /></Field><Field label={t('customers.billingUser')}><input name="billing_user_id" type="number" min="1" /></Field></DialogForm></>} />
    <DataState loading={resource.loading} error={resource.error} empty={resource.data?.length === 0} onRetry={resource.refresh}>
      <div className="table-wrap"><table><thead><tr><th>{t('customers.code')}</th><th>{t('customers.name')}</th><th>{t('common.status')}</th><th>{t('customers.billingUser')}</th><th>{t('common.created')}</th><th>{t('common.actions')}</th></tr></thead><tbody>{resource.data?.map((customer) => <tr key={customer.id}><td><code>{customer.customer_code}</code></td><td>{customer.display_name}</td><td><Status value={customer.status} /></td><td>{customer.billing_user_id ?? '—'}</td><td>{formatDate(customer.created_at, locale)}</td><td><a className="button button--secondary" href={`#/customers/${customer.id}`}>{t('action.view')}</a></td></tr>)}</tbody></table></div>
    </DataState>
  </>
}
