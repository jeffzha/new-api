import type { ApiEnvelope, EntityId } from './contracts'

const API_PREFIX = '/api/admin/workbench'
const SAFE_PATH = /^\/[A-Za-z0-9._~!$&'()*+,;=:@%/-]*(?:\?[A-Za-z0-9._~!$&'()*+,;=:@%/?-]*)?$/
const MUTATING_METHODS = new Set(['POST', 'PUT', 'PATCH', 'DELETE'])

export class ApiError extends Error {
  readonly status: number
  readonly code?: string
  readonly requestId?: string

  constructor(message: string, status: number, code?: string, requestId?: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.requestId = requestId
  }
}

function redirectForRecentAuthentication(response: Response, envelope: ApiEnvelope<unknown>) {
  if (response.status === 403 && envelope.error?.code === 'recent_auth_required') {
    window.location.assign('/workbench-admin?step_up=1')
  }
}

export function parseCookie(cookieHeader: string, name: string): string | undefined {
  for (const part of cookieHeader.split(';')) {
    const separator = part.indexOf('=')
    if (separator < 0) continue
    const key = part.slice(0, separator).trim()
    if (key !== name) continue
    try {
      return decodeURIComponent(part.slice(separator + 1).trim())
    } catch {
      return undefined
    }
  }
  return undefined
}

export function buildRequestInit(
  method: string,
  body: unknown,
  cookieHeader: string,
): RequestInit {
  const normalizedMethod = method.toUpperCase()
  const headers = new Headers({ Accept: 'application/json' })
  if (body !== undefined) headers.set('Content-Type', 'application/json')
  if (MUTATING_METHODS.has(normalizedMethod)) {
    const csrf = parseCookie(cookieHeader, 'claw_admin_csrf')
    if (!csrf) throw new ApiError('Administrative session is missing its CSRF token.', 403, 'csrf_missing')
    headers.set('X-CSRF-Token', csrf)
  }
  return {
    method: normalizedMethod,
    credentials: 'include',
    cache: 'no-store',
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  }
}

export async function apiRequest<T>(
  path: string,
  options: { method?: string; body?: unknown; signal?: AbortSignal } = {},
): Promise<T> {
  if (!SAFE_PATH.test(path) || path.startsWith('//') || path.includes('..')) {
    throw new ApiError('Unsafe API path was rejected.', 400, 'unsafe_path')
  }
  const method = (options.method ?? 'GET').toUpperCase()
  const init = buildRequestInit(method, options.body, document.cookie)
  init.signal = options.signal
  const response = await fetch(`${API_PREFIX}${path}`, init)
  let envelope: ApiEnvelope<T>
  try {
    envelope = (await response.json()) as ApiEnvelope<T>
  } catch {
    throw new ApiError('The server returned an invalid response.', response.status, 'invalid_response')
  }
  if (!response.ok || !envelope.success || envelope.data === undefined) {
	if (MUTATING_METHODS.has(method)) redirectForRecentAuthentication(response, envelope)
    throw new ApiError(
      envelope.error?.message ?? `Request failed (${response.status}).`,
      response.status,
      envelope.error?.code,
      envelope.error?.request_id,
    )
  }
  return envelope.data
}

async function apiFormRequest<T>(path: string, form: FormData): Promise<T> {
  if (!SAFE_PATH.test(path) || path.startsWith('//') || path.includes('..')) {
    throw new ApiError('Unsafe API path was rejected.', 400, 'unsafe_path')
  }
  const csrf = parseCookie(document.cookie, 'claw_admin_csrf')
  if (!csrf) throw new ApiError('Administrative session is missing its CSRF token.', 403, 'csrf_missing')
  const response = await fetch(`${API_PREFIX}${path}`, {
    method: 'POST', credentials: 'include', cache: 'no-store', body: form,
    headers: { Accept: 'application/json', 'X-CSRF-Token': csrf },
  })
  let envelope: ApiEnvelope<T>
  try {
    envelope = (await response.json()) as ApiEnvelope<T>
  } catch {
    throw new ApiError('The server returned an invalid response.', response.status, 'invalid_response')
  }
  if (!response.ok || !envelope.success || envelope.data === undefined) {
	redirectForRecentAuthentication(response, envelope)
    throw new ApiError(envelope.error?.message ?? `Request failed (${response.status}).`, response.status, envelope.error?.code, envelope.error?.request_id)
  }
  return envelope.data
}

