# Ordinary Wallet Native Funding Integrity V2 Proposal

Status: wire candidate for coordinator freeze. Additive native foundation is
authorized; new wire/API and consumer activation await this contract's freeze.
No live activation is authorized by this document. Target: the existing mixed Gateway
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

First-version eligibility excludes every customer with any existing Ops FIFO
history or intersecting authority. A customer having historical unknown native
activity alone is eligible; the actual mixed-source population must be scanned,
not inferred from the descriptive count 196. Same-customer old FIFO plus native
transition is deferred. Existing holds/charges retain their frozen unknown/old
owner. Ops must reject intersecting active claims and atomically claim exact
manifest facts/chains. Replaying native monetary facts already posted by old FIFO
is forbidden. No path/time filter or evidence-recordType separation establishes
disjoint authority. The wire rules below define exact grant/borrowed membership.

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

Implement the authorized additive epoch/admission/original receipt/disposition/
scanner foundation separately, disabled by default. Freeze this wire and companion
activation/report rules in root/Ops before new wire/API or consumer integration.
Then integrate transactional writers, frozen export, Ops exclusive claims and
activation, and operational rollout checks. Each step is separately reviewable.

Real SQLite/MySQL/PostgreSQL acceptance must cover a populated mixed-source
upgrade twice, existing holds and unknown history, known/unknown TopUps, same-second
and reverse payment order, simultaneous writes, missing/tampered native receipts,
missing/orphan captures and facts, publication lag, late 5->2->5->0 revisions,
source switch, flag pause, crash/restart, old-writer fence and overlapping Ops
ownership rejection. The end-to-end existing-source result must show native
integrity activation only after reconciliation while whole reports remain
incomplete because historical unknown and required legacy gaps are retained.

## Exact Dependency Wire

Keep the existing exact `CNDependency` JSON field set and existing `CNFact` wire.
Do not add `integrity` or other extension keys to dependency or fact. The current
Go struct has 14 serialized keys (the coordination shorthand "16-field" does not
authorize adding two fields). All legacy source/version semantics stay unchanged.
The following new tagged tuple alone selects sidecar semantics:

```json
{
  "sourceId": "ordinary_wallet_native_funding_integrity",
  "version": "native-funding-integrity-v2",
  "required": true,
  "coverageFrom": "2026-10-15T00:00:00Z",
  "coverageTo": "2026-10-15T01:00:00Z",
  "sourceCutoff": "2026-10-15T01:00:00Z",
  "capturedThrough": "2026-10-15T01:00:00Z",
  "publishedThrough": "42",
  "pendingCount": 0,
  "unknownCount": 0,
  "supportedPaths": ["ordinary-wallet/native-funding-integrity-v2"],
  "excludedPaths": ["historical-and-uninstrumented", "ops-fifo-customer-transition"],
  "proofId": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "status": "complete"
}
```

The digest above is illustrative, not a valid downloadable proof. `proofId` is
required and content addressed for this tuple; fetch the exact manifest using it.
An unrecognized tuple, absent sidecar, wrong hash, incomplete pages or unsupported
sidecar schema fails closed. No dependency complete status alone grants authority.
For this version only, `unknownCount` counts unresolved integrity errors, including
missing capture, mismatch, orphan, duplicate and broken writer fence. It does not
count properly reconciled intentional unknown dispositions. Sidecar
`intentionalUnknownByReason` records those explicitly. `pendingCount` counts
unresolved originals/dispositions/facts awaiting completion/publication, each
entity once by its stable ID. Status complete requires both counts zero, an
unbroken fence, nonempty known activity and all graph/control equations verified.
Legacy required gaps and their unknown counts are never rewritten or suppressed.

## Canonical Encoding And Frozen Header

All objects below are strict: reject unknown/missing keys, duplicate JSON keys,
invalid enum values and noncanonical encodings. Nullable fields are present as
null. IDs are nonempty UTF-8 strings; array ordering is specified below. Sequence,
ordinal, revision, quota and count fields are decimal strings, regex
`0|[1-9][0-9]*`, bounded to signed int64; signed quota delta permits `-` only for
nonzero values. Values are canonical exact decimal strings, never JSON floats.
Times use existing CN UTC microsecond precision (`2006-01-02T15:04:05.000000Z`). Hashes use lowercase
`sha256:` plus 64 hex digits. Hash input is UTF-8 RFC 8785 JCS with no BOM/newline.
Both producers and consumers must use mature RFC 8785 implementations and the
same shared conformance vectors, including numeric and Unicode edge cases.
Do not write a custom number/Unicode canonicalizer. Go struct field order or
Python default JSON serialization is not canonicalization. Sidecar JCS bodyHash
is distinct from the existing CONTRACT_V2 digest algorithm (`v2.digest`); never
compare or substitute these hashes. Validate each using its declared algorithm.

