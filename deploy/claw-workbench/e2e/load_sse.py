#!/usr/bin/env python3
"""50/100-concurrency SSE acceptance load scenario (dry-run by default)."""

from __future__ import annotations

import argparse
import asyncio
import concurrent.futures
import json
import math
import os
import sys
import time
import uuid
from collections import Counter
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

from e2e_lib import (
    ConfigError,
    HTTPClient,
    Redactor,
    StoredCookie,
    json_bytes,
    load_config,
    load_evidence_file,
    load_json_fixture,
    percentile,
    resolve_secret,
    sanitized_endpoint,
    utc_now,
    verify_evidence_signature,
    write_atomic,
)
from run_e2e import (
    BILLING_IMPORT_REQUIREMENTS,
    BYOK_REQUIREMENTS,
    DEFAULT_PATHS,
    OAUTH_REQUIREMENTS,
    SANDBOX_REQUIREMENTS,
    SCHEDULED_REQUIREMENTS,
    SELECTOR_REQUIREMENTS,
    Runner,
    nested_forbidden,
    release_manifest_errors,
)
from load_observer_signature import (
    validate_observer_verifier,
    verify_observer_evidence,
)


REQUIRED_ACCEPTANCE_REQUIREMENTS = frozenset().union(
    SELECTOR_REQUIREMENTS,
    OAUTH_REQUIREMENTS,
    SCHEDULED_REQUIREMENTS,
    SANDBOX_REQUIREMENTS,
    BYOK_REQUIREMENTS,
    BILLING_IMPORT_REQUIREMENTS,
)
ACCEPTANCE_FIELDS = {
    "schema_version", "run_id", "started_at", "finished_at", "target", "mode",
    "permissions", "release_manifest", "summary", "requirement_summary", "results",
}
RESULT_FIELDS = {
    "order", "phase", "name", "status", "message", "endpoint", "http_status",
    "duration_ms", "evidence",
}
SUMMARY_STATUSES = ("pass", "fail", "blocker", "skipped")
STATUS_RANK = {"pass": 0, "skipped": 1, "fail": 2, "blocker": 3}
WORKBENCH_TURN_EVENT_TYPE = "workbench.turn"
WORKBENCH_TERMINAL_EVENT_TYPE = "workbench.turn_status"
WORKBENCH_TERMINAL_STATUSES = frozenset(
    {
        "completed",
        "failed_before_accept",
        "failed_after_accept",
        "cancel_confirmed",
        "provider_unknown",
    }
)
EXTERNAL_EVIDENCE_FIELDS = {
    "schema_version", "collector_id", "acceptance_run_id", "release_manifest",
    "concurrency", "window_started_at", "window_finished_at", "collected_at",
    "observed_requests", "metrics",
}
EXTERNAL_OBSERVED_REQUEST_FIELDS = {"planned", "terminal", "successful"}
EXTERNAL_METRIC_FIELDS = {
    "sample_count", "active_connections_peak", "memory_peak_bytes",
    "persisted_turn_events", "db_latency_ms",
}
EXTERNAL_SERVICE_FIELDS = {"new_api", "claw_control", "adp"}
EXTERNAL_CONNECTION_FIELDS = {"edge", *EXTERNAL_SERVICE_FIELDS}
EXTERNAL_PERSISTED_FIELDS = {"before", "after", "delta"}
EXTERNAL_DB_LATENCY_FIELDS = {"sample_count", "p50", "p95", "p99"}
EXTERNAL_PLACEHOLDERS = frozenset(
    {"", "unknown", "not_collected", "not-collected", "placeholder", "n/a", "null"}
)
MAX_EXTERNAL_EVIDENCE_BYTES = 1024 * 1024
MAX_SSE_EVENT_BYTES = 256 * 1024


def parse_levels(value: str) -> list[int]:
    try:
        levels = [int(part) for part in value.split(",") if part]
    except ValueError as error:
        raise argparse.ArgumentTypeError("concurrency must contain 50 and/or 100") from error
    if not levels or len(set(levels)) != len(levels) or any(level not in {50, 100} for level in levels):
        raise argparse.ArgumentTypeError("only unique concurrency levels 50 and 100 are supported")
    return levels


