#!/usr/bin/env python3
"""Read-only Prometheus collector for the paid 50/100 SSE load gates.

The collector never accepts metric values, request counts, or observation
timestamps from its caller.  It establishes a Prometheus baseline, announces
readiness, observes one configured load level, and atomically writes the exact
external-evidence contract consumed by ``load_sse.py``.
"""

from __future__ import annotations

import argparse
import json
import math
import os
import re
import ssl
import sys
import time
from collections import defaultdict
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Callable
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode, urlsplit
from urllib.request import HTTPSHandler, Request, build_opener

from e2e_lib import (
    ConfigError,
    NoRedirect,
    load_config,
    load_evidence_file,
    resolve_secret,
    validate_base_url,
    write_atomic,
)
from load_sse import load_acceptance_binding, validate_external_evidence
from load_observer_signature import (
    validate_observer_signing_key,
    write_signed_observer_evidence,
)


COLLECTOR_ID = "claw-load-observer/v1"
MAX_PROMETHEUS_CONFIG_BYTES = 64 * 1024
MAX_PROMETHEUS_RESPONSE_BYTES = 2 * 1024 * 1024
COMPONENTS = ("edge", "new_api", "claw_control", "adp")
SERVICES = ("new_api", "claw_control", "adp")
METRIC_NAME_RE = re.compile(r"[A-Za-z_:][A-Za-z0-9_:]*\Z")
LABEL_NAME_RE = re.compile(r"[A-Za-z_][A-Za-z0-9_]*\Z")


@dataclass(frozen=True)
class MetricSelector:
    metric: str
    labels: dict[str, str]

    def promql(self) -> str:
        if not self.labels:
            return self.metric
        encoded = ",".join(
            f'{key}="{_promql_quote(value)}"' for key, value in sorted(self.labels.items())
        )
        return f"{self.metric}{{{encoded}}}"


@dataclass(frozen=True)
class TurnMetric:
    selector: MetricSelector
    status_label: str
    successful_status: str


@dataclass(frozen=True)
class DBLatencyMetric:
    bucket: MetricSelector
    count: MetricSelector
    upper_bound_label: str


@dataclass(frozen=True)
class ObserverConfig:
    prometheus_url: str
    bearer_token: str
    ca_pem: str
    timeout_seconds: int
    poll_seconds: float
    range_step_seconds: int
    evidence_signing_key_file: str
    active: dict[str, MetricSelector]
    memory: dict[str, MetricSelector]
    terminal_turns: TurnMetric
    persisted_events: MetricSelector
    db_latency: DBLatencyMetric


def _promql_quote(value: str) -> str:
    return value.replace("\\", "\\\\").replace("\n", "\\n").replace('"', '\\"')


def _strict_json_constant(value: str) -> None:
    raise ValueError(value)


def _require_exact_keys(value: Any, expected: set[str], label: str) -> dict[str, Any]:
    if not isinstance(value, dict) or set(value) != expected:
        raise ConfigError(f"{label} must contain exactly {sorted(expected)}")
    return value


def _load_selector(value: Any, label: str) -> MetricSelector:
    selector = _require_exact_keys(value, {"metric", "labels"}, label)
    metric = selector["metric"]
    labels = selector["labels"]
    if not isinstance(metric, str) or not METRIC_NAME_RE.fullmatch(metric):
        raise ConfigError(f"{label}.metric is not a Prometheus metric name")
    if not isinstance(labels, dict) or len(labels) > 16:
        raise ConfigError(f"{label}.labels must be a bounded object")
    normalized: dict[str, str] = {}
    for key, item in labels.items():
        if not isinstance(key, str) or not LABEL_NAME_RE.fullmatch(key):
            raise ConfigError(f"{label}.labels contains an invalid label name")
        if not isinstance(item, str) or not item or len(item) > 256 or "\n" in item or "\r" in item:
            raise ConfigError(f"{label}.labels.{key} must be a non-empty bounded string")
        normalized[key] = item
    return MetricSelector(metric=metric, labels=normalized)


