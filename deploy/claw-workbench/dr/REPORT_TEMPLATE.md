# Claw Workbench cross-region DR report

- Run/action/mode: `<run-id>` / `<action>` / `dry-run|live`
- Started/finished UTC: `<ISO-8601>` / `<ISO-8601>`
- Source scope: control PostgreSQL, ADP PostgreSQL and Redis forensic RDB
- Both encrypted repositories verified: `true|false`
- Actual isolated restore drill: `true|false`

## Objective evidence

| Metric | Objective | Observed | Result | Scope |
|---|---:|---:|---|---|
| RPO | `<seconds>` | `<seconds or null>` | `snapshot_freshness_*|met|not_met|not_verified|not_exercised` | source backup age or actual drill restore-point age |
| RTO | `<seconds>` | `<seconds or null>` | `partial_artifact_drill_only|not_verified|not_exercised` | artifact retrieval and validation; no service cutover |

Never label full-service RTO as met from an artifact-only drill. Attach separate
evidence for isolated database startup, application health checks, DNS/traffic
cutover, customer validation, and service restoration before evaluating the
full-service RTO objective.
A single successful backup proves snapshot freshness at that instant, not the
maximum interval between successful backups. Continuous RPO compliance needs
timer history and alerts for missed/failed runs.

## Target results

| Phase | Target | Status | Seconds | Sanitized evidence |
|---|---|---|---:|---|
| source backup | local | `<status>` | `<seconds>` | manifest SHA-256 only |
| encrypted upload/check | primary | `<status>` | `<seconds>` | no endpoint credentials |
| encrypted upload/check | secondary | `<status>` | `<seconds>` | no endpoint credentials |

## Follow-up

- `<failed target, incomplete verification, retention issue, or missing full-service exercise>`
