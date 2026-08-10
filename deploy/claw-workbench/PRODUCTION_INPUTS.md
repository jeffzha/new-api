# Production external inputs

This checklist is the exact external material required by the current
implementation. Provider credentials must never be pasted into chat, committed
to Git, written to `.env`, or placed in an image layer. Each secret file contains
only the raw value: no `NAME=`, quotes, JSON wrapper, BOM, or explanatory text.

The deployment process generates database passwords, Redis credentials, HMAC
keys, session keys, locator/encryption keys, and acceptance signing keys on the
host. Those values are not operator inputs.

Run the idempotent bootstrap once as root on the Linux deployment host:

```sh
python3 scripts/bootstrap_generated_secrets.py
```

It atomically creates only deployment-owned values with UID `10001` and mode
`0600`. Existing secure files are preserved byte-for-byte; an empty, symlinked,
wrong-owner, or overly permissive file fails closed instead of being replaced.
It deliberately does not create ADP, AGSX, COS, PKI, OAuth, or billing values.

## 1. Tencent ADP application

For every customer App, provide these non-secret identifiers:

| Field | Required value |
|---|---|
| `provider_environment` | `china_tencent_cloud` for `adp.tencentcloudapi.com`; use `china_tencent_adp` only for an ADP independent-site account |
| `region` | Tencent region that owns the App, normally `ap-guangzhou` |
| `app_id` | Published Claw-mode ADP App ID |
| `space_id` | Space containing the App |
| `template_agent_id` | Published template Agent copied for each new-api user |
| App status | Claw mode (`AppMode=4`) and published/available |

Do not guess the Region or template Agent. Use Tencent Cloud API Explorer with
the same dedicated management identity that will be stored in the credential
profile:

1. Call `DescribeApp` at `POST https://adp.tencentcloudapi.com/`, API version
   `2026-05-20`, first with the Region selected in the ADP console. Pass the
   exact `AppId`, `Domain=2`, and an empty `FieldMask.Paths` (or request only the
   non-secret fields). Record a Region only when the call succeeds and the
   response proves the same `AppId`, `SpaceId`, `AppMode=4`, and running release
   status.
2. Call `DescribeAgentSummaryList` with `Scope=0`, the same `AppId`,
   `PageNumber=0`, and a bounded `PageSize`. Select only a configuration Agent
   whose `Profile.Role=0`. If more than one main Agent is returned, the product
   owner must choose explicitly; deployment automation must not pick the first
   result.
3. Confirm the chosen ID with `DescribeAgentDetail(AppId, AgentId)` and verify
   that it belongs to the same App before saving it as `template_agent_id`.
4. Confirm that **Allow dynamic Agent configuration during conversations** is
   enabled and that the application was published again after the change.

The AppKey is a conversation credential, not a Tencent management SecretKey.
An AppKey pasted into chat, a ticket, shell history, or source control is treated
as compromised and must be rotated before production. If the console does not
offer a rotation action, obtain the supported rotation procedure from Tencent
Cloud support rather than reusing the exposed value.

Create three single-line files below `secrets/provider/`. Replace
`CUSTOMER_CODE` with an uppercase stable customer code; the file names become
the exact `env://` references stored by claw-control.

```text
secrets/provider/WORKBENCH_PROVIDER_CUSTOMER_CODE_ADP_APP_KEY
secrets/provider/WORKBENCH_PROVIDER_CUSTOMER_CODE_TENCENT_SECRET_ID
secrets/provider/WORKBENCH_PROVIDER_CUSTOMER_CODE_TENCENT_SECRET_KEY
```

Their contents are, respectively, the App's conversation `AppKey`, the Tencent
Cloud/ADP management `SecretId`, and its matching `SecretKey`. The management
credential and AppKey are different credential classes and must not be reused.
The management identity needs only the App/Agent/catalog operations enabled by
the product: `DescribeApp`, `DescribeAgentDetail`, `CopyAgentFromApp`,
`ModifyAgent`, `DescribeModelList`, `DescribeSkillSummaryList`,
`DescribeSkillDetail`, `DescribePluginSummaryList`, and `DescribePlugin`.
Workspace access additionally needs `CreateWorkspaceCredential` when that
capability is enabled.
Conversation execution uses the AppKey path. Any additional permission required
by a real provider response is added only after its exact Action and RequestId
are recorded during acceptance; do not grant account-wide administrator access.

On the deployment host the files must be non-symlink regular files, owned by UID
`10001`, mode `0400` or `0600`, and at most 4096 bytes. Generate the canonical
fingerprints locally without printing the secrets:

