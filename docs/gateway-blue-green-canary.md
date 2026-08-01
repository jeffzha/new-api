# Gateway Blue/Green Deployment Runbook

This runbook applies the Nexus blue/green release and immutable-version rules
to `gateway.nexus-reach.com` (`124.174.0.221`) without changing the existing
Nexus SG Nginx workflow.

## Production topology

```text
Public HTTPS Caddy (Docker, ports 80/443)
  /apidocs/*                 -> api-docs:80
  material/liveness routes  -> hwdrama-proxy:3001
  all remaining routes      -> Caddy weighted blue/green proxy
                                  blue:  new-api:3000
                                  green: new-api-green:3000
```

Both application slots run in the existing `new-api-seedance` Compose project
and share PostgreSQL, Redis, `new_api_data`, and `new_api_logs`. The green
service is created by extending the production `new-api` service, so no host
application port is exposed.

The material proxy, API docs, PostgreSQL, Redis, and public Caddy remain
singletons. Deploying an application candidate does not recreate them.

## Version format

Gateway releases use an immutable image and runtime version:

```text
<nearest-git-tag>.gateway.<UTC yyyyMMddTHHmmssZ>.g<12-char-commit>[.dirty]
```

Production deployment requires a clean commit. The optional `.dirty` form is
available only for preflight output because remote builds use `git archive
HEAD` and cannot include working-tree changes.

The build passes `BUILD_VERSION` into the default/classic frontends and all Go
binaries. Deployment succeeds only when `/new-api --version` exactly matches
the planned image tag.

## One-time Caddy preparation

Install a managed 100% Blue / 0% Green block. This replaces only the final
`reverse_proxy new-api:3000` line; domain/IP certificates, API docs, material
routes, and liveness routes are preserved.

```powershell
powershell -ExecutionPolicy Bypass `
  -File scripts/install-gateway-canary-caddy.ps1 `
  -Yes
```

The script:

1. verifies the active Blue service;
2. backs up the production Caddyfile;
3. checks that nobody changed the file after it was read;
4. writes through the existing bind-mounted file inode;
5. runs `caddy validate` inside the production container;
6. reloads Caddy only after validation succeeds;
7. checks the domain and public-IP status endpoints;
8. restores and reloads the backup if validation, reload, or public checks fail.

The installation also records the currently running Blue image and runtime
version as its bootstrap release manifest, so a later emergency rollback can
verify that slot even though it predates this deployment workflow.

## Deploy the inactive slot

```powershell
powershell -ExecutionPolicy Bypass `
  -File scripts/deploy-gateway-slot.ps1 `
  -Slot Auto `
  -BatchUpdateMode Direct `
  -Yes
```

The deployment strictly validates the managed Caddy block and only replaces a
slot whose effective weight is zero. The weights are checked both before and
after the image build. Immediately before replacing the candidate, deployment
and traffic changes share a non-blocking server lock. This prevents an inactive
slot from becoming active while it is being replaced, while emergency traffic
changes remain available during the longer build and backup operations.

The remote Docker build uses:

```text
BUN_REGISTRY=https://registry.npmmirror.com
BUN_MAX_HTTP_REQUESTS=8
GO_PROXY=https://goproxy.cn,direct
```

The lower Bun request concurrency avoids integrity failures caused by
overloading the domestic mirror or the server's outbound path.
If Bun still reports a tarball integrity failure, the image build retries up
to `DockerBuildAttempts` times and reuses completed Docker layers. Other build
errors fail immediately and are never retried.

If an immutable image was built and verified by CI or by a detached recovery
job, deploy that exact tag without uploading source or rebuilding it:

```powershell
./scripts/deploy-gateway-slot.ps1 `
  -Slot Auto `
  -ImageTag '<prebuilt-version>' `
  -UseExistingImage `
  -BatchUpdateMode Direct `
  -Yes
