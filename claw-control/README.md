# claw-control

## Agent Store control plane

`WORKBENCH_AGENT_STORE_ENABLED=false` is the rollback-safe default. When it is
enabled, workbench entry consumes the existing new-api session ticket, creates
the revocable `claw_control_session` plus its hashed CSRF binding, and redirects
to `/agent-store` without minting an ADP SSO ticket. An SSO ticket is created
only after an authorized catalog item is launched.

The store models a logical product separately from its customer deployment:

- `AgentCatalogItem` and immutable `AgentCatalogVersion` contain presentation
  metadata only;
- `CustomerAgentDeployment` binds the item to an existing customer-owned
  `CustomerApp` and its exact verified config/auth/fingerprint snapshot;
- `AgentCatalogEntitlement` grants a paid customer, user, role, or plan access;
- `AgentLaunchAudit` records authorization and terminal launch outcomes.

No table or response stores or returns AppKey, SecretId, SecretKey, credential
references, provider AppId/SpaceId, template AgentId, or raw provider payloads.
Verification resolves the existing server-side references and trusts only the
authenticated `DescribeApp` readback. AppMode 1, 2, 3, and 4 are accepted;
`DescribeAgentDetail` is required only for AppMode 4 when
`AppConfig.Mode.ClawAgentConfig.CustomConfig.Enabled` is true. Runtime profiles
other than the production-accepted `claw_dynamic_v2` remain execution-disabled
until their real Tencent E2E gate is complete.

Administrator routes are under
`/api/admin/workbench/agent-store/items`; every mutation requires the existing
super-admin session, CSRF header, authentication no older than 15 minutes,
`expected_version`, and an audit row. User routes are:

```text
GET  /api/workbench/agent-store/status
GET  /api/workbench/agent-store
GET  /api/workbench/agent-store/{slug}
POST /api/workbench/agent-store/{slug}/launch
```

`status` is intentionally unauthenticated and always returns only
`{"success":true,"data":{"enabled":<bool>}}`; catalog routes still return 404
when the feature flag is disabled.

The launch body must be empty. The service resolves customer, Application,
AppMode, runtime profile, config and entitlement from the bound control session,
creates a 60-second single-use selection nonce bound to the item, deployment,
published catalog version and their row versions, and immediately consumes it
through the established selection/SSO chain. Consumption locks and revalidates
publication, deployment, paid-plan and entitlement state before signing SSO, so
an intervening unpublish fails closed. Cross-customer and unauthorized lookups
deliberately return 404.

For a store deployment, the signed app-context contract adds the following
all-or-none fields: `provider_app_mode`, `runtime_profile`, and
`execution_enabled`. A legacy customer App without a deployment omits all three
for backward compatibility. Dynamic Claw requires a non-empty server-owned
template Agent; the other four runtime profiles do not.

`claw-control` is the standalone control-plane service for the first phase of the ADP Claw workbench. It owns its own Go module and database and does not import or connect to the new-api database.

Implemented in this phase:

- customers, multi-customer memberships, and customer-scoped immutable identity bindings;
- multiple selectable App/Space profiles per customer, stable selectors/aliases, an explicit default App, and immutable App configuration versions;
- immutable fixed monthly plan versions, customer plan periods, manual payment confirmation, and fixed invoices;
- manual Tencent usage/cost audits that never alter customer invoices;
- optional asynchronous Tencent Fee Center read-only imports that create only account-scoped, unverified audit drafts;
- hashed entry, context-selection, control-session, and ADP SSO credentials, plus a separate admin session and CSRF token;
- admin audit rows and durable control outbox rows for future cache/session side effects;
- `/healthz`, `/readyz`, admin APIs, internal identity confirmation, migrations, and a fixed-plan period reconciliation worker.

Prometheus metrics use a separate listener and are disabled by default. Set
`CLAW_METRICS_ADDR` only on an internal management/container network (the
deployment overlay uses `:9090` without publishing a host port), then scrape
`GET /internal/metrics`. Never route that listener through the public ingress.
Metric formulas and alert examples are documented in
`../deploy/claw-workbench/observability/METRICS.md`.

Explicitly excluded:

- per-Turn reserve, settlement, refund, debt, token pricing, or wallet charging;
- direct access to new-api users, sessions, API keys, quota, logs, or database;
- custody of raw Tencent provider secrets or an embedded KMS/Vault. Credential profiles store only `env://WORKBENCH_PROVIDER_*` references and SHA-256 fingerprints; production must inject the referenced values from an independently operated secret-management boundary;
- automatic customer allocation or customer billing from account-level Tencent Fee Center data. The importer creates only account-scoped, unverified usage drafts for manual review and never mutates invoices, quota, balances, or wallets;
- external production acceptance. The trusted Tencent App verifier, Redis outbox delivery, in-site notifications, ClamAV-scanned encrypted evidence store, admin UI, and read-only Fee Center importer are implemented, but real Tencent credentials, ADP integration, Redis/ClamAV availability, backup/restore, failover, and load drills still require environment-specific validation before release.

## Run

Copy `.env.example` values into the process environment, then run:

```powershell
go mod download
go run ./cmd/server
```

The default SQLite DSN is intended for local development only. Production should use an independently provisioned MySQL or PostgreSQL database that is not the new-api database.

Authentication in this phase is deliberately narrow:

- browser admin routes use the independent `claw_admin_session` cookie; mutations also require the double-submit `claw_admin_csrf` cookie and `X-CSRF-Token` header. `CLAW_ADMIN_TOKEN` remains emergency/bootstrap access only and must never be sent to browser code;
- internal routes require a registered service identity and the headers `X-Workbench-Contract-Version: 1`, `X-Workbench-Service`, `X-Workbench-Timestamp`, `X-Workbench-Nonce`, and `X-Workbench-Signature`;
- configure service secrets with `CLAW_INTERNAL_HMAC_KEYS`. The first-phase route scopes are `new-api-core` for entry-ticket issue and `adp-backend` for ADP-ticket consume, identity confirmation, authz, and App context.

The HMAC canonical string is:

```text
CONTRACT_VERSION\nMETHOD\nPATH\nTIMESTAMP\nNONCE\nSHA256(body)
```

`TIMESTAMP` is Unix seconds, hashes and signatures are lowercase hex, and a nonce is accepted only once within the configured skew window. Production should add network isolation or mTLS in addition to HMAC.

After a request signature has been authenticated, every internal response, including business 4xx/5xx responses, carries the same `X-Workbench-Contract-Version`, plus `X-Workbench-Response-Timestamp`, `X-Workbench-Response-Nonce` (the request nonce), and `X-Workbench-Response-Signature`. Its canonical string is:

```text
CONTRACT_VERSION\nSTATUS\nPATH\nTIMESTAMP\nREQUEST_NONCE\nSHA256(body)
```

The caller must verify the echoed nonce, time window, HTTP status, exact response bytes, and lowercase-hex HMAC before parsing JSON. Responses to requests that fail service/signature authentication are intentionally unsigned because no trusted caller secret has been established.

The first-phase secret resolver accepts only `env://VARIABLE_NAME` references. AppKey, Tencent SecretId, and SecretKey values are resolved only for authenticated internal App-context calls; they are never stored in the database or audit rows. This resolver is not KMS/Vault: if deployment policy requires either, operators must supply that integration outside this process and inject only the referenced runtime values.

App and credential configuration accepts only `provider_environment=china_tencent_cloud|china_tencent_adp`. Internal App context maps these to the ADP resolver contract `vendor="Tencent"` and `service_vendor="ChinaTencentCloud"|"ChinaTencentADP"`; arbitrary provider strings are rejected before persistence. Capabilities are restricted to `chat`, `files`, `web_search`, `tools`, `connectors`, `oauth`, `scheduled_tasks`, and the read-only `catalog_models|catalog_skills|catalog_plugins`; blank or unknown values are rejected. Both the internal App context and browser-safe config return the same typed seven-field limits contract.

Credential profiles have an explicit immutable owner scope: `platform` for an intentionally shared provider account, or `customer:{id}` with the matching customer ID for customer-scoped BYOK. Profile version uniqueness is scoped by owner, provider environment, name, and version. App creation, migration, trusted verification, and runtime App-context resolution all reject a customer profile owned by another customer. Administrative credential projections expose owner scope, fingerprint, lifecycle status, and version only; Secret references and resolved values are never returned.

Provider-secret fingerprints are server-derived, domain-separated canonical
SHA-256 values. Credential writes and App-config writes verify the submitted
fingerprint against the resolved runtime secret, and runtime/verification/
approval paths recheck it. `/readyz` returns 503 when any active/retiring
credential pair or current/pending AppKey is missing, legacy, or mismatched.
After migration `0015`, legacy rows require the explicit audited admin endpoint
`POST /api/admin/workbench/secret-fingerprints/re-enroll` with confirmation
`rebind-current-runtime-secrets`; `/healthz` remains available so the operation
can be performed through direct maintenance access before the instance joins
the load balancer. This endpoint accepts only a direct loopback request carrying
the bootstrap bearer token; admin sessions and forwarded/container-network
requests cannot invoke it, and caller-supplied actor headers are ignored.