An explicitly requested local `freeze` materializes rows from one consistent DB
snapshot. GET never scans mutable native/capture tables. The manifest has exactly:

| Key | Type and invariant |
| --- | --- |
| schema | literal `cn-funding-integrity-manifest-v2` |
| sourceId, version, path | exact tagged values above |
| sourceSystem, gatewayId, streamEpoch | existing immutable fact stream identity |
| integrityEpoch | independent UUID; never replaces streamEpoch |
| policyVersion, writerProtocolVersion | `ordinary-unknown-first-paid-fifo-v1`, `native-mutation-receipt-v2` |
| batchSequence, parentProofId | positive decimal string; first batch `1`/null, otherwise previous source-frozen proof hash in this integrity epoch |
| controlGeneration, writerSetHash, fenceHash | durable generation and frozen control/fence digests |
| membershipThrough, mutationThrough, publicationThrough | committed membership/mutation counter and original stream publication sequence |
| capturedAt, sourceCutoff, capturedThrough | UTC times; informational, not membership predicates |
| publishedThrough | decimal publication sequence, equal to publicationThrough and base dependency publishedThrough |
| authorityKinds | sorted array `charge,funding_allocation,funding_lot,payment,recognized_revenue` |
| supportedPaths, excludedPaths, companionSourceIds | sorted unique string arrays; include existing required legacy dependencies |
| status | `complete`, `pending` or `gap` |
| pendingCount, unresolvedIntegrityCount | decimal strings matching base counts |
| intentionalUnknownByReason | exact keys below, decimal-string original disposition counts |
| knownMutationCount, admittedCustomerCount | decimal strings; derived from frozen rows |
| collections | exact descriptor map for every collection listed below |

`intentionalUnknownByReason` exact keys are `opening_history`, `opening_hold`,
`missing_payment_value`, `allocation_disabled`, `admission_disabled`,
`preexisting_fact`, `suspended`, `source_switched`, `unsupported_path`. Each counts
dispositions whose primary reason is that key, including zeros. Unknown portions
are in disposition quota/value fields; these counts never imply known money.
`unsupported_path` is intentional only for a frozen excluded path/customer with
full original receipt; unsupported mutation of an admitted covered balance is an
integrity error. Pending/error detail belongs in `findings`, not this reason map.

Every descriptor has exactly `{count,root}`. `count` is a decimal string. A row's
ordinal starts at `0` and is contiguous. For collection name C, define
`h0=SHA256(JCS(["cn-integrity-rows-v2",C]))` and
`h(i+1)=SHA256(JCS(["cn-integrity-row-v2",hi,i,row]))`, with digests encoded in the
specified string format and i encoded as a decimal string. Root is h(count),
including h0 for an empty collection. Every row and every page must be consumed
to verify that root; a page digest alone is insufficient. No cross-resource hash
cycles occur. Manifest digest is SHA256(JCS(the whole manifest)), and both
snapshotId and proofId equal that digest; neither is a field inside manifest.
Parent hash is the previous immutable manifest, so this is acyclic.

## Frozen Collections And Graph Schema

Every collection exists, including empty ones. Fields below are exact, not
examples of optional metadata. References must resolve in this snapshot or in
the explicitly imported immutable parent history; the first version exports
cumulative collections, so all references resolve locally. String arrays are
sorted unique unless explicitly a chronological predecessor list. `body` in facts
is the unchanged complete CNFact. Rows sort lexicographically by the indicated
tuple using UTF-8 bytes, with sequence/revision compared numerically.

