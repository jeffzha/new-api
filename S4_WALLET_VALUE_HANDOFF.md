# S4 Ordinary Wallet Payment Value

Baseline: integrated Gateway `6325660`.
Branch: `codex/s4-paid-lot-attribution`.
Native funding integrity v2 now hooks the actual native writers and seals
cumulative 14-collection snapshots with authenticated read-only discovery/export.
This package does not activate an Ops authority or any production source.

## Integrity V2 Delivery

Contract lineage includes `67b3f3e` (ordinary native lot identity and exact debt
retirement) and `1914f4b` (irreversible source-switch isolation B). The stable
reviewed package `fa99045c7c61f0d915783fce56631d26877af371` is not rewritten.

The local Go operations are PrepareCNIntegrityCapture,
ImportCNIntegrityEligibilitySnapshot, AdmitCNIntegrityUser,
StartCNIntegrityCapture, StopCNIntegrityCapture and FreezeCNIntegrity.
Prepared and active admissions require the expected control generation and an
explicitly imported Ops-approved inventory. Active admission supports new users
without reopening the epoch. Import requires local operator evidence; matching
a body hash is not issuer authentication. Ops must resolve the same immutable
inventory in its independent reviewed store before granting authority.

Freeze locks the epoch, global discovery counter and publication stream in one
repeatable-read transaction. It records native account/lot cutoffs, scans native
receipts and outbox ownership in both directions, retains findings with explicit
resolution rows, and verifies exact parent row inclusion for every collection.
Fact IDs and valueByCurrency come from committed staged evidence and do not
change when publication catches up. A later known revision cannot retroactively
adopt an earlier unknown receipt. Existing five-kind fact wire stays unchanged.

All nested trees are serialized as JCS, including typed CNFact bodies and
RawMessage payloads. Manifest bytes are stored and returned unchanged. Pages
are derived only from sealed rows and prefix hashes; later live mutations do
not alter them. MySQL collection payloads use LONGTEXT to avoid the 64 KiB limit.
Frozen snapshots are also advertised through the existing exact 14-field
CNDependency tuple. Its reader selects the latest sealed proof and never freezes.

The existing export-token/no-store boundary protects:

- GET `/api/ops-export/v2/funding-integrity/snapshots?after=0&limit=500`
- GET `/api/ops-export/v2/funding-integrity/snapshots/:proofId`
- GET `/api/ops-export/v2/funding-integrity/snapshots/:proofId/:collection?ordinal=0&limit=500`

No GET starts capture, creates a freeze, imports approval or grants authority.
Malformed queries return 400, missing proof 404 and ahead discovery cursor 409.

`model/testdata/cn-task/integrity-v2-real/{sqlite,mysql,postgres}/batch-{1..7}`
contains exact manifests, all 14 paginated collections (limit 2), discovery index,
the exact Ops inventory export and source test/database metadata. The flow is
initial pending -> debt-retiring native topup -> Task charge revisions 1/0/1 ->
active admission of customer 1003 plus inherited unknown hold finalization ->
durable pause with an unknown native topup. An additional rollback-isolated
native test seals pending-publication and complete children, proving unchanged
dispositions across publication without altering the seven-batch samples.

Eligibility fixtures were created by actual Ops freeze/review/export services
with separate synthetic actors. They are synthetic integration qualification:
productionEligibility=false, financialGrants=0. Their provenance manifest is
copied into the sample root. They are not approval for the existing 196-user
source. The independent verifier re-exports the review stores read-only and
compares exact bytes and SHA-256 to the supplied Ops provenance.

The initial independent raw-page replay rejected noncanonical facts/originals
pages with integrity_json_not_canonical. Original failed pages were overwritten
before a separate copy was made; they are not retained evidence. The initial
failure is recorded in the coordination transcript. The explicitly reconstructed
CNFact/RawMessage regression and its fixed output live under
`integrity-v2-noncanonical-regression`; the HTTP test generates them and records
this evidence limit. New samples must pass the strict consumer without
normalizing any inbound bytes.