Changing `credential_profile_id` on an existing App requires an
`app_credential_change` two-person approval. The request binds the exact App,
current/pending config versions, source/target profiles, and all optimistic row
versions. The first App configuration remains a single-admin bootstrap.
An additional App cannot use that exception to introduce another credential:
its first config must use the primary App's exact current credential. A later
credential change uses the same `app_credential_change` flow. Migration targets
remain governed by the separate AppId migration approval before cutover.
Target verification atomically creates a generation-versioned rebuild job for
all active identity bindings. Signed internal `claim` and `report` tuples carry
the verified `provider_app_mode`, `runtime_profile`, and `execution_enabled`
snapshot. ADP workers copy and read back per-user Agents only for
`claw_dynamic_v2`; other profiles verify target App/profile readiness and report
an empty AgentId. Approval request and
execution both reject a stale App/config/credential/member-set fingerprint or
any member without a successful readback. Replanning preserves the superseded
generation; safe retry uses optimistic job/member versions. A provider-unknown
task is Describe-only when a known AgentId exists and is never automatically
sent through `CopyAgent` again.

## Multi-customer and multi-App context selection

An entry assertion proves only the current new-api identity. A user with one
eligible customer membership enters its explicit default App directly for
backward compatibility. A user with multiple eligible memberships receives a
restricted control session plus safe customer/App projections. Each candidate
contains a two-minute, one-time selection token whose hash is stored with the
session, user, identity version, membership, App/config, and authorization
epochs. The browser selects by that token only and never submits a trusted
`customer_id` or App profile ID.

Selection atomically rechecks the membership, customer, paid-plan access mode,
App status, current config, and all epoch/version bindings. Replay, expiry,
cross-session presentation, and any App disable/migration/config change fail
closed. Only after selection does claw-control mint the ADP SSO ticket. The SSO
ticket, control session, authz tuple, and App-context request all carry the
selected customer/App/config and cannot be overwritten by the browser or ADP.

## Main API surface

- `POST/GET /api/admin/workbench/customers`
- `POST /api/admin/workbench/customers/{id}/members`
- `DELETE /api/admin/workbench/customers/{id}/members/{user_id}`
- `PUT /api/admin/workbench/customers/{id}/app`
- `POST /api/admin/workbench/customers/{id}/app/verify`
- `GET/POST /api/admin/workbench/customers/{id}/apps`
- `POST /api/admin/workbench/customers/{id}/apps/{selector}/verify`
- `POST /api/admin/workbench/customers/{id}/apps/{selector}/default`
- `POST /api/admin/workbench/customers/{id}/app/{enable|suspend|disable|prepare}`
- `POST /api/admin/workbench/plan-catalog`
- `GET/POST /api/admin/workbench/credential-profiles`
- `POST /api/admin/workbench/credential-profiles/{id}/rotations`
- `POST /api/admin/workbench/credential-profiles/{id}/retire`
- `POST /api/admin/workbench/customers/{id}/plan-periods`
- `POST /api/admin/workbench/plan-periods/{id}/confirm-payment`
- `POST /api/admin/workbench/plan-periods/{id}/cancel`
- `GET /api/admin/workbench/customers/{id}/invoices`
- `POST /api/admin/workbench/invoices/{id}/void`
- `POST/GET /api/admin/workbench/usage-audits`
- `POST /api/admin/workbench/usage-audits/{id}/review`
- `POST /api/admin/workbench/usage-audits/{id}/revisions`
- `POST/GET /api/admin/workbench/tencent-billing-imports`
- `GET /api/admin/workbench/tencent-billing-imports/{id}`
- `POST /api/admin/workbench/tencent-billing-imports/{id}/retry`
- `GET /api/workbench/entry?ticket=...`
- `GET /api/workbench/config`
- `GET /api/workbench/plan`
- `GET /api/workbench/selections`
- `POST /api/workbench/selections/choose`
- `POST /api/internal/workbench/entry-tickets/issue`
- `POST /api/internal/workbench/tickets/consume`
- `POST /api/internal/workbench/authz`
- `POST /api/internal/workbench/app-context`
- `POST /api/internal/workbench/identities/confirm`
- `POST /api/internal/workbench/resources/bind`

