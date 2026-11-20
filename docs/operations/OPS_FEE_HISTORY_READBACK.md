# OPS fee display history readback

When a deployment changes the fee namespace or starts its live cursor at a
cutover time, old usage logs may have no matching local fee-cache row even
though OPS already has a charge. This is a display-cache gap, not a reason to
replay usage, recalculate charges, or import balances again.

The cmd/ops-fee-history operator reads the existing signed GET
/api/v1/catalog/v1/fees?since=<RFC3339>~0 contract. It inserts missing
ops_fees rows under the reviewed active connection. Existing rows are kept,
so a delayed history response cannot replace a newer live correction. An
independent hashed ops_fee_syncs record tracks history progress. The live
cursor, wallets, source logs, and pricing are unchanged. No schema migrations
or application workers start. A null OPS amount becomes unconfirmed, never
an estimated zero or a permanently waiting result.

Before use, run the local real SQLite/MySQL/PostgreSQL fee test matrix and the
operator command tests. Build and checksum the operator from the committed
tested source. Review target environment, currency, connection SHA, history
lower bound, resource limits, and the station's combined read budget.

Example (credentials supplied only through a private runtime environment):

    ops-fee-history --since 2026-11-20T08:00:00Z \
      --connection <reviewed-current-connection-sha256> --gap 10s --max-pages 200

It makes at most one page request per step, at least 10 seconds apart
(at most six history requests per minute in this example). Existing live
pollers keep their own schedule. Identical history runs share a lease,
persistent request spacing, completion marker and error backoff. Different
lower bounds have separate progress: do not run them concurrently. Prioritize
recent history first, then backfill older history sequentially. Each page
contains at most 500 results; cache inserts are batched by 50. Failed pages
cannot advance progress. On rate limit or read failure, the command exits
with its progress and backoff retained. Re-run with identical arguments after
investigating; it resumes instead of replaying completed pages.

Run the operator with a limited DB pool and bounded CPU/memory. Existing
production API calls must stay available throughout. Do not introduce a
maintenance/write gate, restart or stop serving instances, interrupt active
streams, reset the live cursor, or re-run wallet cutover to repair display.
Monitor current public availability, fee coverage and live sync health.
Afterward verify a previously missing authorized log page through its API,
new-call fee continuity, unchanged source balances and held bills, and honest
remaining unconfirmed/no-result counts. Production observation only confirms
the locally tested repair; it does not replace its pre-release tests.
