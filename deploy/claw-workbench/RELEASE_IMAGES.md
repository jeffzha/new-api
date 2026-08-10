# Claw Workbench release images

Production hosts do not build application images. The manual GitHub Actions
workflow `.github/workflows/claw-workbench-release-images.yml` builds the
new-api, claw-control, and ADP Workbench candidates, publishes them to GHCR,
scans the immutable digests, and emits a small release candidate manifest.

## One-time repository configuration

1. Push the hardened ADP branch to the pinned trusted fork
   `jeffzha/adp-chat-client`. Do not build production images from an unreviewed
   pull-request ref.
2. If the fork is private and the workflow token cannot read it, add the
   least-privilege `ADP_WORKBENCH_CHECKOUT_TOKEN` Actions secret. It needs read
   access to that repository only.
3. Configure the `claw-workbench-production` GitHub environment with required
   reviewers. The workflow never deploys; approval authorizes only a candidate
   image publication.
4. Grant the repository workflow permission to write packages. GHCR pull access
   for the production host must use a separate read-only token.

## Publishing a candidate

Run **Claw workbench release images** manually from the exact new-api commit and
enter the exact lowercase 40-character ADP fork commit. The workflow fails
closed when the configured fork or commit is malformed, either checkout is not
exact and clean, a build fails, or Trivy finds a fixed HIGH/CRITICAL
vulnerability or an embedded secret.

All three images are built for `linux/amd64`, carry an
`org.opencontainers.image.revision` label for their own source repository, and
publish BuildKit provenance plus an SBOM. The release candidate artifact
contains only revisions and immutable `repository@sha256:...` references; it
contains no registry credential or provider secret.

The ADP runtime is installed from its frozen `uv.lock` into a dedicated virtual
environment. Build-time Python package installers are removed from the final
runtime image, so their dependency trees cannot become an unused but vulnerable
production surface. Any fixed HIGH or CRITICAL dependency reported by the image
scanner must be upgraded in the ADP fork and re-locked before publication.
Runtime packages that legitimately require `setuptools` use an explicit locked
version so its vendored libraries are covered by the same scanner gate.

Use the three digest references only for the inactive Blue/Green color. Keep the
active color on its previous digests until the inactive color has passed
preflight and live acceptance. The deployment host must pull with its read-only
registry credential; it must not rebuild, retag, or replace either digest.

The candidate artifact also records the exact ClamAV digest that passed the
same release scan. This keeps the evidence-scanning runtime dependency in the
immutable candidate provenance instead of relying on a separately copied
deployment value.

The candidate artifact is not the authoritative production release manifest.
After deployment and cutover, run `scripts/collect-release-manifest.sh` on the
production host. That collector reads the actual containers, image labels,
database migration state, active color, Caddy version, configuration digest,
and provider region.
