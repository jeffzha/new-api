# Admin SPA API contract

Every endpoint is same-origin, requires `claw_admin_session`, and returns an envelope:

```json
{ "success": true, "data": {} }
```

Mutations additionally require the `claw_admin_csrf` cookie value in `X-CSRF-Token`. The SPA never sends `Authorization` or `X-Claw-Actor`.

All mutations also require a recent new-api step-up. A missing or expired proof returns HTTP 403 with `error.code=recent_auth_required`; the SPA must navigate to `/workbench-admin?step_up=1`, complete password, 2FA, or Passkey verification on new-api, and enter the returned one-time ticket through `/api/workbench/entry`. Passwords are never sent to claw-control.

## Read projections required by the SPA

### `GET /api/admin/workbench/dashboard`

```json
{
  "success": true,
  "data": {
    "customers_total": 12,
    "customers_active": 10,
    "apps_active": 8,
    "plans_expiring": 2,
    "usage_audits_pending": 3,
    "recent_audits": []
  }
}
```

### `GET /api/admin/workbench/customers/{id}`

```json
{
  "success": true,
  "data": {
    "customer": {},
    "members": [],
    "app": null,
    "current_config": null,
    "pending_config": null,
    "verifications": [],
    "plan_periods": [],
    "invoices": []
  }
}
```

`app`, `current_config`, `pending_config`, and `verifications` must not expose Secret values, internal Secret references, or fingerprints. App configuration mutations accept a write-only `app_key`; an empty value on an existing App preserves its current key, while a non-empty value creates a new encrypted vault version. The SPA must never render a field for an existing reference. Config `limits` is the typed seven-field object and `capabilities` is a string array. `current_config` is the active verified version; `pending_config` is the draft eligible for verification. They must never be conflated.

### Collection projections

- `GET /api/admin/workbench/plan-catalog?limit=100` → `data: PlanVersion[]` directly; each entry is a flat plan-version projection, not `{ plan, version }`
- `GET /api/admin/workbench/credential-profiles?limit=100[&customer_id={id}]` → `data: CredentialProfile[]`; a customer filter returns platform profiles plus profiles owned by that customer
- `GET /api/admin/workbench/audits?limit=100` → `data: AdminAudit[]`

Credential responses include `owner_scope` (`platform` or `customer:{id}`), optional
`customer_id`, fingerprints, status, and version, but never Secret references or
resolved values. The safe projection also includes `row_version` for rotation
and retirement optimistic locking; it is distinct from immutable credential
`version`. `POST /credential-profiles` accepts an explicit matching
`owner_scope`/`customer_id`; omitting both preserves the platform-level legacy
behavior. Rotation and retirement responses use the same safe projection. An App
configuration may select only a platform profile or a profile owned by that exact
customer. Collection responses preserve the array in `data` and add pagination
metadata without changing existing consumers:

```json
{
  "success": true,
  "data": [],
  "meta": { "next_cursor": "opaque-or-empty" }
}
```

Clients send the opaque value back as `cursor`; it is bound to the route and
active filters and must not be combined with legacy `before_id`. The default
page size is 100 and the hard maximum is 200. This applies to customers,
customer Apps, plan catalog, invoices, usage/admin audits, credential profiles,
evidence, notifications, approvals, Tencent billing imports, and Agent Store
items/audits. Agent Store item ordering uses `(sort_order,item_id)`; all numeric
control-plane tables use descending immutable ID keysets. Existing notification
and approval `before_id` requests remain accepted for compatibility.

### Multi-App administration

- `GET /api/admin/workbench/customers/{id}/apps` returns all App profiles with
  stable `selector`, customer-unique `alias`, `slot`, lifecycle status, and row
  version. It also projects the numeric `current_config_version` and
  `pending_config_version` needed by read-only verification forms. It never
  returns AppKey or credential secret references.
- `POST /api/admin/workbench/customers/{id}/apps` creates a non-default App and
  its first immutable draft. It accepts the normal App configuration fields
  plus a lowercase `alias`; the server generates the selector. The credential
  profile must exactly match the primary App's current config, preventing this
  first draft from bypassing credential-change approval.
- `POST /api/admin/workbench/customers/{id}/apps/{selector}/verify` accepts only
  `expected_version` and `config_version` and runs trusted verification.
- `POST /api/admin/workbench/customers/{id}/apps/{selector}/{enable|suspend|disable|prepare}`
  applies the lifecycle transition to that exact App record. It does not change
  the primary slot or mutate another App.