def load_observer_config(path_value: str, *, allow_http_loopback: bool = False) -> ObserverConfig:
    path = Path(os.path.abspath(path_value))
    raw = load_evidence_file(str(path), maximum=MAX_PROMETHEUS_CONFIG_BYTES)
    try:
        payload = json.loads(raw, parse_constant=_strict_json_constant)
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as error:
        raise ConfigError("Prometheus observer configuration is not strict UTF-8 JSON") from error
    root = _require_exact_keys(
        payload,
        {
            "schema_version", "prometheus_url", "bearer_token", "ca_file",
            "timeout_seconds", "poll_seconds", "range_step_seconds",
            "evidence_signing_key_file", "metrics",
        },
        "observer configuration",
    )
    if root["schema_version"] != 1:
        raise ConfigError("observer configuration schema_version must be 1")
    if not isinstance(root["prometheus_url"], str):
        raise ConfigError("prometheus_url must be a string")
    prometheus_url = validate_base_url(
        root["prometheus_url"], allow_http=allow_http_loopback,
    )
    parsed = urlsplit(prometheus_url)
    if parsed.path not in {"", "/"}:
        raise ConfigError("prometheus_url must not contain a path")

    bearer_ref = root["bearer_token"]
    if bearer_ref is not None and not isinstance(bearer_ref, str):
        raise ConfigError("bearer_token must be null or an env:/file: secret reference")
    bearer_token = resolve_secret(bearer_ref) if bearer_ref else ""

    ca_ref = root["ca_file"]
    if ca_ref is not None and (not isinstance(ca_ref, str) or not ca_ref.startswith("file:")):
        raise ConfigError("ca_file must be null or file:/absolute/path")
    ca_pem = ""
    if ca_ref:
        try:
            ca_pem = load_evidence_file(ca_ref[5:], maximum=1024 * 1024).decode("ascii")
        except UnicodeDecodeError as error:
            raise ConfigError("ca_file must contain ASCII PEM certificates") from error

    timeout = root["timeout_seconds"]
    poll = root["poll_seconds"]
    step = root["range_step_seconds"]
    if not isinstance(timeout, int) or isinstance(timeout, bool) or not 1 <= timeout <= 60:
        raise ConfigError("timeout_seconds must be an integer between 1 and 60")
    if (
        not isinstance(poll, (int, float)) or isinstance(poll, bool)
        or not math.isfinite(float(poll)) or not 0.25 <= float(poll) <= 30
    ):
        raise ConfigError("poll_seconds must be between 0.25 and 30")
    if not isinstance(step, int) or isinstance(step, bool) or not 1 <= step <= 30:
        raise ConfigError("range_step_seconds must be an integer between 1 and 30")
    signing_key_ref = root["evidence_signing_key_file"]
    if (
        not isinstance(signing_key_ref, str)
        or not signing_key_ref.startswith("file:")
        or not Path(signing_key_ref[5:]).is_absolute()
    ):
        raise ConfigError("evidence_signing_key_file must be file:/absolute/path")

    metrics = _require_exact_keys(
        root["metrics"],
        {
            "active_connections_peak", "memory_peak_bytes", "terminal_turns",
            "persisted_turn_events", "db_latency",
        },
        "metrics",
    )
    active_payload = _require_exact_keys(
        metrics["active_connections_peak"], set(COMPONENTS), "metrics.active_connections_peak",
    )
    memory_payload = _require_exact_keys(
        metrics["memory_peak_bytes"], set(SERVICES), "metrics.memory_peak_bytes",
    )
    active = {
        component: _load_selector(active_payload[component], f"metrics.active_connections_peak.{component}")
        for component in COMPONENTS
    }
    memory = {
        service: _load_selector(memory_payload[service], f"metrics.memory_peak_bytes.{service}")
        for service in SERVICES
    }

    turns_payload = _require_exact_keys(
        metrics["terminal_turns"],
        {"metric", "labels", "status_label", "successful_status"},
        "metrics.terminal_turns",
    )
    turns_selector = _load_selector(
        {"metric": turns_payload["metric"], "labels": turns_payload["labels"]},
        "metrics.terminal_turns",
    )
    status_label = turns_payload["status_label"]
    successful_status = turns_payload["successful_status"]
    if not isinstance(status_label, str) or not LABEL_NAME_RE.fullmatch(status_label):
        raise ConfigError("metrics.terminal_turns.status_label is invalid")
    if status_label in turns_selector.labels:
        raise ConfigError("terminal Turn selector must not pre-filter the status label")
    if not isinstance(successful_status, str) or not successful_status or len(successful_status) > 64:
        raise ConfigError("metrics.terminal_turns.successful_status is invalid")

    persisted_selector = _load_selector(
        metrics["persisted_turn_events"],
        "metrics.persisted_turn_events",
    )
    if persisted_selector.metric == turns_selector.metric:
        raise ConfigError("terminal Turn and persisted Turn-event metrics must be independent families")

    db_payload = _require_exact_keys(
        metrics["db_latency"],
        {"bucket_metric", "count_metric", "labels", "upper_bound_label"},
        "metrics.db_latency",
    )
    db_bucket = _load_selector(
        {"metric": db_payload["bucket_metric"], "labels": db_payload["labels"]},
        "metrics.db_latency.bucket",
    )
    db_count = _load_selector(
        {"metric": db_payload["count_metric"], "labels": db_payload["labels"]},
        "metrics.db_latency.count",
    )
    if db_count.metric != db_bucket.metric.removesuffix("_bucket") + "_count":
        raise ConfigError("DB latency bucket_metric/count_metric must be one histogram family")
    upper_bound_label = db_payload["upper_bound_label"]
    if not isinstance(upper_bound_label, str) or not LABEL_NAME_RE.fullmatch(upper_bound_label):
        raise ConfigError("metrics.db_latency.upper_bound_label is invalid")
    if upper_bound_label in db_bucket.labels:
        raise ConfigError("DB bucket selector must not pre-filter the upper-bound label")

    return ObserverConfig(
        prometheus_url=prometheus_url,
        bearer_token=bearer_token,
        ca_pem=ca_pem,
        timeout_seconds=timeout,
        poll_seconds=float(poll),
        range_step_seconds=step,
        evidence_signing_key_file=signing_key_ref,
        active=active,
        memory=memory,
        terminal_turns=TurnMetric(turns_selector, status_label, successful_status),
        persisted_events=persisted_selector,
        db_latency=DBLatencyMetric(db_bucket, db_count, upper_bound_label),
    )


