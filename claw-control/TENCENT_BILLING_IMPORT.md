# Tencent Fee Center read-only import

This optional claw-control worker imports platform-account cost evidence. It is
not a customer billing engine and cannot update an invoice, new-api balance,
quota, API key, relay request, ADP Turn, or customer/App attribution.

## Provider contract

The only upstream origin is:

```text
POST https://billing.tencentcloudapi.com/
Content-Type: application/json; charset=utf-8
Authorization: TC3-HMAC-SHA256 ...
X-TC-Version: 2018-07-09
```

The worker calls:

1. `X-TC-Action: DescribeBillDetail` with `Offset`, `Limit<=300`, `Month`,
   `NeedRecordNum=1`, required operator-confirmed `BusinessCode`, optional
   returned `Context`, and the server-configured `PayerUin`.
2. `X-TC-Action: DescribeBillAdjustInfo` with the same `Month` and server-side
   `PayerUin`.

The client rejects redirects, bounds each request, response body, page count,
record count, evidence bytes, and retry count, and starts calls no faster than
one every 200 ms. `Total` is not trusted as an exhaustion condition because
Tencent documents that it may be cached and lower than the actual record
count. A short final page ends pagination; a full final allowed page fails with
`page_limit_exceeded` instead of silently accepting incomplete evidence.

Official documentation:

- [DescribeBillDetail](https://cloud.tencent.com/document/api/555/19182)
- [DescribeBillAdjustInfo](https://cloud.tencent.com/document/api/555/112039)
- [TC3-HMAC-SHA256 signing](https://cloud.tencent.com/document/product/1278/46712)

## Local administrative API

All endpoints require the existing claw-control administrator session and CSRF
protection for mutations. Emergency bootstrap access also requires
`X-Claw-Actor` under the existing middleware contract.

Create or idempotently retrieve a run:

```http
POST /api/admin/workbench/tencent-billing-imports
Content-Type: application/json

{
  "month": "2026-08",
  "business_code": "p_adp"
}
```

The API deliberately does not accept SecretId, SecretKey, PayerUin, customer,
App, plan-period, resource, cost, confidence, evidence, or invoice fields.
`month` must be within the current Tencent 18-month window. The response is
`202 Accepted` and contains an opaque `import_id` with `status=pending`.

```text
GET  /api/admin/workbench/tencent-billing-imports?limit=100
GET  /api/admin/workbench/tencent-billing-imports/{import_id}
POST /api/admin/workbench/tencent-billing-imports/{import_id}/retry
```

Retry accepts no body and only transitions `failed` back to `pending`. Reusing
the same payer/month/BusinessCode scope never creates a second ImportRun or a
second UsageAudit draft.

The response projection includes only scope, progress, exact non-negative cost,
review reasons, safe provider query RequestIds, evidence reference/hash,
UsageAudit public ID, sanitized error code, and timestamps. It always states:

```json
{
  "account_scoped": true,
  "allocation_confidence": "unverified",
  "invoice_mutation": false
}
```

## Amount and evidence rules

Every component is parsed from its decimal string. Scientific notation,
non-numeric values, more than 18 integer/fraction digits, and absurd aggregate
amounts fail closed.

```text
raw_cost_cny   = sum(DetailSet[*].ComponentSet[*].RealCost)
draft_cost_cny = max(raw_cost_cny, 0)
```

No float conversion or automatic rounding occurs. Every run requires manual
review because it is account-scoped. Negative bill components, a negative net
cost, and any abnormal adjustment add explicit review reasons. Negative net
cost is clamped to zero; the worker never creates a credit. Adjustment amounts
are not added a second time because the relationship between adjustments and
detail rows must be reviewed from provider evidence.

All successful raw action responses are wrapped with their action-specific
query RequestId, passed through the evidence MIME/content and ClamAV pipeline,
then AES-256-GCM encrypted in the existing evidence store. The evidence hash
and query RequestIds are independent from ADP chat/Turn RequestIds.

## Configuration

| Variable | Default | Rule |
|---|---:|---|
| `CLAW_TENCENT_BILLING_IMPORT_ENABLED` | `false` | Feature and worker switch |
| `CLAW_TENCENT_BILLING_SECRET_ID` | empty | Required only when enabled; runtime secret |
| `CLAW_TENCENT_BILLING_SECRET_KEY` | empty | Required only when enabled; runtime secret |
| `CLAW_TENCENT_BILLING_PAYER_UIN` | empty | Numeric server-side payer scope |
| `CLAW_TENCENT_BILLING_IMPORT_INTERVAL` | `5s` | Worker poll interval |
| `CLAW_TENCENT_BILLING_TIMEOUT` | `15s` | Per-request timeout, at most 30s |
| `CLAW_TENCENT_BILLING_PAGE_SIZE` | `300` | 1 to Tencent's maximum 300 |
| `CLAW_TENCENT_BILLING_MAX_PAGES` | `100` | Hard page ceiling |
| `CLAW_TENCENT_BILLING_MAX_RECORDS` | `30000` | Hard record ceiling, never over 200000 |
| `CLAW_TENCENT_BILLING_MAX_ATTEMPTS` | `3` | Automatic attempts per manual retry window |
| `CLAW_TENCENT_BILLING_LEASE_DURATION` | `30m` | Renewable multi-replica claim lease |
| `CLAW_TENCENT_BILLING_MAX_RESPONSE_BYTES` | `4194304` | Per-action response ceiling |

The Compose entrypoint reads credentials only from
`secrets/billing/tencent_secret_id` and
`secrets/billing/tencent_secret_key` when enabled. Both must be owned by UID
10001 and inaccessible to group/other users. Credentials and raw PayerUin are
absent from ImportRun rows, administrator audit hashes, logs, and API responses.

## State and recovery

```text
pending -> running -> draft_created
                   -> pending (bounded transient retry)
                   -> failed  (permanent/exhausted)
failed  -> pending (explicit administrator retry)
```

Claim and completion use conditional `status + row_version + lease_token`
updates. MySQL/PostgreSQL additionally lock the candidate row; SQLite relies on
the conditional update. A separate account-wide coordinator lease serializes
all provider jobs across Blue/Green, so the five-request/second client limiter
is not multiplied by replica count. The active worker renews both leases after
each provider response. Expired exhausted leases become safely retryable `failed` records.
UsageAudit's nullable unique `import_source_key` is a second idempotency guard
if a crash occurs after draft creation but before ImportRun completion.