| Collection / sort key | Exact row keys |
| --- | --- |
| admissions / membershipSequence | admissionId, membershipSequence, customerKey, nativeUserId, billingMode, admittedAt, openingBalance, openingUnknownQuota, openingHoldIds, openingReceiptHash, nativeLineageId, eligibilityId |
| eligibility / customerKey | eligibilityId, customerKey, opsInventoryId, opsInventoryHash, opsFifoHistoryCount, intersectingAuthorityIds, decision |
| openings / openingId | openingId, admissionId, nativeKind, nativeKey, nativeRevision, heldQuota, actualQuota, ownerMode, nativeLineageId, originalBodyHash |
| originals / mutationSequence | receiptId, mutationSequence, membershipSequence, admissionId, customerKey, nativeKind, nativeKey, nativeRevision, predecessorReceiptId, writerId, writerBuild, controlGeneration, writerSetHash, fenceHash, effectiveAt, occurredAt, quotaDelta, cumulativeActual, heldDelta, originalBody, originalBodyHash, paymentSnapshotHash, nativeLineageId |
| dispositions / receiptId | dispositionId, receiptId, admissionId, customerKey, originalBodyHash, outcome, reason, knownQuota, unknownQuota, valueByCurrency, allocationSliceIds, factIds, nativeLineageId |
| slices / sliceId | sliceId, receiptId, lotId, allocationId, predecessorSliceId, quotaDelta, valueDelta, currency, nativeLineageId |
| memberships / factId | factId, bodyHash, kind, economicId, segmentId, revision, predecessorFactId, publicationSequence, customerKey, receiptId, admissionId, nativeLineageId, claimMode, borrowedAuthorityId |
| facts / factId | factId, bodyHash, body |
| edges / fromId,relation,toId | fromId, relation, toId, targetBodyHash |
| controls / generation | generation, previousGeneration, state, admissionEnabled, allocationEnabled, captureEnabled, writerSetHash, fenceHash, mutationThrough, operatorEvidenceId, changedAt |
| writers / leaseGeneration,writerId | writerId, buildId, protocolVersion, capabilityPaths, leaseGeneration, leaseExpiresAt, drainAcknowledgedThrough, membershipBodyHash |
| fence / fenceId | fenceId, generation, writerSetHash, writerIds, inventoryEvidenceId, inventoryEvidenceHash, oldWritersDrained, redisDisabled, batchDisabled, externalSqlExcluded, unguardedHandlesExcluded, operatorAttestationId, attestedAt, fenceBodyHash |
| findings / findingId | findingId, category, entityType, entityId, reason, pending, relatedIds, resolvesFindingId |

`nativeUserId` is a positive decimal string. `decision` is `eligible`/`excluded`;
eligible requires opsFifoHistoryCount `0` and no intersectingAuthorityIds.
`ownerMode` is `unknown` or `external_existing`; inherited holds never get new
known-value ownership. `nativeKind` identifies the original source boundary
(`topup_credit`, `wallet_reserve`, `wallet_release`, `wallet_final`, `task_reserve`,
`task_revision`, `quota_mutation`, `source_switch`). `originalBody` has exactly
`schema,nativeKind,nativeKey,nativeRevision,nativeRowId,customerKey,balanceBefore,
balanceAfter,heldBefore,heldAfter,actualBefore,actualAfter,billingModeBefore,
billingModeAfter,nativeStatus,paymentSnapshot,quotaConversionSnapshot,effectiveAt,
occurredAt`. Schema is `cn-original-mutation-v2`. IDs/status/modes are strings;
amounts use signed quota strings, nativeRevision is positive. The two snapshot
fields are null except for topup_credit, where each is either null (unknown value)
or the unchanged committed snapshot object from TopUp; their existing exact
schemas stay unchanged. Every field is present for every nativeKind: held and
actual are `0` only when that boundary actually has none. This is independent
source data, not a copy of attribution output. Topup credit matches credited
quota, successful status and CompleteTime; reserves/releases match native held
values; final/task revisions match original actual target. Source-native revision
history must be persisted before overwriting cumulative rows. Unknown original
schema makes the proof unverifiable. A new protocol schema requires review.

`outcome` is `known`, `unknown`, `mixed`, `no_economic_effect` or `invalidated`.
`reason` is null or one of the intentional unknown keys. Missing capture cannot
be represented as outcome unknown. `valueByCurrency` is an object of uppercase
ISO currency to canonical decimal. Nonmonetary reserve/release has no money.
`claimMode` is `grant`/`borrowed`, and borrowedAuthorityId is null exactly for grant.
Every member has one matching fact body/hash and original receipt/admission.
`relation` is `payment_lot`, `charge_allocation`, `lot_allocation`,
`allocation_revenue`, `receipt_fact`, `revision_predecessor` or `borrowed_authority`.
Edges bind exact body hashes, including borrowed payment/charge dependencies.
`state` is `prepared`, `fenced`, `active`, `paused`, `broken` or `closed`.
`category` is `missing_capture`, `mismatch`, `orphan`, `duplicate`, `fence_broken`,
`pending_native` or `pending_publication`; only the last two have pending=true.