Verification uses Go 1.27.2, SQLite 3.50.4, MySQL 8.0.39 REPEATABLE READ and
PostgreSQL 17.6, on owned ports 33174/55474. The shared 53394/55494 services were
not stopped. Focused capture/value/export tests passed in 70.487s, including
fresh/repeated/representative-upgrade migrations, native MinInt64 recharge,
omitted factIds detection, changed-parent rejection, and failed-freeze rollback
of snapshots, discovery sequence and value cutoffs. HTTP package passed in
0.905s. The final related three-engine selection passed in 314.321s, recorded in
`s4-integrity-related-final.log`; backend package compile checks also passed in
`s4-integrity-compile-final.log`. Strict independent replay of all 21 real native
snapshots passed; `s4-integrity-independent-replay-results.json` records each raw
file's SHA-256 and verification scope. I12 source-switch evidence is additional
rollback-isolated synthetic output: zero quota delta still appends account and
lot-isolation control states, breaks the epoch and exports current cutoffs at
the exact manifest mutation prefix. Broken epoch authorization must be rejected.
The separate S5 value validator currently rejects those broken control-trigger
samples with integrity_value_native_account_mismatch; this consumer diagnostic
is recorded in `s4-integrity-source-switch-consumer.log` and reported to the
coordinator. It is not counted as a successful 21-snapshot replay. Run the
verifier with `--source-switch` to reproduce that additional diagnostic.

```powershell
$env:CN_INTEGRITY_OPS_APPROVED_FIXTURE_DIR='D:/JiuLu/TokenOps/.testtmp/integrity-integration-inventories-20261015'
$env:CN_INTEGRITY_WRITE_EVIDENCE='1'
../../.tools/go/bin/go.exe test ./model -run 'CN|Topup|TopUp|PaymentSnapshot|AgencyFunding' -count=1 -v -timeout=600s
../../.tools/go/bin/go.exe test ./service/opsexport -count=1 -v -timeout=120s
```

For independent replay, add the S5 checkout and `.tools/python-packages` to
PYTHONPATH, then run `model/testdata/cn-task/verify_integrity_exports.py --approvals`
with the Ops fixture directory. It runs strict raw-byte manifest/pages, roots,
row identity/order, parent inclusion, current S5 graph/native financial/slices,
value states and control checks. Graph validation is not financial authorization;
all qualification artifacts still have financialGrants=0.

Operational limits remain explicit. Generic quota/source-switch guards retain
control/value evidence and permanently break capture, but do not emit a full
typed original for every unsupported mutation. Ordinary refund writer support
is not introduced: current refund holds belong to AgencyFundingLot and cannot
borrow numeric CNWalletValueLot IDs. Closed/broken epochs cannot be restarted;
reviewed replacement/ownership transfer is not provided by this package. Old
known corrections keep their original owner. External SQL, unguarded handles,
old binaries and Redis/batch require the explicit operator fence. Minimum server
versions and production rollout are not claimed by these qualification results.

## Q71 Current-Read Follow-Up

The follow-up is a separate commit after `fa99045`, never a rewrite of that
reviewed package. On MySQL 8.0.39 REPEATABLE READ, an outer native transaction
first reads the value account, another transaction credits 9 and consumes 6,
then the original transaction performs covered reserve/final/topup. Before the
fix all three incorrectly permanently suspend the owner (GapCount 1). Topup
unknown becomes 12 instead of 3. If the intervening credit and debit are both 6,
the account matches by accident but final allocation misses the new paid lot.
Raw failing production-baseline evidence: `s4-wallet-value-rr-before.log`.

The fix locks User before reading the current value account, agency ownership,
opening wallet/Task holds, source-switch reload, live lots, allocations, slices
and revision heads. Shared funding allocation/validation and covered Task User
reads use current reads too. Admission first inserts an uncommitted draft with
ON CONFLICT DO NOTHING, then initializes its immutable opening snapshot from
current holds before commit; existing committed openings are never overwritten.
This preserves independent first-owner insertion without absent-account gap locks.
The draft is rolled back with the native transaction on any failure.

