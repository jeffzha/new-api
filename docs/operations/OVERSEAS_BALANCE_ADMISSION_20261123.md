# Overseas OPS balance admission repair

The first fee-latency candidate (66eb499bc) was withdrawn after the release
window contained 503 responses from wallet admission. Public traffic was
returned to the healthy vendor Blue/Green pair on ports 3046/3048. Keep that
failed release's monitor baseline and evidence; do not describe it as a
successful release or erase its errors with a new baseline.

The reader serialized every caller behind one station request gate. When
completed calls invalidated the short authorization snapshot, concurrent
callers could queue duplicate wallet reads until their five-second deadline.
There was a second race: a completed call could clear authorization between
another caller's successful read and AdmissionPosition's cache lookup.

The repair coalesces in-flight individual reads by user. Each joined caller
receives the same immutable validated OPS response and checks its own quota
estimate. AdmissionPosition carries that response into the existing shared
database admission transaction; it no longer rereads mutable display state.
A completion invalidates future admissions without erasing results already
obtained by concurrent callers. A cancelled waiter cannot cancel the shared
read, which retains a bounded five-second context. Completed flights are
removed. Different users still share the two-second request budget and
authentication/throttle/service-error backoff. No polling interval or OPS
rate limit is raised by this repair.

The existing transaction still subtracts pending holds across all gateway
instances and rejects a stale ledger position. Negative and zero balances
remain blocked, old display snapshots never authorize calls, and an actual
OPS failure remains unavailable. No wallet migration, local money debit,
schema change, price change or domestic gateway change is included.

Local verification covers concurrent estimates, cancellation of both owner
and waiter, negative balances, 401/403/404/429/500, the budget wait, repeated
invalidation, and completion while other callers are waiting. The completion
race was reproduced as a failing test before repair. Run the affected packages
with the Go race detector and the real SQLite/MySQL/PostgreSQL wallet, receipt,
outbox and fee matrix before release. Preserve the eight manual-review video
bills and the already recorded smoke request
202611230746455886106988268d9d6Tyl0j02Y; never replay it.

Release only a committed tested artifact to zero-traffic candidates. Verify
the effective regional settings, request budget across active instances,
readiness, stored fee responses, static files, protected upstream routes and
rollback targets. Keep the healthy service running, use gradual Blue/Green
traffic and inspect ongoing connections before retiring old instances. No
maintenance gate, wallet pause, intentional 409/503 or interrupted task is
permitted. Post-release observation confirms pre-release verification.
