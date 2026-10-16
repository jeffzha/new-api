# Operator Recovery Environment Follow-Up

The recovery test originally accepted any error from the injected-output call.
That could hide a failure before the database commit. The test now requires
`errors.Is(err, errOutputUnavailable)` before checking the snapshot count, so
the original error chain is printed if journal persistence or freeze fails.
No product code, database schema, deployment flags, or HTTP routes changed.

## Reproduction

Run from the Gateway repository root. The same source and cached dependencies
were tested with all inherited OPS_CN flags, Redis and batch settings removed:

```powershell
$env:GOMODCACHE='C:/Users/winton/.codex/worktrees/9564/TokenOps/.tools/gomod'
$env:GOCACHE='C:/Users/winton/.codex/worktrees/9564/TokenOps/.tools/gocache'
$env:GOTOOLCHAIN='local'
$env:GOPROXY='off'
$env:GOSUMDB='off'
$env:AGENCY_HUB_RUN_EXTERNAL_DB_TESTS='0'
Get-ChildItem Env:OPS_CN* | Remove-Item
Remove-Item Env:REDIS_CONN_STRING,Env:BATCH_UPDATE_ENABLED -ErrorAction SilentlyContinue
& ../../.tools/go/bin/go.exe test -buildvcs=false -p 1 ./cmd/cn-integrity-operator -count=1 -v
```

The local toolchain is Go 1.27.2 on Windows/amd64. Offline module settings use
already cached dependencies; they are not application feature flags.

- Restricted Windows sandbox: FAIL before freeze, with `os.Rename` returning
  Access denied while replacing the existing journal inside the sandbox temp
  directory. The precise original error is retained in
  `s4-integrity-operator-clean-env-reproduction.log`. No snapshot committed.
- The same command outside that sandbox: SQLite recovery PASS 1.62s, package
  PASS 1.861s; explicit-database startup test PASS. MySQL/PostgreSQL explicitly
  SKIP because the external-database flag is zero. Full log:
  `s4-integrity-operator-clean-env-elevated.log`.
- The coordinator independently reported the same environment distinction on
  integrated Gateway `261d9bd` with Go 1.25.1: restricted replacement failed;
  `require_escalated` SQLite recovery PASS 1.71s and package PASS 2.016s. This is
  coordinator-reported evidence, not a second retained local execution log.

The passing test creates an empty SQLite file, verifies operator open did not
create the integrity schema, then explicitly runs the finance migration twice.
It does not need inherited capture flags or preexisting business tables for
this empty-cohort recovery case. Ordinary production operator use still needs
an already migrated Gateway database. The local command never migrates on open.

## Filesystem Guarantees

The journal writes and syncs a same-directory temporary file, closes it, then
uses `os.Rename` to replace the previous state. The pending request must persist
successfully before freeze starts. Replacement failure returns the original
error; it does not fall back to truncate-in-place or delete-then-rename.

On Linux, same-directory rename provides atomic replacement during normal
operation. The code syncs the file but not its parent directory after rename.
No Linux runtime or filesystem crash/power-loss test was performed here; neither
directory-entry durability after power loss nor recovery across an OS crash is
qualified. Linux source review is not Linux execution evidence.

Go's `os.Rename` documentation explicitly does not guarantee atomic behavior on
non-Unix platforms, even within one directory. Go 1.25.1 Windows implements this
through `MoveFileEx` with `MOVEFILE_REPLACE_EXISTING`. The ordinary writable
Windows-directory test proves the exercised output/process restart recovery;
it does not establish Windows atomic replacement or power-loss durability.
Run one producer per private journal on a directory where the operator can
create and replace files. Permissions or platform restrictions are reported as
failures rather than silently weakening persistence semantics.