Regression cases cover net-zero and net-increase concurrent payment/consumption,
all three covered entrances, newly committed wallet plus Task holds, agency
bootstrap then switch-back outside the old snapshot, and the independent-user
barrier. External old-read-snapshot upgrades are inapplicable on SQLite; its
serialized write path remains covered. Publication/proof scans retain their
consistent read snapshot because they do not mutate live attribution.

Coordinator separately ran the prior wallet/F12 matrix with Go 1.25.1 against
three exclusive databases (`coordinator_wallet_20261015`), PASS 43.376s, log
`D:/JiuLu/TokenOps/.testtmp/coordinator-wallet-f12-three-db.log`. That result covers
the original package, not Q71.

Q71 related matrix passed on Go 1.27.2, SQLite 3.50.4, MySQL 8.0.39
REPEATABLE READ and PostgreSQL 17.6 in 248.847s. The wallet-value matrix itself
passed in 36.88s; all new MySQL/PostgreSQL snapshot regressions passed. Evidence:
`model/testdata/cn-task/s4-wallet-value-rr-matrix.log`. The before-fix log is
`model/testdata/cn-task/s4-wallet-value-rr-before.log`.

```powershell
$env:AGENCY_HUB_RUN_EXTERNAL_DB_TESTS='1'
$env:AGENCY_HUB_TEST_MYSQL_DSN='root@tcp(127.0.0.1:33174)/s4_cn_finance?charset=utf8mb4&parseTime=True&loc=UTC'
$env:AGENCY_HUB_TEST_POSTGRES_DSN='host=127.0.0.1 port=55474 user=s4test dbname=s4_cn_finance sslmode=disable'
../../.tools/go/bin/go.exe test ./model -run 'CN|Topup|TopUp|PaymentSnapshot|AgencyFunding' -count=1 -v -timeout=300s
```

Coordinator subsequently reports the integrated Q71 package `bb01e24` passed its
exclusive three-DB wallet/provider-receipt matrix on Go 1.25.1 in 72.836s,
including covered-writer old-snapshot, current opening admission and independent
first-owner cases. This is additional coordinator evidence, not a locally rerun log.

## Integrity Native Capture Boundary

The wire proposal includes the final range/head/source/debt correction
`d441e69a93d546e3beb840da791f9de4f9ad33bc` and exact native owner helper/example
`f18bd9febcd9b148c0ac884e3bd456510bfd17c3`. Dependency/fact fields stay unchanged;
five-kind authority is exact frozen manifest membership, with grant/borrowed
exclusion and cumulative batch CAS. publishedThrough is a decimal publication
sequence. Economic lineage excludes both streamEpoch and integrityEpoch.

`model/cn_integrity_foundation.go` and `model/cn_integrity_capture.go` add epoch,
admission, original mutation, disposition, writer/fence, control and immutable
slice-delta persistence plus an independent diagnostic scanner. Migration creates
tables and one empty gate, never an epoch. Explicit local APIs prepare, admit,
start and stop capture with generation CAS. Start requires nonempty writer IDs,
builds, full protocol/capability declarations, unexpired leases, inventory hash,
operator evidence and explicit drain attestations; flags alone cannot start it.
The persisted fence binds the exact writer set and attestation. Native writes
validate current writer/build/membership/fence/control under User then epoch locks.
The gate uses shared reads before epoch access so an inactive gate does not
serialize independent first-user admissions or create Q71 gap-lock deadlocks.

Actual ordinary TopUp, unbound/bound wallet reserve/final/cancel and wallet Task
reserve/revision paths now capture within their native transaction. OriginalBody
uses the exact typed schema and JCS body hash. NativeRevision is the capture
journal counter, independent of the old source row Version stored separately.
Committed TopUp snapshots are decoded unchanged; private Task credentials never
enter the economic projection. Known/unknown/no-effect dispositions and staged
fact IDs commit with the original mutation. Each known slice increase/reversal
also appends an immutable delta; later v1 slice edits cannot erase that history.

Opening native rows/holds remain unknown, and legacy paid-lot owners require a
reviewed transition before admission. Negative opening balances remain signed.
Opening charges cannot consume newly admitted paid lots. Durable pause keeps
capture and unknown allocation; capture disable, invalid writer, source switch
or uncovered quota writer permanently breaks the epoch. Original business writes
continue after a break, with the discontinuity retained for independent scanning.
No re-enable clears the gap. Receipt failure rolls back the native mutation.

