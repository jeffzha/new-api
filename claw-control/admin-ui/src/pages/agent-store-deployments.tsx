import { useState } from 'react'

import { adminApi } from '../api/client'
import type { AgentStoreDeployment, AgentStoreItem, Customer } from '../api/contracts'
import { Button, DialogForm, Field, Status, formatDate, toNumber } from '../components/ui'
import { useI18n } from '../i18n'
import { AgentStoreDeploymentFields, EntitlementEditor, parseEntitlements } from './agent-store-deployment-fields'

const EXECUTABLE_RUNTIME_PROFILES = new Set([
  'standard_v2',
  'multi_agent_v2',
  'workflow_v2',
  'claw_static_v2',
  'claw_dynamic_v2',
])

export function AddAgentStoreDeploymentDialog(props: { item: AgentStoreItem; customers: Customer[]; onChanged: () => void }) {
  const { t } = useI18n()

  async function create(form: FormData) {
    await adminApi.createAgentStoreDeployment(props.item.item_id, {
      expected_item_version: props.item.row_version,
      customer_id: toNumber(form, 'customer_id'),
      customer_app_id: toNumber(form, 'customer_app_id'),
      entitlements: parseEntitlements(form),
    })
    props.onChanged()
  }

  return <DialogForm title={t('agentStore.addDeployment')} trigger={t('agentStore.addDeployment')} submitLabel={t('action.create')} onSubmit={create}>
    <AgentStoreDeploymentFields customers={props.customers} />
    <EntitlementEditor />
  </DialogForm>
}