Hashes must bind independent data without recursion: originalBodyHash hashes
only originalBody; native receipt identities/hashes exclude disposition/fact
hashes. Fact bodyHash hashes JCS of the actual unchanged CNFact body. Sidecar
memberships/edges bind those hashes after staging. nativeLineageId is stable from
original stable source/Gateway identity, customer and native purchase/charge key;
it MUST NOT contain streamEpoch, integrityEpoch, batchSequence, proofId or
authority ID. New stream/epoch/auth cannot
manufacture a new economic owner. SourceEvidence arrays are part of bodyHash;
legacy facts cannot be enriched to acquire membership.
membershipBodyHash hashes the writer row with only membershipBodyHash omitted;
writerSetHash hashes the sorted array of membership digests at the referenced
control generation. Lease renewal appends a new writer membership version and
control/fence; it never overwrites an earlier exported row. fenceBodyHash
hashes the fence row with only fenceBodyHash omitted and fenceHash equals that
digest. openingReceiptHash hashes the ordered opening rows of that admission.
paymentSnapshotHash is null or hashes the committed payment snapshot alone.

## Readonly Export And Verification

Reuse existing export-token bearer authentication, constant-time configured hash
comparison and no-store behavior. Base URL is `/api/ops-export/v2/funding-integrity`.
No refund credential and no public control mutation endpoint are introduced.

| Method/path | Result |
| --- | --- |
| GET `/snapshots/{proofId}` | exact manifest bytes; content hash must equal proofId |
| GET `/snapshots/{proofId}/{collection}?ordinal=0&limit=500` | frozen contiguous page |

Allowed collections are exactly the table above. `ordinal` is the next decimal
ordinal; limit is an integer 1..1000, default 500. Snapshot/resource/ordinal are
the full page identity; no mutable "latest" alias. Response has exactly
`schema,snapshotId,proofId,collection,ordinal,nextOrdinal,totalCount,root,
prefixHash,suffixHash,rows`. Schema is `cn-funding-integrity-page-v2`;
snapshotId=proofId, prefixHash=h(ordinal), suffixHash=h(ordinal+len(rows)),
nextOrdinal is null at count, otherwise that ending ordinal. Row order is frozen.
End ordinal returns an empty terminal page. Ordinal beyond count, unknown query
key, unknown collection or malformed identity returns 400; absent snapshot 404;
incomplete/unsealed snapshot 409. Retention may return 410 for explicitly retired
unreferenced snapshots, but active/parent-referenced proofs cannot be retired.
Retries return identical rows and hashes; consumers reject skipping/duplicates,
cross-proof pages, count/root mismatch and reference/hash mismatch. Publication
progress requires a new frozen snapshot, never rescanning while paging.

Ops imports every collection, validates canonical hashes/counts/roots and the
original receipt/disposition/fact graph, independently checks its local posting
and authority inventory, and commits a verified proof record. It does not trust
Gateway's eligibility declaration in place of checking local Ops state. A
manifest root proves immutable bytes, not source truth; original receipt scans,
writer fencing and accounting equations are independent required evidence.

## Exact Authority And Continuous Batch CAS

The initial authority can grant all five kinds `payment`, `charge`, `funding_lot`,
`funding_allocation`, `recognized_revenue`, but ONLY the exact membership rows
whose claimMode is grant. This closes the actual production dependency problem:
old payments_topups/charge global proofs can be gap and there is no preexisting
Bridge authority. Do not relax those global proofs or grant the whole tagged
path/time interval. A known lot requires its linked verified payment; a recognized
amount requires its linked original charge, allocation and actual canonical cash
posting. A borrowed dependency must already have exactly one eligible authority,
matching body hash and successful canonical posting. It cannot simultaneously
be granted here. A mixed graph with absent/unposted/ambiguous dependency is pending
or gap, never recognized revenue with invented cash.

Compare every proposed grant with ALL active authority predicates, all sourceEvidence
entries and all already posted facts/chains for the source/customer. Two recordType
filters can match different evidence entries of one fact. Thus a broad topup
authority whose economicKinds contains funding_lot intersects a native lot with
both topup and native-lot evidence; reject it before activation. A reviewed scope
split is a later transition protocol. First production registration may configure
payment-only authority if needed, marking those exact payments borrowed; otherwise
one exact manifest authority grants the closed five-kind graph. Existing unknown
history does not block this finite known subset and never becomes grant membership.