def load_acceptance_binding(path_value: str, config: dict[str, Any]) -> tuple[str, dict[str, Any]]:
    allowed_signers_ref = config.get("acceptance_report_allowed_signers_file")
    if not allowed_signers_ref:
        raise ConfigError("acceptance_report_allowed_signers_file is required to verify acceptance results")
    raw = verify_evidence_signature(path_value, str(allowed_signers_ref))
    try:
        acceptance = json.loads(raw)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise ConfigError("acceptance results are not valid UTF-8 JSON") from error
    if not isinstance(acceptance, dict):
        raise ConfigError("acceptance results root must be an object")
    if set(acceptance) != ACCEPTANCE_FIELDS:
        missing = sorted(ACCEPTANCE_FIELDS.difference(acceptance))
        unknown = sorted(set(acceptance).difference(ACCEPTANCE_FIELDS))
        raise ConfigError(f"acceptance results fields differ from the report contract; missing={missing}, unknown={unknown}")
    if acceptance.get("schema_version") != 1:
        raise ConfigError("acceptance report schema_version must be 1")
    run_id = str(acceptance.get("run_id", ""))
    try:
        uuid.UUID(run_id)
    except ValueError as error:
        raise ConfigError("acceptance run ID is invalid") from error
    if acceptance.get("mode") != "live":
        raise ConfigError("acceptance results are not a live E2E run")
    timestamps: dict[str, datetime] = {}
    for key in ("started_at", "finished_at"):
        try:
            timestamp = datetime.fromisoformat(str(acceptance[key]).replace("Z", "+00:00"))
            if timestamp.tzinfo is None:
                raise ValueError("timezone is missing")
            timestamps[key] = timestamp.astimezone(timezone.utc)
        except ValueError as error:
            raise ConfigError(f"acceptance {key} is not a timezone-aware ISO-8601 timestamp") from error
    if timestamps["finished_at"] < timestamps["started_at"]:
        raise ConfigError("acceptance finished_at precedes started_at")
    if timestamps["finished_at"] > datetime.now(timezone.utc) + timedelta(minutes=5):
        raise ConfigError("acceptance finished_at is more than five minutes in the future")

    permissions = acceptance.get("permissions")
    if (
        not isinstance(permissions, dict)
        or set(permissions) != {"provider_cost", "mutations", "eicar"}
        or any(value is not True for value in permissions.values())
    ):
        raise ConfigError("acceptance permissions must prove provider-cost, mutation, and EICAR execution")

    results = acceptance.get("results")
    if not isinstance(results, list) or not results:
        raise ConfigError("acceptance results must contain executed checks")
    recomputed_summary = {status: 0 for status in SUMMARY_STATUSES}
    recomputed_requirements: dict[str, dict[str, Any]] = {}
    for expected_order, result in enumerate(results, 1):
        if not isinstance(result, dict) or set(result) != RESULT_FIELDS:
            raise ConfigError(f"acceptance result {expected_order} differs from the check contract")
        if result.get("order") != expected_order:
            raise ConfigError("acceptance result order must be contiguous and match array order")
        if any(not isinstance(result.get(key), str) for key in ("phase", "name", "status", "message", "endpoint")):
            raise ConfigError(f"acceptance result {expected_order} contains invalid string fields")
        status = result["status"]
        if status not in recomputed_summary:
            raise ConfigError(f"acceptance result {expected_order} has an invalid status")
        http_status = result.get("http_status")
        if http_status is not None and (
            not isinstance(http_status, int) or isinstance(http_status, bool) or not 100 <= http_status <= 599
        ):
            raise ConfigError(f"acceptance result {expected_order} has an invalid HTTP status")
        duration = result.get("duration_ms")
        if not isinstance(duration, (int, float)) or isinstance(duration, bool) or duration < 0:
            raise ConfigError(f"acceptance result {expected_order} has an invalid duration")
        evidence = result.get("evidence")
        if not isinstance(evidence, dict):
            raise ConfigError(f"acceptance result {expected_order} evidence must be an object")
        recomputed_summary[status] += 1
        requirement = evidence.get("requirement_id")
        if requirement is None:
            continue
        if not isinstance(requirement, str) or not requirement:
            raise ConfigError(f"acceptance result {expected_order} has an invalid requirement ID")
        assertions = evidence.get("assertion_types")
        if (
            not isinstance(assertions, list)
            or not assertions
            or assertions != sorted(set(assertions))
            or any(not isinstance(item, str) or not item for item in assertions)
        ):
            raise ConfigError(f"acceptance result {expected_order} has invalid assertion evidence")
        current = recomputed_requirements.setdefault(
            requirement,
            {"status": "pass", "checks": [], "assertion_types": []},
        )
        current["checks"].append(expected_order)
        current["assertion_types"] = sorted(set(current["assertion_types"]).union(assertions))
        if STATUS_RANK[status] > STATUS_RANK[current["status"]]:
            current["status"] = status

    summary = acceptance.get("summary")
    if summary != recomputed_summary:
        raise ConfigError("acceptance summary does not match the executed checks")
    if recomputed_summary["fail"] or recomputed_summary["blocker"] or recomputed_summary["skipped"]:
        raise ConfigError("acceptance results are not a complete successful live E2E run")
    if acceptance.get("requirement_summary") != recomputed_requirements:
        raise ConfigError("acceptance requirement summary does not match the executed checks")
    missing_requirements = sorted(REQUIRED_ACCEPTANCE_REQUIREMENTS.difference(recomputed_requirements))
    nonpassing_requirements = sorted(
        requirement
        for requirement in REQUIRED_ACCEPTANCE_REQUIREMENTS.intersection(recomputed_requirements)
        if recomputed_requirements[requirement]["status"] != "pass"
        or not recomputed_requirements[requirement]["checks"]
        or not recomputed_requirements[requirement]["assertion_types"]
    )
    if missing_requirements or nonpassing_requirements:
        raise ConfigError(
            f"acceptance requirement matrix is incomplete; missing={missing_requirements}, nonpassing={nonpassing_requirements}"
        )
    if acceptance.get("target") != sanitized_endpoint(config["base_url"]):
        raise ConfigError("acceptance target differs from the load target")
    if acceptance.get("release_manifest") != config.get("release_manifest"):
        raise ConfigError("acceptance release manifest differs from the load configuration")
    return run_id, dict(acceptance["release_manifest"])


