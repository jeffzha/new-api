# Ordinary Wallet Native Funding Integrity V2 Proposal

Status: proposed for coordinator contract review; no v2 implementation or live
activation is authorized by this document. Target: the existing mixed Gateway
source, currently described as 196 users, with historical unknown value intact.
An empty-history v1 fixture is not acceptance evidence for that source.

## Contract Identity And Meaning

| Field | Proposed frozen value |
| --- | --- |
| supported path | `ordinary-wallet/native-funding-integrity-v2` |
| proof sourceId | `ordinary_wallet_native_funding_integrity` |
| proof version | `native-funding-integrity-v2` |
| allocation policy | `ordinary-unknown-first-paid-fifo-v1` |
| source evidence system | `gateway-cn` |
| financial fact wire | existing CONTRACT_V2 facts and cumulative revision chains |

Complete means every in-scope original native mutation has an immutable capture
disposition and every known-value chain reconciles through publication. Intentional
unknown does not make this capture-integrity proof incomplete; missing capture
does. Unknown contributes no inferred revenue. Existing required
`funding_lots_allocations`, historical/uninstrumented and suspended-source gaps
remain required and visible. Whole-report sourceCoverageComplete remains false.

CONTRACT_V2 must explicitly separate native-path activation prerequisites from
whole-report completeness. Activation checks this integrity proof, exact frozen
scope, publication watermark and exclusive accounting ownership. Required legacy
coverage gaps still block complete financial reporting. This is a versioned
contract amendment, not an authority-row toggle or a renamed v1 complete proof.

## Epoch, Population And Sequence

Persist a source epoch with sourceSystem, gatewayId, original streamEpoch,
integrityEpoch UUID, policyVersion, writerProtocolVersion and captureStartedAt.
Append-only cohort admissions record customerKey, membership sequence, native
User ID, admission time, original source/billing mode, immutable opening native
balance and all supported outstanding hold/receipt IDs with versions and amounts.
Freeze the initial customer manifest and its hash from a consistent source scan;
derive the count from that scan, never hardcode 196. Include historical users
with in-flight native work. Later users append admissions; removals/source
switches append tombstones and never delete earlier population.

Every supported wallet mutation obtains a monotonically increasing epoch sequence
in the same transaction as its native receipt, disposition and outbox writes.
A sequence is visible only on commit. Lock order is User, epoch counter, then
attribution rows; no code holding the epoch counter may acquire another User.
The source control/scan barrier also never locks a User after taking the counter.
This simple source counter serializes a small part of settlement; throughput
must be measured before rollout, with a versioned partitioned vector alternative
only if required. No reliance on timestamp order or auto-increment commit order.

A proof scope freezes `(source, streamEpoch, integrityEpoch, membershipThrough,
mutationThrough, publicationThrough, cohortHash, openingManifestHash)` and hashes
the ordered source/disposition/fact graph. A repeatable DB snapshot includes the
committed epoch counter and all corresponding receipts. Publication can lag:
missing publications yield pending. coveredThrough time is informational; the
sequence and chain ownership define membership and corrections.

## Persistent Dispositions

Add an immutable disposition ledger keyed by `(integrityEpoch, nativeKind,
nativeKey, nativeRevision)`, unique across retries. Store epoch mutation sequence,
admission ID, customer, original writer/version, native receipt hash, quota
delta/cumulative actual, payment snapshot hash, currency/value when verified,
original effective/occurred times, known/unknown portions, reason and exact
captured fact IDs. A disposition is not itself monetary revenue.

| Native boundary | Required receipt and disposition |
| --- | --- |
| each successful TopUp credit | native TopUp ID/tradeNo, credited quota, committed CompleteTime and frozen snapshots; known, unknown_missing_value, disabled, preexisting_fact, suspended, or source_switched |
| reserve/release/cancel | lifecycle or Task native revision, held delta, admission/opening-hold ownership; no revenue on hold |
| final debit / Task correction | original terminal/revision receipt, actual target and delta; exact known allocation slices plus unknown actual, including zero and negative corrections |
| uncovered quota/mode mutation | immutable invalidation/source-switch receipt and permanent suspension; original business operation persists |
| control flags / writer cutover | durable enable/pause/resume/broken-capture barrier, writer membership and last committed sequence |

Known requires verified native purchase evidence and unambiguous source ownership.
Unknown reasons are explicit and distinct from missing receipts. Pre-staged facts
remain immutable. Disposition corrections append linked revisions; a replay with
different source hash conflicts. Every native revision has exactly one owner and
one disposition even if no financial fact is appropriate. Quarantine is visible
as capture gap/pending and cannot stand in for an intentional unknown disposition.

## Independent Reconciliation