class PrometheusClient:
    def __init__(self, config: ObserverConfig) -> None:
        self.base_url = config.prometheus_url
        self.token = config.bearer_token
        self.timeout = config.timeout_seconds
        context = ssl.create_default_context(cadata=config.ca_pem or None)
        self.opener = build_opener(NoRedirect, HTTPSHandler(context=context))

    def _request(self, endpoint: str, parameters: dict[str, str]) -> dict[str, Any]:
        url = f"{self.base_url}{endpoint}?{urlencode(parameters)}"
        headers = {"Accept": "application/json"}
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        request = Request(url, headers=headers, method="GET")
        try:
            response = self.opener.open(request, timeout=self.timeout)
        except HTTPError as error:
            raise ConfigError(f"Prometheus returned HTTP {error.code}") from error
        except (URLError, TimeoutError, OSError) as error:
            raise ConfigError("Prometheus request failed") from error
        with response:
            final = urlsplit(response.geturl())
            expected = urlsplit(self.base_url)
            if (final.scheme, final.netloc) != (expected.scheme, expected.netloc):
                raise ConfigError("Prometheus response changed origin")
            content_type = response.headers.get_content_type()
            if content_type != "application/json":
                raise ConfigError("Prometheus response Content-Type is not application/json")
            raw = response.read(MAX_PROMETHEUS_RESPONSE_BYTES + 1)
        if len(raw) > MAX_PROMETHEUS_RESPONSE_BYTES:
            raise ConfigError("Prometheus response exceeds the collector limit")
        try:
            payload = json.loads(raw, parse_constant=_strict_json_constant)
        except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as error:
            raise ConfigError("Prometheus returned invalid strict JSON") from error
        if (
            not isinstance(payload, dict) or payload.get("status") != "success"
            or set(payload).difference({"status", "data", "warnings", "infos"})
            or not isinstance(payload.get("data"), dict)
        ):
            raise ConfigError("Prometheus returned an unsuccessful or unknown response contract")
        if payload.get("warnings"):
            raise ConfigError("Prometheus returned query warnings")
        return payload["data"]

    def instant(self, selector: MetricSelector, at: float) -> list[dict[str, Any]]:
        data = self._request(
            "/api/v1/query", {"query": selector.promql(), "time": _timestamp_parameter(at)},
        )
        if data.get("resultType") != "vector" or not isinstance(data.get("result"), list):
            raise ConfigError("Prometheus instant query did not return a vector")
        return data["result"]

    def range(
        self, selector: MetricSelector, start: float, finish: float, step: int,
    ) -> list[dict[str, Any]]:
        data = self._request(
            "/api/v1/query_range",
            {
                "query": selector.promql(),
                "start": _timestamp_parameter(start),
                "end": _timestamp_parameter(finish),
                "step": str(step),
            },
        )
        if data.get("resultType") != "matrix" or not isinstance(data.get("result"), list):
            raise ConfigError("Prometheus range query did not return a matrix")
        return data["result"]


def _timestamp_parameter(value: float) -> str:
    if not math.isfinite(value) or value < 0:
        raise ConfigError("invalid Prometheus evaluation timestamp")
    return f"{value:.6f}"


