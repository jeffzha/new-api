# Agency Hub deployment

Agency Hub is deployed beside new-api and shares the primary database. It is
not placed in the model relay path. Run the migration command with a dedicated
migration credential before starting the service:

```sh
AGENCY_HUB_AUTO_MIGRATE=false ./agency-hub migrate
# Run the deterministic financial/integrity scan. This only records Root-visible
# reconciliation issues; it never changes balances or replays money.
./agency-hub reconcile
AGENCY_HUB_BASE_PATH=/agency \
AGENCY_HUB_PUBLIC_BASE_URL=https://gateway.nexus-reach.com \
AGENCY_COMMISSION_PROCESSING_ENABLED=false \
AGENCY_WITHDRAWALS_ENABLED=false \
./agency-hub serve
```

## Local SQLite and production PostgreSQL

The same binary selects its database from `SQL_DSN`, using the repository's
SQLite default when the variable is empty. This is suitable for local
development and isolated tests. Start the gateway against the same absolute
SQLite path first, so it initializes its core tables. Then point Agency Hub at
that file; a separate empty file cannot provide the gateway's users and tokens.
Create the parent directory before starting either process. Existing process
environment variables and a local `.env` may still select PostgreSQL/MySQL;
check `SQL_DSN` when intentionally using SQLite:

```sh
SQLITE_PATH=./data/agency-local.db \
AGENCY_HUB_AUTO_MIGRATE=true \
./agency-hub serve
```

For a deployment, set `SQL_DSN` to the existing PostgreSQL database used by
the gateway. Agency tables are prefixed with `agency_hub_` and are created by
the separate migration command; the migration does not recreate PostgreSQL,
drop existing tables, or touch users, tokens, channels, or task data:

```sh
export SQL_DSN='postgresql://newapi:<password>@postgres:5432/newapi'
export AGENCY_HUB_AUTO_MIGRATE=false
./agency-hub migrate
./agency-hub reconcile
./agency-hub serve
```

In Compose, run the migration against the same `SQL_DSN` before starting the
service. A safe first deployment sequence is:

```sh
docker network create agency_backend 2>/dev/null || true
docker compose -f deploy/agency-hub/docker-compose.yml build agency-hub
docker compose -f deploy/agency-hub/docker-compose.yml run --rm \
  --entrypoint /agency-hub agency-hub migrate
docker compose -f deploy/agency-hub/docker-compose.yml up -d agency-hub
docker compose -f deploy/agency-hub/docker-compose.yml run --rm \
  --entrypoint /agency-hub agency-hub reconcile
```

The migration is idempotent and uses GORM `AutoMigrate`; rerunning it is the
normal way to add newly released `agency_hub_` tables and columns. Take the
same PostgreSQL backup used for the gateway before running it, and verify
`/agency/readyz` after the service starts. Do not point a production hub at a
new empty PostgreSQL database: it must share the gateway's existing database
so customer bindings, wallet facts, and billing journals remain consistent.

The current release adds `agency_hub_export_jobs.error_code` and
`agency_hub_topup_facts.quota_conversion_snapshot`, and replaces the
old unique `(user_id,status)` provisioning index with a non-unique reporting
index. This preserves failed/cancelled history and allows a later attempt to
reach the same terminal state. `migrate` makes those changes; restarting only
the application does not apply them when auto-migration is disabled.

### Reconciliation schema upgrade and review

The reconciliation workflow adds `active_key`, `resolution_evidence` and
`repair_event_id` to `agency_hub_reconciliation_issues`. Migration backfills an
active key for existing open issues, creates the unique
`uidx_agency_reconcile_active` index, and only then removes the old
`uidx_agency_reconcile_open` index. Closed issues have a NULL active key, so a
recurring discrepancy can retain multiple historical resolutions. Historical
differences and operator explanations are preserved. This is an incremental
upgrade of the existing shared database, not a new database initialization.

For an upgrade, back up and verify the shared database first. Stop **all** Hub
replicas and standalone reconciliation/worker processes before migrating; old
writers do not populate the new active key. Run `agency-hub migrate` with the
new binary and the migration credential, then start the new Hub version with
auto-migration disabled. Do not run old and new Hub writers together during
the backfill. `/agency/readyz` returns 503 when these columns or the new index
are missing. PostgreSQL/MySQL DDL may commit independently, so a failed
migration must be inspected and rerun before writers resume.

