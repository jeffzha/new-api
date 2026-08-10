from __future__ import annotations

import json
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch
from urllib.parse import parse_qs, urlsplit


E2E_ROOT = Path(__file__).resolve().parents[1]
if str(E2E_ROOT) not in sys.path:
    sys.path.insert(0, str(E2E_ROOT))

from e2e_lib import ConfigError, validate_config_contract  # noqa: E402
from load_observer import (  # noqa: E402
    DBLatencyMetric,
    MetricSelector,
    ObserverConfig,
    PrometheusClient,
    TurnMetric,
    collect_evidence,
    load_observer_config,
)
from load_observer_signature import (  # noqa: E402
    validate_observer_signing_key,
    validate_observer_verifier,
    verify_observer_evidence,
    write_signed_observer_evidence,
)
from load_sse import validate_external_evidence  # noqa: E402


def vector(metric: dict[str, str], value: float, *, at: float = 1_800_000_000) -> dict[str, object]:
    return {"metric": metric, "value": [at, str(value)]}


def matrix(
    metric: dict[str, str],
    values: list[float],
    *,
    start: float = 1_800_000_000,
    finish: float | None = None,
) -> dict[str, object]:
    if finish is None:
        finish = start + len(values) - 1
    interval = (finish - start) / max(len(values) - 1, 1)
    return {
        "metric": metric,
        "values": [[start + index * interval, str(value)] for index, value in enumerate(values)],
    }