def _sample_value(value: Any, label: str) -> float:
    if not isinstance(value, (list, tuple)) or len(value) != 2:
        raise ConfigError(f"{label} has an invalid Prometheus sample")
    try:
        timestamp = float(value[0])
        sample = float(value[1])
    except (TypeError, ValueError) as error:
        raise ConfigError(f"{label} has a non-numeric Prometheus sample") from error
    if not math.isfinite(timestamp) or not math.isfinite(sample) or sample < 0:
        raise ConfigError(f"{label} has a non-finite or negative Prometheus sample")
    return sample


def _series_key(metric: Any, label: str) -> tuple[tuple[str, str], ...]:
    if not isinstance(metric, dict):
        raise ConfigError(f"{label} has invalid Prometheus labels")
    pairs: list[tuple[str, str]] = []
    for key, value in metric.items():
        if not isinstance(key, str) or not isinstance(value, str):
            raise ConfigError(f"{label} has non-string Prometheus labels")
        pairs.append((key, value))
    return tuple(sorted(pairs))


def _instant_snapshot(
    result: list[dict[str, Any]],
    label: str,
    *,
    expected_at: float | None = None,
    tolerance_seconds: float = 0,
) -> dict[tuple[tuple[str, str], ...], float]:
    if not result:
        raise ConfigError(f"{label} has no Prometheus series")
    snapshot: dict[tuple[tuple[str, str], ...], float] = {}
    for index, series in enumerate(result):
        if not isinstance(series, dict) or set(series) != {"metric", "value"}:
            raise ConfigError(f"{label}[{index}] differs from the Prometheus vector contract")
        key = _series_key(series["metric"], f"{label}[{index}]")
        if key in snapshot:
            raise ConfigError(f"{label} contains duplicate Prometheus series")
        if expected_at is not None:
            try:
                sample_at = float(series["value"][0])
            except (TypeError, ValueError, IndexError) as error:
                raise ConfigError(f"{label}[{index}] has an invalid timestamp") from error
            if abs(sample_at - expected_at) > tolerance_seconds:
                raise ConfigError(f"{label}[{index}] is stale or outside the requested instant")
        snapshot[key] = _sample_value(series["value"], f"{label}[{index}]")
    return snapshot


def _matrix_series(result: list[dict[str, Any]], label: str) -> list[tuple[dict[str, str], list[tuple[float, float]]]]:
    if not result:
        raise ConfigError(f"{label} has no Prometheus series")
    normalized: list[tuple[dict[str, str], list[tuple[float, float]]]] = []
    seen: set[tuple[tuple[str, str], ...]] = set()
    for index, series in enumerate(result):
        if not isinstance(series, dict) or set(series) != {"metric", "values"}:
            raise ConfigError(f"{label}[{index}] differs from the Prometheus matrix contract")
        metric = series["metric"]
        key = _series_key(metric, f"{label}[{index}]")
        if key in seen:
            raise ConfigError(f"{label} contains duplicate Prometheus series")
        seen.add(key)
        values = series["values"]
        if not isinstance(values, list) or not values:
            raise ConfigError(f"{label}[{index}] has no samples")
        points: list[tuple[float, float]] = []
        previous = -1.0
        for sample_index, raw in enumerate(values):
            sample = _sample_value(raw, f"{label}[{index}].values[{sample_index}]")
            timestamp = float(raw[0])
            if timestamp <= previous:
                raise ConfigError(f"{label}[{index}] samples are not strictly ordered")
            previous = timestamp
            points.append((timestamp, sample))
        normalized.append((dict(metric), points))
    return normalized


def _counter_increases(
    result: list[dict[str, Any]],
    baseline: dict[tuple[tuple[str, str], ...], float],
    label: str,
) -> list[tuple[dict[str, str], float]]:
    increases: list[tuple[dict[str, str], float]] = []
    for metric, points in _matrix_series(result, label):
        key = tuple(sorted(metric.items()))
        previous = baseline.get(key, 0.0)
        increase = 0.0
        for _, current in points:
            increase += current - previous if current >= previous else current
            previous = current
        increases.append((metric, increase))
    for key, value in baseline.items():
        if not any(tuple(sorted(metric.items())) == key for metric, _ in increases):
            if value != 0:
                raise ConfigError(f"{label} lost a non-zero baseline series")
    return increases


def _exact_integer(value: float, label: str) -> int:
    rounded = round(value)
    if not math.isfinite(value) or value < 0 or abs(value - rounded) > 1e-9:
        raise ConfigError(f"{label} is not an exact non-negative integer")
    return int(rounded)


