# Claw control-plane governance constraints

This document is normative for the standalone `claw-control` service. It keeps
high-risk lifecycle and compliance behavior outside the new-api relay, billing,
and provider adapters so upstream merges remain low-conflict.

## Boundary

- `claw-control` owns customer/App lifecycle, control-plane identities,
  credential metadata, plans, notifications, approvals, retention, and audit
  export.
- Secret material is never stored in the database or returned by an API. A
  credential/config stores only `env://WORKBENCH_PROVIDER_*` references and a
  non-secret fingerprint.
- ADP owns conversations, tasks, Agents, and tool execution. Read-only catalog
  discovery does not grant execution or binding authority.
- new-api remains the account and balance authority. This governance package
  does not modify relay or token-billing behavior.

## Read-only catalog capabilities

Plans and App config versions may contain `catalog_models`, `catalog_skills`,
and `catalog_plugins`. These values authorize only discovery/listing. They do
not imply `tools`, `connectors`, OAuth, scheduled-task execution, resource
binding, or any write operation. Runtime execution checks must continue to use
the existing execution capability set.

## Multi-customer and multi-App selection

- A new-api identity may have active memberships in multiple customers. The
  entry assertion contains only `new_api_user_id` and `identity_version` and is
  never treated as a tenant/App choice.
- Active membership slots are customer-scoped. The legacy `primary` membership
  value remains readable during migration but new rows use `customer:{id}`.
- Every App has an unguessable stable selector and a customer-unique alias.
  Exactly one selectable App occupies the `primary` slot and is the explicit
  default; other Apps use `app:{selector}`. Changing the default does not change
  either stable selector or alias.
- A single eligible membership enters its default App directly for backward
  compatibility. Multiple eligible memberships create a `selection_pending`
  control session that has zero customer/App authority.
- Candidate projections expose customer code/display name, member role, App
  selector/alias/display/status/default flag, access mode, and a short-lived
  selection token. They never expose database IDs, provider AppId, SpaceId, or
  secret references.
- Raw selection tokens are never persisted. Their hashes are stored with the
  control session, new-api user, identity version, exact identity/member/App/
  config IDs, independent authorization epochs, and a two-minute expiry.
- Selection performs an atomic conditional consume and a fresh database
  authorization check. Replay, expiry, cross-session/cross-user presentation,
  inactive membership/customer/plan/App, a config promotion, default change,
  disable, or migration fails closed.
- A selected control session and every SSO ticket contain the immutable chosen
  customer, identity binding, App profile, config version, access mode, and
  aggregate authorization epoch. Every App-context request must carry the exact
  canonical subject, authorization epoch, App profile, config version, and a
  closed-set purpose. It never falls back to a current/default App. Internal
  authz accepts an omitted App tuple only for the interactive session flow where
  the selected context is still unambiguous and its signed result supplies the
  exact tuple used by the following App-context request.

## Plan notifications

The period worker reconciles persistent in-site notifications for:

- a paid active/scheduled period entering the seven-day expiry window;
- a paid active/scheduled renewal period that follows an earlier paid period;
- an App suspension recorded by `app.suspend.plan_inactive`.

Each source event has a stable, unique event key. Multiple workers, restarts,
and repeated reconciliation therefore create at most one notification per
event. Reading a notification is idempotent and audited.

## Credential state machine

```text
initial create -> active
active -> staged successor --(approved rotation)--> retiring + successor active
retiring + successor active --(approved rollback)--> active + successor retiring
retiring --(no current/pending App config references)--> retired
```

- Initial creation is the only direct activation path.
- Rotation and rollback require an approved two-person governance request.
- `active` and `retiring` versions remain runtime-eligible during the overlap;
  `staged`, `retired`, and `disabled` versions do not.
- Retirement is idempotent, optimistic-lock protected, and blocked while a
  current or pending App configuration references the old version.

## Two-person approvals

Production App disable, AppId migration cutover, credential rotation,
credential rollback, and changing `credential_profile_id` on an App that
already has a current configuration use the approval state machine:

```text
pending -> approved -> executed
       \-> rejected
       \-> expired
```

- The requester and approver must be different administrator identities.
- Requests carry a 5-minute to 24-hour expiry, immutable payload hash, unique
  idempotency key, and resource row-version snapshots.
- Decisions and execution use optimistic locking. A resource change after the
  request makes execution fail closed.