def clone_client(source: HTTPClient, timeout: int) -> HTTPClient:
    client = HTTPClient(source.base_url, timeout=timeout)
    client.cookies = {name: StoredCookie(cookie.value, cookie.path, cookie.secure) for name, cookie in source.cookies.items()}
    return client


def _structured_sse_payload(data_lines: list[str]) -> dict[str, Any] | None:
    if not data_lines:
        return None
    encoded = "\n".join(data_lines).encode("utf-8")
    if len(encoded) > MAX_SSE_EVENT_BYTES:
        raise RuntimeError("SSE event exceeded 256 KiB")
    try:
        payload = json.loads(encoded)
    except (UnicodeDecodeError, json.JSONDecodeError):
        return None
    return payload if isinstance(payload, dict) else None


def run_one(source: HTTPClient, timeout: int, path: str, fixture: dict[str, Any]) -> dict[str, Any]:
    client = clone_client(source, timeout)
    payload = json.loads(json.dumps(fixture))
    client_request_id = str(uuid.uuid4())
    payload["ClientRequestId"] = client_request_id
    started = time.perf_counter()
    first_event_ms = None
    terminal = False
    terminal_outcome = "none"
    turn_id = ""
    status = 0
    response = None
    try:
        status, response_headers, response, _, _ = client.open_stream(
            "POST", path, body=json_bytes(payload),
            headers={"Content-Type": "application/json", "Accept": "text/event-stream"},
        )
        if status not in {200, 201}:
            response.read(4096)
            return {"status": status, "first_event_ms": None, "completed": False, "elapsed_ms": (time.perf_counter() - started) * 1000}
        if not str(response_headers.get("Content-Type", "")).lower().startswith("text/event-stream"):
            raise RuntimeError("load response is not text/event-stream")
        data_lines: list[str] = []
        data_bytes = 0

        def process_frame() -> bool:
            nonlocal first_event_ms, terminal, terminal_outcome, turn_id, data_lines, data_bytes
            structured = _structured_sse_payload(data_lines)
            data_lines = []
            data_bytes = 0
            if structured is None:
                return False
            if first_event_ms is None:
                first_event_ms = (time.perf_counter() - started) * 1000
            event_type = structured.get("Type")
            if event_type == WORKBENCH_TURN_EVENT_TYPE:
                observed_request_id = structured.get("ClientRequestId")
                observed_turn_id = structured.get("TurnId")
                if observed_request_id != client_request_id:
                    raise RuntimeError("workbench.turn ClientRequestId does not match the submitted request")
                if not isinstance(observed_turn_id, str) or not observed_turn_id:
                    raise RuntimeError("workbench.turn omitted a valid TurnId")
                if turn_id and turn_id != observed_turn_id:
                    raise RuntimeError("SSE stream changed TurnId")
                turn_id = observed_turn_id
                return False
            if event_type != WORKBENCH_TERMINAL_EVENT_TYPE:
                return False
            observed_turn_id = structured.get("TurnId")
            outcome = structured.get("Status")
            if not turn_id or observed_turn_id != turn_id:
                raise RuntimeError("workbench terminal event is not bound to the submitted Turn")
            if outcome not in WORKBENCH_TERMINAL_STATUSES:
                raise RuntimeError("workbench terminal event has an unknown Status")
            terminal = True
            terminal_outcome = str(outcome)
            return True

        for _ in range(10000):
            line = response.readline(65537)
            if not line:
                break
            if len(line) > 65536:
                raise RuntimeError("SSE line exceeded 64 KiB")
            try:
                text = line.decode("utf-8").rstrip("\r\n")
            except UnicodeDecodeError as error:
                raise RuntimeError("SSE stream is not valid UTF-8") from error
            if not text:
                if process_frame():
                    break
                continue
            if text == "data":
                data_lines.append("")
                data_bytes += 1 if len(data_lines) > 1 else 0
            elif text.startswith("data:"):
                value = text[5:].lstrip(" ")
                data_lines.append(value)
                data_bytes += len(value.encode("utf-8")) + (1 if len(data_lines) > 1 else 0)
            if data_bytes > MAX_SSE_EVENT_BYTES:
                raise RuntimeError("SSE event exceeded 256 KiB")
        if data_lines and not terminal:
            process_frame()
        return {
            "status": status,
            "first_event_ms": first_event_ms,
            "completed": terminal,
            "terminal_outcome": terminal_outcome,
            "elapsed_ms": (time.perf_counter() - started) * 1000,
        }
    except Exception as error:
        return {"status": status, "first_event_ms": first_event_ms, "completed": False, "elapsed_ms": (time.perf_counter() - started) * 1000, "error_type": type(error).__name__}
    finally:
        if response:
            response.close()


