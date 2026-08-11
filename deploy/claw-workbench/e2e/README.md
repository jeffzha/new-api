# Claw Workbench live acceptance

This directory contains a dependency-free, fail-closed acceptance runner for the
live Claw Workbench edge. It follows design section 19 in order and never stores
session cookies, one-time tickets, API keys, provider secrets, request bodies, or
raw responses in its reports.

## Safety defaults

- The runner is a dry run unless `--execute` is present.
- Lifecycle and gate mutations additionally require `--allow-mutations`.
- EICAR upload requires both `--allow-mutations` and `--allow-eicar`.
- A real Turn, model call, asset generation, or any fixture marked
  `provider_cost: true` requires `--allow-provider-cost`.
- Provider load tests require `--execute`, `--allow-provider-cost`, and an exact
  `--confirm-task-count` equal to the planned request count.
- Missing session/secret references produce `blocker`/`skipped` results. The
  runner never substitutes an anonymous request for an authenticated test.
- HTTPS is mandatory except for loopback fake-server tests explicitly run with
  `--allow-http`.

Session material must use `env:NAME` or a restricted `file:/absolute/path`
reference. On POSIX, a secret file must be a regular non-symlink file and have no
group/other permission bits (`0600` is recommended). On Windows, use environment
references; live secret-file ACL validation intentionally fails closed.
Safe, non-secret request fixtures may use `file:C:/absolute/path.json` on Windows.
`base_url`, `direct_adp_base_url`, and string values under `variables` may also
use `env:NAME` or a restricted file reference. This allows the target and test
customer identifiers to stay outside the checked-in configuration.

### Non-HTTP acceptance evidence

Scheduled-worker single-claim, provider no-repost, Agent Store CopyAgent counts,
App-migration lineage, retention delivery/deletion, integration provider
readback, BYOK leak scan, and billing coordinator single-claim are not public
product APIs. The runner does not call
fabricated `/acceptance/...` routes. `acceptance_observer` pins a dedicated
`claw-acceptance-observer` Ed25519 public key and maps qualified requirements to
detached-signed evidence files. Each exact v1 object binds the run ID, canonical
release-manifest SHA-256, requirement, observation time window, read-only source
kind/query digest/row count, and an exact closed fact object. PostgreSQL and
Prometheus are accepted only for requirements they can prove; provider call
absence/readback requires `provider_audit`, COS deletion requires
`object_inventory`, and bundle scanning requires `artifact_scan`. Missing, stale, future,
extra-field, wrong-run, wrong-release, wrong-fact, or invalidly signed evidence
is a blocker. The observer private key must never be given to this runner.

The evidence producer is deliberately a separate production interface. It must
derive facts from a bounded read-only PostgreSQL/Prometheus query, provider
request audit, object inventory, or artifact scan and publish `<evidence>.sig`
before the evidence file. Hand-written summary
files are not evidence.

### Agent Store, migration, retention, and integration execution

These phases are mandatory in the full runner, but the checked-in example does
not invent five Tencent Applications or pre-fill evidence. Empty fixtures are a
required blocker in live mode until the operator supplies real disposable
customers/Apps and independently signed evidence.

- `agent_store_checks` proves the exact provider-derived AppMode/runtime-profile
  tuple for `standard_v2`, `multi_agent_v2`, `workflow_v2`, `claw_static_v2`,
  and `claw_dynamic_v2`. Each acceptance deployment is explicitly execution-enabled
  and must complete its own real Turn and captured-Conversation history readback.
  The launch fixture sets `consume_launch_redirect: true`; the runner validates
  the same-origin SSO path, consumes it, proves replay rejection, and checks the
  authenticated Workbench root without reporting the ticket. The runner hashes
  the resulting same-origin AppContext and verifies that exact hash again before
  both the profile Turn and history request; an interposed launch cannot pass.
  A separate disabled deployment fixture must prove that launch is rejected.
  Signed provider audit proves zero `CopyAgentFromApp` for all four non-dynamic
  profiles. Separate provider-audit and PostgreSQL evidence prove exactly one
  CopyAgent request and one active dynamic binding.
- `app_migration_checks` uses a real source Conversation, prepare, provider
  verify, two-person cutover, old-history read, old-write rejection and current
  exact-completed Turn. Signed PostgreSQL evidence proves exactly one immutable
  lineage row and one cutover event.
