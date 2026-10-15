# CN Finance V2 integration slice

Base: `4cbc5d90564238cd6d3464ebedf1015ee9198457`, domestic
`codex/seedance-cn-adapter`. This is a shadow integration slice, not full S4
acceptance or production authorization. No pricing, UI, enforcement, payment
execution or production database is changed by this delivery.

## Read interfaces

All use the dedicated export bearer configured by `OPS_EXPORT_TOKEN_SHA256`,
bound to persistent `OPS_GATEWAY_ID`, with `Cache-Control: no-store`.

- GET `/api/ops-export/v1/catalog`: complete configured metadata, synthetic
  channel/price-only models, disabled/hidden models, vendors and separate pricing.
  Catalog hash is independent from pricing values. Over 10,000 entries or 8 MiB
  fails with 413; no silent truncation. No channel secrets are selected.
- GET `/api/ops-export/v1/inventory`: repeatable identity snapshot shared with
  Bridge `objects/sync`, flat user/api_key/channel objects. Only ID/name/type/status
  columns are selected; soft-deleted users/keys and keys of deleted users are
  excluded. Envelope: gatewayId, sourceSystem=`gateway-cn`, schemaVersion=1,
  content-derived snapshotId, snapshotTypes, objects. More than 10,000 total
  objects or 8 MiB returns 413; unsupported query parameters return 400.
- GET `/api/ops-export/v2/facts?after=...&limit=...`: new source
  `gateway-cn-finance-v2`, schema `2`, persistent epoch, serialized sequence.
  Cursors are unpadded base64url `cnf2:<epoch>:<sequence>`. The old v1 endpoint's
  cursor happens to contain `v2:`; it is unrelated and rejected here.
- GET `/api/ops-export/v2/daily?date=YYYY-MM-DD&through=...`: immutable published
  facts and dependency checkpoint at the requested watermark. Raw cumulative
  version sums differ from economic revision deltas. Monetary totals and
  purchased-value recognition remain distinct.
- GET `/api/ops-export/v2/refund-capabilities`: `status=blocked`,
  `reservationAvailable=false`, `executionAvailable=false`,
  `manualVoucherEvidenceAvailable=false`. No money mutation endpoints exist.

`hasMore` describes published sequence backlog. False never proves source
completeness: inspect `publicationPendingCount` and daily dependencies. Checkpoint
rows can advance the cursor without an item. Waiting predecessors remain pending;
quarantined payloads and missing source evidence remain visible blockers.

New wire timestamps use UTC with exactly six fractional digits,
`YYYY-MM-DDTHH:MM:SS.ffffffZ`. The canonicalization does not change economic
identities or sourcePeriod. Already published historical payloads are immutable.
Dependency `coverageFrom == coverageTo` is legal for gap/pending with no covered
interval; `complete` requires strictly increasing bounds. During the first
five-minute cutoff window a source reports `coverage_window_not_advanced` rather
than claiming a complete empty interval.

## Local capture configuration

Defaults preserve existing business paths. Enable only in a reviewed isolated
environment after migrations and a complete balance-writer inventory:

```dotenv
OPS_GATEWAY_ID=your-stable-domestic-gateway
OPS_EXPORT_TOKEN_SHA256=replace-with-dedicated-random-token-sha256
OPS_CN_FINANCE_V2_ENABLED=false
OPS_CN_DURABLE_WALLET_ENABLED=false
OPS_ENFORCE_CALLS=false
OPS_PRICING_ENABLED=false
```

The ordinary durable session additionally requires batch updates and Redis to be
disabled. It freezes actual `common.QuotaPerUnit`, currency display mode and
runtime USD exchange rate at reservation. Original denomination is USD; only
USD/CNY modes produce nominal money. TOKENS/CUSTOM keep money unknown. These
nominal values do not prove cash or purchased-value revenue; tax remains unknown.
No arbitrary export-only conversion environment variables are used.

The additive `CN_EXPORT_0001` migration runs when the dedicated exporter starts.
Preserve all `cn_finance_*` and `cn_wallet_lifecycles` tables together. Never clear
the singleton or silently regenerate its epoch. Disabling flags does not erase
evidence. Drain in-flight durable sessions before switching binaries or flags.
Restoring an older application binary while durable sessions remain is unsafe;
pause admissions, reconcile them and retain the additive schema. Rebuild/replaced
streams require explicit Ops authority registration and comparison.