async def run_level(source: HTTPClient, level: int, timeout: int, path: str, fixture: dict[str, Any]) -> dict[str, Any]:
    window_started_at = utc_now()
    started = time.perf_counter()
    loop = asyncio.get_running_loop()
    # asyncio's shared executor is normally capped below 50 workers. An
    # explicitly sized pool is required for the configured number to mean
    # concurrent open SSE connections rather than merely queued tasks.
    with concurrent.futures.ThreadPoolExecutor(max_workers=level, thread_name_prefix="claw-sse") as pool:
        tasks = [loop.run_in_executor(pool, run_one, source, timeout, path, fixture) for _ in range(level)]
        results = await asyncio.gather(*tasks)
    first = [float(item["first_event_ms"]) for item in results if item["first_event_ms"] is not None]
    completed = sum(bool(item["completed"]) for item in results)
    successful = sum(item.get("terminal_outcome") == "completed" for item in results)
    statuses = Counter(str(item["status"]) for item in results)
    errors = Counter(str(item.get("error_type")) for item in results if item.get("error_type"))
    outcomes = Counter(str(item.get("terminal_outcome", "none")) for item in results)
    return {
        "concurrency": level,
        "planned_requests": level,
        "completed_requests": completed,
        "completion_rate": completed / level,
        "successful_requests": successful,
        "successful_completion_rate": successful / level,
        "terminal_outcome_counts": dict(outcomes),
        "first_event_ms": {"p50": percentile(first, 0.50), "p95": percentile(first, 0.95), "p99": percentile(first, 0.99)},
        "http_status_counts": dict(statuses),
        "error_type_counts": dict(errors),
        "wall_time_ms": (time.perf_counter() - started) * 1000,
        "window_started_at": window_started_at,
        "window_finished_at": utc_now(),
    }


def _aware_timestamp(value: Any, label: str) -> datetime:
    try:
        parsed = datetime.fromisoformat(str(value).replace("Z", "+00:00"))
    except ValueError as error:
        raise ConfigError(f"{label} must be a timezone-aware ISO-8601 timestamp") from error
    if parsed.tzinfo is None:
        raise ConfigError(f"{label} must be a timezone-aware ISO-8601 timestamp")
    return parsed.astimezone(timezone.utc)


def _exact_nonnegative_integer(value: Any, label: str, *, positive: bool = False) -> int:
    if not isinstance(value, int) or isinstance(value, bool) or value < (1 if positive else 0):
        qualifier = "positive" if positive else "non-negative"
        raise ConfigError(f"{label} must be a {qualifier} integer")
    return value


def _finite_nonnegative_number(value: Any, label: str) -> float:
    if not isinstance(value, (int, float)) or isinstance(value, bool):
        raise ConfigError(f"{label} must be a finite non-negative number")
    normalized = float(value)
    if not math.isfinite(normalized) or normalized < 0:
        raise ConfigError(f"{label} must be a finite non-negative number")
    return normalized


def _reject_external_placeholders(value: Any, label: str = "external evidence") -> None:
    if isinstance(value, str) and value.strip().lower() in EXTERNAL_PLACEHOLDERS:
        raise ConfigError(f"{label} contains a placeholder value")
    if isinstance(value, dict):
        for key, item in value.items():
            _reject_external_placeholders(item, f"{label}.{key}")
    elif isinstance(value, list):
        for index, item in enumerate(value):
            _reject_external_placeholders(item, f"{label}[{index}]")


def _reject_nonfinite_json(value: str) -> None:
    raise ValueError(value)