async function downloadEvidence(evidenceRef: string, filename: string): Promise<void> {
  const path = `/evidence/${encodeURIComponent(evidenceRef)}`
  if (!SAFE_PATH.test(path) || path.includes('..')) throw new ApiError('Unsafe API path was rejected.', 400, 'unsafe_path')
  const response = await fetch(`${API_PREFIX}${path}`, { credentials: 'include', cache: 'no-store', headers: { Accept: 'application/octet-stream' } })
  if (!response.ok) {
    let envelope: ApiEnvelope<never> | undefined
    try { envelope = (await response.json()) as ApiEnvelope<never> } catch { /* use the generic message */ }
    throw new ApiError(envelope?.error?.message ?? `Request failed (${response.status}).`, response.status, envelope?.error?.code, envelope?.error?.request_id)
  }
  const objectURL = URL.createObjectURL(await response.blob())
  try {
    const link = document.createElement('a')
    link.href = objectURL
    link.download = filename
    link.rel = 'noopener'
	document.body.append(link)
    link.click()
	link.remove()
  } finally {
    URL.revokeObjectURL(objectURL)
  }
}

export function buildAuditExportPath(input: import('./contracts').AuditExportInput): string {
  const query = new URLSearchParams({
    format: input.format,
    start: input.start,
    end: input.end,
    limit: String(input.limit ?? 100),
  })
  if (input.customer_id !== undefined) query.set('customer_id', String(input.customer_id))
  if (input.after_id !== undefined) query.set('after_id', String(input.after_id))
  if (input.through_id !== undefined) query.set('through_id', String(input.through_id))
  return `/audits/export?${query.toString()}`
}

async function downloadAuditExport(input: import('./contracts').AuditExportInput): Promise<void> {
  const path = buildAuditExportPath(input)
  if (!SAFE_PATH.test(path) || path.includes('..')) throw new ApiError('Unsafe API path was rejected.', 400, 'unsafe_path')
  const response = await fetch(`${API_PREFIX}${path}`, {
    credentials: 'include',
    cache: 'no-store',
    headers: { Accept: input.format === 'csv' ? 'text/csv' : 'application/json' },
  })
  if (!response.ok) {
    let envelope: ApiEnvelope<never> | undefined
    try { envelope = (await response.json()) as ApiEnvelope<never> } catch { /* use the generic message */ }
    throw new ApiError(envelope?.error?.message ?? `Request failed (${response.status}).`, response.status, envelope?.error?.code, envelope?.error?.request_id)
  }
  const objectURL = URL.createObjectURL(await response.blob())
  try {
    const link = document.createElement('a')
    link.href = objectURL
    link.download = `claw-admin-audits.${input.format}`
    link.rel = 'noopener'
    document.body.append(link)
    link.click()
    link.remove()
  } finally {
    URL.revokeObjectURL(objectURL)
  }
}