Persist an immutable economic owner key from
`(sourceSystem,gatewayId,customerKey,kind,economicId,segmentId,
nativeLineageId)`. Use the existing canonical kind-chain identity to detect
equivalent aliases. Independently enforce uniqueness of stable
`(sourceSystem,gatewayId,customerKey,nativeChainKey)` so changed financial chain
aliases cannot evade the original native owner; nativeChainKey is the original
native purchase or charge identity. streamEpoch and integrityEpoch are attributes
only. Source/stream reset or renaming cannot evade an existing owner. Atomic
claim uniqueness is both per stable chain and per canonical factId/bodyHash.
The claimed owner is fixed on the first accepted grant and shared by every
revision; authority ID, integrityEpoch and batch are attributes, not key material.
Opening unknown/external chains cannot later acquire a native known owner.

Each new frozen batch is cumulative and extends the prior accepted batch:

1. Freeze against one committed counter/snapshot. batchSequence increments by one,
   parentProofId is the exact previous source-frozen proof (Gateway cannot know
   Ops acceptance), and all three sequence
   cutoffs are nondecreasing, including equality. Publication cutoff stays in the
   original stream. A new hold/unknown/control/broken batch may have exactly the
   same publicationThrough as its parent and MUST still be discovered/imported.
2. Retain every prior immutable row and membership unchanged, including fact
   hashes, claimMode, owner lineage and source receipt. Append new originals,
   revisions, controls, findings/resolutions and publication memberships only.
   A finding resolution is a new finding event referencing the earlier ID; the
   earlier evidence remains. Snapshot counters summarize unresolved heads.
3. Verify all new rows, parent immutable row-set inclusion and the full graph.
   Re-sorting a larger cumulative set can change ordinals; compare stable row IDs
   and row hashes across batches, not page ordinal prefixes. Late
   revisions have new mutation/publication sequences but original chain owner,
   even when effectiveAt predates admission. They cannot be dropped by time.
4. In one Ops transaction, CAS the accepted head from
   `(source,streamEpoch,integrityEpoch,parentProofId,batchSequence-1)` to this
   verified imported proof head. Ops fetches missing source-frozen parents and
   imports every parent in order. A complete eligible batch atomically claims
   new chains/facts and advances ingest authorization; pending/gap batches are
   durably imported without new grants and retain their findings/control fence.
   Same proof retry is idempotent. Different child of an already advanced head
   conflicts; do not partially install either child. Source freeze itself uses
   a durable parent-head CAS, so source siblings cannot be sealed normally.
5. Ingestion may post only exact granted membership from verified accepted
   batches, and borrowed facts use their existing unique owner. Arrival ahead of
   a verified batch waits. New batches permit continuous sync; accepting the first
   batch is not permanent permission for future arbitrary facts. Publication lag
   or broken capture stops head advancement and preserves prior owner/corrections.

The fixed nullable `resolvesFindingId` references an earlier finding, and counts consider only
unresolved heads. A resolution has the same category/entity and reason evidence,
and never deletes the original. This field is part of the exact findings schema.
New epochs require separately reviewed opening/fence and retain the stable chain
claims; they cannot reclaim old facts or reset old gaps.

## Durable Start And Mutation Fence

Default is no epoch and no v2 complete proof. Environment flags alone cannot
create an epoch, grant writer membership or attest drainage. Use local operator
CLI commands with explicit expectedGeneration; prepare -> register writer set ->
drain and attest -> fence -> start. Every change appends a control receipt and
CASes durable generation. prepare cannot admit known value; start requires frozen
inventory evidence for all actual writer processes/handles and their pending
work. Unknown old binaries, Redis, batch or external SQL writers prevent complete.
Heartbeat presence cannot prove their absence. Trusted operator attestation and
deployment inventory are required evidence, alongside DB capability checks.

Every covered original mutation, including flags-off capture and correction,
locks User then current durable epoch control/counter, validates exact active
generation, writer membership/build/capabilities/lease and fence hash in that same
transaction, then writes original receipt, disposition and attribution/outbox.
Never acquire another User while holding counter. Control operations use counter
without later acquiring User; this fences in-flight writers at a committed prefix.
Business mutations with unsupported/broken capture append an invalidation when
possible and leave the epoch broken; they cannot produce a complete prefix beyond
the break. Durable admission/allocation pause continues receipts and unknown
dispositions plus original frozen reversals. Disabling capture permanently breaks
that epoch. Restore requires a new reviewed epoch and retains previous evidence.

