# Agency browser acceptance

Run from the repository root in Windows PowerShell:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\agency-web\e2e\run.ps1 -TestRoot E:\new-api-test-cache
```

The runner builds the current React application, starts an explicitly enabled Go test fixture on localhost, and uses installed Microsoft Edge through Playwright. It places Go/Bun caches, browser temporary files, the SQLite database, build outputs, screenshots, traces, and JSON test results on the selected drive. It does not download a browser or contact the production deployment. Dependencies must first be installed with `bun install` in `agency-web`.

The fixture uses real Agency Hub Gin routes, isolated SQLite migrations, operator login, session validation, CSRF, verification proofs, idempotency, pricing publication, payout-account encryption, and withdrawal accounting. Root session creation and the upstream gateway verification page/signature issuance are test fixtures; this suite does **not** prove production gateway refresh/SSO, cross-origin cookies, TLS/mTLS, PostgreSQL/MySQL concurrency, external transfers, or deployment readiness.

The tests exercise:

- Agency creation, invitation link, temporary password visibility and explicit acknowledgement that destroys its stored ciphertext; the new operator must change that password before using business APIs, and cannot access Root APIs.
- Operator sales-only publication with unchanged settlement coefficient, plus a concurrent publication producing HTTP 409 while the user's draft remains intact.
- Payout-account creation, currency amount conversion, withdrawal submission, review, approval, payment lease creation, browser reload, and recording a simulated successful payment. The lease travels through browser JSON as an exact decimal string above JavaScript's safe-integer range, and the final persisted balances are asserted.
- An uncertain payment enters investigation and returns to approved only with structured unpaid evidence; available, locked and paid amounts stay unchanged during recovery before the next simulated payment attempt.
- An operator creates an asynchronous usage export, waits for the real export worker, downloads the CSV, and checks the UTF-8 BOM, expected same-agency rows and exclusion of another agency's data.
- Root starts provisioning a legacy customer with a running task, sees the blocker, cancels it without changing the original balance, and transfers an existing managed customer to a reviewed target agency with the expected binding revision.
- Root inspects actual reconciliation evidence; inconsistent wallet data and unsupported evidence cannot be closed. Restoring a missing delivery requires an issue/body-bound proof and creates exactly one delivery while preserving the immutable outbox and financial amounts. Saved evidence and audit survive reload, operators cannot read the Root endpoints, and manual reconciliation produces a durable execution record with the correct proof scope.

These scenarios are grouped into six browser tests. Creation, withdrawal,
export and reconciliation also save Chinese screenshots under the run's
`results` directory for visual review, including desktop and mobile evidence
dialogs. These show synthetic test data.

All identities, money and payment references are synthetic. The test server is excluded from production builds by the `agency_browser` build tag and also requires `AGENCY_BROWSER_FIXTURE=1` before starting.
