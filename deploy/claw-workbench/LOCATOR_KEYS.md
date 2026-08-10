# Workbench locator keys

The ADP runtime uses two independent encryption domains for opaque locators:

- `secrets/adp_file_locator_key` encrypts private COS file locators;
- `secrets/adp_workspace_locator_key` encrypts Tencent Provider Workspace IDs.

Generate both as independent standard-base64 encodings of exactly 32 random
bytes. Never reuse the ADP/control HMAC, session secret, usage-evidence key,
COS credential or provider credential.

```sh
umask 077
openssl rand -base64 32 > secrets/adp_file_locator_key
openssl rand -base64 32 > secrets/adp_workspace_locator_key
printf '{}\n' > secrets/adp_file_locator_previous_keys.json
printf '{}\n' > secrets/adp_workspace_locator_previous_keys.json
chmod 0600 secrets/adp_*locator*
```

The non-secret active IDs live in `.env` as
`WORKBENCH_FILE_LOCATOR_KEY_ID` and `WORKBENCH_WORKSPACE_LOCATOR_KEY_ID`.
Ciphertexts carry that ID in the protected JWE `kid` header.

For a rotation, put the previous ID and key into the matching server-only JSON
secret, replace the active key file, and increment the ID on both blue and
green instances before switching traffic. For example:

```json
{"v1":"<old standard-base64 32-byte key>"}
```

Keep the previous key until every row encrypted with it has been re-encrypted
or deleted and a restore drill has proved that old data remains readable. Key
files and previous-key maps must not enter `.env`, Git, logs, browser responses
or ordinary database backups.

`scripts/validate_secret_keys.py` is called by preflight and validates the full
rotation schema before containers start. Each map is limited to 32 entries and
64 KiB, rejects duplicate or malformed key IDs, requires canonical standard
base64 values that decode to exactly 32 bytes, and rejects the active key ID in
the historical map. It compares both raw file values and canonical decoded
bytes across active/historical locator keys, evidence keys, HMAC keys, the
session secret, and the admin token. Reusing a base64 key string in another
purpose is therefore rejected even if one consumer would use the text while
another consumer decodes it.

`WORKBENCH_WORKSPACE_HOST_SUFFIXES` is not a secret, but it must remain empty
until the exact HTTPS hostname suffix returned by a real
`CreateWorkspaceCredential` call is captured and its ownership is verified.
An empty, broad, malformed or non-matching suffix fails Provider Workspace
directory and download operations closed. When `WORKBENCH_FILES_ENABLED=true`,
preflight requires a non-empty, explicitly verified suffix; leaving it empty is
permitted only while the file capability remains disabled.