## Implemented source boundaries

| Source | Evidence and current scope |
| --- | --- |
| Ordinary billing session | Opt-in same-transaction user debit/lifecycle/outbox; zero delta, trusted zero reserve, supplements, releases, cancellation and image reserve rollback. Tokens retain existing separate transaction behavior. |
| Task adjustment/reconciliation | Hooks inside atomic funding/task CAS transactions; stable public task/request identity; frozen conversion inherited from an existing session chain. Task-only missing money stays unknown. |
| Durable agency final journal | Immutable native outbox scanned without source-row watermark, exact request association required; financial revisions may jump. |
| Agency funding | Forward native paid allocations frozen at financial journal commit; purchase value from `AgencyTopupFact`, never non-accounting `SourceSnapshotJSON`. |
| Payments | Verified top-up snapshot and native agency purchase evidence; missing amount remains unknown; matching native payment references suppress generic duplicates. |
| Commission | Committed `AgencyCommissionLedger.AmountMicros`; no coefficient-derived supplier cost or estimated tax. |
| Ordinary subscription session | Opt-in permanent request receipt, absolute reserve and final actual including zero delta/actual zero; subscription, token and immutable CN final charge commit together. Explicit cancellation intent and atomic recovery survive legacy record cleanup. |
| Legacy wallet/batch/subscription | Durable intent where instrumented, `acknowledged_unverified`; other direct writers remain uncovered. Never a settled debit fact from an acknowledgment outside the money transaction. |

Per-source dependency proofs scan actual durable rows and published receipts.
They cover named forward paths only. Historical/uninstrumented wallet writes,
direct/agency subscriptions and approved schedules, bonus/debt allocation
history, cash refunds and safe holds remain explicit gaps. Supplier bills,
provider fees and paid tax/expense evidence are Ops-owned dependencies that S5
must combine by registered authority. Zero rows do not remove required sources.
This slice cannot claim globally complete coverage, close readiness or verified
net operating benefit.

`OPS_CN_DURABLE_SUBSCRIPTION_ENABLED=true` selects the ordinary subscription
BillingSession lane when CN capture is enabled and Redis/batch are disabled.
`CNSubscriptionLifecycle` and `CNSubscriptionCancellation` are permanent financial
receipts, independent of legacy preconsume cleanup. Reservation retries freeze
identity and use absolute targets; final retries require the same actual amount.
The recovery worker drains explicit subscription cancellation intents as well as
wallet intents. Active requests without such intent are never auto-cancelled.
Legacy subscription refunds now mutate quota and their marker in one transaction;
that repair alone does not turn legacy requests into final CN financial receipts.

Subscription requests freeze `UsageGeneration` with explicit `PeriodBound=true`.
Automatic and both admin reset entrances (including no clock advancement) archive
the old total/used projection in `CNSubscriptionPeriod` and increment generation
in the same transaction. Late requests adjust only their frozen period and retain
its total limit. Reset/reserve/cancel lock user before subscription. Upgrade-era
receipts without period identity remain explicit `subscription_period_identity_missing`
gaps, never guessed to be current generation. Retain period archives permanently.
For negative token adjustments, a missing/deleted token is not recreated: frozen
owner is verified when the row remains, subscription quota is released, and the
permanent receipt records disposition plus exact `TokenDisposedQuota`. Positive
debits and owner mismatch fail closed. Raw/direct subscription writes remain
outside this generation-aware request coverage.

Daily adds `ordinary_subscription_durable_session` with version
`durable-subscription-session-v1`. Generic incomplete scopes use
`measured-scope-gap-v2`: observed pending/missing counts may be zero while explicit
`writer_coverage_unproven`, `historical_coverage_not_certified` or
`external_authority_proof_required` keeps status incomplete. Unsupported scopes
have null proofId/sourceCutoff. Empty scans never certify uninstrumented writers.

## Validation

