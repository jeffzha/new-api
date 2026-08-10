# Workbench private-file deployment

Workbench file upload is fail-closed and is disabled by default. It must not be
enabled until all checks below pass.

## Runtime contract

1. The browser streams a file only to ADP. ADP writes it to the dedicated
   `/var/lib/workbench-quarantine` tmpfs, validates name/MIME/magic bytes, and
   submits the full content to the internal ClamAV `INSTREAM` service.
2. Only a clean verdict is accepted. Timeout, unavailable scanner, malformed
   response, size-limit response, or any non-clean verdict rejects the upload
   before COS is called.
3. A clean file is written with a random customer/binding-owned key to an
   existing private COS bucket. The browser receives only an opaque
   `WorkbenchFileId` and inert display metadata.
4. The encrypted locator and SHA-256 stay in the ADP database. A short-lived
   signed download URL is created server-side only when sending an authorized
   chat request to the provider. Provider responses and history are projected
   before persistence so that this locator is not replayed to the browser.

The customer plan may contain a larger `max_file_bytes`, but the effective
limit is the minimum of the plan limit, process ceiling, ClamAV stream limit,
and per-upload quarantine budget. The shipped deployment reserves 100 MiB of a
128 MiB tmpfs for two uploads, so the effective per-file ceiling is 50 MiB.

## Required setup

- Create a dedicated COS bucket with public access blocked. Do not allow this
  service identity to change bucket ACL/policy or operate outside the
  `workbench/` prefix.
- When enabling files, put the least-privilege COS credentials in
  `secrets/files/cos_secret_id` and `secrets/files/cos_secret_key`; the files
  are not required while `WORKBENCH_FILES_ENABLED=false`. They are exported only as
  `WORKBENCH_FILE_COS_SECRET_*`, never as the legacy ambient `TC_SECRET_*`.
- Set `WORKBENCH_FILE_COS_REGION` and `WORKBENCH_FILE_COS_BUCKET` in `.env`.
- Set `WORKBENCH_WORKSPACE_HOST_SUFFIXES` to the exact verified DNS suffix
  returned by `CreateWorkspaceCredential`. Preflight rejects file enablement
  when this allowlist is empty or malformed.
- Keep `WORKBENCH_FILES_ENABLED=false` through the first deployment. Verify
  ClamAV is healthy and its signature database is current, then run the EICAR,
  timeout, size-limit, private-COS ACL, cross-user IDOR, signed-URL redaction,
  and compensation-delete tests before changing it to `true`.
- The default scanner image uses a domestic registry mirror. Pin the tested
  production image by digest instead of relying on the moving `stable-debian`
  tag.

ClamAV's TCP protocol is unauthenticated and unencrypted, so port 3310 is never
published and the scanner is attached only to the internal workbench network.
FreshClam alone receives egress for signature updates. The official ClamAV
documentation recommends substantial memory for signature loading and reload;
the overlay therefore defaults to 4 GiB for the scanner. If the host cannot
provide this safely, leave file upload disabled rather than weakening the scan
gate.

References:

- <https://docs.clamav.net/manual/Installing/Docker.html>
- <https://docs.clamav.net/manual/Usage/ClamdProtocol.html>

## Verification

Run `scripts/preflight.sh`. It validates the scanner/process/quarantine limits,
dedicated secret wiring, isolated tmpfs mounts, Blue/Green parity, and the rest
of the workbench deployment contract. Docker-level checks still require the
target Linux host because the local Windows workspace has no Docker daemon.

The `.env` file must be a current-user-owned regular file with no group/other
permissions (normally mode `0600`). It is parsed as a strict allowlisted
`KEY=VALUE` format; quoting, expansion, command substitution, duplicate keys,
unknown keys, surrounding whitespace, and control characters are rejected.
Neither preflight nor backup sources `.env` as shell code.