The scanner uses one repeatable-read snapshot and enumerates successful native
TopUps, ordinary wallet rows and ordinary Task rows independently of capture rows.
It checks both directions, native snapshots (including fields omitted by public
TopUp JSON), opening/sequence/previous-receipt/balance continuity, disposition
coverage, orphans, control generations, immutable writer/fence metadata and leases.
Slice deltas reconcile in both directions to current native slices/allocations
and each receipt's cumulative known quota. Native payload/opening local checksums
remain distinct from JCS originalBody hashes and unchanged v2.digest facts.

The native scanner is paired with the frozen fact graph and value-state replay.
Missing fences, broken control state, source mutation discontinuities and absent
or mismatched publication create retained findings. Internal writer/fence storage
is projected into the exact sidecar schema; it is not itself the wire payload.

The native capture matrix runs on real SQLite 3.50.4, MySQL 8.0.39 REPEATABLE
READ and PostgreSQL 17.6. It covers actual TopUp/wallet/Task capture and reversals,
opening ownership, concurrent admitted users, old read snapshots, invalid writer
membership/capability/lease, durable pause, source switch, flags off, unidentified
credit, receipt-failure rollback and continued capture after a permanent break.
Raw evidence: `model/testdata/cn-task/s4-integrity-native-writers.log`.

```powershell
../../.tools/go/bin/go.exe test ./model -run '^TestCN(IntegrityFoundation|WalletValue|FinanceContract)AcrossDialects$' -count=1 -v -timeout=180s
```

## Native Scope

Admission requires `OPS_CN_WALLET_VALUE_ENABLED=true`, CN finance capture,
Redis disabled and batch disabled. Local prices and quota calculation are
unchanged. Existing durable wallet/Task admission flags remain necessary.

- Future verified ordinary topups: Epay, Stripe, Creem, Waffo and Waffo Pancake
  through `creditTopUpQuotaWithFunding` in their original credit transaction.
- `CNWalletLifecycle` reserve, bound reserve, final debit and cancellation.
- Ordinary wallet-backed `CNTaskBilling` reserve, acceptance, corrections and
  reversal through the existing single settler. Subscription value is excluded.
- Actual debit, cash allocation, increment slices, allocation/revenue outbox and
  native receipt commit or roll back together. Reader/backfill never allocates.

Opening wallet plus existing outstanding supported reservations are unknown.
The fixed policy consumes unknown first, then known lots by frozen purchase
CreatedAt and stable LotID, including same-second ties and reverse commit order.
A payment with unknown amount/currency remains unknown. Verified zero
payment is a zero-value bonus. No current price or FX infers purchase value.
Each increment freezes its source and exact value; decreases unwind increments
in reverse order. The final share receives the remaining cash, and full reversal
restores the exact frozen value, including rounding remainder.

Topup facts use the actual saved `TopUp.CompleteTime`, PaymentSnapshot and
QuotaConversionSnapshot from the callback transaction. Missing/inconsistent
source snapshots or an already staged payment/lot cause unknown treatment;
immutable earlier facts are never enriched with new native authority.

## Business Compatibility And Ownership

The startup GORM callback invalidates the ordinary pool before an uncovered
User quota/billing-mode update. The original business mutation proceeds. Its
transaction also persists Suspended, SuspendedAt, SuspendReason and lot Isolated.
With explicitly disabled default transactions, invalidation commits first;
a subsequently failed mutation can conservatively leave a permanent gap.
No cash amount, original payment receipt or prior allocation is deleted.

The callback is installed in InitDB even when export/admission flags are off.
Covered writers maintain ExpectedQuota for reserve, final extra debit, release,
Task revisions and topups. Net balance mismatch is an additional external-write
detector, not a claim of universal writer coverage. Net-zero GORM bypass writes
also invalidate. Suspended pools never automatically resume, including after
later verified payments or role changes. Reversals of prior known allocations
still restore their frozen cash into isolated lots.