Freeze validates durable generation/writer fence against its committed prefix,
independently enumerates original business receipts forward and reverse against
the graph, and seals only immutable rows. A scanner that sees only successful
disposition rows or uses ExpectedQuota equality as writer coverage is insufficient.
There is no automatic startup migration that opens/fences/activates an epoch.

## Consumer Files And Native Identity

Machine-readable snapshot definitions are in
`model/testdata/cn-task/integrity-v2-snapshots.schema.json`. Both snapshot keys in
originalBody are always present; each is null or the decoded committed object,
never a JSON string containing JSON. Their nested field names are snake_case.
`paymentSnapshot` has exactly `actual_money,currency_code,payment_reference`.
Empty actual_money is intentional unknown; nonempty actual_money requires a
three-uppercase-letter currency and canonical nonnegative decimal, max 64 UTF-8
bytes. payment_reference is trimmed, nonempty and max 191 UTF-8 bytes; schema
maxLength alone does not replace that byte bound. Preserve verified zero exactly
as `0`. Never normalize or enrich a legacy stored snapshot on import.

`quotaConversionSnapshot` has required `schema_version` (integer 1), `basis_field`,
`basis_value`, `calculation`, `credited_quota`, and optional `quota_per_unit` (omit
when absent, do not encode null/empty). Basis fields are `topup.amount`,
`topup.money`, `topup.money_decimal`; calculations are `multiply_quota_per_unit`,
`identity`, `assisted_exact_quote`. basis_value/quota_per_unit are canonical
nonnegative decimal strings, max 128 bytes. Multiplication requires a strictly
positive quota_per_unit. credited_quota is positive decimal int64 and must equal
the original actually credited mutation; int64/schema/decimal bounds are checked
in addition to the structural JSON schema. Calculation values describe the saved
provider conversion, never a new currency conversion or inferred payment amount.

`CNIntegrityNativeIdentity` in `model/cn_integrity_wire.go` is the shared Go
identity definition. Customer is the existing positive User ID decimal string.
For every original/opening row map nativeKind to the stable entity/key below:

| nativeKind | entity | nativeKey |
| --- | --- | --- |
| topup_credit | topup | original TopUp.TradeNo |
| wallet_reserve, wallet_release, wallet_final | wallet | original CNWalletLifecycle.RequestID |
| task_reserve, task_revision | task | original CNTaskBilling.Key (not public/provider TaskID) |
| quota_mutation, source_switch | user | original User.ID decimal string; not grantable economics |

Exact nativeChainKey is lowercase raw SHA256 hex of RFC8785 JCS encoding of
`["cn-native-chain-v2","gateway-cn",gatewayId,customerKey,entity,nativeKey]`.
Exact nativeLineageId is lowercase raw SHA256 hex of JCS encoding of
`["cn-native-lineage-v2",nativeChainKey]`. These ID values are raw 64-hex strings;
hash-typed fields still use `sha256:`. No concatenation, separator escaping,
numeric ID coercion, Unicode normalization or epoch/auth suffix is allowed.
These new identities do not use the legacy CNIdentity JSON codec/digest. The Go
helper first marshals the string array then invokes the mature JCS implementation;
the marshal engine's intermediate escaping cannot change canonical bytes.
Payment/lot share a purchase owner; charge/allocation/revenue share their original
charge owner, with graph edges retaining each purchase dependency's separate owner.

`model/testdata/cn-task/integrity-v2-jcs-vectors.json` contains input bytes,
canonical bytes (both base64) and expected SHA256 for numeric, Unicode, escaping,
snapshot, large string-sequence and native chain cases. Go uses pinned
`github.com/cyberphone/json-canonicalization v0.0.0-20261011152517-32a48195b9b2`;
Python uses `rfc8785==0.1.4`. Run `go test ./model -run TestCNIntegrityWireVectors`
and `python model/testdata/cn-task/verify_integrity_jcs.py`. The adjacent generator
prints the same deterministic fixture using the Python implementation. Both
implementations must pass exact bytes/hash equality before accepting a proof.
This does not change or reinterpret existing `v2.digest` facts.