The Root **Sync and reconciliation** page lists issues and execution history
with cursor pagination. Inspect an issue to read the current authoritative
evidence, expected/observed values and permitted actions. Supported checks cover
funding accounts, commission balances, withdrawal locks, funding lots, active
customer bindings, billing operations and outbox delivery presence. A remaining
difference or an unsupported/missing source cannot be marked resolved or ignored.
Component checks additionally correlate the journal, original finalize operation,
immutable component result, funding matrix and allocation/lot identity; cumulative
model-refund watermarks must agree with their original sources. These checks do
not yet cover the complete debt/repayment/ledger chain or the interaction between
component payment chargebacks and model refunds.
Operation verification correlates the actual committed event with outbox
identity, content hashes and metadata. Completed deliveries require matching
terminal permanent receipts; poison/unknown states remain inconsistent. The
scanner uses these same checks in bounded, consistent pages, rather than
checking only aggregate sums or event counts. Each page is a current snapshot.
Manual and regular scheduled runs explicitly use a zero cutoff and record
`consistency=current_state_per_page` in their summary. A requested historical
cutoff or daily run records a failed result with an explanation that immutable
historical snapshots are unavailable; it must never certify a past close by
checking today's mutable balances. This protection does not implement the
historical daily close.

Closing a verified issue requires a fresh evidence hash, an explanation and a
Root verification proof bound to that exact issue and request. The proof,
resolution, audit and idempotency result commit together. Evidence that changes
after review is rejected; reopen/refresh the detail and verify again. Legacy
resolved/ignored records without verification evidence remain payment-blocking
until Root rechecks them; the new review retains the previous status and note.

The only automatic repair currently offered is restoring a missing delivery
from an intact, validated, immutable billing outbox event. A matching terminal
receipt restores a completed delivery; otherwise the delivery is pending. This
does not invent an event, change a wallet, release withdrawal funds or send a
payment. Other monetary discrepancies require a separately designed source
repair and remain unresolved. Current-state evidence verification is not the
complete historical financial close required by the design document.

The gateway also adds private TEXT columns `topups.payment_snapshot` and
`topups.quota_conversion_snapshot`. Those core-table columns belong to the
gateway's schema migration path, not `agency-hub migrate`. Complete the normal
gateway schema upgrade before admitting callbacks to the new gateway binary,
and run the separate Agency migration before starting the new hub. Back up the
whole shared primary database before either step; keep onboarding, commission
processing and withdrawals disabled during rollout. Running only the hub
migration does not upgrade the gateway's core tables.

The background worker performs the configured current-state reconciliation (five
minutes by default) and attempts a separate daily run from 02:30 Asia/Shanghai.
The daily key contains the execution date; its requested cutoff is the previous
calendar day's `23:59:59.999`. A restart after 03:00 still attempts that day's run,
and the durable key prevents duplicate execution. Both kinds of run remain
visible in Root's execution history. Historical daily runs currently fail
explicitly as described above, while current-state checks continue to work.

The production Root funds-command path now uses a dedicated gateway mTLS
listener. Configure its private network, certificates and Hub client with
[the command transport deployment guide](COMMAND-TRANSPORT.md) and
`compose.commands.override.yml`. The ordinary Hub HTTP router does not expose
internal command endpoints. Without the transport, remote commands fail rather
than falling back to shared-database queue writes; direct SQLite development
commands require the explicit local flag described in that guide.

For Docker Compose, provide `SQL_DSN` and five host-side secret files through
`AGENCY_HUB_SSO_PUBLIC_KEY_HOST_FILE`,
`AGENCY_HUB_COMMAND_SERVICE_PUBLIC_KEY_HOST_FILE`,
`AGENCY_HUB_COMMAND_SERVICE_PRIVATE_KEY_HOST_FILE`,
`AGENCY_HUB_DELIVERY_KEY_FILE`, and `AGENCY_PAYOUT_KEY_FILE`.
The payout key file must contain one 32-byte
base64url or hexadecimal key; it is read through
`AGENCY_PAYOUT_KEY_FILE=/run/secrets/agency_payout_key` and is never placed in
the Compose environment. New ciphertexts use `AGENCY_PAYOUT_KEY_ID` (default
`agency-payout-v1`). During key rotation, mount a private file through
`AGENCY_PAYOUT_OLD_KEYS_FILE` containing one `key_id=key` entry per line. Old
keys are read only for decrypting historical account snapshots; all newly
written accounts use the current key.

The compose file builds the current `agency-web` sources, embeds that exact Vite
output in a freshly compiled Linux binary, and uses `deploy/agency-hub/Dockerfile`.
It does not depend on manually copied binaries or the old committed frontend.
The runtime runs as UID/GID 10001, mounts a
persistent export volume, and joins the external `agency_backend` network. Create
that network once (and attach PostgreSQL or use a reachable DSN) before starting:

```sh
docker network create agency_backend 2>/dev/null || true
docker compose -f deploy/agency-hub/docker-compose.yml up -d --build
```

