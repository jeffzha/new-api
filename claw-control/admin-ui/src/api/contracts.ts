export type EntityId = number | string
export type CustomerRole = 'owner' | 'admin' | 'member' | 'viewer'

export interface Limits {
  customer_concurrency: number
  user_concurrency: number
  max_runtime_seconds: number
  max_reasoning_rounds: number
  max_output_tokens: number
  web_search_per_turn: number
  max_file_bytes: number
}

export interface Customer {
  id: number
  customer_code: string
  display_name: string
  status: string
  billing_user_id?: number
  row_version: number
  created_at?: string
  updated_at?: string
}

export interface CustomerMember {
  id: number
  customer_id: number
  new_api_user_id: number
  role: CustomerRole
  status: string
  auth_epoch: number
  created_at?: string
}

export interface CustomerApp {
  id: number
  customer_id: number
  slot?: string
  selector?: string
  alias?: string
  provider_environment: string
  app_id: string
  display_name: string
  status: string
  auth_epoch?: number
  row_version: number
  current_config_version_id?: number
  pending_config_version_id?: number
  verified_at?: string
  created_at?: string
}

export interface AppConfigVersion {
  id: number
  config_version: number
  status: string
  region: string
  space_id: string
  template_agent_id: string
  credential_profile_id?: number
  credential_change_approval_id?: number
  row_version: number
  limits?: Limits
  capabilities?: string[]
}

export interface AppVerification {
  verification_id: string
  status?: string
  verified_at?: string
}

export interface PlanVersion {
  id: number
  plan_id?: number
  plan_code: string
  display_name: string
  version?: number
  monthly_price_cny: string
  currency?: string
  capabilities?: string[]
  limits?: Limits
  valid_from?: string
  valid_to?: string
}

export interface PlanPeriod {
  id: number
  customer_id: number
  plan_version_id: number
  start_at: string
  end_at: string
  amount_cny: string
  payment_status: string
  status: string
  row_version: number
}

export interface Invoice {
  id: number
  invoice_number: string
  period_id: number
  amount_cny: string
  status: string
  issued_at?: string
  paid_at?: string
}

export interface CredentialProfile {
  id: number
  owner_scope: string
  customer_id?: number
  provider_environment: string
  name: string
  fingerprint: string
  fingerprint_version: number
  status: string
  version: number
  row_version: number
  rotated_at?: string
  created_at?: string
  updated_at?: string
}

export interface AppDraftResult {
  app: CustomerApp
  config_version: AppConfigVersion
}

export interface GovernanceNotification {
  id: number
  notification_id: string
  customer_id: number
  type: string
  title: string
  message: string
  resource_type: string
  resource_id: string
  status: 'unread' | 'read' | string
  occurred_at: string
  read_at?: string
  read_by?: string
}

export interface RetentionPolicy {
  id: number
  customer_id: number
  retention_days: number
  legal_hold: boolean
  status: string
  row_version: number
  updated_by: string
  created_at: string
  updated_at: string
}

export interface RetentionCounts {
  resource_bindings: number
  control_sessions: number
  sso_tickets: number
  selection_nonces: number
  adp_account_bindings: number
  identity_bindings: number
  customer_members: number
  app_verifications: number
  app_configurations: number
  customer_apps: number
  delivered_outbox: number
  pending_outbox: number
  preserved_evidence: number
  preserved_invoices: number
  preserved_audits: number
  preserved_usage_audits: number
  preserved_plan_periods: number
}

export interface RetentionRun {
  id: number
  retention_run_id: string
  customer_id: number
  policy_id: number
  policy_version: number
  cutoff_at: string
  status: string
  row_version: number
  requested_by: string
  executed_by?: string
  executed_at?: string
  created_at: string
}

export interface RetentionRunView {
  run: RetentionRun
  counts: RetentionCounts
}

export type ApprovalAction = 'app_disable' | 'app_id_migration' | 'app_credential_change' | 'credential_rotation' | 'credential_rollback'

export interface GovernanceApproval {
  id: number
  approval_id: string
  request_key: string
  customer_id?: number
  action_type: ApprovalAction
  payload_hash: string
  status: string
  requested_by: string
  approved_by?: string
  rejected_by?: string
  executed_by?: string
  reason: string
  decision_reason?: string
  expires_at: string
  approved_at?: string
  rejected_at?: string
  executed_at?: string
  row_version: number
  created_at: string
  updated_at: string
}

export interface AuditExportInput {
  format: 'csv' | 'json'
  start: string
  end: string
  customer_id?: number
  after_id?: number
  through_id?: number
  limit?: number
}

export interface UsageAudit {
  id: number
  public_id?: string
  audit_id?: string
  customer_id?: number
  customer_app_id?: number
  plan_period_id?: number
  period_start: string
  period_end: string
  source: string
  allocation_confidence: string
  upstream_cost_cny: string
  status: string
  row_version: number
  evidence_ref?: string
  created_at?: string
}

export interface UsageAuditRevision {
  id: number
  revision_id: string
  original_audit_id: number
  replacement_audit_id: number
  reason: string
  created_by: string
  applied_at: string
}

export interface UsageRevisionResult {
  revision: UsageAuditRevision
  replacement: UsageAudit
}

