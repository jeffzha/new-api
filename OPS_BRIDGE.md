# TokenOps B integration

Owner: Zhong Qiuyuan (B1-B12). Worktree: `new-api-ops-b`, branch
`codex/ops-b-integration`, base `837b7fef1`. This is local development; it has not
been pushed or deployed. The existing `new-api` checkout is unchanged.

## Configuration

| Variable | Meaning |
| --- | --- |
| `OPS_BRIDGE_ENABLED=true` | Explicitly enable the bridge; disabled by default. |
| `OPS_BRIDGE_URL` | Ops origin/base URL; HTTPS required except loopback HTTP. |
| `OPS_GATEWAY_ID` | Gateway registered in the target Ops tenant. |
| `OPS_GATEWAY_KEY` | Machine credential issued by Ops, separate from user API keys. |
| `OPS_BRIDGE_STATE_DIR` | Private, unique, persistent directory for lock and outbox state. |
| `OPS_ENFORCE_CALLS=true` | Opt into supported synchronous and selected video-task Ops call/Coding entitlement gating, in addition to wallet billing; false by default. |
| `OPS_EXPORT_TOKEN_SHA256` | SHA-256 hex digest of a dedicated read-only bearer token, 32-256 characters before hashing. |

Finance export uses `OPS_GATEWAY_ID`, falling back to
`UPSTREAM_EVENT_GATEWAY_ID`. Conflicting nonempty identities fail startup.
Keep credentials in environment/secret storage. Bridge credentials are excluded
from persisted state and API responses. Use a separate state directory per
gateway/process; changing the Ops URL or Gateway ID does not reuse old state.
Back up private state with the database and retain it across restarts.

The bridge has a process lock, fsync/atomic replacement, bounded state, retry
backoff, manual retry and explicit archive. A storage failure remains a fault
until repaired and restarted. Shutdown joins the worker. Do not delete pending
state to make a failed delivery disappear. Start the Ops usage worker separately.

## Price Workflow

Root UI: `/system-settings/models/tokenops`.

1. Read the published Ops compatibility catalog and preview supported USD
   components converted to the existing billing expression format.
2. Select models. Repeated import preserves local administrator modifications;
   overwriting requires explicit model selection. Stale plans fail.
3. Prepare the Ops import, apply local options, persist evidence and confirm.
   Confirmation failures remain pending and retry after restart.
4. Submit the current local pricing snapshot back to Ops as source evidence.
   This never publishes or edits official prices.

Official prices belong to the Ops super administrator; tenants cannot edit them.
Local gateway prices remain editable. Frozen Ops customer quotes are a separate
existing integration mode. This bridge does not implement or enable the other
workspace's `OPS_PRICING_ENABLED` wallet-pricing mode. Full conditional-price
publishing still needs the A3 contract; unsupported catalog components fail
conversion rather than being silently dropped. Customer legacy versions have
not all been tested. Ops-to-gateway future improvements are separately scheduled.

## APIs

Root-authenticated routes under `/api/ops-bridge`:

- `GET /api/ops-bridge` (status, without an additional suffix)
- `POST /imports/preview`
- `POST /imports/:id/apply` with `models` and explicit `overwrite_models`
- `POST /deliveries/:id/retry`
- `POST /notifications/refresh`
- `POST /objects/sync`
- `POST /pricing/sync`
- `POST /archive`

The bridge pulls notifications, persists delivery/ACK state and prevents replay
after restart/archive. Inventory submits stable external IDs; existing EPH
ownership remains the authority for organization/project data. Ops preserves
organization fields omitted by an inventory source.

Dedicated read-only exporter:

- `GET /api/ops-export/v1/facts?after=<integer cursor>&limit=500`
- `GET /api/ops-export/v1/daily?date=YYYY-MM-DD`

