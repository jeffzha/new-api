import { useCallback, useState } from 'react'

import { adminApi } from '../api/client'
import type { AgentStoreItem, AgentStoreVersion } from '../api/contracts'
import { Button, DataState, DialogForm, Field, PageHeader, Status, toNumber } from '../components/ui'
import { useResource } from '../hooks/useResource'
import { useI18n } from '../i18n'
import { AgentStoreDeploymentFields } from './agent-store-deployment-fields'
import { AddAgentStoreDeploymentDialog, AgentStoreDeployments } from './agent-store-deployments'

export default function AgentStorePage() {
  const { t } = useI18n()
  const load = useCallback((signal: AbortSignal) => adminApi.agentStoreItems(signal), [])
  const resource = useResource(load)
  const loadCustomers = useCallback((signal: AbortSignal) => adminApi.customers(signal), [])
  const customers = useResource(loadCustomers)
  const [actionError, setActionError] = useState<string>()
  const [pendingItem, setPendingItem] = useState<string>()

  async function create(form: FormData) {
    await adminApi.createAgentStoreItem({
      slug: String(form.get('slug') ?? '').trim().toLowerCase(),
      display_name: String(form.get('display_name') ?? '').trim(),
      summary: String(form.get('summary') ?? '').trim(),
      description: String(form.get('description') ?? '').trim(),
      avatar_url: String(form.get('avatar_url') ?? '').trim(),
      category: String(form.get('category') ?? '').trim().toLowerCase(),
      tags: parseTags(form.get('tags')),
      sort_order: toNumber(form, 'sort_order'),
      featured: form.get('featured') === 'on',
      deployment: {
        customer_id: toNumber(form, 'customer_id'),
        customer_app_id: toNumber(form, 'customer_app_id'),
        execution_enabled: false,
      },
      entitlements: [],
    })
    resource.refresh()
  }

  async function edit(item: AgentStoreItem, form: FormData) {
    const metadata = editableVersion(item)
    await adminApi.updateAgentStoreItem(item.item_id, {
      expected_version: item.row_version,
      display_name: String(form.get('display_name') ?? metadata.display_name).trim(),
      summary: String(form.get('summary') ?? metadata.summary).trim(),
      description: String(form.get('description') ?? metadata.description).trim(),
      avatar_url: String(form.get('avatar_url') ?? metadata.avatar_url ?? '').trim(),
      category: String(form.get('category') ?? metadata.category).trim().toLowerCase(),
      tags: parseTags(form.get('tags')),
      sort_order: toNumber(form, 'sort_order', item.sort_order),
      featured: form.get('featured') === 'on',
    })
    resource.refresh()
  }

  async function transition(item: AgentStoreItem, action: 'publish' | 'unpublish' | 'disable' | 'archive', reason = '') {
    setActionError(undefined)
    setPendingItem(item.item_id)
    try {
      await adminApi.transitionAgentStoreItem(item.item_id, action, item.row_version, reason)
      resource.refresh()
    } catch (error) {
      setActionError(error instanceof Error ? error.message : t('common.error'))
    } finally {
      setPendingItem(undefined)
    }
  }

  return <>
    <PageHeader title={t('agentStore.title')} actions={<>
      <Button tone="secondary" onClick={resource.refresh}>{t('action.refresh')}</Button>
      <DialogForm title={t('agentStore.create')} trigger={t('agentStore.create')} submitLabel={t('action.create')} onSubmit={create}>
        <Field label={t('agentStore.slug')} hint={t('agentStore.slugHint')}><input name="slug" required minLength={3} maxLength={64} pattern="[a-z0-9][a-z0-9-]*[a-z0-9]" /></Field>
        <Field label={t('agentStore.name')}><input name="display_name" required maxLength={160} /></Field>
        <Field label={t('agentStore.summary')}><textarea name="summary" required maxLength={500} rows={3} /></Field>
        <Field label={t('agentStore.description')}><textarea name="description" maxLength={20000} rows={5} /></Field>
        <Field label={t('agentStore.avatar')}><input name="avatar_url" type="url" maxLength={1024} /></Field>
        <Field label={t('agentStore.category')}><input name="category" required maxLength={96} /></Field>
        <Field label={t('agentStore.tags')} hint={t('agentStore.tagsHint')}><input name="tags" maxLength={512} /></Field>
        <Field label={t('agentStore.sortOrder')}><input name="sort_order" type="number" defaultValue="0" /></Field>
        <AgentStoreDeploymentFields customers={customers.data ?? []} />
        <label className="check-field"><input name="featured" type="checkbox" /> {t('agentStore.featured')}</label>
      </DialogForm>
    </>} />
    <p className="security-note">{t('agentStore.boundary')}</p>
    {actionError && <p className="inline-error" role="alert">{actionError}</p>}
    <DataState loading={resource.loading} error={resource.error} empty={resource.data?.length === 0} onRetry={resource.refresh}>
      <div className="card-grid agent-store-admin-grid">
        {resource.data?.map((item) => {
          const metadata = editableVersion(item)
          const busy = pendingItem === item.item_id
          return <article className="plan-card" key={item.item_id}>
            <div className="section-title"><div><code>{item.slug}</code><h2>{metadata.display_name}</h2></div><Status value={item.status} /></div>
            <p>{metadata.summary}</p>
            <dl className="definition-grid">
              <div><dt>{t('agentStore.deployments')}</dt><dd>{item.deployments.length}</dd></div>
              <div><dt>{t('agentStore.activeDeployments')}</dt><dd>{item.deployments.filter((deployment) => deployment.status === 'active').length}</dd></div>
            </dl>
            <div className="tag-list">{metadata.tags.map((tag) => <span key={tag}>{tag}</span>)}</div>
            <div className="agent-store-admin-actions">
              <DialogForm title={t('agentStore.edit')} trigger={t('action.edit')} submitLabel={t('action.save')} onSubmit={(form) => edit(item, form)}>
                <Field label={t('agentStore.name')}><input name="display_name" required maxLength={160} defaultValue={metadata.display_name} /></Field>
                <Field label={t('agentStore.summary')}><textarea name="summary" required maxLength={500} rows={3} defaultValue={metadata.summary} /></Field>
                <Field label={t('agentStore.description')}><textarea name="description" maxLength={20000} rows={5} defaultValue={metadata.description} /></Field>
                <Field label={t('agentStore.avatar')}><input name="avatar_url" type="url" maxLength={1024} defaultValue={metadata.avatar_url} /></Field>
                <Field label={t('agentStore.category')}><input name="category" required maxLength={96} defaultValue={metadata.category} /></Field>
                <Field label={t('agentStore.tags')}><input name="tags" defaultValue={metadata.tags.join(', ')} /></Field>
                <Field label={t('agentStore.sortOrder')}><input name="sort_order" type="number" defaultValue={item.sort_order} /></Field>
                <label className="check-field"><input name="featured" type="checkbox" defaultChecked={item.featured} /> {t('agentStore.featured')}</label>
              </DialogForm>
              {item.status !== 'disabled' && item.status !== 'archived' && <AddAgentStoreDeploymentDialog item={item} customers={customers.data ?? []} onChanged={resource.refresh} />}
              {canPublish(item) && <Button disabled={busy} onClick={() => void transition(item, 'publish')}>{t('agentStore.publish')}</Button>}
              {item.status === 'published' && <Button tone="secondary" disabled={busy} onClick={() => void transition(item, 'unpublish')}>{t('agentStore.unpublish')}</Button>}
              {canArchive(item) && <Button tone="secondary" disabled={busy} onClick={() => void transition(item, 'archive')}>{t('agentStore.archive')}</Button>}
              {item.status !== 'disabled' && item.status !== 'archived' && <DisableDialog item={item} disabled={busy} onDisable={(reason) => transition(item, 'disable', reason)} />}
            </div>
            <AgentStoreDeployments item={item} onChanged={resource.refresh} />
          </article>
        })}
      </div>
    </DataState>
  </>
}