def external_evidence_refs(load: dict[str, Any], levels: list[int]) -> dict[int, str]:
    configured = load.get("external_evidence")
    if not isinstance(configured, dict) or set(configured) != {"50", "100"}:
        raise ConfigError("load.external_evidence must contain exact file references for levels 50 and 100")
    refs: dict[int, str] = {}
    for level in levels:
        ref = configured.get(str(level))
        if not isinstance(ref, str) or not ref.startswith("file:"):
            raise ConfigError(f"load.external_evidence.{level} must use file:/absolute/path")
        path = Path(ref[5:])
        if not path.is_absolute():
            raise ConfigError(f"load.external_evidence.{level} must use file:/absolute/path")
        refs[level] = ref
    return refs


def validate_external_evidence(
    evidence: Any,
    *,
    acceptance_run_id: str,
    release_manifest: dict[str, Any],
    scenario: dict[str, Any],
) -> dict[str, Any]:
    level = int(scenario["concurrency"])
    label = f"external evidence level {level}"
    if not isinstance(evidence, dict) or set(evidence) != EXTERNAL_EVIDENCE_FIELDS:
        raise ConfigError(f"{label} differs from the exact collector contract")
    _reject_external_placeholders(evidence, label)
    if evidence.get("schema_version") != 1 or evidence.get("collector_id") != "claw-load-observer/v1":
        raise ConfigError(f"{label} has an unsupported schema or collector")
    if evidence.get("acceptance_run_id") != acceptance_run_id:
        raise ConfigError(f"{label} is not bound to this acceptance run")
    if evidence.get("release_manifest") != release_manifest:
        raise ConfigError(f"{label} is not bound to the exact release manifest")
    if evidence.get("concurrency") != level:
        raise ConfigError(f"{label} is not bound to concurrency {level}")

    observed = evidence.get("observed_requests")
    if not isinstance(observed, dict) or set(observed) != EXTERNAL_OBSERVED_REQUEST_FIELDS:
        raise ConfigError(f"{label}.observed_requests differs from the exact contract")
    expected_observed = {
        "planned": int(scenario["planned_requests"]),
        "terminal": int(scenario["completed_requests"]),
        "successful": int(scenario["successful_requests"]),
    }
    for key, expected in expected_observed.items():
        _exact_nonnegative_integer(observed.get(key), f"{label}.observed_requests.{key}")
        if observed[key] != expected:
            raise ConfigError(f"{label}.observed_requests.{key} does not match the load runner")

    scenario_start = _aware_timestamp(scenario["window_started_at"], "scenario.window_started_at")
    scenario_finish = _aware_timestamp(scenario["window_finished_at"], "scenario.window_finished_at")
    window_start = _aware_timestamp(evidence["window_started_at"], f"{label}.window_started_at")
    window_finish = _aware_timestamp(evidence["window_finished_at"], f"{label}.window_finished_at")
    collected_at = _aware_timestamp(evidence["collected_at"], f"{label}.collected_at")
    now = datetime.now(timezone.utc)
    if scenario_finish < scenario_start or window_finish < window_start:
        raise ConfigError(f"{label} has an inverted time window")
    if window_start > scenario_start or window_finish < scenario_finish:
        raise ConfigError(f"{label} does not cover the complete load-level time window")
    if (scenario_start - window_start).total_seconds() > 120:
        raise ConfigError(f"{label} starts more than 120 seconds before the load level")
    if (window_finish - scenario_finish).total_seconds() > 120:
        raise ConfigError(f"{label} ends more than 120 seconds after the load level")
    if collected_at < window_finish:
        raise ConfigError(f"{label}.collected_at precedes the observed window")
    if collected_at > now + timedelta(minutes=5):
        raise ConfigError(f"{label}.collected_at is more than five minutes in the future")
    if now - collected_at > timedelta(minutes=10):
        raise ConfigError(f"{label}.collected_at is older than ten minutes")

    metrics = evidence.get("metrics")
    if not isinstance(metrics, dict) or set(metrics) != EXTERNAL_METRIC_FIELDS:
        raise ConfigError(f"{label}.metrics differs from the exact collector contract")
    sample_count = _exact_nonnegative_integer(
        metrics.get("sample_count"), f"{label}.metrics.sample_count", positive=True,
    )
    if sample_count < 2:
        raise ConfigError(f"{label}.metrics.sample_count must contain at least two observations")

    connections = metrics.get("active_connections_peak")
    if not isinstance(connections, dict) or set(connections) != EXTERNAL_CONNECTION_FIELDS:
        raise ConfigError(f"{label}.metrics.active_connections_peak must cover edge and every backend")
    for component in sorted(EXTERNAL_CONNECTION_FIELDS):
        _exact_nonnegative_integer(connections.get(component), f"{label}.metrics.active_connections_peak.{component}")
    for component in ("edge", "claw_control", "adp"):
        if connections[component] == 0:
            raise ConfigError(f"{label} did not observe an active {component} connection")

    memory = metrics.get("memory_peak_bytes")
    if not isinstance(memory, dict) or set(memory) != EXTERNAL_SERVICE_FIELDS:
        raise ConfigError(f"{label}.metrics.memory_peak_bytes must cover all three services")
    for component in sorted(EXTERNAL_SERVICE_FIELDS):
        _exact_nonnegative_integer(memory.get(component), f"{label}.metrics.memory_peak_bytes.{component}", positive=True)

    persisted = metrics.get("persisted_turn_events")
    if not isinstance(persisted, dict) or set(persisted) != EXTERNAL_PERSISTED_FIELDS:
        raise ConfigError(f"{label}.metrics.persisted_turn_events differs from the exact contract")
    for key in sorted(EXTERNAL_PERSISTED_FIELDS):
        _exact_nonnegative_integer(persisted.get(key), f"{label}.metrics.persisted_turn_events.{key}")
    if persisted["after"] - persisted["before"] != persisted["delta"]:
        raise ConfigError(f"{label}.metrics.persisted_turn_events.delta is arithmetically inconsistent")
    if persisted["delta"] < expected_observed["terminal"]:
        raise ConfigError(f"{label} did not persist at least one Turn event per terminal request")

    db_latency = metrics.get("db_latency_ms")
    if not isinstance(db_latency, dict) or set(db_latency) != EXTERNAL_DB_LATENCY_FIELDS:
        raise ConfigError(f"{label}.metrics.db_latency_ms differs from the exact contract")
    db_sample_count = _exact_nonnegative_integer(
        db_latency.get("sample_count"), f"{label}.metrics.db_latency_ms.sample_count", positive=True,
    )
    if db_sample_count < expected_observed["terminal"]:
        raise ConfigError(f"{label}.metrics.db_latency_ms did not observe every terminal request")
    p50 = _finite_nonnegative_number(db_latency.get("p50"), f"{label}.metrics.db_latency_ms.p50")
    p95 = _finite_nonnegative_number(db_latency.get("p95"), f"{label}.metrics.db_latency_ms.p95")
    p99 = _finite_nonnegative_number(db_latency.get("p99"), f"{label}.metrics.db_latency_ms.p99")
    if not p50 <= p95 <= p99:
        raise ConfigError(f"{label}.metrics.db_latency_ms percentiles are not monotonic")
    return evidence


