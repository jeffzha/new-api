# S4 Ordinary Task Single Settler

## Q60 Follow-Up: Complete Recovered Task Results

Probe now reconstructs the known accepted Task as SUBMITTED, commits its
initial charge once, and invokes the existing updateVideoSingleTask pipeline.
It therefore uses normal result URL/proxy selection, finish/start times,
plugin state, sanitization, terminal status CAS, Ops usage intent/reservation
transition and financial completion, rather than a partial terminal writer.
Transient query failure leaves a persisted nonterminal Task for normal polling.
The pre-dispatch seed also freezes the known Action.

Lost-acceptance-payload automatic query is restricted to the actually verified
Alibaba/OpenAISeedance paths. Other providers return an explicit
provider_submission_query_identity_requires_repair without querying or submitting;
response-only PluginState/Endpoint.FetchPath identity is never guessed. Jimeng's
query req_key/action dependency remains a manual-repair gap. This does not add
supplier acceptance coverage beyond the original two providers.

The actual controller/relay entrance matrix passes all 14 cases on SQLite
3.50.4 (0.784s), MySQL 8.0.39 (2.596s), PostgreSQL 17.6 (2.088s).
Lost-payload cases run real probe recovery then the user-scoped Task lookup:
correct video URL, positive FinishTime, durable OpsUsagePending and usage bytes.
With an actual configured local bridge, native usage contains seedance_video
and total_tokens=100000. Query count remains one and submit count remains one;
funding/usage/provider finalization assertions still pass. Tests also require
zero queries for an identity-dependent provider and original Action snapshots.
Logs: `model/testdata/cn-task/q60-controller-{sqlite,mysql,postgres}.log`.

Related `go test ./service ./relay ./controller ./router -p 1 -run 'Task|CN|Responses' -skip '^TestUpdateOptionRejectsIncompleteSeedanceVideoCNYPrices$' -count=1`
PASS (2.834s/1.238s/2.718s/1.735s), including Q58 three-engine regression.
The excluded baseline assertion remains documented separately; no full-suite
claim is made. Evidence: `model/testdata/cn-task/q60-regression.log`.

## Q59 Follow-Up: Pre-Claim Receipt Upgrade

Confirmed before the fix on SQLite: an existing wallet receipt admitted a new
subscription reserve after migration, and an existing subscription receipt
admitted a new wallet reserve. Both tests called real prepare entrances with
the claim table absent, then migrated twice before invoking the other lane.
Evidence: `model/testdata/cn-task/q59-before.log`.

Request claim now checks existing wallet, subscription and Task receipt owner
and lane inside the same transaction, under the unique claim row lock. Conflicts
roll back the new claim; historical receipts are never rewritten or deleted.
The original lane can still resume and claim its original identity.
`go test ./model -run 'CNTaskAcrossDialects|CNWallet|CNSubscription' -count=1 -v`
PASS (35.833s) on SQLite 3.50.4, MySQL 8.0.39 and PostgreSQL 17.6. Tests verify
exact original receipt bytes, only one combined reserve, no conflicting owner,
and original-lane resumption; the fresh cross-user three-lane race remains PASS.
Evidence: `model/testdata/cn-task/q59-matrix.log`.

## Q58 Follow-Up: Stale Reconciliation Worker

The existing Attempts counter now fences worker claims and retry/finish writes.
Claim compares the due-list attempt and increments it atomically; bookkeeping
writes require processing status and the same attempt. General reconciliation
updates cannot reopen settled/not_needed records. No schema or price change.
Legacy replay tests explicitly seed reopened records through their fixture DB,
so their original settlement-idempotency coverage remains.

`go test ./service -run '^TestCNTaskReconciliationRecoveryFencesStaleWorkers$' -count=1 -v`
PASS on SQLite 3.50.4, MySQL 8.0.39 and PostgreSQL 17.6. A real RunOnce final
transaction fails at its outbox insert, leaving ProviderBill/Pending. The test
executes actual recovery before the worker retry starts; receipt and provider
record remain final/settled after the old retry, late finish and generic update.
Balances/fact count remain exactly 50/2 and another worker pass has no pending
record. It also verifies newer claims reject obsolete due snapshots and old
retry/finish, and that not_needed is terminal.
Evidence: `model/testdata/cn-task/q58-matrix.log` and `q58-regression.log`.