- `retention_checks` runs last because success deletes the disposable customer's
  operational data. HTTP fixtures prove legal-hold and stale-policy fail-closed
  behavior and enqueue durable delivery. Separate signed evidence proves the
  completed receipt, exact ADP deletion, empty COS inventory, durable provider
  revocation, and preservation of invoice/audit/receipt records.
- `integration_execution_mode: blocked` requires catalog, bind/readback,
  dependent-Turn rejection, unbind and post-unbind readback. Set it to `enabled`
  only after the provider contract and disposable integration exist; then the
  runner requires a structured completed Turn, signed provider binding
  readback, usage/limit evidence, unbind and a subsequent rejected Turn.

Cost-bearing fixtures use `sse_terminal` with a JSON pointer to the request
fixture's `ClientRequestId`. Success requires `Type=workbench.turn` bound to
that ID and a later `Type=workbench.turn_status` for the same `TurnId` with exact
`Status=completed`. Model text, another Turn, `[DONE]`, or an arbitrary provider
completed event cannot pass.

### Managed sandbox profiles

`sandbox_mode: off` proves code and PTY fail closed. `sandbox_mode: enabled`
requires six successful `/code` fixtures for exactly Python, JavaScript,
TypeScript, Java, R, and Bash plus `sandbox_pty`. The dedicated PTY runner mints
a one-time ticket, performs a same-origin RFC 6455 upgrade, verifies the ready
frame, exercises input, resize, Ctrl-C and normal close, caps individual frames
at 64 KiB and captured output at 256 KiB, and always closes the socket in
`finally`. Tickets and terminal output are never written to reports.

## Quick start

1. Copy `config.example.json` outside the Git working tree.
2. Put safe JSON request fixtures in a protected operator directory. Request
   fixtures must not contain Cookie, ticket, API key, AppKey, AK/SK, or other
   secrets.
3. Set session material in the environment, for example:

   ```powershell
   $env:CLAW_E2E_USER_A_COOKIE = 'session=...'
   $env:CLAW_E2E_ADMIN_COOKIE = 'session=...'
   ```

4. Validate and render the no-network plan:

   ```powershell
   python deploy/claw-workbench/e2e/run_e2e.py `
     --config C:\secure\claw-e2e.json `
     --output C:\secure\claw-e2e-report
   ```