class FakePrometheus:
    def __init__(self, config: ObserverConfig, concurrency: int) -> None:
        self.config = config
        self.concurrency = concurrency

    def instant(self, selector: MetricSelector, at: float) -> list[dict[str, object]]:
        name = selector.metric
        if name.startswith("test_active_"):
            return [vector({"instance": "one"}, 0, at=at)]
        if name.startswith("test_memory_"):
            return [vector({"instance": "one"}, 1024, at=at)]
        if name == "workbench_turns_total":
            return [
                vector({"instance": "one", "status": "completed"}, 100, at=at),
                vector({"instance": "one", "status": "failed"}, 0, at=at),
            ]
        if name == "workbench_turn_events_persisted_total":
            return [vector({"instance": "one"}, 500, at=at)]
        if name == "workbench_turn_event_persist_db_seconds_count":
            return [vector({"instance": "one"}, 300, at=at)]
        if name == "workbench_turn_event_persist_db_seconds_bucket":
            return [
                vector({"instance": "one", "le": "0.001"}, 50, at=at),
                vector({"instance": "one", "le": "0.005"}, 150, at=at),
                vector({"instance": "one", "le": "0.01"}, 300, at=at),
                vector({"instance": "one", "le": "+Inf"}, 300, at=at),
            ]
        raise AssertionError(name)

    def range(
        self, selector: MetricSelector, start: float, finish: float, step: int,
    ) -> list[dict[str, object]]:
        del step
        name = selector.metric
        if name.startswith("test_active_"):
            component = name.removeprefix("test_active_")
            peak = 0 if component == "new_api" else self.concurrency
            return [matrix({"instance": "one"}, [0, peak, 0], start=start, finish=finish)]
        if name.startswith("test_memory_"):
            return [matrix({"instance": "one"}, [1024, 2048, 1536], start=start, finish=finish)]
        if name == "workbench_turns_total":
            return [
                matrix(
                    {"instance": "one", "status": "completed"},
                    [100, 100 + self.concurrency // 2, 100 + self.concurrency],
                    start=start,
                    finish=finish,
                ),
                matrix(
                    {"instance": "one", "status": "failed"}, [0, 0, 0],
                    start=start, finish=finish,
                ),
            ]
        if name == "workbench_turn_events_persisted_total":
            persisted = self.concurrency * 3
            return [matrix(
                {"instance": "one"},
                [500, 500 + persisted // 2, 500 + persisted],
                start=start,
                finish=finish,
            )]
        if name == "workbench_turn_event_persist_db_seconds_count":
            transactions = self.concurrency * 3
            return [matrix(
                {"instance": "one"},
                [300, 300 + transactions // 2, 300 + transactions],
                start=start,
                finish=finish,
            )]
        if name == "workbench_turn_event_persist_db_seconds_bucket":
            transactions = self.concurrency * 3
            increments = {
                "0.001": transactions // 5,
                "0.005": transactions * 4 // 5,
                "0.01": transactions,
                "+Inf": transactions,
            }
            baselines = {"0.001": 50, "0.005": 150, "0.01": 300, "+Inf": 300}
            return [
                matrix(
                    {"instance": "one", "le": bound},
                    [baseline, baseline + increments[bound] // 2, baseline + increments[bound]],
                    start=start,
                    finish=finish,
                )
                for bound, baseline in baselines.items()
            ]
        raise AssertionError(name)


def observer_config(prometheus_url: str = "https://prometheus.example") -> ObserverConfig:
    active = {
        component: MetricSelector(f"test_active_{component}", {"job": component})
        for component in ("edge", "new_api", "claw_control", "adp")
    }
    memory = {
        service: MetricSelector(f"test_memory_{service}", {"job": service})
        for service in ("new_api", "claw_control", "adp")
    }
    return ObserverConfig(
        prometheus_url=prometheus_url,
        bearer_token="",
        ca_pem="",
        timeout_seconds=2,
        poll_seconds=0.25,
        range_step_seconds=1,
        evidence_signing_key_file="file:/observer-ed25519",
        active=active,
        memory=memory,
        terminal_turns=TurnMetric(
            MetricSelector("workbench_turns_total", {"job": "adp"}),
            "status",
            "completed",
        ),
        persisted_events=MetricSelector(
            "workbench_turn_events_persisted_total", {"job": "adp"},
        ),
        db_latency=DBLatencyMetric(
            MetricSelector("workbench_turn_event_persist_db_seconds_bucket", {"job": "adp"}),
            MetricSelector("workbench_turn_event_persist_db_seconds_count", {"job": "adp"}),
            "le",
        ),
    )


class PrometheusHandler(BaseHTTPRequestHandler):
    requests: list[tuple[str, dict[str, list[str]], str]] = []

    def do_GET(self) -> None:  # noqa: N802
        parsed = urlsplit(self.path)
        type_ = "vector" if parsed.path == "/api/v1/query" else "matrix"
        result = (
            [{"metric": {"instance": "one"}, "value": [1_800_000_000, "1"]}]
            if type_ == "vector"
            else [{"metric": {"instance": "one"}, "values": [[1_800_000_000, "1"], [1_800_000_001, "2"]]}]
        )
        self.__class__.requests.append(
            (parsed.path, parse_qs(parsed.query), self.headers.get("Authorization", "")),
        )
        body = json.dumps({"status": "success", "data": {"resultType": type_, "result": result}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format: str, *args: object) -> None:
        del format, args


class LoadObserverTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)

    def tearDown(self) -> None:
        self.temp.cleanup()

    def keypair(self, name: str, identity: str) -> tuple[Path, Path]:
        private = self.root / name
        subprocess.run(
            ["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(private)],
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        public = private.with_suffix(".pub").read_text(encoding="ascii").strip()
        allowed = self.root / f"{name}-allowed"
        allowed.write_text(f"{identity} {public}\n", encoding="ascii")
        return private, allowed

    def test_collects_exact_evidence_only_from_prometheus_results(self) -> None:
        config = observer_config()
        ready: list[float] = []
        evidence = collect_evidence(
            FakePrometheus(config, 50),
            config,
            acceptance_run_id="f5e22d4c-93af-4db0-9f8d-804820b716c8",
            release_manifest={"new_api_image": "sha256:new", "adp_image": "sha256:adp"},
            concurrency=50,
            timeout_seconds=30,
            ready_callback=ready.append,
        )
        self.assertEqual(1, len(ready))
        self.assertEqual({"planned": 50, "terminal": 50, "successful": 50}, evidence["observed_requests"])
        self.assertEqual(150, evidence["metrics"]["persisted_turn_events"]["delta"])
        self.assertEqual(150, evidence["metrics"]["db_latency_ms"]["sample_count"])
        self.assertEqual(50, evidence["metrics"]["active_connections_peak"]["edge"])
        self.assertEqual(0, evidence["metrics"]["active_connections_peak"]["new_api"])
        self.assertGreater(evidence["metrics"]["db_latency_ms"]["p99"], 0)
        scenario = {
            "concurrency": 50,
            "planned_requests": 50,
            "completed_requests": 50,
            "successful_requests": 50,
            "window_started_at": evidence["window_started_at"],
            "window_finished_at": evidence["window_finished_at"],
        }
        self.assertEqual(
            evidence,
            validate_external_evidence(
                evidence,
                acceptance_run_id=evidence["acceptance_run_id"],
                release_manifest=evidence["release_manifest"],
                scenario=scenario,
            ),
        )

    def test_strict_config_has_metric_selectors_and_rejects_supplied_evidence_values(self) -> None:
        payload = {
            "schema_version": 1,
            "prometheus_url": "http://127.0.0.1:9090",
            "bearer_token": None,
            "ca_file": None,
            "timeout_seconds": 5,
            "poll_seconds": 0.25,
            "range_step_seconds": 1,
            "evidence_signing_key_file": f"file:{self.root / 'observer-ed25519'}",
            "metrics": {
                "active_connections_peak": {
                    component: {"metric": f"test_active_{component}", "labels": {"job": component}}
                    for component in ("edge", "new_api", "claw_control", "adp")
                },
                "memory_peak_bytes": {
                    service: {"metric": f"test_memory_{service}", "labels": {"job": service}}
                    for service in ("new_api", "claw_control", "adp")
                },
                "terminal_turns": {
                    "metric": "workbench_turns_total",
                    "labels": {"job": "adp"},
                    "status_label": "status",
                    "successful_status": "completed",
                },
                "persisted_turn_events": {
                    "metric": "workbench_turn_events_persisted_total",
                    "labels": {"job": "adp"},
                },
                "db_latency": {
                    "bucket_metric": "workbench_turn_event_persist_db_seconds_bucket",
                    "count_metric": "workbench_turn_event_persist_db_seconds_count",
                    "labels": {"job": "adp"},
                    "upper_bound_label": "le",
                },
            },
        }
        path = self.root / "observer.json"
        path.write_text(json.dumps(payload), encoding="utf-8")
        parsed = load_observer_config(str(path), allow_http_loopback=True)
        self.assertEqual(
            "workbench_turns_total{job=\"adp\"}",
            parsed.terminal_turns.selector.promql(),
        )
        self.assertEqual(
            "workbench_turn_events_persisted_total{job=\"adp\"}",
            parsed.persisted_events.promql(),
        )

        same_family = json.loads(json.dumps(payload))
        same_family["metrics"]["persisted_turn_events"]["metric"] = "workbench_turns_total"
        path.write_text(json.dumps(same_family), encoding="utf-8")
        with self.assertRaisesRegex(ConfigError, "independent families"):
            load_observer_config(str(path), allow_http_loopback=True)

        payload["observed_requests"] = {"terminal": 50}
        path.write_text(json.dumps(payload), encoding="utf-8")
        with self.assertRaisesRegex(ConfigError, "exactly"):
            load_observer_config(str(path), allow_http_loopback=True)

    def test_prometheus_client_uses_only_query_apis_and_bearer_auth(self) -> None:
        PrometheusHandler.requests = []
        server = ThreadingHTTPServer(("127.0.0.1", 0), PrometheusHandler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            config = observer_config(f"http://127.0.0.1:{server.server_port}")
            config = ObserverConfig(**{**config.__dict__, "bearer_token": "read-only-token"})
            client = PrometheusClient(config)
            selector = MetricSelector("workbench_active_turns", {"job": "adp"})
            self.assertEqual(1, len(client.instant(selector, 1_800_000_000)))
            self.assertEqual(1, len(client.range(selector, 1_800_000_000, 1_800_000_001, 1)))
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)
        self.assertEqual(["/api/v1/query", "/api/v1/query_range"], [item[0] for item in PrometheusHandler.requests])
        self.assertTrue(all(item[2] == "Bearer read-only-token" for item in PrometheusHandler.requests))
        self.assertEqual("workbench_active_turns{job=\"adp\"}", PrometheusHandler.requests[0][1]["query"][0])

    def test_missing_prometheus_series_fails_closed(self) -> None:
        config = observer_config()
        fake = FakePrometheus(config, 50)
        original = fake.instant

        def missing(selector: MetricSelector, at: float) -> list[dict[str, object]]:
            if selector.metric == "test_memory_adp":
                return []
            return original(selector, at)

        fake.instant = missing  # type: ignore[method-assign]
        with self.assertRaisesRegex(ConfigError, "has no Prometheus series"):
            collect_evidence(
                fake,
                config,
                acceptance_run_id="f5e22d4c-93af-4db0-9f8d-804820b716c8",
                release_manifest={"new_api_image": "sha256:new"},
                concurrency=50,
                timeout_seconds=30,
            )

    def test_stale_range_fails_before_ready(self) -> None:
        config = observer_config()
        fake = FakePrometheus(config, 50)
        original = fake.range

        def stale(
            selector: MetricSelector, start: float, finish: float, step: int,
        ) -> list[dict[str, object]]:
            if selector.metric == "test_active_edge":
                return [matrix({"instance": "one"}, [0, 1], start=1, finish=2)]
            return original(selector, start, finish, step)

        fake.range = stale  # type: ignore[method-assign]
        ready: list[float] = []
        with self.assertRaisesRegex(ConfigError, "does not cover"):
            collect_evidence(
                fake,
                config,
                acceptance_run_id="f5e22d4c-93af-4db0-9f8d-804820b716c8",
                release_manifest={"new_api_image": "sha256:new"},
                concurrency=50,
                timeout_seconds=30,
                ready_callback=ready.append,
            )
        self.assertEqual([], ready)

    def test_external_evidence_signature_is_independent_and_fail_closed(self) -> None:
        observer_key, observer_allowed = self.keypair("observer", "claw-load-observer")
        main_key, main_allowed = self.keypair("main", "claw-workbench-e2e")
        validate_observer_signing_key(
            f"file:{observer_key}", f"file:{observer_allowed}", f"file:{main_allowed}",
        )
        validate_observer_verifier(f"file:{observer_allowed}", f"file:{main_allowed}")

        evidence = self.root / "load-50.json"
        content = '{"collector_id":"claw-load-observer/v1"}\n'
        write_signed_observer_evidence(evidence, content, f"file:{observer_key}")
        self.assertEqual(
            content.encode("utf-8"),
            verify_observer_evidence(str(evidence), f"file:{observer_allowed}"),
        )

        import load_observer_signature

        original_load = load_observer_signature.load_evidence_file
        swapped = False

        def replace_after_read(path_value: str, *, maximum: int) -> bytes:
            nonlocal swapped
            data = original_load(path_value, maximum=maximum)
            if path_value == str(evidence) and not swapped:
                swapped = True
                evidence.write_text(content.replace("v1", "replaced"), encoding="utf-8")
            return data

        with patch(
            "load_observer_signature.load_evidence_file", side_effect=replace_after_read,
        ):
            verified = verify_observer_evidence(str(evidence), f"file:{observer_allowed}")
        self.assertEqual(content.encode("utf-8"), verified)
        self.assertIn("replaced", evidence.read_text(encoding="utf-8"))

        with self.assertRaisesRegex(ConfigError, "signature verification failed"):
            verify_observer_evidence(str(evidence), f"file:{observer_allowed}")

        write_signed_observer_evidence(evidence, content, f"file:{observer_key}")
        wrong_identity = self.root / "wrong-identity"
        wrong_identity.write_text(
            observer_allowed.read_text(encoding="ascii").replace("claw-load-observer", "wrong"),
            encoding="ascii",
        )
        with self.assertRaisesRegex(ConfigError, "exact identity"):
            verify_observer_evidence(str(evidence), f"file:{wrong_identity}")

        _, wrong_allowed = self.keypair("wrong-observer", "claw-load-observer")
        with self.assertRaisesRegex(ConfigError, "signature verification failed"):
            verify_observer_evidence(str(evidence), f"file:{wrong_allowed}")

        evidence.with_name(evidence.name + ".sig").unlink()
        with self.assertRaises(ConfigError):
            verify_observer_evidence(str(evidence), f"file:{observer_allowed}")

        write_signed_observer_evidence(evidence, content, f"file:{observer_key}")
        wrong_message = self.root / "wrong-namespace.json"
        wrong_message.write_text(content, encoding="utf-8")
        subprocess.run(
            [
                "ssh-keygen", "-Y", "sign", "-f", str(observer_key),
                "-n", "wrong-namespace", str(wrong_message),
            ],
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        shutil.copyfile(
            str(wrong_message) + ".sig", str(evidence) + ".sig",
        )
        with self.assertRaisesRegex(ConfigError, "signature verification failed"):
            verify_observer_evidence(str(evidence), f"file:{observer_allowed}")

        same_main = self.root / "same-main-allowed"
        same_main.write_text(
            observer_allowed.read_text(encoding="ascii").replace(
                "claw-load-observer", "claw-workbench-e2e",
            ),
            encoding="ascii",
        )
        with self.assertRaisesRegex(ConfigError, "different Ed25519 keys"):
            validate_observer_verifier(f"file:{observer_allowed}", f"file:{same_main}")

    def test_load_runner_contract_rejects_observer_private_key(self) -> None:
        config = json.loads((E2E_ROOT / "config.example.json").read_text(encoding="utf-8"))
        config["load"]["external_evidence_signing_key_file"] = "file:/run/secrets/observer-key"
        with self.assertRaisesRegex(ConfigError, "must never contain"):
            validate_config_contract(config)


if __name__ == "__main__":
    unittest.main()