Build the scanner from original successful TopUps/actual credited mutations,
wallet lifecycle and Task final/revision receipts, original in-flight receipts,
User/source transitions and durable writer/control barriers. Independently join
those rows to admission/disposition ledger, cash lots/allocations/slices, outbox,
published facts and exclusive Ops ownership. Also scan the reverse direction to
find orphan dispositions, missing originals, duplicate claims, alien lots and
extra monetary facts. Scanning only existing shadow/capture rows is insufficient.

At each frozen watermark require:

1. Every original eligible mutation has one exact disposition; every disposition
   resolves to its original mutation and admitted customer. Unsupported writers
   and unexplained native balance changes create a gap, never fabricated unknown.
2. Each known lot has one verified original purchase, immutable quota/value and
   currency, purchase-time plus stable LotID ordering, and a single pool owner.
3. Each charge satisfies known allocated quota plus explicit unknown actual equals
   its native cumulative actual. Slice totals equal allocation quantity/value.
   Revisions, predecessors, deltas, payment/lot/customer/charge links and original
   currency agree through allocation and recognized_revenue publication.
4. Signed unknown balance plus nonisolated unspent known quota equals native User
   quota plus supported outstanding holds. Retired debt, isolated value and
   refundable holds have explicit ownership/equations and cannot be counted twice.
5. All expected monetary facts are immutable, staged once and published through
   the claimed watermark. Missing/unpublished is pending; mismatch, lost receipt,
   duplicate, orphan, invalid currency or unsupported mutation is gap.

Proof includes per-reason counts and frozen manifest hashes. Zero admitted native
activity cannot activate an unobserved path. Intentional unknown can coexist with
integrity complete only when original mutation and complete disposition reconcile;
the companion required legacy coverage proof remains gap and reports unknown.

## Opening Work And Late Revisions

Historical balances and inherited holds enter as explicit unknown. Do not
retroactively attribute old charges or carry inferred historical paid value.
Admission freezes each pre-existing receipt's ownership so finalization cannot
consume newly admitted paid lots for an old unknown obligation.

Owned chains remain in the epoch for all later revisions, even with pre-cutoff
effectiveAt or payment times before admission. A new revision gets a new committed
mutation sequence but retains the original chain/period and allocation owner.
Exact negative corrections unwind their original frozen slices and residuals;
they are not payment refunds. In-flight operations above the watermark are pending
in a later proof, never dropped by a time-window filter. Switching source never
reassigns historical reversals or automatically resumes a suspended pool.

## Ops Ownership And Activation

Before activation, persist a transition receipt with previous authority, customer
and chain/lot scope, last posted Ops watermark, native opening manifest and an
explicit reconciled native chain set. Ops must reject intersecting active claims
and atomically claim this epoch's monetary chains. Replaying native monetary facts
already posted by old FIFO is forbidden. First rollout should use a provably
disjoint set of new purchases and new charge chains; existing holds/charges stay
with their frozen old owner. Any overlapping value requires explicit reconciliation
approved under the frozen contract, not a SQL authority change.

Run register, native capture, publish, daily, source-vs-native compare, then
activate for the same immutable scope/watermark. Activation requires nonempty
observed known activity and zero unresolved integrity errors/pending publications.
Verify historical unknown and companion gaps remain in report output. A later
uncovered mutation suspends further known allocation and creates a required gap;
prior known corrections continue under their original owner.

## Flags And Rolling Upgrade

Deploy additive schemas and receipt-capable binaries with native authority off.
Record all writer instances/build versions and drain old writer processes and
their pending work before opening an epoch. Do not claim complete integrity while
old binaries, custom unguarded DB handles, Redis/batch, external SQL or unsupported
writers may mutate admitted balances. This operational fence is an activation
precondition, not something ExpectedQuota mismatch alone can prove.

Disable admission/allocation flags via durable control barriers. Existing epoch
receipt capture, unknown/disabled dispositions, suspension and exact reversals
must continue. Disabling the capture protocol itself or running an old writer
breaks the epoch; re-enable records the gap and cannot silently make it complete.
Upgrading writers changes their membership/barrier receipt before traffic resumes.
A new epoch requires explicit opening reconciliation and preserves all old gaps,
ownership and revision obligations; it cannot erase unknown history.

## Implementation And Acceptance Order

Freeze this contract and companion activation/report rules in root/Ops first.
Then implement additive epoch/admission/disposition/control models; transactional
writer receipts; independent scanner; wire proof; Ops exclusive claims/activation;
and operational rollout checks. Each step is separately reviewable.

Real SQLite/MySQL/PostgreSQL acceptance must cover a populated mixed-source
upgrade twice, existing holds and unknown history, known/unknown TopUps, same-second
and reverse payment order, simultaneous writes, missing/tampered native receipts,
missing/orphan captures and facts, publication lag, late 5->2->5->0 revisions,
source switch, flag pause, crash/restart, old-writer fence and overlapping Ops
ownership rejection. The end-to-end existing-source result must show native
integrity activation only after reconciliation while whole reports remain
incomplete because historical unknown and required legacy gaps are retained.