export interface TencentBillingImport {
  import_id: string
  month: string
  business_code: string
  status: string
  attempt_count: number
  max_attempts: number
  detail_page_count: number
  detail_record_count: number
  upstream_cost_cny: string
  manual_review_required: boolean
  review_reasons: string[]
  bill_query_request_ids: string[]
  adjust_query_request_ids: string[]
  evidence_ref?: string
  evidence_hash?: string
  usage_audit_id?: string
  error_code?: string
  requested_by: string
  request_id?: string
  started_at?: string
  completed_at?: string
  created_at: string
  updated_at: string
  account_scoped: true
  allocation_confidence: 'unverified'
  invoice_mutation: false
}

export interface EvidenceMetadata {
  evidence_ref: string
  customer_id?: number
  filename: string
  mime_type: string
  size_bytes: number
  content_sha256: string
  status: string
  created_by: string
  created_at: string
}

export interface MarginBreakdown {
  app_exact_cost_cny: string
  app_exact_records: number
  estimated_allocation_cost_cny: string
  estimated_allocation_records: number
  account_only_cost_cny: string
  account_only_records: number
  unverified_cost_cny: string
  unverified_records: number
}

export interface MarginReport {
  scope: 'customer' | 'platform'
  customer_id?: number
  start_at?: string
  end_at?: string
  invoiced_revenue_cny: string
  reviewed_cost_cny: string
  margin_cny: string
  confidence: 'app_exact' | 'estimated_allocation' | 'account_only' | 'unverified'
  account_only_allocated_to_customer: false
  formula: string
  breakdown: MarginBreakdown
}

export interface AdminAudit {
  id: number
  actor: string
  action: string
  resource_type: string
  resource_id: string
  result?: string
  reason?: string
  request_id?: string
  created_at: string
}

export interface Dashboard {
  customers_total: number
  customers_active: number
  apps_active: number
  plans_expiring: number
  usage_audits_pending: number
  recent_audits?: AdminAudit[]
}

export interface CustomerDetail {
  customer: Customer
  members: CustomerMember[]
  app?: CustomerApp
  current_config?: AppConfigVersion
  pending_config?: AppConfigVersion
  verifications?: AppVerification[]
  plan_periods: PlanPeriod[]
  invoices?: Invoice[]
}

export interface ApiEnvelope<T> {
  success: boolean
  data?: T
  error?: {
    code?: string
    message?: string
    request_id?: string
  }
}

export interface AppConfigInput {
  expected_version: number
  provider_environment: string
  region: string
  space_id: string
  app_id: string
  template_agent_id: string
  credential_profile_id: number
  app_key?: string
  display_name: string
  limits: Limits
  capabilities: string[]
}

export interface CustomerUpdateInput {
  expected_version: number
  display_name: string
  billing_user_id: number | null
}

export interface AgentStoreVersion {
  version_id: string
  generation: number
  display_name: string
  summary: string
  description: string
  avatar_url?: string
  category: string
  tags: string[]
}

export interface AgentStoreDeployment {
  deployment_id: string
  customer_id: number
  customer_app_id: number
  status: string
  row_version: number
  provider_app_mode: number
  runtime_profile: string
  dynamic_agent_config: boolean
  execution_enabled: boolean
  verified_config_version: number
  verified_at?: string
  provider_display_name?: string
  provider_description?: string
  provider_avatar_url?: string
  capabilities: string[]
  entitlements: AgentStoreEntitlement[]
}

export interface AgentStoreEntitlement {
  entitlement_id: string
  deployment_id: string
  subject_type: 'customer' | 'user' | 'role' | 'plan'
  subject_ref: string
  status: string
  valid_from: string
  valid_until?: string
}

export interface AgentStoreItem {
  item_id: string
  slug: string
  status: string
  row_version: number
  sort_order: number
  featured: boolean
  current_version?: AgentStoreVersion
  draft_version?: AgentStoreVersion
  deployments: AgentStoreDeployment[]
  /** @deprecated First-deployment compatibility projection. */
  deployment: AgentStoreDeployment
  /** @deprecated First-deployment compatibility projection. */
  entitlements: AgentStoreEntitlement[]
}

export interface AgentStoreEntitlementInput {
  subject_type: AgentStoreEntitlement['subject_type']
  subject_ref: string
  valid_from?: string
  valid_until?: string
}

export interface AgentStoreCreateInput {
  slug: string
  display_name: string
  summary: string
  description: string
  avatar_url: string
  category: string
  tags: string[]
  sort_order: number
  featured: boolean
  deployment: {
    customer_id: number
    customer_app_id: number
    execution_enabled: false
  }
  entitlements: []
}

export interface AgentStoreUpdateInput {
  expected_version: number
  display_name: string
  summary: string
  description: string
  avatar_url: string
  category: string
  tags: string[]
  sort_order: number
  featured: boolean
}

export interface AgentStoreDeploymentCreateInput {
  expected_item_version: number
  customer_id: number
  customer_app_id: number
  entitlements: AgentStoreEntitlementInput[]
}

export interface AgentStoreDeploymentUpdateInput {
  expected_deployment_version: number
  execution_enabled: boolean
  entitlements?: AgentStoreEntitlementInput[]
}
