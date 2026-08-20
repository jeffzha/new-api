# ADP Claw implementation baseline

Recorded at: 2026-08-09; release gate rebased after upstream rc.25 integration
on 2026-08-21 (Asia/Shanghai)

This file fixes the comparison points used by the upstream-compatibility gates in the implementation design. It contains no credentials or deployment secrets.

## Accepted architecture constraint

The project owner accepted the **isolated extension domain + thin new-api bridge**
architecture on 2026-08-09. This is a release constraint, not an implementation
preference:

- new-api is limited to the authenticated entry, one-time ticket and read-only
  identity-status bridge;
- customer, membership, App secret, fixed-plan, invoice, governance and upstream
  cost-import ownership remains in the independent `claw-control` service;
- shadow accounts, Agents, Conversations, Turns, Workspaces, files and provider
  execution remain in the hardened ADP fork;
- no cross-database ORM, shared database account, shared browser session, shared
  service secret, or Claw-specific hook in existing new-api user/relay/billing
  lifecycles is allowed;
- an implementation that cannot satisfy these boundaries stays disabled until an
  ADR describes the alternatives, upstream merge impact and rollback and receives
  explicit project-owner approval.

## Revisions

| Component | Baseline revision | Purpose |
|---|---|---|
| new-api feature start | `91f6b455dbd36858a7cff9d65b87e1d5a3fb4861` | Measure only Claw-related changes made after implementation started |
| post-rc.25 integration gate | `2676c981ce0b485d4fccc96a8e30e83dd353514f` | Current release gate after accepting the one-time upstream frontend, RelayKit, auth, channel-ID and deployment integration |
| new-api upstream/main at rc.25 integration | `f116414284162ad15d8925f7bca494c109b83e93` | Exact official upstream revision merged into the integration baseline |
| new-api merge base | `7c28993f6bd9e92616f3f578212577f8b7c40b45` | Distinguish pre-existing fork changes from this feature |
| TencentCloudADP/adp-chat-client | `186084bfddc42cc369c722cced95842dd83c305f` | Audited ADP fork base |
| claw-control | new component | All files are additive; no upstream code is copied into the service |

The historical implementation audit remains reproducible against the
**new-api feature start** revision. After the accepted rc.25 integration moved
the official frontend root and RelayKit contracts, the automated release gate
measures new changes against the **post-rc.25 integration gate** revision. This
prevents official upstream restructuring from being misclassified as new Claw
surface while retaining the older revision for replay audits.

## Required audit commands

Run from the new-api worktree before every Claw release:

```powershell
$baseline = '2676c981ce0b485d4fccc96a8e30e83dd353514f'
git status --short
git diff --name-status $baseline -- .
git diff --numstat $baseline -- .
git diff --check $baseline -- .
git diff $baseline -- relay constant service controller model router web/src/routes
```

Run from the ADP fork worktree:

```powershell
$baseline = '186084bfddc42cc369c722cced95842dd83c305f'
git status --short
git diff --name-status $baseline -- .
git diff --numstat $baseline -- .
git diff --check $baseline -- .
```

Every release report must list:

1. Modified files that already existed at the applicable baseline.
2. Added files and their ownership component.
3. Direct added/deleted line counts in existing files.
4. Any prohibited new-api core path with a non-empty diff.
5. Feature-off regression results and `/playground/legacy` availability.
6. Upstream merge conflicts and their resolution.

The same mechanical limits are enforced by
`.github/scripts/check_claw_constraints.py` in the `Claw control` workflow. The
check is intentionally stricter than a report: it blocks prohibited `relay/**`
changes, upstream-existing billing/quota/settlement paths, more than 10 modified
upstream source files, more than 100 directly changed lines in those files, or an
additive source ratio below 90%. Any exception still requires an ADR and explicit
project-owner approval before the code is written.

The gate also enforces a closed Claw path allowlist and scans the isolated bridge
and service for dependencies on new-api's DB models, relay, quota, API-key and
billing internals. The only DB-model exception is the documented read-only
`model.User` lookup in `controller/workbench_identity_bridge.go`; mutation APIs
remain prohibited. Its fixture suite and current-tree check run in the same CI
job, so an implementation cannot silently replace the original 10/100/90 checks.

The ADP fork has its own `.github/scripts/check_workbench_boundaries.py` gate.
It reports every baseline-existing patch with additions/deletions, emits a JSON
manifest artifact, and rejects new-api core dependencies and high-signal
Secret/provider-locator disclosure patterns. See
`docs/adp-claw-boundary-gates.md` for commands, allowlists and the fail-closed ADR
override contract.