export const adminApi = {
  agentStoreItems: (signal?: AbortSignal) => apiRequest<import('./contracts').AgentStoreItem[]>('/agent-store/items?limit=100', { signal }),
  createAgentStoreItem: (body: import('./contracts').AgentStoreCreateInput) => apiRequest<import('./contracts').AgentStoreItem>('/agent-store/items', { method: 'POST', body }),
  updateAgentStoreItem: (itemId: string, body: import('./contracts').AgentStoreUpdateInput) => apiRequest<import('./contracts').AgentStoreItem>(`/agent-store/items/${encodeURIComponent(itemId)}`, { method: 'PATCH', body }),
  createAgentStoreDeployment: (itemId: string, body: import('./contracts').AgentStoreDeploymentCreateInput) => apiRequest<import('./contracts').AgentStoreItem>(`/agent-store/items/${encodeURIComponent(itemId)}/deployments`, { method: 'POST', body }),
  updateAgentStoreDeployment: (itemId: string, deploymentId: string, body: import('./contracts').AgentStoreDeploymentUpdateInput) => apiRequest<import('./contracts').AgentStoreItem>(`/agent-store/items/${encodeURIComponent(itemId)}/deployments/${encodeURIComponent(deploymentId)}`, { method: 'PATCH', body }),
  verifyAgentStoreDeployment: (itemId: string, deploymentId: string, expectedVersion: number, expectedDeploymentVersion: number) => apiRequest<import('./contracts').AgentStoreItem>(`/agent-store/items/${encodeURIComponent(itemId)}/deployments/${encodeURIComponent(deploymentId)}/verify`, { method: 'POST', body: { expected_version: expectedVersion, expected_deployment_version: expectedDeploymentVersion } }),
  disableAgentStoreDeployment: (itemId: string, deploymentId: string, expectedDeploymentVersion: number, reason: string) => apiRequest<import('./contracts').AgentStoreItem>(`/agent-store/items/${encodeURIComponent(itemId)}/deployments/${encodeURIComponent(deploymentId)}/disable`, { method: 'POST', body: { expected_deployment_version: expectedDeploymentVersion, reason } }),
  transitionAgentStoreItem: (itemId: string, action: 'publish' | 'unpublish' | 'disable' | 'archive', expectedVersion: number, reason = '') => apiRequest<import('./contracts').AgentStoreItem>(`/agent-store/items/${encodeURIComponent(itemId)}/${action}`, { method: 'POST', body: { expected_version: expectedVersion, reason } }),
  dashboard: (signal?: AbortSignal) => apiRequest<import('./contracts').Dashboard>('/dashboard', { signal }),
  customers: (signal?: AbortSignal) => apiRequest<import('./contracts').Customer[]>('/customers?limit=100', { signal }),
  customer: (id: number, signal?: AbortSignal) => apiRequest<import('./contracts').CustomerDetail>(`/customers/${id}`, { signal }),
  createCustomer: (body: unknown) => apiRequest<import('./contracts').Customer>('/customers', { method: 'POST', body }),
  updateCustomer: (id: number, body: import('./contracts').CustomerUpdateInput) => apiRequest<import('./contracts').Customer>(`/customers/${id}`, { method: 'PATCH', body }),
  addMember: (customerId: number, body: { new_api_user_id: number; role: import('./contracts').CustomerRole }) => apiRequest(`/customers/${customerId}/members`, { method: 'POST', body }),
  updateMember: (customerId: number, userId: number, body: { role: import('./contracts').CustomerRole; expected_auth_epoch: number }) => apiRequest(`/customers/${customerId}/members/${userId}`, { method: 'PATCH', body }),
  disableMember: (customerId: number, userId: number, reason: string) => apiRequest(`/customers/${customerId}/members/${userId}?reason=${encodeURIComponent(reason)}`, { method: 'DELETE' }),
  saveApp: (customerId: number, body: unknown) => apiRequest(`/customers/${customerId}/app`, { method: 'PUT', body }),
  verifyApp: (customerId: number, body: { expected_version: number; config_version: number }) => apiRequest(`/customers/${customerId}/app/verify`, { method: 'POST', body }),
  transitionApp: (customerId: number, action: string, body: unknown) => apiRequest(`/customers/${customerId}/app/${action}`, { method: 'POST', body }),
  createCustomerApp: (customerId: number, body: import('./contracts').AdditionalAppInput) => apiRequest<import('./contracts').AppDraftResult>(`/customers/${customerId}/apps`, { method: 'POST', body }),
  verifyCustomerApp: (customerId: number, selector: string, body: { expected_version: number; config_version: number }) => apiRequest<import('./contracts').AppVerification>(`/customers/${customerId}/apps/${encodeURIComponent(selector)}/verify`, { method: 'POST', body }),
  setDefaultCustomerApp: (customerId: number, selector: string, body: { expected_target_version: number; expected_current_default_version: number }) => apiRequest<import('./contracts').CustomerApp>(`/customers/${customerId}/apps/${encodeURIComponent(selector)}/default`, { method: 'POST', body }),
  transitionCustomerApp: (customerId: number, selector: string, action: string, body: { expected_version: number; reason: string }) => apiRequest<import('./contracts').CustomerApp>(`/customers/${customerId}/apps/${encodeURIComponent(selector)}/${encodeURIComponent(action)}`, { method: 'POST', body }),
  plans: (signal?: AbortSignal) => apiRequest<import('./contracts').PlanVersion[]>('/plan-catalog?limit=100', { signal }),
  createPlan: (body: unknown) => apiRequest<import('./contracts').PlanVersion>('/plan-catalog', { method: 'POST', body }),
  credentials: (signal?: AbortSignal, customerId?: number) => apiRequest<import('./contracts').CredentialProfile[]>(`/credential-profiles?limit=100${customerId === undefined ? '' : `&customer_id=${encodeURIComponent(customerId)}`}`, { signal }),
  createCredential: (body: unknown) => apiRequest<import('./contracts').CredentialProfile>('/credential-profiles', { method: 'POST', body }),
  stageCredentialRotation: (profileId: number, body: unknown) => apiRequest<import('./contracts').CredentialProfile>(`/credential-profiles/${profileId}/rotations`, { method: 'POST', body }),
  retireCredential: (profileId: number, body: unknown) => apiRequest<import('./contracts').CredentialProfile>(`/credential-profiles/${profileId}/retire`, { method: 'POST', body }),
  createPeriod: (customerId: number, body: unknown) => apiRequest<import('./contracts').PlanPeriod>(`/customers/${customerId}/plan-periods`, { method: 'POST', body }),
  confirmPayment: (periodId: number, body: unknown) => apiRequest(`/plan-periods/${periodId}/confirm-payment`, { method: 'POST', body }),
  cancelPeriod: (periodId: number, body: { expected_version: number; reason: string }) => apiRequest<import('./contracts').PlanPeriod>(`/plan-periods/${periodId}/cancel`, { method: 'POST', body }),
  invoices: (customerId: number, signal?: AbortSignal) => apiRequest<import('./contracts').Invoice[]>(`/customers/${customerId}/invoices?limit=100`, { signal }),
  voidInvoice: (invoiceId: number, body: { reason: string }) => apiRequest<import('./contracts').Invoice>(`/invoices/${invoiceId}/void`, { method: 'POST', body }),
  usageAudits: (signal?: AbortSignal) => apiRequest<import('./contracts').UsageAudit[]>('/usage-audits?limit=100', { signal }),
  billingImports: (signal?: AbortSignal) => apiRequest<import('./contracts').TencentBillingImport[]>('/tencent-billing-imports?limit=100', { signal }),
  createBillingImport: (body: { month: string; business_code: string }) => apiRequest<import('./contracts').TencentBillingImport>('/tencent-billing-imports', { method: 'POST', body }),
  retryBillingImport: (id: string) => apiRequest<import('./contracts').TencentBillingImport>(`/tencent-billing-imports/${encodeURIComponent(id)}/retry`, { method: 'POST' }),
  createUsageAudit: (body: unknown) => apiRequest<import('./contracts').UsageAudit>('/usage-audits', { method: 'POST', body }),
  reviewUsageAudit: (id: EntityId, body: unknown) => apiRequest<import('./contracts').UsageAudit>(`/usage-audits/${id}/review`, { method: 'POST', body }),
  reviseUsageAudit: (id: EntityId, body: unknown) => apiRequest<import('./contracts').UsageRevisionResult>(`/usage-audits/${id}/revisions`, { method: 'POST', body }),
  evidence: (signal?: AbortSignal) => apiRequest<import('./contracts').EvidenceMetadata[]>('/evidence?limit=100', { signal }),
  uploadEvidence: (file: File, customerId?: number) => {
    const form = new FormData()
    form.set('file', file)
    if (customerId !== undefined) form.set('customer_id', String(customerId))
    return apiFormRequest<import('./contracts').EvidenceMetadata>('/evidence', form)
  },
  downloadEvidence,
  marginReport: (customerId?: number, signal?: AbortSignal) => apiRequest<import('./contracts').MarginReport>(customerId === undefined ? '/margin-report' : `/margin-report?customer_id=${customerId}`, { signal }),
  audits: (signal?: AbortSignal) => apiRequest<import('./contracts').AdminAudit[]>('/audits?limit=100', { signal }),
  downloadAuditExport,
  notifications: (signal?: AbortSignal) => apiRequest<import('./contracts').GovernanceNotification[]>('/notifications?limit=100', { signal }),
  markNotificationRead: (notificationId: string) => apiRequest<import('./contracts').GovernanceNotification>(`/notifications/${encodeURIComponent(notificationId)}/read`, { method: 'POST' }),
  approvals: (status?: string, signal?: AbortSignal) => apiRequest<import('./contracts').GovernanceApproval[]>(`/approvals?limit=100${status ? `&status=${encodeURIComponent(status)}` : ''}`, { signal }),
  requestApproval: (body: {
    action_type: import('./contracts').ApprovalAction
    customer_id?: number
    resource_id?: number
    evidence_ref?: string
    request_key: string
    reason: string
    ttl_seconds: number
  }) => apiRequest<import('./contracts').GovernanceApproval>('/approvals', { method: 'POST', body }),
  decideApproval: (approvalId: string, action: 'approve' | 'reject' | 'execute', body: { expected_version: number; reason?: string }) => apiRequest<import('./contracts').GovernanceApproval>(`/approvals/${encodeURIComponent(approvalId)}/${action}`, { method: 'POST', body }),
  retentionPolicy: (customerId: number, signal?: AbortSignal) => apiRequest<import('./contracts').RetentionPolicy>(`/customers/${customerId}/retention-policy`, { signal }),
  setRetentionPolicy: (customerId: number, body: { expected_version: number; retention_days: number; legal_hold: boolean }) => apiRequest<import('./contracts').RetentionPolicy>(`/customers/${customerId}/retention-policy`, { method: 'PUT', body }),
  dryRunRetention: (customerId: number) => apiRequest<import('./contracts').RetentionRunView>(`/customers/${customerId}/retention-runs`, { method: 'POST' }),
  executeRetention: (runId: string, expectedVersion: number) => apiRequest<import('./contracts').RetentionRunView>(`/retention-runs/${encodeURIComponent(runId)}/execute`, { method: 'POST', body: { expected_version: expectedVersion } }),
  customerApps: (customerId: number, signal?: AbortSignal) => apiRequest<import('./contracts').CustomerApp[]>(`/customers/${customerId}/apps`, { signal }),
  prepareAppMigration: (customerId: number, body: import('./contracts').AppConfigInput) => apiRequest<import('./contracts').AppDraftResult>(`/customers/${customerId}/app-migrations`, { method: 'POST', body }),
  verifyAppMigration: (customerId: number, appId: number, body: { expected_version: number; config_version: number }) => apiRequest<import('./contracts').AppVerification>(`/customers/${customerId}/app-migrations/${appId}/verify`, { method: 'POST', body }),
}