export function AgentStoreDeployments(props: { item: AgentStoreItem; onChanged: () => void }) {
  const { locale, t } = useI18n()
  const [error, setError] = useState<string>()
  const [pendingDeployment, setPendingDeployment] = useState<string>()

  async function run(deployment: AgentStoreDeployment, operation: () => Promise<unknown>) {
    setError(undefined)
    setPendingDeployment(deployment.deployment_id)
    try {
      await operation()
      props.onChanged()
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : t('common.error'))
      throw caught
    } finally {
      setPendingDeployment(undefined)
    }
  }

  async function update(deployment: AgentStoreDeployment, form: FormData) {
    await run(deployment, () => adminApi.updateAgentStoreDeployment(props.item.item_id, deployment.deployment_id, {
      expected_deployment_version: deployment.row_version,
      execution_enabled: form.get('execution_enabled') === 'on',
      entitlements: parseEntitlements(form),
    }))
  }

  async function setExecution(deployment: AgentStoreDeployment, enabled: boolean) {
    await run(deployment, () => adminApi.updateAgentStoreDeployment(props.item.item_id, deployment.deployment_id, {
      expected_deployment_version: deployment.row_version,
      execution_enabled: enabled,
    }))
  }

  async function verify(deployment: AgentStoreDeployment) {
    await run(deployment, () => adminApi.verifyAgentStoreDeployment(
      props.item.item_id,
      deployment.deployment_id,
      props.item.row_version,
      deployment.row_version,
    ))
  }

  async function disable(deployment: AgentStoreDeployment, reason: string) {
    await run(deployment, () => adminApi.disableAgentStoreDeployment(
      props.item.item_id,
      deployment.deployment_id,
      deployment.row_version,
      reason,
    ))
  }

  return <section className="deployment-section" aria-labelledby={`deployments-${props.item.item_id}`}>
    <h3 id={`deployments-${props.item.item_id}`}>{t('agentStore.deployments')}</h3>
    {error && <p className="inline-error" role="alert">{error}</p>}
    <div className="deployment-list">
      {props.item.deployments.map((deployment) => {
        const busy = pendingDeployment === deployment.deployment_id
        const executionAllowed = isExecutionAllowed(deployment)
        return <article className="deployment-card" key={`${deployment.deployment_id}:${deployment.row_version}`}>
          <div className="section-title">
            <div><h4>{t('agentStore.customerId')} {deployment.customer_id}</h4><p><code>{deployment.deployment_id}</code></p></div>
            <Status value={deployment.status} />
          </div>
          <dl className="definition-grid">
            <div><dt>{t('agentStore.appMode')}</dt><dd>{deployment.provider_app_mode || t('agentStore.unverified')}</dd></div>
            <div><dt>{t('agentStore.runtime')}</dt><dd><code>{deployment.runtime_profile || t('agentStore.unverified')}</code></dd></div>
            <div><dt>{t('agentStore.customerAppId')}</dt><dd>{deployment.customer_app_id}</dd></div>
            <div><dt>{t('agentStore.execution')}</dt><dd><Status value={deployment.execution_enabled ? 'enabled' : 'disabled'} /></dd></div>
            <div><dt>{t('agentStore.verifiedAt')}</dt><dd>{formatDate(deployment.verified_at, locale)}</dd></div>
            <div><dt>{t('agentStore.configVersion')}</dt><dd>{deployment.verified_config_version || '-'}</dd></div>
          </dl>
          <h5>{t('agentStore.entitlements')}</h5>
          <div className="table-wrap">
            <table>
              <thead><tr><th scope="col">{t('agentStore.subjectType')}</th><th scope="col">{t('agentStore.subjectRef')}</th><th scope="col">{t('agentStore.validity')}</th></tr></thead>
              <tbody>{deployment.entitlements.map((entitlement) => <tr key={entitlement.entitlement_id}>
                <td>{t(subjectTypeMessage(entitlement.subject_type))}</td>
                <td><code>{entitlement.subject_ref}</code></td>
                <td>{formatDate(entitlement.valid_from, locale)} - {formatDate(entitlement.valid_until, locale)}</td>
              </tr>)}</tbody>
            </table>
          </div>
          <div className="agent-store-admin-actions">
            <DialogForm title={t('agentStore.manageDeployment')} trigger={t('agentStore.manageDeployment')} submitLabel={t('action.save')} onSubmit={(form) => update(deployment, form)}>
              <p className="form-wide security-note">{executionAllowed ? t('agentStore.executionHint') : t('agentStore.executionUnavailable')}</p>
              <label className="check-field"><input name="execution_enabled" type="checkbox" defaultChecked={deployment.execution_enabled} disabled={!executionAllowed} /> {t('agentStore.execution')}</label>
              <EntitlementEditor entitlements={deployment.entitlements} />
            </DialogForm>
            {props.item.status !== 'archived' && <Button disabled={busy} onClick={() => void verify(deployment).catch(() => undefined)}>{t('action.verify')}</Button>}
            {executionAllowed && !deployment.execution_enabled && <Button disabled={busy} onClick={() => void setExecution(deployment, true).catch(() => undefined)}>{t('action.enable')}</Button>}
            {deployment.execution_enabled && <Button tone="secondary" disabled={busy} onClick={() => void setExecution(deployment, false).catch(() => undefined)}>{t('agentStore.stopExecution')}</Button>}
            {deployment.status !== 'disabled' && <DisableDeploymentDialog deployment={deployment} disabled={busy} onDisable={(reason) => disable(deployment, reason)} />}
          </div>
        </article>
      })}
    </div>
  </section>
}

function DisableDeploymentDialog(props: { deployment: AgentStoreDeployment; disabled: boolean; onDisable: (reason: string) => Promise<void> }) {
  const { t } = useI18n()
  return <DialogForm title={t('agentStore.disableDeployment')} trigger={t('agentStore.disableDeployment')} submitLabel={t('action.disable')} onSubmit={async (form) => {
    const reason = String(form.get('reason') ?? '').trim()
    if (!reason) throw new Error(t('agentStore.reasonRequired'))
    await props.onDisable(reason)
  }}>
    <Field label={t('agentStore.reason')} hint={t('agentStore.disableDeploymentHint')}><textarea name="reason" required maxLength={500} rows={4} disabled={props.disabled} /></Field>
  </DialogForm>
}

function subjectTypeMessage(subjectType: AgentStoreDeployment['entitlements'][number]['subject_type']) {
  switch (subjectType) {
    case 'user': return 'agentStore.subjectUser' as const
    case 'role': return 'agentStore.subjectRole' as const
    case 'plan': return 'agentStore.subjectPlan' as const
    default: return 'agentStore.subjectCustomer' as const
  }
}

export function isExecutionAllowed(deployment: Pick<AgentStoreDeployment, 'runtime_profile' | 'status'>): boolean {
  return EXECUTABLE_RUNTIME_PROFILES.has(deployment.runtime_profile) && ['verified', 'active'].includes(deployment.status)
}
