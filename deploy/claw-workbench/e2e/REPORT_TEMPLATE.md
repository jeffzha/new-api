# Claw Workbench acceptance report

- Run ID: `<opaque local run id>`
- Started/finished (UTC): `<ISO-8601>`
- Target: `<scheme://host>`
- Mode: `dry-run | live`
- Provider-cost permission: `disabled | enabled`
- Mutation permission: `disabled | enabled`

## Immutable release binding

| Item | Exact value |
|---|---|
| new-api / claw-control / ADP revision | `<40-hex> / <40-hex> / <40-hex>` |
| new-api / claw-control / ADP image digest | `sha256:<64-hex> / sha256:<64-hex> / sha256:<64-hex>` |
| Deployment config SHA-256 | `<64-hex>` |
| Control / ADP migration head | `<version> / <version>` |
| Caddy / active color / provider region | `<version> / blue|green / <region>` |
| Collected at | `<timezone-aware ISO-8601; no more than 30 minutes before live E2E>` |

Any missing or placeholder binding is a blocker; results from another image,
configuration, migration state, color, or region cannot be attached later.

## Executive summary

| Passed | Failed | Blocked | Skipped |
|---:|---:|---:|---:|
| 0 | 0 | 0 | 0 |

## Section 19 ordered results

| # | Phase | Check | Result | HTTP | Duration | Sanitized evidence |
|---:|---|---|---|---:|---:|---|
| 1 | routing | Legacy Playground | `<status>` | `<code>` | `<ms>` | `<no secrets>` |

## P0/P1/P2 coverage

| Scope | Required workflows | Result |
|---|---|---|
| P0 | customer/App/plan, identity, Turn, isolation, policy/limits, gates, usage, security/regression | `<pass/blocker>` |
| P1 | files/EICAR, OAuth/Skill, billing import, approval/audit/retention, load/Blue-Green/DR, UI acceptance | `<pass/blocker>` |
| P2 | multi-context selector, scheduled tasks, managed sandbox, BYOK | `<pass/blocker>` |

## Performance attachment

| Concurrency | Requests | First event P50/P95/P99 | Completion rate | Wall time |
|---:|---:|---|---:|---:|
| 50 | 50 | `<ms>` | `<%>` | `<s>` |
| 100 | 100 | `<ms>` | `<%>` | `<s>` |

External evidence to attach for the exact run interval:

- edge/backend active connections;
- ADP/new-api/claw-control/container memory;
- persisted Turn-event count and database write latency;
- upstream throttling and error counts.

## Blockers and follow-up

- `<missing credential, fixture, external collector, or failed restore>`

Reports must never contain Cookie values, tickets, API keys, AppKey, AK/SK,
Authorization headers, request bodies, raw SSE events, or raw provider responses.