def wait_for_external_evidence(
    ref: str,
    *,
    wait_seconds: int,
    allowed_signers_ref: str,
    acceptance_run_id: str,
    release_manifest: dict[str, Any],
    scenario: dict[str, Any],
) -> dict[str, Any]:
    path = ref[5:]
    deadline = time.monotonic() + wait_seconds
    while True:
        try:
            raw = verify_observer_evidence(path, allowed_signers_ref)
        except ConfigError:
            if time.monotonic() >= deadline:
                raise ConfigError(
                    f"external evidence for level {scenario['concurrency']} was not available within {wait_seconds} seconds"
                )
            time.sleep(0.5)
            continue
        try:
            evidence = json.loads(
                raw,
                parse_constant=_reject_nonfinite_json,
            )
        except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as error:
            raise ConfigError(
                f"external evidence for level {scenario['concurrency']} is not strict UTF-8 JSON"
            ) from error
        return validate_external_evidence(
            evidence,
            acceptance_run_id=acceptance_run_id,
            release_manifest=release_manifest,
            scenario=scenario,
        )


def write_load_reports(output: str, payload: dict[str, Any], redactor: Redactor) -> None:
    target = Path(output)
    safe = redactor.clean(payload)
    write_atomic(target / "load-results.json", json.dumps(safe, ensure_ascii=False, indent=2) + "\n")
    rows = []
    for result in safe["scenarios"]:
        latency = result["first_event_ms"]
        rows.append(
            f"| {result['concurrency']} | {result['planned_requests']} | {result['completed_requests']} | "
            f"{result['completion_rate']:.2%} | {result['successful_completion_rate']:.2%} | {latency['p50']} | {latency['p95']} | {latency['p99']} | "
            f"{result['wall_time_ms']:.1f} | {json.dumps(result['http_status_counts'], separators=(',', ':'))} |"
        )
    release_rows = [
        f"| {str(key).replace('|', '&#124;')} | {json.dumps(value, ensure_ascii=False).replace('|', '&#124;').replace('`', '&#96;')} |"
        for key, value in sorted(safe.get("release_manifest", {}).items())
    ] or ["| missing | blocker |"]
    external_rows = []
    for result in safe["scenarios"]:
        evidence = result.get("external_collection")
        if not isinstance(evidence, dict):
            continue
        metrics = evidence["metrics"]
        latency = metrics["db_latency_ms"]
        external_rows.append(
            f"| {result['concurrency']} | {metrics['sample_count']} | "
            f"{json.dumps(metrics['active_connections_peak'], separators=(',', ':'))} | "
            f"{json.dumps(metrics['memory_peak_bytes'], separators=(',', ':'))} | "
            f"{metrics['persisted_turn_events']['delta']} | "
            f"{latency['p50']} / {latency['p95']} / {latency['p99']} | "
            f"{evidence['window_started_at']} / {evidence['window_finished_at']} |"
        )
    if not external_rows:
        external_rows.append("| dry-run | 0 | not executed | not executed | 0 | not executed | not executed |")
    markdown = f"""# Claw Workbench SSE load report

- Acceptance run ID: `{safe.get('acceptance_run_id', 'missing')}`
- Started/finished (UTC): `{safe['started_at']}` / `{safe['finished_at']}`
- Target: `{safe['target']}`
- Mode: `{safe['mode']}`
- Provider-cost authorization: `{safe['provider_cost_allowed']}`

## Immutable release binding

| Item | Exact value |
|---|---|
{os.linesep.join(release_rows)}

| Concurrency | Planned | Terminal | Terminal rate | Success rate | First P50 ms | First P95 ms | First P99 ms | Wall ms | HTTP status |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|
{os.linesep.join(rows)}

## Bound external collector evidence

| Concurrency | Samples | Peak active connections (edge/backends) | Peak memory bytes (services) | Persisted Turn-event delta | DB latency P50 / P95 / P99 ms | Collector window UTC |
|---:|---:|---|---|---:|---|---|
{os.linesep.join(external_rows)}

Every live row is loaded from a bounded, non-symlink `file:` artifact and is
validated against this acceptance run ID, exact release manifest, concurrency,
request counts, and load-level UTC window. Missing, stale, placeholder, malformed,
or arithmetically inconsistent evidence makes the run fail closed. No Cookie,
ticket, secret, request body, raw response, or raw SSE event is stored.
"""
    write_atomic(target / "load-report.md", markdown)


