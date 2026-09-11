# Agency Hub deployment

Agency Hub is deployed beside new-api and shares the primary database. It is
not placed in the model relay path. Run the migration command with a dedicated
migration credential before starting the service:

```sh
AGENCY_HUB_AUTO_MIGRATE=false ./agency-hub migrate
AGENCY_HUB_BASE_PATH=/agency \
AGENCY_HUB_PUBLIC_BASE_URL=https://gateway.nexus-reach.com \
AGENCY_HUB_COMMISSION_PROCESSING_ENABLED=false \
AGENCY_HUB_WITHDRAWALS_ENABLED=false \
./agency-hub serve
```

For Docker Compose, provide `SQL_DSN` and three host-side secret files through
`AGENCY_HUB_SSO_PUBLIC_KEY_HOST_FILE`,
`AGENCY_HUB_COMMAND_SERVICE_PUBLIC_KEY_HOST_FILE`, and
`AGENCY_PAYOUT_KEY_FILE`. The payout key file must contain one 32-byte
base64url or hexadecimal key; it is read through
`AGENCY_PAYOUT_KEY_FILE=/run/secrets/agency_payout_key` and is never placed in
the Compose environment. New ciphertexts use `AGENCY_PAYOUT_KEY_ID` (default
`agency-payout-v1`). During key rotation, mount a private file through
`AGENCY_PAYOUT_OLD_KEYS_FILE` containing one `key_id=key` entry per line. Old
keys are read only for decrypting historical account snapshots; all newly
written accounts use the current key.

The migration command is intentionally separate from `serve`. Run it with a
dedicated migration database credential, then start the service with the
restricted runtime credential. The container readiness probe is
`/agency/readyz`; `/agency/livez` is a database-independent liveness probe.

Run the repeatable local verification from the repository root with:

```sh
GO_BIN=/path/to/go deploy/agency-hub/verify.sh
```

This runs SQLite migration idempotency, reporting-index, readiness, and
`agency-hub` build checks. To include real MySQL and PostgreSQL migration
checks, set `AGENCY_HUB_RUN_EXTERNAL_DB_TESTS=1`,
`AGENCY_HUB_TEST_MYSQL_DSN`, and `AGENCY_HUB_TEST_POSTGRES_DSN` to disposable
test databases before running it.

Keep onboarding, commission processing, and withdrawals disabled until the
Phase A-E test gates pass. The service can be stopped without stopping
new-api; pending gateway outbox rows are consumed after it returns.