Agency bootstrap first suspends an ordinary owner under the same User lock.
Its opening migration has no verified purchase value. An existing agency
account prevents an ordinary pool from adopting its balance. Returning to a
legacy role does not reactivate ordinary known-value allocation. Role business
operations themselves are not newly rejected by this feature.

Quota and billing-mode guard detection follows GORM field names, column names,
struct zero-value selection, Select/Omit, projection models without a primary
field, slice primary keys, UpdateColumn(s), Save and disabled default transactions.
Suspension uses current locking account/lot reads after the User lock so an old
MySQL repeatable-read snapshot cannot hide a newly admitted owner or payment lot.

User locking and permanent request claims serialize supported writers.
The first absent wallet lifecycle read is plain, because FOR UPDATE would
create cross-user MySQL gap-lock deadlocks before first ownership insertion.
Existing receipt mutation remains serialized by the User writer lock.

## Wire And Authority

Frozen path: `ordinary-wallet/native-funding-v1`.
Proof: `ordinary_wallet_native_funding`, version `native-funding-v1`.
Funding policy: `ordinary-unknown-first-paid-fifo-v1`.
All new native facts use billingSource `wallet`, sourceEvidence system `gateway-cn`.

| Record | Stable identity / evidence |
| --- | --- |
| payment | economicId `topup:<tradeNo>`; recordType `topup`, recordId tradeNo |
| funding_lot | economicId `topup-lot:<tradeNo>`; original topup evidence plus `cn_wallet_value_lot`, recordId internal LotID |
| native cash LotID | `CNIdentity("ordinary-wallet-lot", tradeNo)` |
| charge key | `CNIdentity(chargeEconomicId, segmentId)` |
| allocation | `CNIdentity("ordinary-wallet-allocation", chargeKey, internalLotID)`; recordType `cn_wallet_value_allocation`, recordId allocationId |
| recognized_revenue | `CNIdentity("ordinary-wallet-revenue", allocationId)`; same allocation evidence and links |
| admission evidence | `cn_wallet_value_account`, recordId userID; evidenceId hashes policy/user/CreatedAt/OpeningQuota/OpeningBalance |

The charge keeps its nominal conversion and `recognition=null`. Each allocation
has native authority, recognizedValue, customer, payment, lot and charge/segment
links. Each allocation independently publishes a standalone recognized_revenue
chain in the payment currency with identity FX. Revenue funding is null; its
allocationId, lotId, paymentId and exact chargeFactId are in links. Do not book
both an allocation's value and its revenue chain as two revenues.

Allocation and revenue revisions start at 1 and advance only on that allocation's
actual quota change. Equal cumulative targets create no fact. Delta is cumulative
minus predecessor. Negative corrections link restoresAllocationFactId to the
previous allocation fact; they are consumption reversals, never payment_refund.

Actual synthetic publications are in `model/testdata/cn-task/`:

- `s4-wallet-value-{sqlite,mysql,postgres}.jsonl`: customer 101 includes unknown
  consumption, mixed unknown/paid consumption, mixed currencies and r1-r4 reversal.
  It also contains nativeAccounts, sourceTransitions and real sourceProofs.
- `s4-wallet-value-clean-{sqlite,mysql,postgres}.jsonl`: five actual native facts,
  complete narrow proof, then afterUncoveredWriterProofs showing a permanent gap.
  The explicit testProofClock advances beyond every fact timestamp; production
  clock and five-minute cutoff behavior are unchanged. This is a test fixture,
  not evidence that an existing live source is ready for activation.

Native sources: CNWalletValueAccount/Charge/Part/Slice/Lot, existing
CNFundingValueLot/Allocation, underlying User/wallet/Task receipts, and published
payment/lot/allocation/revenue heads. Empty activity, unknown history, suspension,
missing publication and cutoff not advanced never become complete. Global
funding_lots_allocations and other writer gaps remain required gaps.

## Verification

Go runtime: 1.27.2 windows/amd64. Declared Go 1.25.1 was not separately run.
Real engines: SQLite 3.50.4, MySQL 8.0.39 (REPEATABLE READ), PostgreSQL 17.6.
Disposable local ports only: MySQL 33174, PostgreSQL 55474.