- Rejection and expiry are terminal. Execution retries return the already
  executed record and do not repeat the underlying mutation.
- Every request, decision, expiry, execution, and underlying resource mutation
  creates an administrator audit record.
- Test fixtures may construct `app.New(db, false)` for legacy/direct lifecycle
  tests. Production must use `app.New(db, true)` so direct App disable is
  rejected in favor of the approval path.

An `app_credential_change` request uses the pending App-config ID as
`resource_id`. Its immutable payload binds customer, App, current and pending
config IDs and config versions, source and target credential IDs, and every
App/config/credential row version. Execution re-resolves the pending AppKey and
both credential pairs, then records the approval ID on the pending config with
an optimistic version increment. Trusted provider verification and the final
current-config cutover each reload and recheck the same payload and secret
fingerprints. Any concurrent edit, secret change, ownership/provider mismatch,
or missing approval fails closed. Only the first App configuration, where no
current config exists, remains a single-administrator bootstrap.

## Provider-secret fingerprint integrity

Fingerprints are calculated by the server from resolved secret material using
SHA-256, lowercase hexadecimal output, length-prefixed canonical fields, and
separate domains `claw-control/tencent-secret-pair/v1` and
`claw-control/tencent-app-key/v1`. Administrators submit the expected
fingerprint, but create/rotation/config writes accept it only when it exactly
matches the server-derived value. Resolution and comparison run again at
approval execution, provider verification, final cutover, and internal
`AppContext` issuance. Resolver errors are mapped to generic messages; secret
references and values are not returned or audited.

`GET /readyz` verifies every active/retiring credential pair and every current
or pending AppKey. A missing secret, a canonical mismatch, or a legacy
fingerprint makes the instance return 503. `/healthz` remains a process-liveness
probe.

Migration `0015_provider_secret_fingerprints` marks pre-existing fingerprints
as version `0`; it never silently trusts or rewrites them. Before putting an
upgraded instance into service, an administrator must reach the instance
directly (or through a maintenance route) and call
`POST /api/admin/workbench/secret-fingerprints/re-enroll` with
`{"confirmation":"rebind-current-runtime-secrets"}`. This all-or-nothing,
audited operation derives version-1 fingerprints from the currently injected
runtime secrets and updates only legacy rows. It cannot overwrite a version-1
mismatch; that requires an approved credential rotation or a new App config.
The route is an emergency-maintenance exception: it accepts only a direct
loopback connection plus the bootstrap bearer token, ignores all supplied actor
headers, records the fixed maintenance actor, and rejects both ordinary admin
sessions and non-loopback/container-network callers.

Creating an additional App is not another first-configuration escape hatch.
If the customer has a primary current config, the additional App's initial
config must reference that exact current credential profile. After the
additional App is verified, a later config may change credentials only through
the normal `app_credential_change` approval. `PrepareMigration` is separate:
its target does not receive production traffic and the final AppId cutover
already requires its own two-person approval.

AppId migration is prepare, verify, approve, and cut over. The candidate has a
separate `migration:*` slot and must pass provider verification. Verification
atomically creates a versioned rebuild job containing every currently active
identity binding. ADP Blue and Green claim member tasks through the HMAC
internal API, copy a Kind=1 Agent into the target App, run
`DescribeAgentDetail`, and report the exact target profile/config fingerprint
and a sanitized readback hash. Provider credentials exist only in the signed
claim response and worker memory; neither migration table nor audit payload
stores them. The approval request and execute transaction independently lock
and recheck that the latest generation is ready, every active binding has one
successful readback, and the target App/config/credential fingerprint is
unchanged. A new or removed active binding therefore blocks cutover.

An administrator with a current reauthenticated session may replan using both
target and job expected versions. Replan supersedes rather than deletes the old
generation and snapshots the new active member set. A failed member may be
retried only through its job/member expected versions. Failures known to occur
before `CopyAgent` may retry copy; a provider-unknown outcome can only enter a
`readback` recovery task when the worker durably retained the returned AgentId,
so response loss never causes an automatic second `CopyAgent`. An unknown
outcome without an AgentId remains fail-closed and requires an audited operator
procedure rather than a database edit.

Cutover also requires active customer-scoped evidence, archives the old primary
App, promotes the candidate to `primary`, preserves the prior runtime status,
bumps both authorization epochs, invalidates caches, and audits both sides.