```

`UseExistingImage` still requires an explicit Docker-safe tag. The server must
already contain that image, and the normal candidate health check still
requires `/new-api --version` to exactly equal the tag before a release
manifest can be written.

The production SSH endpoint throttles rapid new handshakes. The scripts pace
successive SSH/SCP sessions by 30 seconds by default; override
`SshConnectionCooldownSeconds` only after verifying the server-side limit.
Read-only checks and source uploads retry up to `SshReadRetryCount` times;
state-changing remote scripts are never retried automatically.

Before starting the candidate, the script writes a compressed PostgreSQL
backup and SHA-256 checksum under `/opt/new-api/deploy/backups`, then verifies
the gzip stream. It validates the Compose model, starts only the candidate
application, verifies health, direct billing mode, and the embedded runtime
version, and writes an atomic release manifest. Caddy traffic is unchanged.

## Canary and promotion

When Green is the candidate:

```powershell
./scripts/set-gateway-canary.ps1 -CandidateSlot Green -CandidateWeight 1 -ExpectedVersion '<deployed-version>' -Yes
./scripts/set-gateway-canary.ps1 -CandidateSlot Green -CandidateWeight 5 -ExpectedVersion '<deployed-version>' -Yes
./scripts/set-gateway-canary.ps1 -CandidateSlot Green -CandidateWeight 50 -ExpectedVersion '<deployed-version>' -Yes
./scripts/set-gateway-canary.ps1 -CandidateSlot Green -Promote -ExpectedVersion '<deployed-version>' -Yes
```

Every non-install traffic command must explicitly provide exactly one of
`CandidateWeight`, `Promote`, or `Rollback`. Positive candidate traffic also
requires `ExpectedVersion`; omitting an operation can never silently reset the
split to Blue.

Rollback Green to Blue:

```powershell
./scripts/set-gateway-canary.ps1 -CandidateSlot Green -Rollback -Yes
```

On the next release, after Green is active, Blue becomes the candidate:

```powershell
./scripts/deploy-gateway-slot.ps1 -Slot Auto -BatchUpdateMode Direct -Yes
./scripts/set-gateway-canary.ps1 -CandidateSlot Blue -CandidateWeight 1 -ExpectedVersion '<deployed-version>' -Yes
./scripts/set-gateway-canary.ps1 -CandidateSlot Blue -Promote -ExpectedVersion '<deployed-version>' -Yes
```

`CandidateSlot` makes Promote and Rollback relative to the actual candidate;
it avoids the legacy assumption that Green is always the new release.

Caddy uses a cookie policy with a random per-update secret and weighted round
robin fallback. Browsers remain on one slot while loading hashed frontend
assets; API clients that do not retain cookies still follow the configured
weights. Every traffic update generates a cryptographically random, unlogged
cookie-signing secret, so clients cannot forge access to a zero-weight slot;
changing weights also invalidates the old slot cookie.

Before assigning non-zero traffic, the script verifies that the candidate's
running image and runtime version match its release manifest. Supplying
`ExpectedVersion` additionally protects operators from promoting the wrong
candidate. Every traffic change automatically checks both public status URLs
and restores the previous Caddyfile if either check fails.

If a slot changes from zero to positive weight, that actual slot (not merely
the operator-supplied label) must match its release manifest. Before reload, the
script also checks the candidate through the Caddy container network, catching
missing DNS/network attachment that an application-local health check cannot.

## Verification order

After deploying the inactive slot and after each traffic change, check:

```powershell
curl.exe --fail --show-error https://gateway.nexus-reach.com/api/status
curl.exe --fail --show-error https://124.174.0.221/api/status
curl.exe --fail --show-error https://gateway.nexus-reach.com/apidocs/
```

Also verify that the public material/liveness routes still reach the singleton
`hwdrama-proxy`. A main-application release must not recreate or retag that
proxy.

## Database and background-task rules

Blue and Green share one database and Redis:

- schema changes must be backward-compatible and expand-only during canary;
- traffic rollback does not roll back schema changes;
- do not remove columns or change stored meaning in the same release;
- inactive candidates default to `BATCH_UPDATE_ENABLED=false` and a unique
  `NODE_NAME`; scheduled jobs must continue using the database lease;
- destructive cleanup belongs in a later release after rollback is no longer
  required.

## Operational notes

- Caddy reload is graceful; `stream_close_delay 5m` reduces disruption to
  existing streaming connections.
- A zero weight disables new selection of that upstream.
- Caddy actively checks `/api/status` and passively removes failed upstreams.
- Existing material proxy and API docs are deployed independently from the
  application slots.
- Do not run Docker volume or image pruning as part of a production slot
  deployment. Rollback images and named data volumes must remain available.