def _aggregate_points(result: list[dict[str, Any]], label: str) -> dict[float, float]:
    aggregated: dict[float, float] = defaultdict(float)
    for _, points in _matrix_series(result, label):
        for timestamp, value in points:
            aggregated[timestamp] += value
    if len(aggregated) < 2:
        raise ConfigError(f"{label} must contain at least two range observations")
    return dict(aggregated)


def _require_range_coverage(
    result: list[dict[str, Any]],
    label: str,
    *,
    start: float,
    finish: float,
    step: int,
    require_start: bool,
) -> None:
    tolerance = step + 0.001
    for _, points in _matrix_series(result, label):
        if require_start and points[0][0] > start + tolerance:
            raise ConfigError(f"{label} does not cover the beginning of the requested window")
        if points[-1][0] < finish - tolerance:
            raise ConfigError(f"{label} does not cover the end of the requested window")


def _histogram_quantile(quantile: float, buckets: dict[float, int], count: int) -> float:
    if count <= 0 or not 0 < quantile < 1 or math.inf not in buckets:
        raise ConfigError("DB latency histogram is empty or lacks +Inf")
    ordered = sorted(buckets)
    previous_count = 0
    previous_bound = 0.0
    target = quantile * count
    for bound in ordered:
        cumulative = buckets[bound]
        if cumulative < previous_count or cumulative > count:
            raise ConfigError("DB latency histogram buckets are not cumulative")
        if cumulative >= target:
            if math.isinf(bound):
                if previous_bound <= 0:
                    raise ConfigError("DB latency histogram has no finite populated bucket")
                return previous_bound
            bucket_count = cumulative - previous_count
            if bucket_count <= 0:
                return bound
            fraction = (target - previous_count) / bucket_count
            return previous_bound + (bound - previous_bound) * fraction
        previous_count = cumulative
        previous_bound = bound
    raise ConfigError("DB latency histogram does not cover its count")


def _iso(timestamp: float) -> str:
    return datetime.fromtimestamp(timestamp, timezone.utc).isoformat().replace("+00:00", "Z")


def _establish_baselines(
    client: PrometheusClient,
    config: ObserverConfig,
    at: float,
) -> dict[str, Any]:
    preflight_start = at - max(2 * config.range_step_seconds, 2)
    instant_tolerance = config.range_step_seconds + 5
    for component, selector in config.active.items():
        _instant_snapshot(
            client.instant(selector, at),
            f"active connections {component}",
            expected_at=at,
            tolerance_seconds=instant_tolerance,
        )
        preflight = client.range(selector, preflight_start, at, config.range_step_seconds)
        _aggregate_points(
            preflight,
            f"active connections {component} preflight",
        )
        _require_range_coverage(
            preflight, f"active connections {component} preflight",
            start=preflight_start, finish=at, step=config.range_step_seconds, require_start=True,
        )
    for service, selector in config.memory.items():
        snapshot = _instant_snapshot(
            client.instant(selector, at),
            f"memory {service}",
            expected_at=at,
            tolerance_seconds=instant_tolerance,
        )
        if sum(snapshot.values()) <= 0:
            raise ConfigError(f"memory {service} has no positive live value")
        preflight = client.range(selector, preflight_start, at, config.range_step_seconds)
        _aggregate_points(
            preflight,
            f"memory {service} preflight",
        )
        _require_range_coverage(
            preflight, f"memory {service} preflight",
            start=preflight_start, finish=at, step=config.range_step_seconds, require_start=True,
        )
    terminal_turns = _instant_snapshot(
        client.instant(config.terminal_turns.selector, at),
        "terminal Turns baseline",
        expected_at=at,
        tolerance_seconds=instant_tolerance,
    )
    turns_preflight = client.range(
        config.terminal_turns.selector, preflight_start, at, config.range_step_seconds,
    )
    _require_range_coverage(
        turns_preflight, "terminal Turns preflight",
        start=preflight_start, finish=at, step=config.range_step_seconds, require_start=True,
    )
    for metric, _ in _matrix_series(turns_preflight, "terminal Turns preflight"):
        if config.terminal_turns.status_label not in metric:
            raise ConfigError("terminal Turn preflight series is missing its status label")

    persisted_events = _instant_snapshot(
        client.instant(config.persisted_events, at),
        "persisted Turn events baseline",
        expected_at=at,
        tolerance_seconds=instant_tolerance,
    )
    persisted_preflight = client.range(
        config.persisted_events, preflight_start, at, config.range_step_seconds,
    )
    _require_range_coverage(
        persisted_preflight, "persisted Turn events preflight",
        start=preflight_start, finish=at, step=config.range_step_seconds, require_start=True,
    )
    db_count = _instant_snapshot(
        client.instant(config.db_latency.count, at),
        "DB latency count baseline",
        expected_at=at,
        tolerance_seconds=instant_tolerance,
    )
    db_bucket = _instant_snapshot(
        client.instant(config.db_latency.bucket, at),
        "DB latency bucket baseline",
        expected_at=at,
        tolerance_seconds=instant_tolerance,
    )
    db_count_preflight = client.range(
        config.db_latency.count, preflight_start, at, config.range_step_seconds,
    )
    _require_range_coverage(
        db_count_preflight, "DB latency count preflight",
        start=preflight_start, finish=at, step=config.range_step_seconds, require_start=True,
    )
    bucket_bounds: dict[float, int] = defaultdict(int)
    for key, value in db_bucket.items():
        labels = dict(key)
        raw_bound = labels.get(config.db_latency.upper_bound_label)
        if raw_bound is None:
            raise ConfigError("DB latency baseline bucket is missing its upper-bound label")
        try:
            bound = math.inf if raw_bound == "+Inf" else float(raw_bound)
        except ValueError as error:
            raise ConfigError("DB latency baseline bucket has an invalid upper bound") from error
        bucket_bounds[bound] += _exact_integer(value, f"DB latency baseline bucket {bound}")
    baseline_count = _exact_integer(sum(db_count.values()), "DB latency baseline count")
    if bucket_bounds.get(math.inf) != baseline_count:
        raise ConfigError("DB latency baseline +Inf bucket does not equal its count")
    db_bucket_preflight = client.range(
        config.db_latency.bucket, preflight_start, at, config.range_step_seconds,
    )
    _require_range_coverage(
        db_bucket_preflight, "DB latency bucket preflight",
        start=preflight_start, finish=at, step=config.range_step_seconds, require_start=True,
    )
    return {
        "terminal_turns": terminal_turns,
        "persisted_events": persisted_events,
        "db_count": db_count,
        "db_bucket": db_bucket,
    }


