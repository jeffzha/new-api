# Claw workbench internal metrics and alerts

## Network boundary

Both metrics listeners are disabled in their application defaults. The
workbench Compose overlay explicitly enables `claw-control` on container port
9090 and ADP on container port 9100. Neither service declares `ports` or
`expose`, the public Caddy configuration has no metrics route, and the endpoint
is therefore intended to be scraped only by a Prometheus instance attached to
`workbench_backend`. Firewall policy must continue to deny these ports from the
host and public networks.

The only metrics resource is `GET /internal/metrics`. It returns Prometheus
text exposition with `Cache-Control: no-store`. Do not attach public ingress,
browser authentication, or tenant identifiers to this listener.
The cost and margin series are finance-sensitive administrator data; restrict
Prometheus, rule evaluation, remote-write, and dashboard access accordingly.

## Configuration

| Service | Setting | Default | Production overlay |
|---|---|---:|---:|
| claw-control | `CLAW_METRICS_ADDR` | empty (disabled) | `:9090` |
| ADP | `WORKBENCH_METRICS_ENABLED` | `false` | `true` |
| ADP | `WORKBENCH_METRICS_HOST` | `127.0.0.1` | `0.0.0.0` inside the container only |
| ADP | `WORKBENCH_METRICS_PORT` | `9100` | `9100` |

`prometheus.yml` and `workbench-alerts.yml` are reference configuration. They
do not add a Prometheus service and do not change the running deployment.
Database-backed claw-control gauges are identical on Blue and Green because
both query the same control database; aggregate them with `max`, not `sum`.
ADP process gauges such as active Turns and SSE connections are per-instance
and should normally be aggregated with `sum`.

## Metric formulas

| Metric | Formula / event boundary |
|---|---|
| `workbench_active_sessions` | Count of selected control sessions with `revoked_at IS NULL AND expires_at > now()`; evaluated on scrape. |
| `workbench_customer_apps{status}` | Count grouped by a fixed App status allow-list. Unknown database values are folded into `other`. |
| `workbench_active_turns` | Scheduled ADP Turn tasks minus their completion callbacks, per process. |
| `workbench_turns_total{status}` | Increment only after a terminal Turn transaction commits. Recovery of an interrupted prior lifecycle increments `provider_unknown`. |
| `workbench_turn_events_persisted_total` | Increment after a successful commit by the exact number of new `WorkbenchTurnEvent` rows in that transaction. Initial metadata, provider, cancellation, terminal, and lifecycle-recovery events are included; rejected or failed transactions add zero. |
| `workbench_turn_duration_seconds` | `terminal persisted timestamp - Turn CreatedAt`, observed only after commit. |
| `workbench_first_event_seconds` | `first provider event AcceptedAt - Turn CreatedAt`, observed only after the first event transaction commits. |
| `workbench_turn_event_persist_db_seconds` | Monotonic elapsed seconds around each successful Turn-event database path, including connection/session acquisition, ownership/idempotency lookup or row lock, insert/update, and commit. It is observed once per successful transaction, including a batch recovery transaction, and never for rejected or failed commits. |
| `workbench_active_sse` | Replay connections entered minus replay `finally` exits, per process. |
| `workbench_sse_reconnect_total` | Increment when a replay starts with `after_sequence > 0`. |
| `workbench_control_event_lag_seconds{stage="publish_queue"}` | `max(0, now - oldest pending outbox CreatedAt)`; zero when the queue is empty. |
| `workbench_control_event_lag_seconds{stage="consume"}` | `max(0, consumer now - validated event CreatedAt)` immediately before dispatch. |
| `workbench_control_event_failures_total{stage}` | Increment on publish/persistence, Redis consumption, or listener dispatch failure. |
| `workbench_authz_denied_total{reason}` | Failed continuous authz calls, classified only as `denied` (expected 4xx) or `error` (availability/integrity failure). |
| `workbench_file_failures_total{stage}` | Scanner exceptions or private COS upload exceptions. Client cancellation alone is not a failure if upload and compensation succeed. |
| `workbench_usage_evidence_capture_failures_total{reason}` | Exceptions while validating, encrypting, or committing `response.completed` evidence; reason is a fixed class, never raw exception text. |
| `workbench_gate_failures_total{gate,reason}` | App, plan, or trusted local-policy access gate returning a denial/error. |
| `workbench_limit_denied_total{limit}` | Local policy denial folded into a fixed capability/access/configuration/search/unbounded/file/other class. |
| `workbench_usage_audit_stale_days` | Maximum, across active Apps, of `max(0, (now - latest locked audit period_end) / 86400)`. For an App with no audit yet, its creation time is the baseline. The series is absent only when there are no active Apps. |
| `workbench_usage_audit_missing_apps` | Count of active Apps with no locked manual usage audit. |
| `workbench_plan_expiring_total{days}` | Count of paid active periods with `now < end_at <= now + days`, for fixed horizons 7 and 30. |
| `workbench_app_verify_total{result}` | Increment after an App verification HTTP operation completes; `result` is fixed to `success` for 2xx/3xx or `failure` for 4xx/5xx. |
| `workbench_identity_bind_total{result}` | Increment after ADP confirms a shadow-account binding; the same bounded result classification is used. |
| `workbench_agent_provision_seconds` | Monotonic elapsed seconds from the committed `provisioning` row through `CopyAgentFromApp` and ownership reporting, including failed/unknown attempts that reached the provider boundary. Existing active bindings are not provisioning attempts. |
| `workbench_action_denied_total{action}` | Locally denied generic ADP Actions with a 4xx policy result. Documented Action names form a fixed allow-list; every unknown/unparseable value is folded into `other`. Provider 5xx errors are not counted as denials. |
| `workbench_upstream_cost_cny{confidence}` | Full-history manually registered cost from the same read-only margin-report source: locked exact/estimated/account-only rows plus draft `unverified` rows, split across four fixed confidence values. |
| `workbench_estimated_margin_cny{confidence}` | `all paid fixed-plan invoice revenue - all locked reviewed upstream cost`. The single emitted confidence is the platform report confidence (`account_only`, else `estimated_allocation`, else `app_exact`, else `unverified`). Draft/unverified cost never changes invoices and is not silently treated as reviewed cost. |

General duration histograms use cumulative buckets `0.1, 0.25, 0.5, 1, 2.5, 5,
10, 30, 60, 300, 900, +Inf`. The Turn-event database histogram instead uses
millisecond-sensitive buckets `0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1,
0.25, 0.5, 1, 2.5, 5, 10, +Inf` seconds. Percentiles are calculated by
Prometheus, for example:

```promql
histogram_quantile(0.95,
  sum by (le) (rate(workbench_first_event_seconds_bucket[5m])))
```

Terminal error ratio over ten minutes:

```promql
sum(rate(workbench_turns_total{status!="completed"}[10m]))
/
clamp_min(sum(rate(workbench_turns_total[10m])), 1e-9)
```

No metric label accepts a customer, user, App, Agent, Conversation, Turn, request,
resource, or event identifier. Request-level diagnosis remains in protected logs
and audit records.