The sample limits the sidecar to 0.75 CPU, 768 MiB RAM, 20 open database
connections and 5 idle connections per database pool. Adjust the
`AGENCY_HUB_CPUS`, `AGENCY_HUB_MEMORY_LIMIT` and `AGENCY_HUB_SQL_MAX_*` variables
after measuring workload and reserving capacity for the gateway. A separate
log database can create a second pool. These limits are resource defaults,
not evidence that the throughput targets have passed.

The migration command is intentionally separate from `serve`. Run it with a
dedicated migration database credential, then start the service with the
restricted runtime credential. The container readiness probe is
`/agency/readyz`; `/agency/livez` is a database-independent liveness probe.

Run the repeatable local verification from the repository root with:

```sh
GO_BIN=/path/to/go deploy/agency-hub/verify.sh
```

This runs SQLite migration idempotency, reporting-index, readiness, and
`agency-hub` build checks. To include real MySQL and PostgreSQL migration
checks, set `AGENCY_HUB_RUN_EXTERNAL_DB_TESTS=1`,
`AGENCY_HUB_TEST_MYSQL_DSN`, and `AGENCY_HUB_TEST_POSTGRES_DSN` to disposable
test databases before running it.

On Windows, use the PowerShell runner to put build caches, Go modules, runtime
temporary files, Bun's package cache, and verification logs on another drive:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\deploy\agency-hub\verify.ps1 -TestRoot E:\new-api-test-cache -Full
```

The default suite checks Agency Hub and its gateway integration packages. `-Full`
checks every package in the root Go module without excluding failing packages.
Both modes run the financial audit tests, build, vet, frontend type checking,
frontend contract/translation tests and a Vite build into a new directory under `TestRoot`. Frontend dependencies
must already be installed in `agency-web`; `-SkipFrontend` explicitly omits
those frontend checks. Embedded release assets are not overwritten by this
verification build.

Each run writes `environment.json`, per-check logs and `checks.json` under
`TestRoot\runs`. Any
failed command makes the runner fail; skipped external-database, live-service,
or performance tests are not acceptance passes. Variables are changed only
for the runner process and restored when it exits. Existing C-drive caches
are left in place. Docker Desktop's disk-image location is configured
separately; this script does not start or relocate Docker.

To update the assets embedded by a local Go build, run `bun run build:embed` in
`agency-web` **before** `go build ./cmd/agency-hub`. Docker source builds perform
this ordering automatically. `Dockerfile.server` is a legacy binary-copy recipe
and is not used by the default Compose file. No production rollout is performed
by the local verification runner.

Run the real React + Gin/SQLite browser workflows with system Microsoft Edge:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\agency-web\e2e\run.ps1 -TestRoot E:\new-api-test-cache
```

This checks first-login password change, creation and password acknowledgement,
sales pricing publication with conflict preservation, and simulated withdrawal
approval/payment accounting including browser reload. It uses synthetic users
and no real payment. The Root gateway signing boundary is a test fixture;
production SSO/TLS, external database concurrency and recovery need separate
acceptance. See [browser acceptance](../../agency-web/e2e/README.md).

The six browser workflows also cover unknown-payment investigation and
recovery without releasing locked funds; actual asynchronous CSV generation
and download with agency-scope assertions; and an existing-customer binding
blocked by an active task, cancellation, and managed-customer transfer.
The reconciliation workflow checks real evidence, refuses unsupported or
inconsistent closure, restores exactly one missing delivery with a Root proof,
retains audit/evidence after reload, rejects operator access, and runs a manual
reconciliation through the correctly bound verification proof. Chinese desktop
and mobile evidence-dialog screenshots are retained for visual inspection.

