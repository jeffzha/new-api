# claw-control admin UI

Static React administration client for the standalone `claw-control` service. It is intentionally isolated from new-api and the ADP fork.

## Security contract

- The browser enters through new-api's RootAuth-only admin ticket exchange. The SPA never accepts, stores, or sends `CLAW_ADMIN_TOKEN`.
- Every request is same-origin under `/api/admin/workbench/**` and uses `credentials: include`.
- Every `POST`, `PUT`, `PATCH`, and `DELETE` copies the non-HttpOnly `claw_admin_csrf` cookie into `X-CSRF-Token`. A missing cookie fails before the network request.
- Responses are treated as `Cache-Control: no-store` data. AppKey, SecretId, and SecretKey values are never accepted; forms accept only `env://WORKBENCH_PROVIDER_*` references and fingerprints.

## Expected API contract

The UI uses existing write endpoints plus these read projections:

- `GET /api/admin/workbench/dashboard`
- `GET /api/admin/workbench/customers/{id}`
- `GET /api/admin/workbench/plan-catalog?limit=100`
- `GET /api/admin/workbench/credential-profiles?limit=100`
- `GET /api/admin/workbench/audits?limit=100`

Existing APIs used are customer create/list/update, member add/role-update/disable, App save/trusted verification/transitions, plan publish/period/payment, invoice list, usage audit create/list/review, and credential profile create/list/rotation/retirement. App verification submits only optimistic-lock versions; administrators cannot self-report provider evidence or verification results.

Credential administration supports explicit `platform` and `customer:{id}` BYOK
ownership. Customer-filtered lists contain only platform profiles plus profiles
owned by that customer, and every profile projection omits Secret references and
resolved values.

When a pending config changes `credential_profile_id` on an App that already
has a current config, the Customer page exposes an explicit
`app_credential_change` approval request. Verification remains unavailable at
the backend until a different administrator approves the request and the
approval is executed from the Governance page. The first App configuration is
the only single-admin exception. Legacy fingerprint re-enrollment is a
maintenance API/CLI operation and is intentionally not exposed as a routine
browser control.

Audit actors are never browser assertions. The backend overwrites any
`X-Claw-Actor` value with the authenticated admin-session subject. Emergency
bootstrap access has a fixed actor and cannot mutate governance approvals.
Creating an additional App with a credential different from the primary App's
current credential is rejected with guidance to use a later approved
credential-change configuration.

The Usage page also manages optional Tencent Fee Center read-only import jobs.
Its create form accepts only a billing month and a pre-confirmed BusinessCode;
Tencent credentials and PayerUin are server-side configuration and are never
accepted or displayed. The page labels every resulting audit as account-scoped
and unverified. A successful import still requires the existing manual review
flow and never changes a customer invoice.

All endpoints return `{ "success": true, "data": ... }`; errors return `{ "success": false, "error": { "code", "message", "request_id" } }`.

## Build

```powershell
bun install
bun run typecheck
bun run test
bun run build
```

The static output is `dist/` and uses `/workbench/admin/` as its base path.