Environment: AGENCY_HUB_RUN_EXTERNAL_DB_TESTS=1 plus
AGENCY_HUB_TEST_MYSQL_DSN / AGENCY_HUB_TEST_POSTGRES_DSN pointing to s4_cn_finance.

Core: `go test ./model -run '^TestCNWalletValueAcrossDialects$' -count=1 -v -timeout=180s`.
Set CN_WALLET_VALUE_WRITE_EVIDENCE=1 to emit the actual wire fixtures.
Raw final output: `model/testdata/cn-task/s4-wallet-value-matrix.log`.
Final combined selection additionally runs
`TestCNRefundProviderTransactionOwnershipAcrossDialects` and
`TestCNFinanceContractAcrossDialects`, with the same count/timeout/verbose flags.
PASS: 53.756s total; wallet 32.29s, refund ownership 9.31s, contract 11.49s.
The old external-snapshot case is intentionally inapplicable on SQLite; its
serialized-writer path is covered by the remaining SQLite matrix.

Coverage includes fresh migration twice; representative pre-lot User/TopUp/live
reservation upgrade twice; populated migration preserving outbox payloads;
unknown-first and mixed currency; zero bonus; exact cash residual and LIFO slices;
transaction failure; cancellation; cold flags-off recovery; original admin,
grant, debit, legacy reserve, Save-zero and nontransactional writer results;
net-zero invalidation; source transitions; same-user topup/reserve/final races;
three independent first-owner insert barriers on MySQL/PostgreSQL; committed
cross-month payment time; all five actual provider callback boundaries;
pre-staged immutable purchase facts; empty, complete and later-gap proofs.
It also covers deterministic reverse/same-second FIFO, billing-mode-only writers,
selected zero/omitted fields, and the old-snapshot admission/source-switch window.

Related selection `go test ./model -run 'CN|Topup|TopUp|PaymentSnapshot|AgencyFunding'
-count=1 -timeout=300s` PASS in 234.752s, including all existing refund tests.
This broad run preceded the final current-read suspension correction; the final
combined matrix above covers that correction and its exact snapshot regression.
Raw output: `s4-wallet-value-regression.log`. Backend compile-only check
`go test ./model ./service ./controller ./router ./relay -run '^$' -count=1`
PASS, recorded in `s4-wallet-value-compile.log` alongside the core log.
Prior baseline controller/service failures remain documented in
S4_TASK_HANDOFF.md; they are not full-suite passes or silently waived failures.

## Remaining Gaps

MJ, realtime/S4R, legacy Task, subscription recognition, refunds, redemption,
affiliate transfers, admin adjustments and generic quota writers are not new
native allocation entrances. Their GORM User mutation invalidates an admitted
ordinary pool; business behavior continues. External SQL, old binaries, custom
DB handles without startup callback, raw/unsupported ORM writes and Redis/batch
buffered changes cannot be certified. All writer processes must run the new
startup hook. No deployment, live account, production DB or real refund was used.

No minimum-version-only DB behavior was introduced; minimum supported server
versions were not executed. The root executable still needs existing web/dist;
affected backend packages are checked separately.

## F12 Provider Transaction Ownership

The approved narrow refund correction adds an atomic native transaction claim:
`CNRefundProviderTransaction.TransactionID = CNIdentity(providerName, receiptID)`.
Its primary key permits one ReservationKey only. The claim and cash outbox commit
in the existing finalization transaction. A competing reservation returns
refund_conflict, retains its committed executing intent and complete hold, and
cannot confirm success or publish a second cash fact. No provider adapter or
real provider I/O is added. Empty receipt IDs cannot succeed.

Cash facts retain `cn_refund_provider_receipt` and add evidence with fixed system
`gateway-cn`, recordType `cn_refund_provider_transaction`, recordId equal to that
transaction hash, and evidenceId binding provider, original receipt and complete
confirmed evidence. Cached/old successful Execute recovery checks ownership too.
Additive migration backfills claims from original successful receipts without
rewriting old duplicate receipts/facts; those duplicates remain report gaps for
Ops reconciliation. The guard prevents further reuse, not historical repair.