## Customer archival and retention

- A customer can be archived only after every App is disabled/archived and
  every plan period is expired/canceled.
- Archival disables memberships and identities, increments authorization
  epochs, revokes sessions through the outbox, and is idempotent.
- Retention policies allow 30 through 3650 days and support legal hold.
- A cleanup dry-run is allowed only for an archived customer whose archival age
  satisfies the active policy. It persists counts plus the policy ID and row
  version used for planning.
- Execution rechecks customer state, retention age, legal hold, policy version,
  optimistic run version, and pending control events, then creates a durable
  cross-component intent. It does not delete local control data yet.
- The retention coordinator locks the customer policy, rechecks legal hold and
  the exact planning version, and v2-HMAC sends only `intent_id`, `customer_id`,
  `policy_version`, `cutoff_at`, and `legal_hold=false` to ADP. It holds that
  policy lock through ADP's exact-scope transaction, so a legal-hold change is
  serialized before or after cleanup rather than racing it.
- ADP stores an idempotent receipt, purges only rows discovered from the exact
  `CustomerId` scope (including Conversation/Workspace/File locator and OAuth
  credential/revocation data), and never receives provider secrets from the
  control plane. A changed replay fails closed. Only a valid signed `completed`
  receipt lets claw-control delete operational identities, sessions, bindings,
  App configurations/verifications, Apps, memberships, and delivered outbox
  events. Delivery failures retain all local data and retry with backoff.
- ADP refuses retention while any OAuth provider revocation is pending, failed,
  unknown, or lacks durable `provider_revoked` evidence. It never deletes the
  encrypted compensation outbox merely to make retention appear successful.
- Evidence objects, invoices, usage audits, plan periods, administrator audits,
  the customer tombstone, policies, and retention-run records are protected
  legal/accounting records and are not removed by ordinary cleanup.

## Audit export

`GET /api/admin/workbench/audits/export` requires a current administrator
session; the emergency bootstrap token is intentionally insufficient. All
administrator responses use `Cache-Control: no-store`.

Required query parameters are `start`, `end`, and `format=csv|json`; optional
scope parameters are `customer_id`, `limit`, `after_id`, and `through_id`.
Ranges must be positive UTC ranges of no more than 366 days and page size is
limited to 1-1000 records.

The first page captures a maximum audit ID and returns it as
`X-Export-Through-ID` (and `snapshot_through_id` in JSON). Every continuation
request must send that value as `through_id`, producing a stable snapshot even
though the export action itself creates a new audit record. CSV fields whose
first non-space character is `=`, `+`, `-`, `@`, tab, or carriage return are
prefixed with an apostrophe to prevent spreadsheet formula execution. Each
export records actor, scope, cursor, row count, and content hash in the audit
log.

Credential lifecycle operations preserve an immutable owner scope. `platform`
profiles are intentionally shared; `customer:{id}` profiles carry the matching
customer ID and can be selected only by that customer's Apps. Rotation copies
the owner scope, approvals and audit rows inherit it, and trusted verification
plus runtime App-context resolution revalidate the relationship. Credential
API projections contain fingerprint, lifecycle/version metadata, and owner
scope only; they never include Secret references or resolved values.

## Administrative endpoints

- `POST /api/admin/workbench/customers/{customer_id}/archive`
- `POST /api/admin/workbench/customers/{customer_id}/app-migrations`
- `POST /api/admin/workbench/customers/{customer_id}/app-migrations/{app_id}/verify`
- `POST /api/admin/workbench/credential-profiles/{profile_id}/rotations`
- `POST /api/admin/workbench/credential-profiles/{profile_id}/retire`
- `GET /api/admin/workbench/notifications`
- `POST /api/admin/workbench/notifications/{notification_id}/read`
- `GET|PUT /api/admin/workbench/customers/{customer_id}/retention-policy`
- `POST /api/admin/workbench/customers/{customer_id}/retention-runs`
- `POST /api/admin/workbench/retention-runs/{run_id}/execute`
- `GET|POST /api/admin/workbench/approvals`
- `POST /api/admin/workbench/approvals/{approval_id}/approve`
- `POST /api/admin/workbench/approvals/{approval_id}/reject`
- `POST /api/admin/workbench/approvals/{approval_id}/execute`

Emergency loopback-only maintenance endpoint:

- `POST /api/admin/workbench/secret-fingerprints/re-enroll`
