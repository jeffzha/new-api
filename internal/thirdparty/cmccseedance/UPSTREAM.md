# CMCC MaaS Seedance Go SDK

## Provenance

- Official download URL: <https://ecloud.10086.cn/api/query/maas/public/backend/model/link/aicc-sdk/golang/download>
- Downloaded on: 2026-07-24
- Upstream SDK version: `1.0.0`
- Upstream bundle name: `golangSDK-0515.zip`
- Upstream SDK archive name: `maas_seedance_sdk_1.0.0_go.tar.gz`
- Downloaded ZIP SHA256: `01b98419fe3b55489225dbb40c5ae22d110d260532520a5628b901bcc47d3837`
- Nested SDK tarball SHA256: `d3128e74467c8a3870b5d305b6d8a8e2f13ebd9e574bb6902a2e2eb75cb75c0a`

The stable official URL redirects to a temporary object-storage URL. The
temporary signed URL is intentionally not recorded here.

## Local changes

The SDK source is kept inside the main module instead of as a nested Go
module. The following changes were made:

1. Removed the upstream nested `go.mod` and `go.sum`; dependencies are managed
   by the repository root module.
2. Rewrote imports beginning with `maas_seedance_sdk_1.0.0_go/` to
   `github.com/QuantumNous/new-api/internal/thirdparty/cmccseedance/`.
3. Excluded macOS AppleDouble metadata files (`._*`) from the upstream
   archives.
4. Did not include the bundle-level example `readme.md`; this directory keeps
   the compilable SDK source and this provenance record.
5. Kept only the Seedance client, secure transport, and their supporting
   packages. The unrelated Ark, ASR, CU, DeepSeek, and generic inference
   clients were excluded.
6. Added finite HTTP timeouts: 15 seconds for model mapping and 120 seconds for
   secure generation/query requests. This prevents a stalled supplier
   connection from blocking a gateway worker indefinitely.

## Licensing

The upstream archive does not contain a LICENSE file. This provenance record
does not grant permission to redistribute or use the SDK. Confirm the
applicable license or obtain written authorization from the supplier before
release or distribution.
