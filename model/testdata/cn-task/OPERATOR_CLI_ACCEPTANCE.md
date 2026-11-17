# Full Operator CLI Acceptance

The executable, rather than a Go control helper, performs prepare, status,
import, admit, start, freeze-loop and stop. A separate opt-in model test binary
only creates isolated synthetic fixtures, calls native business functions,
reads persisted state and exports frozen evidence. It never calls the local
prepare/import/admit/start/stop/freeze APIs. No product code is changed.

Each engine starts with a new Gateway database. The actual CLI prepare result
supplies the random integrity epoch; it is never rewritten. The Python runner
creates a separate synthetic Ops SQLite database, calls the actual product
freeze service, proves self-approval is rejected, independently approves as a
different synthetic actor, and exports the exact eligibility bytes. Reopening
the review database read-only must reproduce those bytes. The real executable
then imports that inventory, admits customer 1001 and starts generation 2 with
explicit feature flags and a writer declaration/drain attestation.

The fixture invokes actual RechargeCreem, ReserveCNWallet and FinalizeCNWallet
with a synthetic payment snapshot. A five-quota topup followed by a one-quota
charge leaves balance four. No provider or external payment is contacted. The
native capture produces three originals and the five expected financial fact
kinds. The actual periodic executable seals at least two complete snapshots;
CLI stop moves the control to paused/generation 3. The loop exits with the
expected generation conflict, including when restarted with its stale journal.

Every retained snapshot is independently checked by the current Ops raw JCS,
page/order/hash/identity, cumulative parent inclusion, graph/financial, value
and control validators. This is synthetic integration verification, not an Ops
authority grant or financial posting authorization. The runner verifies that
its approval-only Ops database has zero authorities, owners, acceptances and
postings. productionEligibility=false and financialGrants=0 throughout.

## Results

| Engine | Actual Version | End-To-End Seconds | Complete Batches | Negative Checks |
| --- | --- | ---: | ---: | ---: |
| SQLite | 3.50.4 | 6.303 | 2 | 15 |
| MySQL | 8.0.39 | 7.760 | 2 | 15 |
| PostgreSQL | 17.6, Windows x64 | 13.569 | 2 | 15 |

Each rejection compares both row counts and full-content stable hashes of 12
tables: epochs, streams, approvals, admissions, writers, fences, snapshots,
controls, originals, snapshot collections, the freeze head and capture gate.
The comparisons are retained, not just asserted, in each `commands.json`.
Coverage includes malformed import/start input, stale import/admit/start/stop
generations, an inventory-excluded customer, missing explicit flags, invalid
writer protocol, incomplete drain attestation, enabled Redis/batch, stale
import after activation, and stale loop restart. The outside-inventory error
is the product's existing sanitized `operation_failed`; the product was not
modified to change that error classification.

Evidence is in `integrity-v2-operator-cli/{sqlite,mysql,postgres}`: command
arguments/exit status/stdout/stderr, before/after content fingerprints, the
actual frozen Ops draft, approved review, export, audit records and provenance,
writer-start input, final results, and every raw page of both batches. The
previous restricted Windows failure log remains unchanged. An initial runner
expectation classified the outside-inventory error incorrectly; its command
transcript remains in the private attempt-a evidence directory, and the final
runner asserts the existing product code precisely.

`source-provenance.json` records Gateway baseline `6f2c68d`, actual Ops HEAD
`d44cd1b`, hashes of the five product inventory/verification modules, both
executables, and the exact qualification sources. Ops advanced from the
initially authorized `242d78c` during coordination; the concrete module hashes
and actual HEAD, rather than an assumed checkout name, identify what ran.
The Go build used 1.27.2, Windows/amd64. Go 1.25.1 and the coordinator's later
integrated Gateway HEAD were not rerun for this full workflow here.

## Reproduce

Run from the Gateway root. Choose unused work/output paths on every invocation;
the runner refuses to overwrite evidence. Use a private work directory with
operator-only access because its generated database-env.json can contain the
explicit database DSN. The committed evidence never includes those DSNs.

```powershell
$go='../../.tools/go/bin/go.exe'
$python='F:/ProgramData/anaconda3/python.exe'
$env:GOMODCACHE='C:/Users/winton/.codex/worktrees/9564/TokenOps/.tools/gomod'
$env:GOCACHE='C:/Users/winton/.codex/worktrees/9564/TokenOps/.tools/gocache'
$env:GOTOOLCHAIN='local'
$env:GOPROXY='off'
$env:GOSUMDB='off'
$env:PYTHONPATH='C:/Users/winton/.codex/worktrees/9564/TokenOps/.tools/python-packages'
New-Item -ItemType Directory -Path tmp/operator-cli-build -Force | Out-Null
& $go build -buildvcs=false -o tmp/operator-cli-build/operator.exe ./cmd/cn-integrity-operator
& $go test -buildvcs=false -c -o tmp/operator-cli-build/model-fixture.exe ./model
& $python model/testdata/cn-task/verify_integrity_operator_cli.py `
  --ops-root D:/JiuLu/TokenOps `
  --operator tmp/operator-cli-build/operator.exe `
  --fixture tmp/operator-cli-build/model-fixture.exe `
  --work tmp/operator-cli-fresh-work `
  --output tmp/operator-cli-fresh-evidence --engines sqlite
```

For all three engines, supply AGENCY_HUB_TEST_MYSQL_DSN and
AGENCY_HUB_TEST_OPERATOR_POSTGRES_DSN securely, then select
`--engines sqlite mysql postgres`. The runner provisions a new randomly named
database per external engine, preserving the existing database. The fixture
only permits MySQL 127.0.0.1:33174 and PostgreSQL 127.0.0.1:55474. It never
connects to shared ports 53394/55494 and never drops databases or stops servers.
The retained run used isolated databases s4_operator_097417a0f5d6 (MySQL) and
s4_operator_5a9a7a0872d8 (PostgreSQL); their names also appear in result.json.
Retained SQLite and Ops review stores remain under the private
`.testtmp/operator-cli-run-20261016c` directory, whose database hashes are in
the public provenance. Review stores can be inspected read-only.

All actual executions ran with the Windows temporary-directory sandbox lifted,
because the earlier restricted environment rejects journal replacement. This
does not relax the journal implementation. Linux execution, Windows atomic
replacement, crash/power-loss durability, public HTTP authentication, live
payments, production eligibility, and real multi-host writer drain are not
qualified. See `s4-integrity-operator-environment.md` for filesystem boundaries.
The original 21 batches and thousand-pair samples are not modified.
