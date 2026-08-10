# ADP Claw ownership-boundary gates

This document describes the executable controls that enforce design section 18.
The scripts measure the current worktree, including untracked files; staging a
file is not required and cannot hide it from the checks.

## new-api gate

Run from this repository:

```powershell
$env:PYTHONDONTWRITEBYTECODE = '1'
python .github/scripts/check_claw_constraints.py --repo .
python -m unittest discover -s .github/scripts/tests -p 'test_*.py' -v
```

The comparison point defaults to feature-start revision
`91f6b455dbd36858a7cff9d65b87e1d5a3fb4861`. `--baseline` exists for fixture
and replay audits; changing it for a release must first update the approved
baseline document.

The allowlist accepts only:

- the additive `claw-control/**` service and `deploy/claw-workbench/**` overlay;
- `docs/adp-claw-*` design/audit documents and the Claw-specific CI files;
- the named identity bridge/controller/router files and `service/workbenchbridge/**`;
- the named Playground entry, selection, legacy, admin-entry, generated route-tree
  and locale files.

It rejects new Claw business under ordinary `model`, `relay`, `setting`,
`middleware`, `constant`, `dto`, `types`, `service`, `controller`, `router` or
frontend areas. It also retains the original maximum 10 modified existing source
files, maximum 100 directly changed lines, minimum 90% additive source and
forbidden billing/quota/relay checks.

The broad deployment path allowlist is paired with a separate content gate:

```powershell
python deploy/claw-workbench/scripts/lint_overlay_security.py `
  --root deploy/claw-workbench
python -m unittest discover -s deploy/claw-workbench/scripts/tests `
  -p 'test_*.py' -v
```

That linter rejects shell sourcing of dotenv files, credential literals or
credential interpolation, published ports, privileged/root runtimes, Docker
socket access, host namespace sharing, and command substitution in declarative
configuration. The workflow runs both its denial fixtures and a scan of the
real overlay before the path/merge-budget gate. Validator and fixture changes
remain visible as dedicated workflow-owned files in the review diff instead of
being hidden inside the deployment preflight.

Imports of new-api core packages from Claw code are rejected. The narrowly scoped
exceptions are the existing auth middleware used by the route registration and
the read-only `model.User` lookup used to revalidate the login identity truth.
The latter is separately checked for write APIs and may not become a Claw data
store. `claw-control` may import only packages inside its own Go module and its
third-party dependencies; importing the parent new-api model/relay/etc. fails.

Cross-component retention follows the same boundary. `claw-control` owns the
policy, legal hold, dry-run, durable delivery intent, retry state, and signed
receipt. ADP owns exact-tenant Conversation/Workspace/File/OAuth deletion and
its idempotent receipt table. The wire DTO contains immutable IDs and policy
metadata only; AppKey, SecretId/SecretKey, OAuth tokens, COS locators, prompts,
and provider responses are forbidden in the control-plane intent and receipt.
Control data is not deleted until a valid signed ADP `completed` receipt is
durable. The production overlay must set the exact private ADP endpoint and a
positive coordinator interval; leaving the interval at zero intentionally keeps
queued intents pending and is not a completed retention deployment.

## ADP fork gate

Run from `D:\codex\adp-chat-client-claw` (or pass any checkout with `--repo`):

```powershell
$env:PYTHONDONTWRITEBYTECODE = '1'
python .github/scripts/check_workbench_boundaries.py --repo . `
  --report-json D:\codex-tmp\adp-workbench-boundary.json
python -m unittest discover -s .github/scripts/tests -p 'test_*.py' -v
```

Its default audited upstream revision is
`186084bfddc42cc369c722cced95842dd83c305f`. Every run prints all patched files
that existed at that revision and their additions/deletions, then lists additive
files. ADP CI uploads the same information as a JSON artifact.

The ADP gate permits workbench implementation changes only within `server/**`,
`client/**` and its own `.github/**`; the root `README.md` is the sole explicit
documentation exception. Added source is rejected when it introduces:

- a new-api DB, API-key, quota, billing, relay or public model-API dependency;
- server-only AppKey, SecretId/SecretKey or workspace/provider locator fields in
  frontend code;
- those values in browser-facing Python returns, response encoders, logs or
  exceptions;
- credential-like literals or private keys outside tests.

Python response/log checks use syntax-tree nodes intersecting added lines. This
avoids treating safe internal encryption/provider code or an unchanged upstream
line as a disclosure while still detecting multi-line response objects. Fixture
tests cover each allowed and denied form.

## ADR override

Both scripts fail closed. The checked-in GitHub workflows never set an override,
so ordinary pull-request and push CI cannot accept an ADR exception.

For an already approved, protected manual audit only, both variables are required:

```powershell
$env:CLAW_CONSTRAINT_ADR_OVERRIDE = 'true'
$env:CLAW_CONSTRAINT_ADR = 'docs/adp-claw-adr-example.md' # new-api repository
```

The ADP repository instead requires an explicitly named
`server/WORKBENCH_ADR_<SLUG>.md`. The file must contain one field per line:

```text
Status: approved
Approved-By: <project owner identity>
Approval-Date: YYYY-MM-DD
Constraint-IDs: <every finding code, comma separated>
Impact: <substantive scope and merge/security impact>
Rollback: <substantive disable/revert procedure>
```

The filename is constrained to its repository's ADR namespace, path traversal is
rejected, future approval dates and placeholder approvers are rejected, impact
and rollback fields must be substantive, and `Constraint-IDs` must cover every
current failure. Merely adding an ADR, supplying only one variable, or writing a
one-line approval never changes the result.

An accepted override changes only the command exit status. The report still lists
every original finding and prints a prominent warning, preserving the audit trail.