5. Run read-only live checks first:

   ```powershell
   python deploy/claw-workbench/e2e/run_e2e.py --execute `
     --config C:\secure\claw-e2e.json --output C:\secure\claw-e2e-report
   ```

6. Only in a dedicated disposable acceptance customer, enable mutations and
   cost-bearing checks:

   ```powershell
   python deploy/claw-workbench/e2e/run_e2e.py --execute `
     --allow-mutations --allow-eicar --allow-provider-cost `
     --config C:\secure\claw-e2e.json --output C:\secure\claw-e2e-report
   ```

The output directory contains `results.json`, `results.json.sig`, `junit.xml`,
and `report.md`. A live run requires
`acceptance_report_signing_key_file: file:/absolute/path` pointing to an
unencrypted OpenSSH Ed25519 private key. Generate a dedicated acceptance signer
with `ssh-keygen -t ed25519 -N "" -f C:\secure\claw-e2e-signing-key`, then put
exactly `claw-workbench-e2e`, a space, and the complete contents of the generated
`.pub` file into a separate OpenSSH `allowed_signers` file. `results.json.sig`
is an OpenSSH detached signature over the exact report bytes, fixed to identity
`claw-workbench-e2e` and namespace `claw-workbench-e2e-v1`, and must remain
beside `results.json` for the load gate. The E2E runner configuration must never
contain the public-verifier field.
Files are written atomically with mode `0600` where the OS honors POSIX modes;
on Windows, place the output directory under an operator-only NTFS ACL.
Endpoint fields contain only scheme, host, and path; query strings are removed.
`release_manifest` is mandatory for a valid acceptance run and binds the report
to all three repository revisions, all three deployed application image digests,
the non-secret deployment-config SHA-256, both migration heads, Caddy version,
active color, and provider region. Point the configuration at the bounded
`state/release-manifest.json` written on the exact deployment host; do not copy
or hand-edit these values. A report without this binding contains a blocker even
if individual HTTP checks pass.

## Request fixtures

Each configured check declares an actor, method, same-origin path and exact
acceptable statuses. JSON bodies live in a separate safe file and are never
copied into a report:

```json
{
  "name": "create disposable acceptance customer",
  "actor": "admin",
  "method": "POST",
  "path": "/api/admin/workbench/customers",
  "body_fixture": "file:C:/secure/create-customer.json",
  "expect_status": [200, 201],
  "capture": {"customer_id": "/data/id"}
}
```

Captured values are kept only in memory and can be referenced later as
`${customer_id}`. All non-GET generic fixtures require `--allow-mutations`,
except fixtures explicitly marked `provider_cost: true`, which require
`--allow-provider-cost`. Model/asset smoke checks always require the latter even
if the flag was accidentally omitted from an individual fixture.

When a user has more than one authorized customer/App context, entry redirects
to `/playground/select`. Configure `selection_targets.<actor>` with an exact
`customer_code` and/or `app_selector`; the runner lists only server-authorized
options, requires exactly one match, consumes its opaque token, proves replay is
rejected, and only then continues ADP SSO. It never accepts a caller-supplied
customer ID or App ID, and never puts the selection token in a report.
For the same new-api user in parallel browser contexts, configure
`selection_targets.user_a_context_1`, `user_a_context_2`, and
`user_a_customer_2`. They clone only the original new-api login cookie into
separate cookie jars, then independently execute entry/selection/SSO; their
ADP sessions are never shared.
Configure `selection_targets.user_a_stale_context` for an additional fresh
browser context. The runner stops that context at `selection_pending`, captures
an unconsumed server option, requires an administrator fixture to change the
acceptance customer's default App to the configured different selector and slot
(thereby advancing the App authorization epoch), requires the old option to fail
with exact status 403 and marker `selection token context is stale`, and requires
a `cleanup: true` fixture that restores the original selector and slot. This
proof cannot reuse the already-consumed replay token. A cross-context IDOR proof
creates a real Conversation in context 1, reads it back successfully, requires
context 2 to receive 404 for that exact Conversation, and deletes it again from
context 1. Pre-seeding any captured resource variable in `variables` or
capturing it more than once is rejected.

The minimal Turn captures its server-issued `ConversationId` as
`${conversation_id}`. The managed-sandbox phase then captures `${sandbox_id}`
from `POST /workbench/sandbox` and uses both opaque values for query, bounded
Shell, Shell stream, workspace file, pause, resume, and stop checks. User
mutations automatically echo the SSO-issued `claw_workbench_csrf` cookie through
`X-Workbench-CSRF`; the token is never written to a report. At least one
`csrf_mode: omit|invalid` mutation must prove a 403. Mark stop and its terminal
poll with `cleanup: true`; the runner moves cleanup into a `finally` block. If an
accepted create response loses its ID, the runner uses the ownership-checked,
read-only `GET /workbench/sandbox?conversation_id=...` lookup to recover the
handle before stopping it. Cleanup never replays a creating POST.

The `provider_unknown_no_duplicate` proof uses a dedicated disposable
Conversation and the acceptance-only sandbox fault hook. The first create is
forced to lose the provider Start response and must return exact 503 with
`acceptance_provider_response_lost`; replay of the identical create must return
the persisted `provider_unknown` row. Read-only acceptance evidence must prove
exactly one provider Start, the same recovered sandbox ID, and no provider
locator. The acceptance cleanup endpoint then stops every recorded provider
instance, proves each is `stopped` or `not_found`, and the runner deletes the
dedicated Conversation. These endpoints are disabled outside the dedicated
acceptance deployment tier and additionally require SSO ownership, capability,
recent authentication, same origin, CSRF, an explicit enable flag, a per-run
UUID, and `sandbox_acceptance_token` from a restricted runtime file.

The sandbox fixture bodies in `config.example.json` are safe JSON files with no
credential material. Recommended contents are:

```json
// sandbox-create.json
{"conversation_id":"${conversation_id}","timeout_seconds":60}

// sandbox-shell.json
{"conversation_id":"${conversation_id}","command":"printf 'claw sandbox E2E'","timeout_seconds":10}