2026-10-15. Independent batch based on `fad2f16cde270e0854eeefb39fb0cf87c7061c68`.
This batch owns opt-in ordinary image/video Task reservation, accepted initial
charge, completion, provider-bill finalization, and failure reversal. It does
not certify all providers or historical writer coverage.

## Authority Contract

- Endpoint: `/api/ops-export/v2/facts`; sourceSystem: `gateway-cn-finance-v2`.
- kind/postingRole: `charge`; basis: `gateway_actual`; quality: `settled_quota`.
- billingSource: `wallet` or `subscription`.
- Evidence: `cn_task_financial_receipt`; recordId: permanent `CNTaskBilling.Key`.
- economicId: `request:<requestId>`; segmentId: `0`; actual request/public Task IDs.
- Initial accepted debit is r1. Later targets revise the same cumulative chain
  with the actual previousFactId. Equal targets add no fact. Actual zero is valid.
- Failure/refund is a negative charge delta, never a payment refund.
- No supplier_cost, revenue, payment, recognition, or funding authority is added.
- Every `ordinary_task_<provider>` proof uses exactly
  `supportedPaths = ["ordinary-task/single-settler-v1"]`, version
  `task-single-settler-v1`. SourceID alone is insufficient for registration.
- S5 registration must also check sourceSystem, wallet/subscription,
  charge-only role, and `cn_task_financial_receipt` evidence.
- Legacy `image_video_tasks` stays unsupported, with empty supportedPaths;
  bound Tasks are excluded from its scan and legacy funding capture.

`model/testdata/cn-task/s4-task-published.{sqlite,mysql,postgres}.jsonl` contains
actual raw `CNFinancePublication.Payload` bytes emitted by the test, not rewritten
examples. Each file has five publications: request-correct 100 -> 140 -> 60 and
request-provider-zero 90 -> 0, including actual publicationSequence/predecessors.

## Persistence And Integration

New production modules: `model/cn_task.go`, `cn_task_effect.go`,
`cn_task_reconciliation.go`, and `service/cn_task_billing.go`.
New tests: `model/cn_task_test.go`, `controller/cn_task_test.go`.

Existing integration changes: controller relay/scheduler, relay Task submit,
Task persistence guards, task billing atomic/reconciliation, service Task
billing/polling/reconciliation, and CN finance migration/capture/proofs/reset.
`billing_session.go`, `funding_source.go`, `quota.go`, realtime and S4R are unchanged.

The explicitly enabled settler replaces ordinary request settlement for its
Task; it never adds a second ordinary BillingSession final. Financial commit
includes funding/token/user/channel usage, Task quota, receipt, fact and effect.
Subscription generation and original conversion are frozen. Deleted/missing
tokens retain exact refund disposal metadata after owner validation.

Normal settlement locks User -> Task -> receipt/reconciliation -> funding/token
-> channel -> publisher. Legacy Task adjustment and provider reconciliation now
lock User first too. Preparation locks User, then the request claim before
creating its receipt and reserving funds.

Minimal shared hooks in `cn_wallet.go` and `cn_subscription.go` claim immutable
`CNTaskRequestOwner(request_id PRIMARY KEY, user_id, lane)` during first prepare,
using insert-on-conflict plus a locked current-row identity check. A committed
claim is never released. The unique insert serializes different users presenting
the same request across Task/wallet/subscription tables. Both-direction Task
receipt checks also protect pre-existing receipts; failed preparation rolls its
claim back. Tests race all three lanes for eight same/different-user scenarios
per engine and require exactly one receipt and one combined 100-quota reserve.

Accepted payloads explicitly preserve hidden BillingReconciliationPending and
OpsUsagePending flags. Reconstructed provider Tasks restore enqueue intent from
the frozen provider snapshot. Scanner, actual upstream-ID probe, status polling,
and provider reconciliation recover without a second submit. Generic adjustment
cannot bypass an asynchronous provider bill. Recovery ignores enable flags;
legacy writers cannot mutate an already bound Task.