The unchanged S5 `sample/cn-integrity-v2/jcs-vectors.json` corpus is also included
as `model/testdata/cn-task/integrity-v2-shared-jcs-vectors.json` and tested by both
Go and Python. These are the same four numeric/binary64/UTF16/non-normalization
vectors; the eight bytes/hash vectors above supplement them with identities and
snapshot examples. Validation passed with both pinned implementations; raw log:
`model/testdata/cn-task/s4-integrity-wire-jcs.log`.

## Append-Only Snapshot Discovery

Every sealed freeze has an independent positive int64 `freezeSequence`, from one
durable Gateway-global counter, shared across integrity epochs and original
stream epochs. Never reset it on stream rebuild. It is index metadata, outside
the manifest hash (the immutable index entry binds the manifest proofId).
Sealing manifest/collections, assigning freezeSequence, appending the index entry
and CASing the source frozen parent head occur in one transaction. A failed
freeze publishes neither a discoverable index entry nor a new parent head.
The counter is not a native mutation/publication counter and never changes facts.

Readonly GET `/api/ops-export/v2/funding-integrity/snapshots?after=0&limit=500`
reads only the append-only sealed snapshot index, with the existing export-token
auth. It never scans current native/capture/disposition tables and never causes
a freeze. Allowed query keys are only `after` (default `0`, decimal int64 cursor)
and `limit` (default 500, range 1..1000). Malformed/unknown query keys return 400.
The `/snapshots` list route is distinct from `/snapshots/{proofId}` manifest GET.

Response exact keys are `schema,sourceSystem,gatewayId,after,observedThrough,
nextCursor,hasMore,items`. schema is `cn-funding-integrity-index-v2`; sourceSystem
is `gateway-cn`; gatewayId is the stable configured identity. Cursor/count fields
are decimal strings; hasMore is boolean. observedThrough is the highest sealed
freezeSequence visible in the index read snapshot, or `0` when empty. Each item
has exactly:

| Key | Type/invariant |
| --- | --- |
| freezeSequence | decimal positive int64, ascending and greater than after |
| proofId | content-addressed sealed manifest hash |
| sourceId, version, path | exact new integrity tagged tuple |
| sourceSystem, gatewayId, streamEpoch | manifest source identity; streamEpoch is per item, since this index spans rebuilds |
| integrityEpoch | independent integrity epoch UUID |
| batchSequence | positive decimal int64 in that epoch |
| parentProofId | null for batch 1, otherwise previous source-frozen proofId |
| publicationThrough | original-stream decimal publication sequence, allowed equal to parent |
| status | immutable manifest `complete`, `pending` or `gap` status |

Return the first limit entries with `after < freezeSequence <= observedThrough`.
nextCursor equals the last returned freezeSequence, or the supplied after for an
empty page. hasMore is true exactly when an additional sealed index entry exists
in that observed prefix. No filter on status, publicationThrough, economic fact
count or dates is permitted. An after ahead of current observedThrough returns
409 `integrity_discovery_cursor_ahead`; do not silently reset an Ops checkpoint.
Index entries and referenced manifests are retained permanently for the initial
version; index compaction/epoch reset requires a separate reviewed protocol.

Ops checkpoints discovery by freezeSequence only, after the entry and complete
immutable payload are durably stored (verification/grant work may remain pending).
It persists each manifest's separate verified/imported/authorized state. A list
poll after hasMore=false resumes at nextCursor and finds later freezes, including
equal-publication and broken-capture batches. Duplicate entry/proof retries are
idempotent only with identical fields. Fetch any missing parent by proofId and
perform ordered per-epoch parent CAS; discovery across epochs does not change
stable native owner claims. Verify index identity/cutoff/status against the hashed
manifest. A proof content hash does not replace the authenticated index's sequence
and source checks.

Existing `/daily` remains keyed/frozen by its existing day and original publication
sequence, and its existing payload/proofs are never rewritten because a new
sidecar was frozen. Do not advance a fabricated fact sequence to advertise an
unknown/control-only batch. V2 authority/health uses the independently verified
snapshot register and discovery checkpoint, not a replacement proof inside an
already frozen `cn_daily(day,seq)` row. The exact existing CNDependency shape above
is the tagged dependency representation for an immutable proof; consumers derive
it from that exact frozen manifest/proofId when storing the v2 snapshot register.
Changing sidecars do not make old global payment/charge/historical proofs complete.