```sh
python3 scripts/provider_fingerprint.py credential \
  WORKBENCH_PROVIDER_CUSTOMER_CODE_TENCENT_SECRET_ID \
  WORKBENCH_PROVIDER_CUSTOMER_CODE_TENCENT_SECRET_KEY
python3 scripts/provider_fingerprint.py app-key \
  WORKBENCH_PROVIDER_CUSTOMER_CODE_ADP_APP_KEY
```

## 2. Tencent AGSX managed sandbox

Provide these non-secret values:

| Field | Required value |
|---|---|
| `WORKBENCH_AGSX_REGION` | Region containing the Tool, initially `ap-guangzhou` |
| `WORKBENCH_AGSX_TOOL_ID` | Exact Sandbox Tool ID; at least ToolId or ToolName is mandatory |
| `WORKBENCH_AGSX_TOOL_NAME` | Exact Tool name |
| data-plane domain | Must equal `<region>.tencentags.com` |
| network/auth mode | Must be `SANDBOX` / `TOKEN` |

Provide three raw single-line secret files:

```text
secrets/sandbox/agsx-api-key       # starts with ark_
secrets/sandbox/cam-secret-id      # Tencent CAM SecretId
secrets/sandbox/cam-secret-key     # matching CAM SecretKey
```

The deployment generates the fourth required file,
`secrets/sandbox/client-token-hmac-key`. The CAM identity must be scoped to the
selected Tool and permit `StartSandboxInstance`,
`DescribeSandboxInstanceList`, `PauseSandboxInstance`,
`ResumeSandboxInstance`, and `StopSandboxInstance`. The `ark_` key is used only
for the E2B-compatible data plane and must not be the AppKey or CAM SecretKey.

All four files must be owned by UID `10001`, mode `0400` or `0600`, with no
control characters. Code execution and PTY remain disabled until the real
lifecycle, cancellation, output-limit, PTY, revocation, and cleanup acceptance
suite passes.

## 3. Private COS for customer files

Provide:

| Field | Required value |
|---|---|
| `WORKBENCH_FILE_COS_REGION` | Bucket region, initially `ap-guangzhou` |
| `WORKBENCH_FILE_COS_BUCKET` | Full private bucket name including AppId suffix |
| bucket access | Private; no anonymous read/list/write |

Provide two raw single-line files:

```text
secrets/files/cos_secret_id
secrets/files/cos_secret_key
```

Use a dedicated CAM sub-user or role, separate from ADP and AGSX. Restrict it to
the one bucket and the `workbench/*` object prefix, with only object upload,
download/signing and deletion permissions (`PutObject`, `GetObject`, and
`DeleteObject`). Bucket creation, bucket policy changes, public ACL changes, and
access to other buckets are not required. Files are uploaded with private ACL,
and retention/failed-upload compensation requires deletion permission.

The two files must be owned by UID `10001`, mode `0400` or `0600`. File support
is enabled only after ClamAV, EICAR rejection, private-COS upload/download,
presigned URL expiry, compensation deletion, and retention deletion pass.

## 4. Existing internal PKI

Export the following PEM files from the existing PKI:

```text
secrets/internal_ca.crt
secrets/workbench_control.crt
secrets/workbench_control.key
secrets/new_api_identity.crt
secrets/new_api_identity.key
```

Certificate requirements:

- `internal_ca.crt` contains the CA chain that validates both server
  certificates.
- `workbench_control.crt` has DNS SAN `workbench-control.internal` and server
  authentication usage.
- `new_api_identity.crt` has DNS SAN `new-api-identity.internal` and server
  authentication usage.
- Each private key is unencrypted PEM and exactly matches its certificate.
- Both certificates remain valid for more than 30 days at deployment time.
- The two leaf certificates/keys are distinct and are not the public
  `gateway.nexus-reach.com` certificate.

The `.crt` files may be mode `0644` but must not be group/world writable. Private
keys must be owned by root or UID `10001` and mode `0600` or stricter.

## 5. Optional external integrations

These are not required to start the base Workbench and remain disabled when
omitted:

- OAuth connector provider client IDs, allowed endpoints/scopes, and client
  secrets under `secrets/oauth/`.
- Tencent Fee Center read-only import `SecretId`, `SecretKey`, `PayerUin`, and
  confirmed product/business codes under `secrets/billing/`.
- A read-only GHCR pull token on the production host after the candidate images
  have been published.

## Delivery format

The preferred handoff is one encrypted archive or a protected server directory
containing only the paths above. Tell the deployer the directory path and the
non-secret identifiers; do not send the file contents through chat. The
preflight validates file type, ownership, mode, size, key independence,
certificate chain/SAN/expiry, provider fingerprint, sandbox contract, and
feature-specific required values before any container receives traffic.

The operator may copy `production-input-status.json.example` to the ignored
`state/production-input-status.json` file and fill only non-secret discovery
results. Secret values never belong in that status file.