function DisableDialog({ item, disabled, onDisable }: { item: AgentStoreItem; disabled: boolean; onDisable: (reason: string) => Promise<void> }) {
  const { t } = useI18n()
  return <DialogForm title={t('agentStore.disable')} trigger={t('action.disable')} submitLabel={t('action.disable')} onSubmit={async (form) => {
    const reason = String(form.get('reason') ?? '').trim()
    if (!reason) throw new Error(t('agentStore.reasonRequired'))
    await onDisable(reason)
  }}><Field label={t('agentStore.reason')} hint={t('agentStore.disableHint')}><textarea name="reason" required maxLength={500} rows={4} disabled={disabled} /></Field><input type="hidden" name="item_id" value={item.item_id} /></DialogForm>
}

function editableVersion(item: AgentStoreItem): AgentStoreVersion {
  const version = item.draft_version ?? item.current_version
  if (version) return version
  return { version_id: '', generation: 0, display_name: item.slug, summary: '', description: '', category: '', tags: [] }
}

function parseTags(value: FormDataEntryValue | null): string[] {
  return Array.from(new Set(String(value ?? '').split(',').map((tag) => tag.trim().toLowerCase()).filter(Boolean)))
}

function canPublish(item: AgentStoreItem): boolean {
  return ['verified', 'unpublished', 'published'].includes(item.status)
}

function canArchive(item: AgentStoreItem): boolean {
  return ['draft', 'rejected', 'unpublished'].includes(item.status)
}