Durable cancellation first commits `CNWalletCancellation` before the session
acknowledges refund. A startup/5-second worker drains explicit pending intents,
including after capture flags are disabled while the exporter remains configured.
Wallet balance, token quota, cancelled lifecycle, zero-charge fact and completion
marker commit together. Failures retain attempts/retry time/sanitized error code
and remain pending in dependency proofs. Intent persistence failure leaves the
session retryable. Active reservations without a terminal intent are never
automatically cancelled. Normal settlement still retains the baseline separate
token transaction; unrelated legacy paths have no new recovery guarantee.

`CNFinanceReceipt` freezes the financial transaction receipt time separately
from legacy usage event time. Corrections/releases use that receipt for occurredAt
and UTC daily deltas; effectiveAt/sourcePeriod retain original business time.
Older audited corrections can use `AgencyBillingCorrection.CreatedAtMS`; a
legacy reversal without a committed receipt stays an explicit gap.

`CNFundingValueLot` and `CNFundingValueAllocation` conserve exact paid value at
lot scope. Each new allocation uses HALF_EVEN_18; the final remaining quota
receives the exact residual. Partial release uses the allocation's frozen actual
value and full release restores it exactly. Upgrades seed immutable allocation
heads without repricing published history; inconsistent prior over-allocation
fails closed. Internal `GetCNFundingValueTx`, `QuoteCNFundingValueTx`,
`ReserveCNFundingValueTx`, `ReleaseCNFundingValueTx`, `RetireCNFundingValueTx`
hooks require a real caller transaction and user/account/native-lot locks.
Callers must update native spendable/held projections in that same transaction.
The exact quote uses the final remaining residual; a stale approval value is
rejected, never changed silently. Operation identity freezes lot/quota/value;
held -> released/retired is mutually exclusive and idempotent. Unknown provider
outcomes cannot be released on a timer. This does not enable cash execution.

Pool invariant: remaining + held + retired + active allocations equals initial
quota/paid value, with nonnegative buckets. Refund reservation moves exact value
from remaining to held, release returns it, and confirmed refund retires it.
These are internal balances, not new exported funding fields or independent
Ops-visible hold proofs. Ops may validate visible allocation totals against paid
or canonical net-paid evidence, but cannot independently verify this hidden pool.
CN outbox/publication and pool tables have no pruning/expiry implementation and
must be retained. Historical cleanup/archive restore needs a separately verified
head checkpoint; an empty pruned history must never become fresh paid value.

`model/cn_finance_v2_test.go` consolidates fixture/revision/cursor/daily, durable
wallet, publisher starvation/poison and catalog regressions. Existing
`service/opsexport/server_test.go` covers v1 compatibility and v2 auth/error
contracts. The nine files in `model/testdata/cn-contract` are unchanged approved
synthetic fixtures, not production evidence.

Native integration tests enter `RecordAgencyTopup`, reserve/finalize the accepted
agency journal and release the original component allocation. They distinguish
payment 80 CNY, nominal charge 42 CNY and purchased value 48 CNY, then verify a
20-quota release reduces purchased value by 16 CNY without fabricating cash
refund evidence. An aborted final transaction leaves no staged financial facts.
`S4_TEST_ARTIFACT_DIR` optionally writes actual Gin response bodies from the HTTP
test using an isolated SQLite database and synthetic inputs. It creates no live
payment executor and leaves no persistent HTTP server.

Run focused tests with Go 1.25.1+:

```text
go test ./model ./service/opsexport -run 'TestCN|TestOpsExport' -v -count=1 -timeout=180s
```

External engine tests require `AGENCY_HUB_RUN_EXTERNAL_DB_TESTS=1` and dedicated
disposable `AGENCY_HUB_TEST_MYSQL_DSN` / `AGENCY_HUB_TEST_POSTGRES_DSN` databases.
The tests drop their listed tables. Never point these variables at shared or
production databases. Exact engine/version results belong in the outer handoff.

Read-only bearer behavior follows the OWASP Authentication and Session Management
Cheat Sheets: server-side dedicated credential comparison, generic rejection,
no credential reflection/logging, no-store responses. Deployment must supply TLS,
network restrictions and token rotation. This is not a full ASVS compliance claim.