Authentication uses `Authorization: Bearer <dedicated token>`, a constant-time
SHA-256 comparison and no-store responses. Facts have stable immutable identity,
frozen quota conversion, correction versions, cursors/hash and UTC daily totals.
`sourceCoverageComplete=false` explicitly retains missing historical wallet,
task/subscription/fee sources. Backfill completion does not imply complete
coverage or verified final profit.

## Call Gate and Native Usage

With call enforcement enabled, supported synchronous calls reserve, renew and
cancel Ops call/Coding/organization allowances. Blocked calls do not reach the
upstream. Existing wallet accounting still runs. Selected DoubaoVideo,
VolcEngine and OpenAISeedance video tasks reserve before provider submission,
renew while pending, recover after restart, consume with the original request ID
on native-usage success and release on an explicit failure. An ambiguous provider
outcome is held for reconciliation and is never retried through another channel.
Generic tasks, other task providers and realtime still return 409 in this mode.

Final native evidence supports text/embedding, OpenAI image JSON/SSE, STT JSON,
TTS SSE with official usage, rerank usage and selected DoubaoVideo/VolcEngine/
OpenAISeedance video task success responses. Streaming evidence requires a clean
terminal marker. Missing native usage is a gap, never a local token estimate.
Image evidence uses completed output/official usage, not requested `n`. Only
usage evidence is retained; prompt and media content are excluded.

Task success CAS persists a sanitized record and `ops_usage_pending` together.
Polling drains pending rows into the durable bridge before clearing the flag.
Stable `ops-task-<publicTaskID>` event IDs survive crash/replay; frozen bridge
scope prevents delivery to a different gateway. Video `total_tokens` is the
independent Ops metric `video_total_token`; final duration converts exactly to
integer milliseconds. Missing resolution/reference/duration or unsupported tool
charges are rejected by Ops instead of being charged as ordinary output tokens.
Matching official price components must be published in Ops.

Binary TTS without usage, other task protocols and realtime remain incomplete.
Existing tasks completed before bridge enablement are not reconstructed from
estimates. Recharge reversal execution needs A-owned refund/closed-period/fee
policy and is not implemented by this bridge.

## Verification

Verified locally:

```powershell
go test ./service/opsbridge ./service ./controller ./router -run 'TestOps|TestRunTaskPolling|TestProcessTask|TestTaskPluginProtocolRoutes|TestExecuteTaskSubmissionNeverRetriesAfterOpsAdmission' -count=1
go build ./...
```

The exporter and task-migration tests ran on real SQLite, MySQL 8.4.6 and
PostgreSQL 16.10. Migration tests cover representative legacy rows, fresh schema,
two startup migrations and pending JSON persistence. Set
`AGENCY_HUB_RUN_EXTERNAL_DB_TESTS=1`, `AGENCY_HUB_TEST_MYSQL_DSN` and
`AGENCY_HUB_TEST_POSTGRES_DSN` to disposable instances, then run
`go test ./model -run '^TestOpsTask' -count=1 -v` and the exporter `TestOps` tests.

The Ops repository's `scripts/b_http_acceptance.py` drives real HTTP import,
confirmation, reverse snapshots, scoped inventory, 200-to-429 call blocking with
one upstream invocation, durable usage and notification ACK/restart. Typecheck,
affected lint, five component tests, frontend build and desktop/mobile browser
checks passed. UI screenshots used fixture APIs; real APIs were tested separately.
Ops full unittest: 177 passed, including native video ingestion through the
durable worker, ledger and replay without duplicate charges. Task tests also
cover reservation restart recovery, renewal, explicit failure release, unknown
provider reconciliation, missing native usage and the no-second-channel rule.

Broader Go regression is not fully green: fixed-price DB matrix, image reserve,
Seedance configuration, account queries and top-up/Stripe quota tests fail on the
clean base too. Those unrelated failures were reproduced rather than changed.
Production deployment, complete source coverage, real accounting periods,
backup/recovery and performance acceptance have not been performed.