// sandbox-shell-oversize.json
{"conversation_id":"${conversation_id}","command":"<more than WORKBENCH_SANDBOX_MAX_COMMAND_CHARS>","timeout_seconds":10}

// sandbox-file.json
{"message":"claw sandbox E2E"}

// sandbox-lifecycle.json
{"conversation_id":"${conversation_id}"}

// sandbox-code-disabled.json
{"conversation_id":"${conversation_id}","language":"python","code":"print('must not execute')","timeout_seconds":10}
```

Create each as a separate valid JSON file; the comments above are labels and
must not be included in the files.

## Section 19 execution order

The runner fixes the order rather than trusting configuration order:

1. Legacy route and configuration preflight.
2. Customer/member creation fixture.
3. Invalid App verification fixture.
4. No-plan enable rejection fixture.
5. Plan/payment/invoice/App-enable fixture.
6. User SSO, entry-ticket replay, ADP SSO replay, identity snapshot.
7. Minimal Turn and `Last-Event-ID` replay without a second POST.
8. Same-customer and cross-customer identity/IDOR checks.
9. EICAR fail-closed upload.
10. Model/Skill/Tool/connector allowlist fixtures.
11. Limit fixtures.
12. Agent Store catalog/launch and all five runtime-profile contracts.
13. Multi-App/multi-customer selector, OAuth/PKCE, conditional integration
    execution, and scheduled-task fixtures.
14. Managed sandbox config, create/query, bounded Shell/Shell stream, workspace
    file, fail-closed code/PTY, pause/resume, and mandatory stop cleanup.
15. Suspended and disabled gate scenarios, always followed by configured restore.
16. Credential/App rotation, App migration lineage/history, and BYOK approval.
17. Expired-plan read-only scenario and restore/renewal.
18. Usage-audit/margin and Tencent billing-import immutability fixtures.
19. Existing model/asset smoke fixtures, only when explicit safe fixtures exist.
20. Anonymous/forged-forward-auth and CSRF/XSS security fixtures.
21. Direct ADP rejection checks against known routes; only 401/403 count as an
    authentication rejection, never 404.
22. Retention legal-hold/race/delivery and signed cross-component receipt checks,
    always last.

BYOK checks use two distinct browser sessions (`admin_requester_session` and
`admin_approver_session`). Safe fixtures may contain only the non-secret
`credential_profile_id` and provider references whose values exactly match
`env://WORKBENCH_PROVIDER_[A-Z0-9_]+`; raw AK/SK/AppKey values and arbitrary
secret-like fields remain rejected before any network request. A required live
phase that lacks its mutation/provider-cost authorization is a blocker, not a
successful skip.

Each high-risk workflow also has a closed set of machine-readable requirement
IDs. Merely configuring one convenient 200 response cannot satisfy a phase:

| Phase | Required IDs |
|---|---|
| selector | `same_customer_multi_app`, `same_user_multi_customer`, `selection_token_replay`, `selection_token_stale`, `cross_context_idor` |
| OAuth | `pkce_s256`, `state_replay`, `disconnect_old_callback`, `cross_scope_callback_rejected`, `refresh_revoke`, `execution_blocked` |
| scheduled tasks | `lifecycle`, `occurrence_idempotency`, `worker_failover`, `provider_unknown_no_repost`, `offline_reauthorization`, `limits`, `agent_lock` |
| managed sandbox | `capability_contract`, `lifecycle_ownership`, `shell_bounded`, `shell_stream_bounded`, `file_roundtrip`, `code_fail_closed`, `pty_fail_closed`, `lifecycle_transitions`, `cross_scope_idor`, `rate_limits`, `provider_unknown_no_duplicate`, `resource_bounds`, `csrf_rejected`, `cleanup_terminal` |
| BYOK | `platform_profile_sharing`, `customer_profile_isolation`, `additional_app_inherits_primary`, `self_approval_rejected`, `two_person_approval`, `rotation`, `rollback_retire`, `readiness_fingerprint_mismatch`, `public_reenroll_hidden`, `secret_leak_scan` |
| billing import | `terminal_import`, `idempotent_import`, `invoice_immutable`, `account_only_unattributed`, `multipage_adjustment`, `failed_retry`, `coordinator_single_claim` |
| Agent Store | `profile_standard_v2`, `profile_multi_agent_v2`, `profile_workflow_v2`, `profile_claw_static_v2`, `profile_claw_dynamic_v2`, `context_standard_v2`, `context_multi_agent_v2`, `context_workflow_v2`, `context_claw_static_v2`, `context_claw_dynamic_v2`, `disabled_deployment_launch_rejected`, `cross_customer_hidden`, `non_dynamic_zero_copy`, `dynamic_copy_single_request`, `dynamic_binding_unique` |
| App migration | `migration_prepared`, `migration_verified`, `cutover_approved`, `lineage_single_activation`, `old_history_readable`, `old_write_rejected`, `current_write_completed`, `cross_scope_history_hidden` |
| retention | `legal_hold_blocked`, `policy_version_race_blocked`, `delivery_enqueued`, `delivery_receipt_completed`, `adp_exact_deletion`, `cos_exact_deletion`, `provider_revoked`, `control_records_preserved` |
| integration execution (`blocked`) | `catalog_readback`, `binding_readback`, `dependent_turn_blocked`, `revocation_readback` |
| integration execution (`enabled`) | `catalog_readback`, `binding_readback`, `provider_binding_readback`, `dependent_turn_completed`, `usage_limit_recorded`, `revocation_readback`, `revocation_blocks_turn` |

