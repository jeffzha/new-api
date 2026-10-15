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
| Legacy wallet/batch/subscription | Durable intent only, `acknowledged_unverified`; never a settled debit fact from an acknowledgment outside the money transaction. |

Per-source dependency proofs scan actual durable rows and published receipts.
They cover named forward paths only. Historical/uninstrumented wallet writes,
ordinary/agency subscriptions and approved schedules, bonus/debt allocation
history, cash refunds and safe holds remain explicit gaps. Supplier bills,
provider fees and paid tax/expense evidence are Ops-owned dependencies that S5
must combine by registered authority. Zero rows do not remove required sources.
This slice cannot claim globally complete coverage, close readiness or verified
net operating benefit.

## Validation

`model/cn_finance_v2_test.go` consolidates fixture/revision/cursor/daily, durable
wallet, publisher starvation/poison and catalog regressions. Existing
`service/opsexport/server_test.go` covers v1 compatibility and v2 auth/error
contracts. The nine files in `model/testdata/cn-contract` are unchanged approved
synthetic fixtures, not production evidence.

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