def collect_evidence(
    client: PrometheusClient,
    config: ObserverConfig,
    *,
    acceptance_run_id: str,
    release_manifest: dict[str, Any],
    concurrency: int,
    timeout_seconds: int,
    ready_callback: Callable[[float], None] | None = None,
) -> dict[str, Any]:
    if concurrency not in {50, 100}:
        raise ConfigError("collector concurrency must be 50 or 100")
    if not isinstance(timeout_seconds, int) or isinstance(timeout_seconds, bool) or not 30 <= timeout_seconds <= 3600:
        raise ConfigError("collector timeout must be an integer between 30 and 3600 seconds")

    preflight_at = time.time()
    baseline = _establish_baselines(client, config, preflight_at)
    # The paid load is allowed to start only after the callback/READY marker.
    # Keeping the evidence window after all preflight queries prevents old
    # preflight range samples from being counted as this run.
    window_start = time.time()
    if ready_callback is not None:
        ready_callback(window_start)
    deadline = time.monotonic() + timeout_seconds
    turn_matrix: list[dict[str, Any]] = []
    terminal = successful = 0
    while True:
        evaluated_at = time.time()
        turn_matrix = client.range(
            config.terminal_turns.selector, window_start, evaluated_at, config.range_step_seconds,
        )
        _require_range_coverage(
            turn_matrix, "terminal Turns",
            start=window_start, finish=evaluated_at,
            step=config.range_step_seconds, require_start=False,
        )
        turn_increases = _counter_increases(
            turn_matrix, baseline["terminal_turns"], "terminal Turns",
        )
        terminal_total = 0.0
        successful_total = 0.0
        for metric, increase in turn_increases:
            if config.terminal_turns.status_label not in metric:
                raise ConfigError("terminal Turn series is missing its status label")
            terminal_total += increase
            if metric[config.terminal_turns.status_label] == config.terminal_turns.successful_status:
                successful_total += increase
        terminal = _exact_integer(terminal_total, "terminal persisted Turns")
        successful = _exact_integer(successful_total, "successful persisted Turns")
        if successful > terminal:
            raise ConfigError("successful persisted Turns exceed terminal Turns")
        if terminal >= concurrency:
            break
        if time.monotonic() >= deadline:
            raise ConfigError("Prometheus did not observe the configured terminal Turn count before timeout")
        time.sleep(config.poll_seconds)

    window_finish = time.time()
    peak_connections: dict[str, int] = {}
    peak_memory: dict[str, int] = {}
    observation_counts: list[int] = []
    for component, selector in config.active.items():
        matrix = client.range(selector, window_start, window_finish, config.range_step_seconds)
        _require_range_coverage(
            matrix, f"active connections {component}",
            start=window_start, finish=window_finish,
            step=config.range_step_seconds, require_start=True,
        )
        points = _aggregate_points(
            matrix,
            f"active connections {component}",
        )
        observation_counts.append(len(points))
        peak_connections[component] = _exact_integer(
            max(points.values()), f"active connections peak {component}",
        )
    for service, selector in config.memory.items():
        matrix = client.range(selector, window_start, window_finish, config.range_step_seconds)
        _require_range_coverage(
            matrix, f"memory {service}",
            start=window_start, finish=window_finish,
            step=config.range_step_seconds, require_start=True,
        )
        points = _aggregate_points(
            matrix,
            f"memory {service}",
        )
        observation_counts.append(len(points))
        peak_memory[service] = _exact_integer(max(points.values()), f"memory peak {service}")
        if peak_memory[service] <= 0:
            raise ConfigError(f"memory peak {service} is not positive")

    persisted_matrix = client.range(
        config.persisted_events, window_start, window_finish, config.range_step_seconds,
    )
    _require_range_coverage(
        persisted_matrix, "persisted Turn events",
        start=window_start, finish=window_finish,
        step=config.range_step_seconds, require_start=True,
    )
    persisted_delta = _exact_integer(
        sum(
            increase for _, increase in _counter_increases(
                persisted_matrix, baseline["persisted_events"], "persisted Turn events",
            )
        ),
        "persisted Turn-event count",
    )
    if persisted_delta < terminal:
        raise ConfigError("persisted Turn-event count is below terminal Turn count")

    db_count_matrix = client.range(
        config.db_latency.count, window_start, window_finish, config.range_step_seconds,
    )
    _require_range_coverage(
        db_count_matrix, "DB latency count",
        start=window_start, finish=window_finish,
        step=config.range_step_seconds, require_start=True,
    )
    db_count_increase = sum(
        increase for _, increase in _counter_increases(
            db_count_matrix, baseline["db_count"], "DB latency count",
        )
    )
    db_sample_count = _exact_integer(db_count_increase, "DB latency sample count")
    if db_sample_count < terminal:
        raise ConfigError("DB latency histogram did not observe every terminal Turn")
    if db_sample_count > persisted_delta:
        raise ConfigError("DB latency transactions exceed persisted Turn-event rows")
    db_bucket_matrix = client.range(
        config.db_latency.bucket, window_start, window_finish, config.range_step_seconds,
    )
    _require_range_coverage(
        db_bucket_matrix, "DB latency buckets",
        start=window_start, finish=window_finish,
        step=config.range_step_seconds, require_start=True,
    )
    bucket_increases: dict[float, float] = defaultdict(float)
    for metric, increase in _counter_increases(
        db_bucket_matrix, baseline["db_bucket"], "DB latency buckets",
    ):
        raw_bound = metric.get(config.db_latency.upper_bound_label)
        if raw_bound is None:
            raise ConfigError("DB latency bucket is missing its upper-bound label")
        try:
            bound = math.inf if raw_bound == "+Inf" else float(raw_bound)
        except ValueError as error:
            raise ConfigError("DB latency bucket has an invalid upper bound") from error
        if bound <= 0 or (not math.isfinite(bound) and bound != math.inf):
            raise ConfigError("DB latency bucket upper bounds must be positive or +Inf")
        bucket_increases[bound] += increase
    buckets = {
        bound: _exact_integer(value, f"DB latency bucket {bound}")
        for bound, value in bucket_increases.items()
    }
    if buckets.get(math.inf) != db_sample_count:
        raise ConfigError("DB latency +Inf bucket does not equal the histogram count")
    p50 = _histogram_quantile(0.50, buckets, db_sample_count) * 1000
    p95 = _histogram_quantile(0.95, buckets, db_sample_count) * 1000
    p99 = _histogram_quantile(0.99, buckets, db_sample_count) * 1000

    before = _exact_integer(
        sum(baseline["persisted_events"].values()),
        "persisted Turn-event baseline",
    )
    collected_at = time.time()
    evidence = {
        "schema_version": 1,
        "collector_id": COLLECTOR_ID,
        "acceptance_run_id": acceptance_run_id,
        "release_manifest": release_manifest,
        "concurrency": concurrency,
        "window_started_at": _iso(window_start),
        "window_finished_at": _iso(window_finish),
        "collected_at": _iso(collected_at),
        "observed_requests": {
            "planned": concurrency,
            "terminal": terminal,
            "successful": successful,
        },
        "metrics": {
            "sample_count": min(observation_counts),
            "active_connections_peak": peak_connections,
            "memory_peak_bytes": peak_memory,
            "persisted_turn_events": {
                "before": before,
                "after": before + persisted_delta,
                "delta": persisted_delta,
            },
            "db_latency_ms": {
                "sample_count": db_sample_count,
                "p50": p50,
                "p95": p95,
                "p99": p99,
            },
        },
    }
    validation_scenario = {
        "concurrency": concurrency,
        "planned_requests": concurrency,
        "completed_requests": terminal,
        "successful_requests": successful,
        "window_started_at": evidence["window_started_at"],
        "window_finished_at": evidence["window_finished_at"],
    }
    return validate_external_evidence(
        evidence,
        acceptance_run_id=acceptance_run_id,
        release_manifest=release_manifest,
        scenario=validation_scenario,
    )


