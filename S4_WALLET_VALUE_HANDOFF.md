# S4 Ordinary Wallet Payment Value

Baseline: integrated Gateway `6325660`.
Branch: `codex/s4-paid-lot-attribution`.
This delivery is v1 only. It does not implement the proposed integrity-v2 proof.

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

`S4_WALLET_INTEGRITY_V2_CONTRACT.md` is the executable proposed contract for the
existing mixed source. It requires coordinator review/freeze before development;
this stable package does not implement v2 or activate a live source.

## Existing 196-User Source: Proposed Integrity V2

This is a design for contract review, not implemented behavior. V1 still scans
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

Minimum implementation after contract freeze:

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