The executable configuration is validated against an equivalent strict runtime
contract and the complete closed-set requirement/semantic matrix before a
`Runner` or any HTTP client is created; unknown/misspelled fields, wrong
actor/method/path contracts, missing cleanup, assertion-label masquerading, and
incomplete phases fail closed before any network request. The runner supports
exact JSON equality/inequality (a
missing pointer never equals `null`), pointer absence, JSON type,
membership in an explicit `json_one_of` value set, response-header, variable
equality/inequality, bounded polling with forbidden intermediate states, value
capture, and canonical JSON SHA-256 capture. Each
requirement is also bound to an allowed surface plus a machine-checkable
actor/method/path/assertion contract; labels alone cannot satisfy coverage. Use
hashes to prove values such as invoice/customer balance are unchanged without
copying response bodies into the report.

`body_min_bytes` alone is not accepted as proof of a command-size limit because
irrelevant padding could inflate the request. Use
`fixture_json_min_lengths: {"/command": 65537}` together with the oversized
fixture; the runner checks the actual command field before sending it.

For a live run, set `release_manifest` to the deployment collector's absolute
file reference (for example `file:/srv/claw-workbench/state/release-manifest.json`)
instead of copying values by hand. The runner reads the bounded regular,
non-symlink JSON file before any request and refuses missing, placeholder, or
malformed release bindings. Live execution additionally requires the collector's
timezone-aware `collected_at` to be no more than 30 minutes old (with at most
five minutes of forward clock skew).

Unconfigured destructive or externally dependent phases are reported as
blockers; they are never silently counted as passes.

## SSE load scenarios

`load_sse.py` supports only the required 50/100 concurrency levels. Give the
load runner a separate configuration: remove
`acceptance_report_signing_key_file` and add
`acceptance_report_allowed_signers_file: file:/absolute/path`. The load runner
must never receive the private key. Dry run:

```powershell
python deploy/claw-workbench/e2e/load_sse.py --config C:\secure\claw-load-e2e.json
```

Explicit live run of both levels (150 provider Turns):

```powershell
python deploy/claw-workbench/e2e/load_sse.py --execute --allow-provider-cost `
  --concurrency 50,100 --confirm-task-count 150 `
  --acceptance-results C:\secure\claw-e2e-report\results.json `
  --config C:\secure\claw-load-e2e.json --output C:\secure\claw-load-report