def parser() -> argparse.ArgumentParser:
    value = argparse.ArgumentParser(description=__doc__)
    value.add_argument("--config", required=True)
    value.add_argument("--output", default="claw-load-report")
    value.add_argument("--concurrency", type=parse_levels, default=[50, 100])
    value.add_argument("--execute", action="store_true")
    value.add_argument("--allow-provider-cost", action="store_true")
    value.add_argument("--confirm-task-count", type=int)
    value.add_argument("--acceptance-results", help="successful main E2E results.json for the exact same release")
    value.add_argument("--allow-http", action="store_true")
    return value


def main(argv: list[str] | None = None) -> int:
    args = parser().parse_args(argv)
    try:
        config = load_config(args.config, allow_http=args.allow_http)
    except ConfigError as error:
        print(f"configuration error: {error}", file=sys.stderr)
        return 2
    if "acceptance_report_signing_key_file" in config:
        print(
            "configuration error: load_sse accepts only acceptance_report_allowed_signers_file; "
            "the private signer belongs in the separate E2E run configuration",
            file=sys.stderr,
        )
        return 2
    if args.execute and "acceptance_report_allowed_signers_file" not in config:
        print(
            "configuration error: live load requires acceptance_report_allowed_signers_file",
            file=sys.stderr,
        )
        return 2
    planned = sum(args.concurrency)
    started = utc_now()
    redactor = Redactor()
    load = config.get("load", {})
    base_payload = {
        "schema_version": 1, "started_at": started, "finished_at": started,
        "target": sanitized_endpoint(config["base_url"]),
        "mode": "live" if args.execute else "dry-run",
        "provider_cost_allowed": args.allow_provider_cost,
        "planned_task_count": planned, "scenarios": [], "blockers": [],
        "acceptance_run_id": "",
        "release_manifest": config.get("release_manifest", {}),
    }
    if not args.execute:
        base_payload["scenarios"] = [{"concurrency": level, "planned_requests": level, "completed_requests": 0, "completion_rate": 0.0, "successful_requests": 0, "successful_completion_rate": 0.0, "terminal_outcome_counts": {}, "first_event_ms": {"p50": None, "p95": None, "p99": None}, "http_status_counts": {}, "wall_time_ms": 0.0} for level in args.concurrency]
        base_payload["blockers"].append("dry-run: no provider Turns were created")
        base_payload["finished_at"] = utc_now()
        write_load_reports(args.output, base_payload, redactor)
        print(json.dumps({"output": str(Path(args.output).resolve()), "planned": planned, "mode": "dry-run"}))
        return 0
    if not args.allow_provider_cost or args.confirm_task_count != planned:
        print(f"error: live load requires --allow-provider-cost and --confirm-task-count {planned}", file=sys.stderr)
        return 2
    if config.get("_release_manifest_source") != "file":
        print("error: live load requires release_manifest as a collector file:/absolute/path reference", file=sys.stderr)
        return 2
    manifest_validation_errors = release_manifest_errors(config.get("release_manifest"), max_age_seconds=1800)
    if manifest_validation_errors:
        print(
            "error: live load requires a fresh immutable release manifest: "
            + "; ".join(manifest_validation_errors),
            file=sys.stderr,
        )
        return 2
    if not args.acceptance_results:
        print("error: live load requires --acceptance-results from the successful main E2E run", file=sys.stderr)
        return 2
    try:
        evidence_refs = external_evidence_refs(load, args.concurrency)
        evidence_wait_seconds = load.get("external_evidence_wait_seconds", 60)
        if (
            not isinstance(evidence_wait_seconds, int)
            or isinstance(evidence_wait_seconds, bool)
            or not 1 <= evidence_wait_seconds <= 300
        ):
            raise ConfigError("load.external_evidence_wait_seconds must be an integer between 1 and 300")
        observer_allowed_signers_ref = load.get("external_evidence_allowed_signers_file")
        main_allowed_signers_ref = config.get("acceptance_report_allowed_signers_file")
        if not isinstance(observer_allowed_signers_ref, str) or not isinstance(main_allowed_signers_ref, str):
            raise ConfigError("both main and load-observer public allowed_signers files are required")
        validate_observer_verifier(
            observer_allowed_signers_ref,
            main_allowed_signers_ref,
        )
    except ConfigError as error:
        print(f"error: invalid external load evidence configuration: {error}", file=sys.stderr)
        return 2
    try:
        run_id, release_manifest = load_acceptance_binding(args.acceptance_results, config)
        base_payload["acceptance_run_id"] = run_id
        base_payload["release_manifest"] = release_manifest
    except ConfigError as error:
        print(f"error: invalid --acceptance-results: {error}", file=sys.stderr)
        return 2
    if not load.get("request_fixture"):
        print("error: load.request_fixture is required", file=sys.stderr)
        return 2
    session_ref = config.get("secrets", {}).get("user_a_session")
    if not session_ref:
        print("error: user_a_session reference is required", file=sys.stderr)
        return 2
    try:
        session = resolve_secret(str(session_ref))
        redactor.add(session)
        fixture = load_json_fixture(str(load["request_fixture"]), dict(config.get("variables", {})))
        forbidden = nested_forbidden(fixture)
        if forbidden:
            raise ConfigError(f"load fixture must not provide trusted identity field {forbidden}")
        namespace = argparse.Namespace(execute=True, allow_mutations=False, allow_eicar=False, allow_provider_cost=True)
        runner = Runner(config, namespace, run_id=str(base_payload["acceptance_run_id"]))
        if not runner.bootstrap("user_a") or not runner.identity("user_a"):
            raise RuntimeError("user A SSO/identity bootstrap failed")
        source = runner.actor_client("user_a")
        if source is None:
            raise RuntimeError("user A client unavailable")
        application = runner.identities.get("user_a", {}).get("application")
        if application and not any(str(key).lower() == "applicationid" for key in fixture):
            fixture["ApplicationId"] = application
        timeout = int(load.get("timeout_seconds", 900))
        path = str(load.get("turn_path", DEFAULT_PATHS["turn"]))
        for level in args.concurrency:
            scenario = asyncio.run(run_level(source, level, timeout, path, fixture))
            scenario["external_collection"] = wait_for_external_evidence(
                evidence_refs[level],
                wait_seconds=evidence_wait_seconds,
                allowed_signers_ref=observer_allowed_signers_ref,
                acceptance_run_id=str(base_payload["acceptance_run_id"]),
                release_manifest=dict(base_payload["release_manifest"]),
                scenario=scenario,
            )
            base_payload["scenarios"].append(scenario)
    except Exception as error:
        base_payload["blockers"].append(f"{type(error).__name__}: {error}")
        base_payload["finished_at"] = utc_now()
        write_load_reports(args.output, base_payload, redactor)
        print(json.dumps({"output": str(Path(args.output).resolve()), "blocker": type(error).__name__}))
        return 2
    base_payload["finished_at"] = utc_now()
    write_load_reports(args.output, base_payload, redactor)
    complete = all(
        item["successful_completion_rate"] == 1.0
        and isinstance(item.get("external_collection"), dict)
        for item in base_payload["scenarios"]
    )
    print(json.dumps({"output": str(Path(args.output).resolve()), "complete": complete}))
    return 0 if complete else 1
if __name__ == "__main__":
    raise SystemExit(main())