## Verification

Runtime: Go 1.27.2 windows/amd64. Declared Go 1.25.1 was not separately executed.
Real engines: SQLite 3.50.4, MySQL 8.0.39, PostgreSQL 17.6.
Local disposable ports: MySQL 33174, PostgreSQL 55474; no production DB access.

Core command: `go test ./model -run '^TestCNTaskAcrossDialects$' -count=1 -v -timeout=180s`.
External fixtures use AGENCY_HUB_RUN_EXTERNAL_DB_TESTS=1 and the documented
AGENCY_HUB_TEST_MYSQL_DSN / AGENCY_HUB_TEST_POSTGRES_DSN variables. PASS on all
three engines, including repeated fresh/representative-old-Task migration,
atomic failure rollback, cold reopen, flags-off recovery, zero/fixed amounts,
frozen subscription reset generation, owner mismatch, deleted token, concurrent
terminal, mixed legacy/durable settlement and hidden flag scanner recovery.
Log delivery uses a separately opened connection per engine; insertion failure
and lost source acknowledgment replay prove receipt/log atomicity and dedup.

Entrance command per engine: `go test ./controller -run '^TestCNTaskControllerEntrances$' -count=1 -v -timeout=120s`.
TEST_TASK_DB_DIALECT selects sqlite/mysql/postgres; TEST_MYSQL_DSN and
TEST_POSTGRES_DSN configure the local fixtures. All 14 cases PASS, using actual
registered Alibaba plugin and native OpenAISeedance through controller/relay,
real loopback HTTP, real scanner/poll/probe/reconciler and Responses bridge.
Includes immediate/deferred, accepted insertion/payload failure, caller cancel,
unknown identity, HTTP 408/409 ambiguity and rotated-credential zero-query checks.

Related selection: `go test ./model ./service ./relay ./controller -p 1 -run 'Task|CN|Responses|FixedPriceBilling' -skip 'TestFixedPriceBillingDatabaseMatrix/sqlite/image_override_reserves_extra_quantity' -count=1 -timeout=300s`.
Model (109.823s), service (1.231s), relay (0.519s) PASS. Controller selection
failed on TestUpdateOptionRejectsIncompleteSeedanceVideoCNYPrices at
controller/option_seedance_video_pricing_test.go:54: expects message
"exactly 3 supported models", actual "exactly 6 supported models".
Repeating that controller selection with only this assertion excluded PASS
(3.218s). This is selected related regression, not a full-controller pass.

Both exclusions were reproduced independently in an unmodified `git archive`
of fad2f16 in a disposable directory, without this batch's source changes:
`go test ./controller -run '^TestUpdateOptionRejectsIncompleteSeedanceVideoCNYPrices$' -count=1 -v`
fails with the same 3/6 message mismatch; and
`go test ./service -run '^TestFixedPriceBillingDatabaseMatrix$/^sqlite$/^image_override_reserves_extra_quantity$' -count=1 -v`
fails at service/text_quota_test.go:292, frozen request expected 1, actual 4.
Raw baseline failures, core/entrance logs, passing selected-controller log and
five-package compile log are committed beside the publication samples.
Local attributes preserve generated bytes, including original log whitespace.
gofmt and git diff --check are required before commit.

## Remaining Gaps

- Provider-specific entrances beyond Alibaba and OpenAISeedance are untested;
  whitelist registration is not provider acceptance evidence.
- Multi-key channels are rejected by this opt-in lane. Credential rotation
  requires repair before polling/probing; receipts contain only a key digest.
- No upstream ID means manual acceptance repair, no guessed request lookup,
  automatic resubmission, or ambiguous refund. HTTP 408/409 retain funds.
- Final provider bill is immutable. A different post-final bill requires repair;
  no administrative correction workflow is added here.
- Redis/batch mode, agency, audio, synchronous non-Task image relay, historical
  adoption, ClickHouse/nontransactional log delivery are outside this proof.
- Minimum MySQL/PostgreSQL versions and declared Go baseline remain untested.
- Root executable build is blocked by the pre-existing absent web/dist asset;
  affected backend packages are built/compiled separately.
