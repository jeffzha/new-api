import { afterEach, describe, expect, it, vi } from 'vitest'

import { adminApi, ApiError, buildAuditExportPath, buildRequestInit, parseCookie } from './client'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('administrative request authentication', () => {
  it('parses the exact CSRF cookie without exposing other cookies', () => {
    expect(parseCookie('token=private; claw_admin_csrf=csrf%20value; theme=dark', 'claw_admin_csrf')).toBe('csrf value')
    expect(parseCookie('claw_admin_csrf_extra=nope', 'claw_admin_csrf')).toBeUndefined()
  })

  it('adds credentials and double-submit CSRF only to mutations', () => {
    const request = buildRequestInit('POST', { enabled: true }, 'claw_admin_csrf=csrf-token')
    const headers = request.headers as Headers
    expect(request.credentials).toBe('include')
    expect(headers.get('X-CSRF-Token')).toBe('csrf-token')
    expect(headers.get('Authorization')).toBeNull()
  })

  it('does not require CSRF for safe reads', () => {
    const request = buildRequestInit('GET', undefined, '')
    expect((request.headers as Headers).get('X-CSRF-Token')).toBeNull()
  })

  it('fails closed when a mutation has no CSRF cookie', () => {
    expect(() => buildRequestInit('DELETE', undefined, 'theme=dark')).toThrow(ApiError)
  })

  it('redirects a sensitive mutation to the same-origin administrator step-up flow', async () => {
    const assign = vi.fn()
    vi.stubGlobal('document', { cookie: 'claw_admin_csrf=csrf-token' })
    vi.stubGlobal('window', { location: { assign } })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      success: false,
      error: { code: 'recent_auth_required', message: 'recent administrator authentication is required' },
    }), { status: 403, headers: { 'Content-Type': 'application/json' } })))

    await expect(adminApi.createCustomer({ customer_code: 'test' })).rejects.toMatchObject({
      status: 403,
      code: 'recent_auth_required',
    })
    expect(assign).toHaveBeenCalledWith('/workbench-admin?step_up=1')
  })

  it('submits only optimistic-lock versions to the trusted verifier endpoint', async () => {
    vi.stubGlobal('document', { cookie: 'claw_admin_csrf=csrf-token' })
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ success: true, data: { verification_id: 'verify_1' } }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await adminApi.verifyApp(42, { expected_version: 4, config_version: 2 })

    expect(fetchMock).toHaveBeenCalledOnce()
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/admin/workbench/customers/42/app/verify')
    expect(JSON.parse(String(init.body))).toEqual({ expected_version: 4, config_version: 2 })
    expect((init.headers as Headers).get('X-CSRF-Token')).toBe('csrf-token')
    expect((init.headers as Headers).get('Authorization')).toBeNull()
  })

  it('creates and verifies a distinct customer App without mutating the primary App endpoint', async () => {
    vi.stubGlobal('document', { cookie: 'claw_admin_csrf=csrf-token' })
    const fetchMock = vi.fn().mockImplementation(async () => new Response(JSON.stringify({ success: true, data: { app: { id: 18 }, config_version: { config_version: 1 } } }), {
      status: 201,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await adminApi.createCustomerApp(42, {
      alias: 'gaokao-zhiyuan-wuyou',
      provider_environment: 'china_tencent_adp',
      region: 'ap-guangzhou',
      space_id: 'default_space',
      app_id: '2048342527164967296',
      template_agent_id: '',
      credential_profile_id: 5,
      app_key: 'fixture-app-key-value',
      display_name: 'Gaokao application',
      limits: { customer_concurrency: 10, user_concurrency: 2, max_runtime_seconds: 300, max_reasoning_rounds: 20, max_output_tokens: 8192, web_search_per_turn: 0, max_file_bytes: 0 },
      capabilities: ['chat'],
    })
    await adminApi.saveCustomerApp(42, 'aps_public', {
      expected_version: 2,
      provider_environment: 'china_tencent_adp',
      region: 'ap-guangzhou',
      space_id: 'default_space',
      app_id: '2048342527164967296',
      template_agent_id: '',
      credential_profile_id: 5,
      app_key: 'corrected-app-key-value',
      display_name: 'Gaokao application',
      limits: { customer_concurrency: 10, user_concurrency: 2, max_runtime_seconds: 300, max_reasoning_rounds: 20, max_output_tokens: 8192, web_search_per_turn: 0, max_file_bytes: 0 },
      capabilities: ['chat'],
    })
    await adminApi.verifyCustomerApp(42, 'aps_public', { expected_version: 1, config_version: 1 })
    await adminApi.transitionCustomerApp(42, 'aps_public', 'enable', { expected_version: 2, reason: '' })
    await adminApi.setDefaultCustomerApp(42, 'aps_public', { expected_target_version: 2, expected_current_default_version: 7 })

    const calls = fetchMock.mock.calls.map(([url, init]) => ({
      url,
      body: JSON.parse(String((init as RequestInit).body)),
    }))
    expect(calls[0]?.url).toBe('/api/admin/workbench/customers/42/apps')
    expect(calls[0]?.body).toMatchObject({ alias: 'gaokao-zhiyuan-wuyou', app_id: '2048342527164967296' })
    expect(calls[1]).toEqual({
      url: '/api/admin/workbench/customers/42/apps/aps_public',
      body: expect.objectContaining({ expected_version: 2, app_key: 'corrected-app-key-value' }),
    })
    expect(calls[2]).toEqual({
      url: '/api/admin/workbench/customers/42/apps/aps_public/verify',
      body: { expected_version: 1, config_version: 1 },
    })
    expect(calls[3]).toEqual({
      url: '/api/admin/workbench/customers/42/apps/aps_public/enable',
      body: { expected_version: 2, reason: '' },
    })
    expect(calls[4]).toEqual({
      url: '/api/admin/workbench/customers/42/apps/aps_public/default',
      body: { expected_target_version: 2, expected_current_default_version: 7 },
    })
    expect(calls.some(({ url }) => url === '/api/admin/workbench/customers/42/app')).toBe(false)
  })

  it('keeps Agent Store deployment and entitlement mutations scoped by CAS versions', async () => {
    vi.stubGlobal('document', { cookie: 'claw_admin_csrf=csrf-token' })
    const fetchMock = vi.fn().mockImplementation(async () => new Response(JSON.stringify({ success: true, data: { item_id: 'aci_1', deployments: [] } }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await adminApi.createAgentStoreDeployment('aci_1', {
      expected_item_version: 3,
      customer_id: 42,
      customer_app_id: 17,
      entitlements: [{ subject_type: 'user', subject_ref: '101' }],
    })
    await adminApi.updateAgentStoreDeployment('aci_1', 'agd_2', {
      expected_deployment_version: 5,
      execution_enabled: true,
      entitlements: [{ subject_type: 'role', subject_ref: 'member' }],
    })
    await adminApi.verifyAgentStoreDeployment('aci_1', 'agd_2', 4, 6)
    await adminApi.disableAgentStoreDeployment('aci_1', 'agd_2', 7, 'customer offboarding')

    const calls = fetchMock.mock.calls.map(([url, init]) => ({
      url,
      body: JSON.parse(String((init as RequestInit).body)),
    }))
    expect(calls).toEqual([
      {
        url: '/api/admin/workbench/agent-store/items/aci_1/deployments',
        body: { expected_item_version: 3, customer_id: 42, customer_app_id: 17, entitlements: [{ subject_type: 'user', subject_ref: '101' }] },
      },
      {
        url: '/api/admin/workbench/agent-store/items/aci_1/deployments/agd_2',
        body: { expected_deployment_version: 5, execution_enabled: true, entitlements: [{ subject_type: 'role', subject_ref: 'member' }] },
      },
      {
        url: '/api/admin/workbench/agent-store/items/aci_1/deployments/agd_2/verify',
        body: { expected_version: 4, expected_deployment_version: 6 },
      },
      {
        url: '/api/admin/workbench/agent-store/items/aci_1/deployments/agd_2/disable',
        body: { expected_deployment_version: 7, reason: 'customer offboarding' },
      },
    ])
    for (const [, init] of fetchMock.mock.calls as [string, RequestInit][]) {
      expect((init.headers as Headers).get('X-CSRF-Token')).toBe('csrf-token')
      expect((init.headers as Headers).get('Authorization')).toBeNull()
    }
  })

  it('cancels a plan period with an optimistic lock and explicit reason', async () => {
    vi.stubGlobal('document', { cookie: 'claw_admin_csrf=csrf-token' })
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ success: true, data: { id: 9, status: 'canceled' } }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await adminApi.cancelPeriod(9, { expected_version: 3, reason: 'contract ended' })

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/admin/workbench/plan-periods/9/cancel')
    expect(JSON.parse(String(init.body))).toEqual({ expected_version: 3, reason: 'contract ended' })
    expect((init.headers as Headers).get('X-CSRF-Token')).toBe('csrf-token')
  })

  it('voids an invoice with an explicit accounting reason', async () => {
    vi.stubGlobal('document', { cookie: 'claw_admin_csrf=csrf-token' })
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ success: true, data: { id: 12, status: 'void' } }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await adminApi.voidInvoice(12, { reason: 'contract canceled' })

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/admin/workbench/invoices/12/void')
    expect(JSON.parse(String(init.body))).toEqual({ reason: 'contract canceled' })
  })

  it('creates an immutable replacement for a locked usage audit', async () => {
    vi.stubGlobal('document', { cookie: 'claw_admin_csrf=csrf-token' })
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ success: true, data: { revision: { revision_id: 'usage_revision_1' } } }), {
      status: 201,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await adminApi.reviseUsageAudit('usage_1', {
      expected_version: 2,
      allocation_confidence: 'account_only',
      upstream_cost_cny: '10.25',
      usage: { runtime_minutes: '50' },
      evidence_ref: 'evidence_2',
      evidence_hash: `sha256:${'a'.repeat(64)}`,
      reason: 'provider correction',
    })

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/admin/workbench/usage-audits/usage_1/revisions')
    expect(JSON.parse(String(init.body))).toMatchObject({ expected_version: 2, reason: 'provider correction' })
  })

  it('builds a bounded audit export against the session-only endpoint', () => {
    const path = buildAuditExportPath({
      format: 'csv',
      start: '2026-08-08T00:00:00.000Z',
      end: '2026-08-09T00:00:00.000Z',
      customer_id: 42,
      after_id: 10,
      through_id: 99,
      limit: 100,
    })
    const query = new URL(`https://admin.invalid${path}`).searchParams
    expect(path.startsWith('/audits/export?')).toBe(true)
    expect(Object.fromEntries(query)).toEqual({
      format: 'csv',
      start: '2026-08-08T00:00:00.000Z',
      end: '2026-08-09T00:00:00.000Z',
      limit: '100',
      customer_id: '42',
      after_id: '10',
      through_id: '99',
    })
  })

  it('uses the exact approval action contract and never sends an actor header', async () => {
    vi.stubGlobal('document', { cookie: 'claw_admin_csrf=csrf-token' })
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ success: true, data: { approval_id: 'apr_1' } }), {
      status: 201,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await adminApi.requestApproval({
      action_type: 'app_id_migration', customer_id: 42, resource_id: 18,
      evidence_ref: 'evidence_1', request_key: 'migration-42-18', reason: 'approved maintenance', ttl_seconds: 900,
    })

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/admin/workbench/approvals')
    expect(JSON.parse(String(init.body))).toEqual({
      action_type: 'app_id_migration', customer_id: 42, resource_id: 18,
      evidence_ref: 'evidence_1', request_key: 'migration-42-18', reason: 'approved maintenance', ttl_seconds: 900,
    })
    expect((init.headers as Headers).get('X-Claw-Actor')).toBeNull()
  })

  it('keeps legal hold and optimistic locking in the retention policy request', async () => {
    vi.stubGlobal('document', { cookie: 'claw_admin_csrf=csrf-token' })
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ success: true, data: { id: 7 } }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await adminApi.setRetentionPolicy(42, { expected_version: 3, retention_days: 365, legal_hold: true })

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/admin/workbench/customers/42/retention-policy')
    expect(init.method).toBe('PUT')
    expect(JSON.parse(String(init.body))).toEqual({ expected_version: 3, retention_days: 365, legal_hold: true })
  })

  it('verifies a migration candidate without accepting administrator-supplied evidence', async () => {
    vi.stubGlobal('document', { cookie: 'claw_admin_csrf=csrf-token' })
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ success: true, data: { verification_id: 'verify_2' } }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await adminApi.verifyAppMigration(42, 18, { expected_version: 2, config_version: 7 })

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/admin/workbench/customers/42/app-migrations/18/verify')
    expect(JSON.parse(String(init.body))).toEqual({ expected_version: 2, config_version: 7 })
  })

  it('creates a Billing import with scope only and never accepts credentials or attribution', async () => {
    vi.stubGlobal('document', { cookie: 'claw_admin_csrf=csrf-token' })
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ success: true, data: { import_id: 'billing_import_1' } }), {
      status: 202,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await adminApi.createBillingImport({ month: '2026-08', business_code: 'p_adp' })

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/admin/workbench/tencent-billing-imports')
    expect(JSON.parse(String(init.body))).toEqual({ month: '2026-08', business_code: 'p_adp' })
    expect(String(init.body)).not.toMatch(/secret|payer|customer|app_id/i)
    expect((init.headers as Headers).get('X-CSRF-Token')).toBe('csrf-token')
  })

  it('filters credential profiles by customer while keeping owner scope explicit', async () => {
    vi.stubGlobal('document', { cookie: 'claw_admin_csrf=csrf-token' })
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ success: true, data: [] }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await adminApi.credentials(undefined, 42)
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/admin/workbench/credential-profiles?limit=100&customer_id=42')

    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ success: true, data: { id: 9 } }), {
      status: 201,
      headers: { 'Content-Type': 'application/json' },
    }))
    await adminApi.createCredential({
      owner_scope: 'customer:42', customer_id: 42, provider_environment: 'china_tencent_adp', name: 'customer-profile',
      secret_id_ref: 'env://WORKBENCH_PROVIDER_CUSTOMER_ID', secret_key_ref: 'env://WORKBENCH_PROVIDER_CUSTOMER_KEY',
      fingerprint: 'sha256:fixture',
    })
    const [url, init] = fetchMock.mock.calls[1] as [string, RequestInit]
    expect(url).toBe('/api/admin/workbench/credential-profiles')
    expect(JSON.parse(String(init.body))).toMatchObject({ owner_scope: 'customer:42', customer_id: 42 })
  })

  it('uses the unified Agent Store verify and atomic publish contracts', async () => {
    vi.stubGlobal('document', { cookie: 'claw_admin_csrf=csrf-token' })
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ success: true, data: { verification: { result: 'verified' } } }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await adminApi.verifyUnifiedAgentStoreListing({
      slug: 'gaokao-zhiyuan-wuyou', display_name: '高考志愿无忧', summary: 'summary', description: '', avatar_url: '', category: '教育', tags: ['高考'], sort_order: 20, featured: false,
      audience_scope: 'all_customers', selected_customer_ids: [], provider_environment: 'china_tencent_adp', region: 'ap-guangzhou', space_id: 'default_space', app_id: '2048342527164967296', app_key: 'write-only-app-key', template_agent_id: '', credential_profile_id: 3,
      capabilities: ['chat'], limits: { customer_concurrency: 10, user_concurrency: 1, max_runtime_seconds: 900, max_reasoning_rounds: 20, max_output_tokens: 8192, web_search_per_turn: 0, max_file_bytes: 0 },
    })
    const [verifyURL, verifyInit] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(verifyURL).toBe('/api/admin/workbench/agent-store/listings/verify')
    expect(JSON.parse(String(verifyInit.body))).toMatchObject({ audience_scope: 'all_customers', app_id: '2048342527164967296', app_key: 'write-only-app-key' })

    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ success: true, data: { item_id: 'agi_1' } }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    await adminApi.publishUnifiedAgentStoreListing('agi_1', 4, 2)
    const [publishURL, publishInit] = fetchMock.mock.calls[1] as [string, RequestInit]
    expect(publishURL).toBe('/api/admin/workbench/agent-store/items/agi_1/listing/publish')
    expect(JSON.parse(String(publishInit.body))).toEqual({ expected_version: 4, expected_deployment_version: 2 })
    expect(String(publishInit.body)).not.toMatch(/app_key|secret/i)
  })
})
