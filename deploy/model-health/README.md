# Standalone model health executor

The same immutable Gateway image can run `/new-api model-health-worker` as a
separate Compose project. It does not start an HTTP listener, run Gateway
migrations, poll customer tasks, or run wallet jobs. Normal Gateway processes
no longer start an embedded model health executor. Both Gateway slots use the
same primary-database lease to report whether the worker is available.

## First migration from an embedded executor

1. Follow the Gateway release gates for the HTTP slots, including the current
   availability, index and static asset checks. This first upgrade changes the
   Gateway code. It does not make an unverified ingress cutover safe.
2. Inventory every old embedded executor using the regional database. Stop
   admitting new monitoring work on the legacy executor before draining it.
   This applies to monitoring only; keep customer APIs and their task workers
   serving. An older binary cancels its worker on shutdown and does not honor
   the new executor lease. Wait for in-flight probe submissions, poll steps
   and notifications to persist their result or otherwise resolve before
   retiring it. A `polling` run with a saved upstream task ID and no active
   step can carry over; do not clear the queue or resubmit it. If an in-flight
   step cannot be drained safely, stop the migration and leave the Gateway
   serving. Confirm every enabled legacy executor has exited before starting
   the standalone service. For rollback to an enabled embedded executor,
   gracefully stop every standalone replica first and confirm the last
   instance has exited and released its lease (or the lease has expired).
3. Start the executor from the same tested release in a separate Compose
   project. `MODEL_HEALTH_IMAGE` selects the immutable image;
   `MODEL_HEALTH_ENV_FILE` selects a protected, region-specific environment
   file; and `MODEL_HEALTH_DB_NETWORK` selects an existing network with access
   to the same regional primary database and approved outbound HTTPS. Do not
   apply the executor Compose file to the Gateway project or publish a port.
4. Verify both slots report `worker: true` through the existing admin
   capabilities endpoint. Verify the model-specific plan
   is ready only for provisioned templates and credentials. Then verify a
   graceful stop and recovery without changing HTTP slot weights. Do not run
   paid probes or send live notifications without the corresponding approval.

During a mixed-version window, a passive legacy HTTP slot still reports
`worker: false` and cannot admit manual probes. Only upgraded HTTP slots read
the shared lease. Keep monitoring admission on upgraded slots once the
standalone executor is active; do not enable the legacy worker to make the
old slot report ready.

Use explicit `SQL_DSN` for the existing primary database; local SQLite also
requires `SQL_DSN=local` and an absolute existing `SQLITE_PATH`. The executor
does not need the Gateway session secret, payment settings or log database.
Keep dedicated probe credentials, their `_ORIGIN` bindings and robot secrets
on the executor. The shared lease contains binding hashes, not secrets.

For end-to-end probes through this Gateway, set `MODEL_HEALTH_GATEWAY_URL`
to its regional public HTTPS origin. Bind the dedicated
`MODEL_HEALTH_KEY_...` and `_ORIGIN` to that Gateway origin, not the
upstream. The executor sends the public model name to the configured platform
endpoint using the existing administrator token channel pin (single attempt);
the Gateway performs model mapping, authentication, billing and upstream relay.
It retains the `X-Oneapi-Request-Id` response for correlation. No platform
key is sent directly to an upstream or media download.

Use an enabled dedicated administrator service identity without a management
access token or interactive credentials. Its API token must have a
`model-health/` name, finite expiry, finite positive quota, an explicit model
allowlist and no automatic cross-group retry. The executor checks these fields
before publishing readiness and before every submission/poll step. Revoke the
token to withdraw detection readiness. Keep platform wallet funds, monitoring
budgets and actual-cost reconciliation separate from customer accounts; do not
mark an unknown charge as zero. Gateway mode accepts only known platform
endpoint paths and its standard video polling path. Existing upstream templates
must be reviewed for platform response contracts before switching the worker.

Keep `CHANNEL_TEST_ENABLED` and `CHANNEL_TEST_FREQUENCY` overrides aligned
with the Gateway's legacy channel-test configuration to prevent overlapping
automatic probes. An optional standby must have the same configuration and
secret references as the active executor.

After the first upgrade, an executor restart does not require changing Caddy
or either Gateway slot. Graceful shutdown finishes the current step and
releases the lease. After an abrupt exit, availability can take up to 75
seconds to expire. Existing submission leases still guard against reissuing
an uncertain paid POST; known asynchronous tasks retain their polling IDs.