All API results use `{ "success": true, "data": ... }`; errors use `{ "success": false, "error": ... }`. All timestamps are stored in UTC. Plan periods are exact calendar months with `[start_at, end_at)` boundaries.

The internal identity payload contains `binding_id`, `canonical_subject`, `customer_id`, `new_api_user_id`, `auth_epoch`, `display_name`, `application_id`, `app_profile_id`, `config_version`, `access_mode`, and `allowed`. An App-context request must provide that exact canonical subject, authorization epoch, requested App profile, requested config version, and one of `interactive`, `scheduled_task`, or `integration_refresh`; it never resolves an omitted/default App. Before releasing credentials, claw-control freshly verifies the new-api identity. Offline purposes require active access plus their matching `scheduled_tasks` or `oauth` capability, while `interactive` may resolve in active or read-only mode so owned history remains readable. App context returns server-resolved provider credentials and always sets `Cache-Control: no-store`. Browser `/api/workbench/config` is deliberately narrower: customer code/display name, customer role, App display/status, access mode, capabilities, and limits only. It never returns AppId, App profile ID, SpaceId, template Agent ID, region, provider environment, AppKey, or Tencent credentials.

The complete first-phase ticket handoff deliberately keeps four credentials separate: entry ticket, control session, ADP SSO ticket, and browser binding.

1. new-api validates its own Web Session and current user status, computes its `identity_version`, then v2 HMAC-calls `POST /api/internal/workbench/entry-tickets/issue` with `{ "new_api_user_id": 123, "identity_version": "v1...", "surface": "workbench", "is_super_admin": false }`. The successful data object is exactly `{ "ticket": "...", "expires_at": 178... }`, where `expires_at` is Unix seconds.
2. The browser opens `GET /api/workbench/entry?ticket=...`. claw-control atomically consumes only that entry ticket, calls new-api `identity-status` with the separate v1 credential, verifies the signed response, and rechecks member/App/fixed-plan access.
3. claw-control creates an unrelated random `claw_control_session` whose hash alone is stored. With one eligible membership it selects that customer's explicit default App. With multiple memberships it redirects to `/playground/select`; the page calls `GET /api/workbench/selections` and may submit only a two-minute opaque token to `POST /api/workbench/selections/choose`. It cannot submit a trusted customer/App ID. The control cookie is Secure, HttpOnly, SameSite=Strict at `Path=/`.
4. Only after a unique default or an atomic selection does claw-control create an unrelated random ADP SSO ticket and a third one-time browser binding. Only the binding hash is stored with the SSO ticket. The `claw_sso_binding` cookie is Secure, HttpOnly, SameSite=Strict and restricted to `Path=/workbench/auth/sso`; the redirect is `/workbench/auth/sso?ticket=<ADP-ticket>` with `Cache-Control: no-store` and `Referrer-Policy: no-referrer`.
5. Caddy forwards only `claw_sso_binding` to ADP on the SSO route. ADP reads that HttpOnly cookie and v2 HMAC-calls `POST /api/internal/workbench/tickets/consume` with `{ "ticket": "...", "browser_binding": "..." }`. claw-control locks the ticket row, constant-time verifies the binding hash, and consumes the ticket atomically only when both values match. ADP deletes the binding cookie on both success and failure. `/api/workbench/config`, `/api/workbench/plan`, and both selection endpoints accept only the control-session cookie.

`CLAW_ADP_SSO_REDIRECT_PATH` must remain `/workbench/auth/sso`; startup rejects a different path because the browser-binding Cookie is deliberately scoped to that exact path. Changing the public prefix requires a coordinated code, Caddy, Cookie-path, and migration review rather than an environment-only override.

An entry ticket can never authenticate ADP, an ADP ticket can never authenticate the browser control plane, and neither raw token is stored. A local `wt1.*` ticket stored only in new-api Redis is not part of this chain. new-api must fail closed if entry-ticket issue fails.

For `surface=admin`, new-api uses its RootAuth-only `POST /api/admin/workbench/session-ticket` flow and sets `is_super_admin=true`. On entry, claw-control calls the dedicated `/api/internal/workbench/admin-identity-status` endpoint and requires the signed response to confirm `is_super_admin=true`; it then creates only `claw_admin_session` plus the CSRF cookie, never an ADP ticket. The admin session cookie is Secure/HttpOnly/Strict with `Path=/api/admin/workbench`; the non-HttpOnly random CSRF cookie is Secure/Strict with `Path=/` so UI code under `/workbench/admin/` can copy it into `X-CSRF-Token`.