```

The load report records first-event P50/P95/P99, terminal and successful
completion rates, exact terminal outcome/HTTP status counts, and wall time. The
stream parser accepts only structured JSON SSE frames. It first binds a
`Type=workbench.turn` frame to the submitted `ClientRequestId` and its non-empty
`TurnId`; only a later `Type=workbench.turn_status` frame for that same Turn is
terminal. Success requires exact `Status=completed`. Model text containing words
such as `"completed"`, provider `response.completed`, `[DONE]`, malformed JSON,
an unknown status, or a terminal event for another Turn cannot produce a pass.
The main E2E reconnect proof uses the same exact binding rules and accepts only
`Status=completed`; a model-output substring or another terminal status is a
failed minimal-Turn check, not successful evidence.

A live run is rejected unless the referenced main E2E report has a valid
detached OpenSSH Ed25519 signature from the pinned public signer, the exact
report schema, contiguous executed checks, a recomputed summary with no
fail/blocker/skip, every closed-set requirement in a passing evidence matrix,
and the exact same target and immutable release manifest. Missing keys, a wrong
identity or namespace, non-Ed25519 keys, altered bytes, and unsigned reports all
fail closed. A handwritten JSON file cannot authorize paid load. The verified
run ID is copied into the load report.

Live load also requires two independently collected evidence files:

```json
"load": {
  "request_fixture": "file:/secure/fixtures/minimal-turn.json",
  "turn_path": "/workbench/chat/message",
  "timeout_seconds": 900,
  "external_evidence": {
    "50": "file:/secure/evidence/claw-load-50.json",
    "100": "file:/secure/evidence/claw-load-100.json"
  },
  "external_evidence_allowed_signers_file": "file:/secure/keys/load-observer-allowed_signers",
  "external_evidence_wait_seconds": 60
}
```

Start the read-only infrastructure collector before `load_sse.py`. It writes
each artifact with a detached OpenSSH Ed25519 `.sig`. The observer exclusively
holds its private key; the runner receives only a pinned one-line public signer
with identity `claw-load-observer` and fixed namespace
`claw-load-external-evidence-v1`. This key must differ from the main E2E signer.
The load schema/runtime reject observer private-key fields. Signature bytes are
published before the evidence file, and the runner parses the exact bytes it
verified, avoiding an atomic-replacement TOCTOU gap. Missing signatures,
altered bytes, wrong identity/namespace/public key, or main-key reuse fail
closed. The runner waits for the
bounded period above, then reads an absolute regular non-symlink file and
requires this exact JSON contract:

```json
{
  "schema_version": 1,
  "collector_id": "claw-load-observer/v1",
  "acceptance_run_id": "<the verified E2E run UUID>",
  "release_manifest": {"<all immutable release fields>": "<exact values>"},
  "concurrency": 50,
  "window_started_at": "<timezone-aware ISO-8601>",
  "window_finished_at": "<timezone-aware ISO-8601>",
  "collected_at": "<timezone-aware ISO-8601>",
  "observed_requests": {"planned": 50, "terminal": 50, "successful": 50},
  "metrics": {
    "sample_count": 2,
    "active_connections_peak": {"edge": 50, "new_api": 1, "claw_control": 50, "adp": 50},
    "memory_peak_bytes": {"new_api": 1, "claw_control": 1, "adp": 1},
    "persisted_turn_events": {"before": 100, "after": 250, "delta": 150},
    "db_latency_ms": {"sample_count": 150, "p50": 1.2, "p95": 3.4, "p99": 5.6}
  }
}
```

Use `concurrency: 100` and the matching counts for the second file. The evidence
must bind the verified acceptance run ID, exact release manifest, level, runner
request counts, and a collector window covering the complete level (with at
most 120 seconds of margin on either side). Collection must finish after that
window, no more than ten minutes before validation, and no more than five
minutes in the future. Every metric is required: edge/new-api/claw-control/ADP
active connections, all three service memory peaks, arithmetically consistent
persisted Turn-event counts, and finite monotonic DB latency percentiles. The
runner additionally requires observed edge/claw-control/ADP traffic, positive
service memory, at least one persisted event and one successful event-writing
database transaction per terminal request, and no more event-writing transactions
than persisted event rows. Terminal Turn outcomes and persisted Turn-event rows
come from independent Prometheus counter families. Missing,
placeholder, stale, malformed, non-finite, mismatched, or incomplete evidence
fails the paid load before the report can be green. `collector_id` is only a
schema field: a handwritten JSON summary cannot pass signature verification.
Collector credentials remain outside the load runner. See `LOAD_OBSERVER.md`
and `load-observer.config.example.json` for collector startup and production
metric prerequisites.

## Self-test

Self-tests use only a loopback fake HTTP server and never call production:

```powershell
python -m unittest discover -s deploy/claw-workbench/e2e/tests -v
```

See `schema.json`, `config.example.json`, and `REPORT_TEMPLATE.md`.