def _configured_output(config: dict[str, Any], concurrency: int) -> Path:
    load = config.get("load")
    refs = load.get("external_evidence") if isinstance(load, dict) else None
    ref = refs.get(str(concurrency)) if isinstance(refs, dict) else None
    if not isinstance(ref, str) or not ref.startswith("file:"):
        raise ConfigError(f"load.external_evidence.{concurrency} must be configured")
    path = Path(ref[5:])
    if not path.is_absolute():
        raise ConfigError("external evidence output must be absolute")
    return path


def _remove_stale_regular_file(path: Path) -> None:
    if not path.exists() and not path.is_symlink():
        return
    if path.is_symlink() or not path.is_file():
        raise ConfigError(f"refusing to replace non-regular output {path}")
    path.unlink()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", required=True, help="load E2E configuration")
    parser.add_argument("--prometheus-config", required=True, help="strict observer JSON configuration")
    parser.add_argument("--acceptance-results", required=True, help="signed main E2E results.json")
    parser.add_argument("--concurrency", required=True, type=int, choices=(50, 100))
    parser.add_argument("--timeout-seconds", type=int, default=1800)
    parser.add_argument(
        "--allow-http-loopback", action="store_true",
        help="allow only loopback HTTP for a local Prometheus self-test",
    )
    args = parser.parse_args()
    try:
        load_config_value = load_config(args.config, allow_http=args.allow_http_loopback)
        run_id, release_manifest = load_acceptance_binding(
            args.acceptance_results, load_config_value,
        )
        observer = load_observer_config(
            args.prometheus_config, allow_http_loopback=args.allow_http_loopback,
        )
        output = _configured_output(load_config_value, args.concurrency)
        ready = output.with_name(output.name + ".ready")
        signature = output.with_name(output.name + ".sig")
        _remove_stale_regular_file(output)
        _remove_stale_regular_file(ready)
        _remove_stale_regular_file(signature)
        load_settings = load_config_value.get("load", {})
        observer_allowed_ref = load_settings.get("external_evidence_allowed_signers_file")
        main_allowed_ref = load_config_value.get("acceptance_report_allowed_signers_file")
        if not isinstance(observer_allowed_ref, str) or not isinstance(main_allowed_ref, str):
            raise ConfigError("load runner public signer references are required")
        validate_observer_signing_key(
            observer.evidence_signing_key_file,
            observer_allowed_ref,
            main_allowed_ref,
        )
        client = PrometheusClient(observer)

        def announce_ready(timestamp: float) -> None:
            write_atomic(
                ready,
                json.dumps(
                    {
                        "collector_id": COLLECTOR_ID,
                        "acceptance_run_id": run_id,
                        "concurrency": args.concurrency,
                        "ready_at": _iso(timestamp),
                    },
                    ensure_ascii=False,
                    separators=(",", ":"),
                ) + "\n",
            )
            print(f"READY {ready}", flush=True)

        evidence = collect_evidence(
            client,
            observer,
            acceptance_run_id=run_id,
            release_manifest=release_manifest,
            concurrency=args.concurrency,
            timeout_seconds=args.timeout_seconds,
            ready_callback=announce_ready,
        )
        write_signed_observer_evidence(
            output,
            json.dumps(evidence, ensure_ascii=False, indent=2) + "\n",
            observer.evidence_signing_key_file,
        )
        print(f"WROTE {output}", flush=True)
        return 0
    except (ConfigError, OSError) as error:
        print(f"BLOCKER: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