Caddy must explicitly remove every `claw_*` cookie before proxying a request to new-api or an ordinary ADP route. The sole exception is `claw_sso_binding`, which is reconstructed as the only Cookie header sent to ADP on `/workbench/auth/sso`; the control session is still stripped. These cookies are control-plane credentials, not upstream login credentials. Access logs and APM must also redact the `ticket` query parameter on `/api/workbench/entry` and `/workbench/auth/sso`.

### Resource ownership mirror

After ADP has durably created or confirmed a shadow account, Agent, Conversation, Workspace, or File, it enqueues a local outbox event and v2 HMAC-calls `POST /api/internal/workbench/resources/bind`. The request carries the complete immutable scope (`binding_id`, `canonical_subject`, `customer_id`, `application_id`, `app_profile_id`, and `config_version`), the resource and parent identifiers, and a stable `source_event_id`/positive `source_version`. The HMAC-authenticated service identity is injected by claw-control; it is never accepted from JSON.

The enforced parent graph is `account -> agent -> conversation -> workspace -> file`. A pre-Turn uploaded file may instead use its owned `account` as parent; it cannot be rebound across identities, customers, Apps, or config versions. Resource IDs are globally unique in the mirror. Account has no parent, and every other accepted parent must already be active in the exact same scope. Exact retries of the same source event are idempotent, including after App rotation; changing the payload for an event, reusing a resource ID, skipping a parent, or crossing any ownership boundary is rejected.

`claw_resource_bindings` stores SHA-256 uniqueness keys for the resource and source event so the constraints remain portable across SQLite, MySQL 5.7, and PostgreSQL without oversized composite indexes. Original identifiers remain available for audit. ADP stores the stable event first in `workbench_resource_outbox`; temporary delivery failures use bounded exponential retry, a crash after remote success safely replays the same event, and permanent scope/payload rejection fails the originating operation closed. Local ADP ownership checks never consult an untrusted browser identifier and are never relaxed while a report is pending.

### Signature compatibility note

The nonce-bearing HMAC scheme described above remains control v2 and protects calls into claw-control. The reverse new-api `identity-status` and `admin-identity-status` callbacks retain the read-only HMAC v1 scheme, but both directions now carry HTTP contract revision `X-Workbench-Contract-Version: 1`. Its request canonical is `CONTRACT_VERSION\nTIMESTAMP\nMETHOD\nPATH\nSHA256(body)`, with `sha256=<hex>` request signature and no nonce. new-api signs its response with `CONTRACT_VERSION\nSTATUS\nPATH\nTIMESTAMP\nREQUEST_SIGNATURE\nSHA256(body)`, echoes the original request signature in `X-Workbench-Response-Nonce`, and returns the same contract-version header; claw-control verifies version, response time, status, exact body, nonce, and HMAC before decoding JSON.

The v1 read-only request can be replayed within its 60-second skew window. This is an explicitly accepted first-phase boundary because the endpoint returns only current status/version and responses are authenticated; production still requires HTTPS/mTLS and the v1 secret must be independent from every control v2 service secret. Do not reuse one verifier or secret for both formats.

ADP authz, browser config/plan, and every cookie-based admin authorization recheck new-api status. Successful checks use a 15-second in-memory positive cache keyed by surface, user, and identity version; disabled, mismatched, unsigned, unreachable, and other error results are never cached and always fail closed. Identity callback URLs reject userinfo/query/fragment, require their exact documented paths, and the HTTP client never follows redirects so signed headers cannot cross hosts.

## Encrypted operational evidence and margin reporting

Usage reconciliation evidence is owned by claw-control, not by new-api billing tables or the ADP fork. `POST /api/admin/workbench/evidence` accepts one multipart `file` plus an optional `customer_id`; omitting the customer is reserved for platform-level `account_only` evidence. Supported formats are PDF, PNG, JPEG, WebP, UTF-8 TXT/CSV, and valid JSON. The service applies an independent request-size ceiling, verifies filename/path boundaries, extension, declared MIME, content signature/text validity, and SHA-256, then sends the bounded in-memory bytes through ClamAV `INSTREAM` before persisting anything. Only an explicit `OK` is accepted. Detection, timeout, transport failure, malformed response, scan limit, or unknown result fails closed and leaves no evidence row or file. MIME/signature validation is defense in depth and is never a substitute for malware scanning.