To run real MySQL/PostgreSQL migration and financial concurrency checks in
isolated Windows processes on the data drive:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\deploy\agency-hub\verify-external-databases.ps1 -TestRoot E:\new-api-test-cache -MySqlBin 'D:\FileRuntimeEnvironment\MySQL5.7.43\mysql-5.7.43-winx64\bin' -PostgresBin 'E:\new-api-test-cache\database-tools\pgsql\bin'
```

Install/extract the database binaries at those paths first, or provide your
own paths. The runner creates new data directories, binds only loopback ports
13316 and 15436, refuses occupied ports, and retains logs/data under
`TestRoot\external-db`. It never connects to production. Both test processes
are stopped in cleanup. Selected tests cover repeated migrations, conserved
concurrent funding operations, first top-up concurrency, and download admission
across multiple sessions of the same actor. They also cover migration from the
legacy reconciliation index without losing history, concurrent creation of one
active issue, all seven evidence invariants, and concurrent HTTP repair/replay
with atomic proof, audit and idempotency records on SQLite, MySQL and PostgreSQL.
PostgreSQL startup waits for `pg_isready` success before initializing fixtures.
A listening TCP port alone is not treated as proof that PostgreSQL accepts SQL.

The runner also seeds synthetic users, tokens, a verified top-up and a paid
withdrawal, then runs real `pg_dump` and `pg_restore` against a second empty
PostgreSQL database. It checks restored payment evidence, conversion facts,
wallets, outbox/delivery and the paid state. Redelivering the original callback
with changed live conversion settings must not credit twice; conflicting known
payment evidence must be rejected. The dump, database directories and logs are
retained. This does not certify a production point-in-time recovery, external
bank reconciliation, key restoration, live write draining, or blue/green rollback.

Keep onboarding, commission processing, and withdrawals disabled until the
Phase A-E test gates pass. The service can be stopped without stopping
new-api; pending gateway outbox rows are consumed after it returns.

Onboarding now defaults to **disabled**. Set `AGENCY_ONBOARDING_ENABLED=true`
explicitly in both gateway and hub environments after the acceptance gates
pass; changing the hub environment alone does not configure the gateway.
Apply environment changes by recreating/restarting the affected processes.
Missing, misspelled or invalid boolean values keep onboarding closed.

`AGENCY_ONBOARDING_ENABLED=false` blocks new invite-based registrations and
existing-customer provisioning, including completion of already queued jobs.
The queue and its admission barrier remain visible; Root may cancel a pending
job during maintenance to restore that legacy customer's admission. Existing
durable users retain their billing mode, and ordinary registration continues.
It is not a rollback switch for already managed accounts.

### Component billing schema rollout (development, not enabled for production)

The current consumer understands `agency-billing-v1` and `agency-billing-v2`.
V2 stores multiple billing components in one immutable event and restores source
lots through an idempotent cumulative refund transaction. Acknowledging an
event remains one operation, while usage and commission rows retain exact
component identity. The gateway variable `AGENCY_COMPONENT_BILLING_ENABLED`
defaults to `false`; keep it false while component payment chargebacks,
the remaining asynchronous lifecycle/caller integrations and deployment gates
remain incomplete. Allocation-scoped debt attribution and model refunds that
repay other outstanding debt have passed isolated SQLite/MySQL 5.7/PostgreSQL 16
acceptance. Historical pooled debts require a uniquely provable source;
ambiguous provenance is rejected without changing funds. This does not complete
component payment chargebacks, which still fail closed and roll back atomically.
Explicit internal component inputs use v2; this
does not add a public customer refund endpoint.

Generic Task and provider-bill corrections for an already-finalized v2 charge
now use the cumulative model-refund transaction together with the task and
reconciliation receipt. Replays are inert; changed committed evidence and an
upward correction to an immutable final charge are rejected. Midjourney and
provider-specific submitted/reserved-to-finalized transitions still need full
integration and acceptance. Do not describe post-finalization refund support
as completion of every asynchronous lifecycle.

The gateway task JSON codecs must be from this same tested source revision.
PostgreSQL uses the repository's simple protocol: properties/private billing
context must be bound as JSON text, not bytea. The matching codecs read both
text and byte values on SQLite/MySQL/PostgreSQL. No task-column migration or
driver-mode change is required for this fix. Run
`TestTaskBillingJSONPersistenceAcrossDialects` and
`TestAgencyTaskReconciliationComponentRefundAcrossDialects` against the target
database dialect when validating a release; the external verification runner
includes both tests.

Migration adds `agency_hub_charge_components` and
`agency_hub_component_funding`, plus component identity hashes on usage and
commission rows. It backfills hashes from original IDs without recalculating
historical balances, builds replacement unique indexes, then removes the old
case-insensitive component indexes. It does not create a new main database.
The funding schema also retains nullable allocation identity on debt rows and
paid/nonpaid debt-repayment provenance, including bonus quota used to repay debt.
Run the matching incremental migration before enabling binaries that depend on
these fields; preserve historical source records for refund and replay checks.

For a future reviewed rollout, verify a database backup, quiesce old Hub
consumers/writers, run the explicit `agency-hub migrate` command against the
existing primary database, deploy the matching new Hub, then check `/agency/readyz`
for all required indexes and `capabilities.billing_schemas`. Old workers cannot
populate the new identity hashes and must not write during this transition.
Only after remaining financial acceptance gates pass should gateway v2
production be enabled. Upgrading Hub alone does not enable the gateway flag.
Do not roll a consumer back to v1-only code while v2 events remain in the queue
or require replay; preserve permanent receipts and immutable original events.

Current acceptance boundaries and stable remaining-item counts are maintained
in [the remaining-work checklist](../../docs/agency-hub-remaining-work.md).
Local tests, an isolated database run or a Linux binary build do not constitute
a production deployment or permission to enable financial switches.