- `POST /api/admin/workbench/customers/{id}/apps/{selector}/default` accepts
  `expected_target_version` and `expected_current_default_version`. It swaps the
  `primary` slot, bumps both App authorization epochs, invalidates cached
  contexts, and audits both sides without changing selectors or aliases.

### Agent Store deployments and entitlements

`GET /api/admin/workbench/agent-store/items?limit=100` returns logical catalog
items. Each item has `deployments: AgentStoreDeployment[]`, and each deployment
has its own `entitlements: AgentStoreEntitlement[]`. The legacy top-level
`deployment` and `entitlements` fields mirror the first deployment only and are
read-only compatibility projections; new UI code must render `deployments`.
Provider credentials and Secret references never appear in this projection.

Every mutation below requires the administrator session, double-submit CSRF,
and recent administrator authentication. All writes use optimistic locking:

- `POST /api/admin/workbench/agent-store/items/{item_id}/deployments` accepts
  `{ expected_item_version, customer_id, customer_app_id, entitlements }` and
  returns the updated item with HTTP 201.
- `PATCH /api/admin/workbench/agent-store/items/{item_id}/deployments/{deployment_id}`
  accepts `{ expected_deployment_version, execution_enabled, entitlements? }`.
  Omitting `entitlements` preserves them; sending an empty array restores the
  default customer entitlement. Execution remains disabled unless the deployment
  is verified or active and its runtime profile is one of `standard_v2`,
  `multi_agent_v2`, `workflow_v2`, `claw_static_v2`, or `claw_dynamic_v2`;
  unknown profiles fail closed.
- `POST /api/admin/workbench/agent-store/items/{item_id}/deployments/{deployment_id}/verify`
  accepts `{ expected_version, expected_deployment_version }`. Provider AppMode,
  runtime profile, presentation data and capabilities are trusted readback and
  cannot be supplied by the browser.
- `POST /api/admin/workbench/agent-store/items/{item_id}/deployments/{deployment_id}/disable`
  accepts `{ expected_deployment_version, reason }`. It disables only that
  customer deployment, revokes its active authorization and prevents direct
  re-enabling; recovery requires trusted verification followed by an explicit
  execution enable.

Entitlement inputs contain `subject_type` (`customer`, `user`, `role`, or
`plan`), `subject_ref`, and optional ISO-8601 `valid_from`/`valid_until`. A
customer subject must match the deployment customer. Duplicate subjects and an
end time that is not later than the start time are rejected.

## Customer workbench context selection

`GET /api/workbench/entry?ticket=...` preserves the existing redirect for a
single eligible membership. When more than one membership is eligible it sets
only a restricted `claw_control_session` cookie and returns:

```json
{
  "success": true,
  "data": {
    "surface": "workbench",
    "selection_required": true,
    "selections": [{
      "selection_token": "one-time-opaque-token",
      "expires_at": "2026-08-09T12:02:00Z",
      "customer_code": "customer-a",
      "customer_display_name": "Customer A",
      "role": "member",
      "app_selector": "aps_...",
      "app_alias": "primary",
      "app_display_name": "Customer A Claw",
      "app_status": "active",
      "is_default": true,
      "access_mode": "active"
    }]
  }
}
```

- `GET /api/workbench/selections` uses the HttpOnly control-session cookie and
  returns freshly issued safe candidates.
- `POST /api/workbench/selections/choose` accepts exactly
  `{ "selection_token": "..." }`; it does not accept `customer_id`, App ID, or
  config version. Success sets the SSO browser-binding cookie and returns a
  short-lived `redirect_url` for the selected ADP context.
- Internal `/authz` and `/app-context` callers include the selected
  `app_profile_id` and `config_version` returned by SSO ticket consumption.
  Omission is backward-compatible only for a customer with one App context.

## Existing endpoints used