Evidence bytes are encrypted with AES-256-GCM using `CLAW_EVIDENCE_MASTER_KEY`, which must be standard base64 for exactly 32 random bytes and must not reuse any administrative, HMAC, identity, database, or provider secret. The database stores only safe metadata and an opaque random server locator; the original bytes are atomically written under `CLAW_EVIDENCE_ROOT` with a random filename and mode `0600`. Temporary or published files are removed when write or database/audit persistence fails. API responses never include the storage path, storage key, encryption key, or nonce.

The evidence endpoints are admin-only and inherit the cookie-session/CSRF boundary: upload requires an authenticated admin session and CSRF header; list and download require an authenticated admin session. All responses are `Cache-Control: no-store`. Downloads use attachment disposition and `X-Content-Type-Options: nosniff`.

`UsageAudit.Lock` no longer trusts a syntactically plausible reference or caller-supplied hash. The referenced active evidence row must exist, its stored content hash must match, and its scope must match the final allocation. `app_exact` and `estimated_allocation` require evidence for the same customer; `account_only` requires evidence with no customer and can never carry customer, App, or plan-period attribution.

`GET /api/admin/workbench/margin-report` is an internal, read-only report. With `customer_id`, it calculates `margin = paid fixed invoice revenue - locked app_exact/estimated_allocation cost`; platform `account_only` cost is never assigned to that customer. Without `customer_id`, the platform report subtracts all reviewed cost, including `account_only`. Draft `unverified` cost is disclosed separately and excluded until reviewed. Confidence has exactly four meanings: `app_exact` is provider-App exact attribution, `estimated_allocation` contains at least one documented allocation, `account_only` is known only at the provider account and is platform-only, and `unverified` has no reviewed attributable cost. Reporting never changes invoices, balances, quotas, or customer bills.

Production must set `CLAW_EVIDENCE_CLAMAV_ADDR` to an internal scanner and keep `CLAW_EVIDENCE_CLAMAV_TIMEOUT` bounded. The supplied Compose topology uses `workbench-clamav:3310` and makes both control colors depend on scanner health; exposing evidence upload without this fail-closed scanner is an explicit release blocker. Production must also mount durable encrypted storage at `CLAW_EVIDENCE_ROOT` and back it up together with the claw-control database. A database backup without the matching encrypted evidence snapshot is incomplete; the independent master key must be backed up through the secret-management recovery process, never inside the evidence archive.

## Tencent Fee Center read-only import

The importer is disabled by default. When enabled, it signs official
`DescribeBillDetail` and `DescribeBillAdjustInfo` requests with
TC3-HMAC-SHA256 and sends them only to the fixed
`https://billing.tencentcloudapi.com/` endpoint with API version
`2018-07-09`. The detail API is paged with `Limit<=300`; the client applies a
conservative 5-request/second ceiling, bounded timeout, response size, pages,
records, attempts, and lease duration.

`CLAW_TENCENT_BILLING_SECRET_ID` and `CLAW_TENCENT_BILLING_SECRET_KEY` are
runtime-only values. In the Compose overlay they are read from
`secrets/billing/tencent_secret_id` and `secrets/billing/tencent_secret_key`
only when `CLAW_TENCENT_BILLING_IMPORT_ENABLED=true`. The PayerUin is also
server-side and only its SHA-256 participates in persisted scope metadata.
None of these values is accepted by an API request.

The worker sums every `ComponentSet.RealCost` with exact decimal arithmetic.
Negative components, a negative net total, or any bill adjustment force manual
review; a negative net total is clamped to zero and can never create a credit.
Raw responses are stored only as scanned, AES-GCM-encrypted account evidence.
The resulting UsageAudit is always `unverified`, has no Customer/App/plan
attribution, and stays a draft until an administrator reviews it. Importing
does not update invoices, new-api quota, balances, or customer billing.

Official references:

- [DescribeBillDetail](https://cloud.tencent.com/document/api/555/19182)
- [DescribeBillAdjustInfo](https://cloud.tencent.com/document/api/555/112039)
- [Tencent Cloud API 3.0 TC3 signing](https://cloud.tencent.com/document/product/1278/46712)

The complete endpoint, state, formula, evidence, configuration, and recovery
contract is in [TENCENT_BILLING_IMPORT.md](./TENCENT_BILLING_IMPORT.md).