The existing refund test file now isolates its DB fixture. Three-dialect tests
cover simultaneous first claims from two distinct users/reservations, one success
and one conflict, restart/backfill twice, no second fact, old duplicate success
recovery blocked, empty receipts, and all prior refund workflow regressions.

## Contract Review Follow-Up

`S4_WALLET_INTEGRITY_V2_CONTRACT.md` defines the reviewed wire implemented by this
additive package. Neither the contract nor test exports activate a live source.

## Existing 196-User Source: Activation Boundary

The historical design rationale below describes the activation boundary. V1 still scans
all unknown historical charges. Such a live source cannot activate v1 merely
because it later exhausts unknown quota. Historical gaps cannot be cleared by
changing an authority row or starting an arbitrary report time window.

The proposed independent `ordinary-wallet/native-funding-integrity-v2` path and
`ordinary_wallet_native_funding_integrity` / `native-funding-integrity-v2` proof
can attest complete capture of the known native subset while the existing
required funding dependency continues to report unknown revenue and suspension.
Overall sourceCoverageComplete and financial reports remain incomplete.
This separation is feasible, but requires a versioned CONTRACT_V2 amendment and
consumer activation/reporting changes. No HTTP v3 or new financial fact shape
is inherently necessary.

The implementation and independent Ops activation must preserve these rules:

1. Persist immutable admission and per-source disposition receipts in the native
   credit/final-debit transaction: known, unknown, disabled, pre-existing fact,
   suspended, source switched. Include original TopUp/receipt key, quota,
   payment snapshot hash, currency, writer version and monotonic native sequence.
   V1 lacks a per-topup disposition ledger, so relabeling its current scan is
   insufficient to distinguish intentional unknown from lost native capture.
2. Freeze source identity/epoch, customer set and admission sequence. Capture a
   consistent publication/source watermark and explicit cutoff. A record's
   effectiveAt alone cannot define scope: late corrections keep old periods,
   and committed payment times can precede admission. A future scope must retain
   revisions of every chain it owns rather than dropping old-period corrections.
3. Scan independently: committed successful TopUps plus credited quota; native
   wallet and Task terminal/revision receipts; disposition/admission receipts;
   cash lots, allocations and increment slices; immutable published facts.
   Resolve every eligible source into known or explicit unknown disposition.
   Merely scanning existing shadow rows would miss a deleted/never-captured
   native chain and is not an integrity proof.
4. Validate the full graph in both directions. Each known lot has exactly one
   original payment, frozen currency/value/quota and no second pool owner.
   Every known allocation belongs to one charge+segment+customer and one lot;
   every native revision and revenue publication has the correct predecessor,
   exact cumulative/delta and links. Slice totals equal allocation quantity/value;
   unknown plus known allocation equals actual charge. Unknown plus nonisolated
   unspent native quota equals User quota plus supported in-flight holds.
   Missing, duplicate, orphaned, tampered, unpublished or currency-mismatched
   evidence makes integrity gap/pending, never an intentional unknown shortcut.
5. Existing Ops FIFO allocations must be reconciled before activating native
   authority for the same customer/lot/charge scope. Freeze a transition receipt
   with previous authority, last posted watermark, unposted native chain set,
   reconciled opening differences and exclusive source ownership. Already posted
   FIFO revenue is not silently replayed as native revenue. Either reconcile it
   explicitly or restrict native activation to a provably disjoint new lot and
   charge set. Reversals stay with their original owner across the boundary.
6. An intentional unknown charge can coexist with complete known-capture
   integrity, but contributes no guessed revenue and remains a required report
   blocker. Later uncovered writers permanently suspend new paid allocations;
   historical known reversals still publish. Any actual capture loss must stop
   the integrity proof independently of unknown coverage. Empty scans cannot
   activate an unobserved path.

The current v1 remains useful for collecting reviewable native evidence on the
existing source while reporting its real gaps. Activating known revenue for the
196-user source requires the reviewed v2 semantics and consumer work above;
the clean disposable fixture alone does not solve that operational requirement.