| Method | Endpoint | Purpose |
|---|---|---|
| GET/POST | `/api/admin/workbench/customers` | list/create customers |
| PATCH | `/api/admin/workbench/customers/{id}` | update display name and nullable billing user ID with `expected_version` |
| POST | `/api/admin/workbench/customers/{id}/members` | add member |
| PATCH | `/api/admin/workbench/customers/{id}/members/{user_id}` | update `owner/admin/member/viewer` role with `expected_auth_epoch` |
| DELETE | `/api/admin/workbench/customers/{id}/members/{user_id}?reason=...` | disable member |
| PUT | `/api/admin/workbench/customers/{id}/app` | create immutable App config draft |
| POST | `/api/admin/workbench/customers/{id}/app/verify` | request trusted Tencent verification using only `expected_version` and `config_version` |
| GET/POST | `/api/admin/workbench/customers/{id}/apps` | list or add stable selectable App/Space profiles |
| POST | `/api/admin/workbench/customers/{id}/apps/{selector}/verify` | verify a non-default App draft |
| POST | `/api/admin/workbench/customers/{id}/apps/{selector}/{enable\|suspend\|disable\|prepare}` | lifecycle transition for one exact additional App |
| POST | `/api/admin/workbench/customers/{id}/apps/{selector}/default` | atomically set the explicit default App |
| POST | `/api/admin/workbench/customers/{id}/app/{enable\|suspend\|disable\|prepare}` | App lifecycle |
| GET/POST | `/api/admin/workbench/plan-catalog` | list/publish immutable plan versions |
| GET/POST | `/api/admin/workbench/credential-profiles` | list/create secret-reference profiles |
| POST | `/api/admin/workbench/customers/{id}/plan-periods` | create customer period |
| POST | `/api/admin/workbench/plan-periods/{id}/confirm-payment` | confirm manual payment |
| GET | `/api/admin/workbench/customers/{id}/invoices?limit=100` | list fixed-plan invoices |
| GET/POST | `/api/admin/workbench/usage-audits` | list/create usage audits |
| POST | `/api/admin/workbench/usage-audits/{id}/review` | review and lock evidence |
| GET/POST | `/api/admin/workbench/tencent-billing-imports` | list/create an asynchronous account-scoped Tencent Fee Center import |
| GET | `/api/admin/workbench/tencent-billing-imports/{id}` | get safe import status, query RequestIds, and draft/evidence references |
| POST | `/api/admin/workbench/tencent-billing-imports/{id}/retry` | retry only a failed import without changing its idempotent scope |
| GET | `/api/admin/workbench/notifications?limit=100` | list governance notifications |
| POST | `/api/admin/workbench/notifications/{notification_id}/read` | mark a notification read |
| GET/POST | `/api/admin/workbench/approvals` | list/request two-person approvals |
| POST | `/api/admin/workbench/approvals/{approval_id}/{approve\|reject\|execute}` | decide or execute an approval with `expected_version` |
| GET/PUT | `/api/admin/workbench/customers/{id}/retention-policy` | read/save retention and legal-hold policy |
| POST | `/api/admin/workbench/customers/{id}/retention-runs` | create an eligible cleanup dry-run |
| POST | `/api/admin/workbench/retention-runs/{run_id}/execute` | execute the exact dry-run with `expected_version` |
| POST | `/api/admin/workbench/customers/{id}/app-migrations` | prepare a separate immutable migration App draft |
| POST | `/api/admin/workbench/customers/{id}/app-migrations/{app_id}/verify` | run trusted migration-candidate verification |
| GET | `/api/admin/workbench/audits/export` | download bounded CSV/JSON audit export using an administrator session |

Errors use `{ "success": false, "error": { "code", "message", "request_id" } }`. The UI displays the sanitized message and request ID only.

The App verification request is exactly:

```json
{ "expected_version": 4, "config_version": 2 }
```

The administrator cannot provide `result`, AppMode, release status, Agent status, provider request IDs, response hashes, or evidence. The trusted claw-control verifier derives and persists all of them from Tencent's authenticated response.

When a pending configuration changes `credential_profile_id` and the App
already has a current configuration, the UI first requests an
`app_credential_change` approval with `customer_id` and the pending config ID as
`resource_id`. A second administrator approves it and an administrator executes
it on the Governance page. Verification remains fail-closed until execution;
the backend binds and rechecks the exact App/config/credential IDs and versions.

The bootstrap bearer token cannot request, approve, reject, or execute a
governance approval; those mutations require a revocable administrator session.
The backend unconditionally replaces `X-Claw-Actor` with the authenticated
session identity, so browser input cannot forge the audit actor.

Legacy fingerprint re-enrollment is not a browser/admin-session API even though
its maintenance path remains under `/api/admin/workbench/`. It accepts only a
direct loopback connection plus the bootstrap bearer token and records a fixed
server actor. New additional Apps must initially use the primary App's current
credential; a different credential is a later `app_credential_change` flow.

## Fail-closed UI boundaries

The credential list projection deliberately omits Secret references and resolved
values. It currently also omits the optimistic-lock `row_version` required by
`POST /credential-profiles/{profile_id}/rotations`. The SPA therefore shows
credential lifecycle status and permits two-person activation/rollback approval
for already staged or activated rotations, but does not guess the row version or
substitute the immutable credential `version` when staging a new rotation.

The customer App list exposes each selectable App's current and pending numeric
configuration versions without exposing secret fields. The SPA therefore uses
server-projected versions for verification after a reload and never guesses a
configuration version from a database ID.
