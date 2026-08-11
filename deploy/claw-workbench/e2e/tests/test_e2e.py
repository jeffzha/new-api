from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import tempfile
import threading
import unittest
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import Mock, patch
from urllib.parse import urlsplit


E2E_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(E2E_DIR))

from e2e_lib import CheckResult, ConfigError, HTTPClient, Redactor, load_config, load_json_fixture, sign_evidence_file, utc_now, validate_config_contract, write_reports  # noqa: E402
from load_sse import (  # noqa: E402
    external_evidence_refs,
    load_acceptance_binding,
    run_one,
    validate_external_evidence,
    wait_for_external_evidence,
)
from load_observer_signature import write_signed_observer_evidence  # noqa: E402
from bounded_websocket import BoundedWebSocket, WebSocketContractError  # noqa: E402
from run_e2e import (  # noqa: E402
    BILLING_IMPORT_REQUIREMENTS,
    BYOK_REQUIREMENTS,
    OAUTH_REQUIREMENTS,
    RETENTION_REQUIREMENTS,
    SANDBOX_REQUIREMENTS,
    SANDBOX_ENABLED_REQUIREMENTS,
    SCHEDULED_REQUIREMENTS,
    SELECTOR_REQUIREMENTS,
    Runner,
    acceptance_preflight_errors,
    closed_phase_semantic_errors,
    requirement_semantic_errors,
    release_manifest_errors,
    verify_structured_sse_terminal,
)


EICAR = b"X5O!P%@AP[4\\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*"


class FakeHandler(BaseHTTPRequestHandler):
    entry_used = False
    sso_used = False
    turn_posts = 0
    reconnect_last_event_id = ""
    sandbox_id = "sandbox-secret"
    sandbox_stop_attempted = False
    sandbox_csrf_failures = 0
    sandbox_file = b""
    sandbox_status = "running"
    selection_required = False
    selection_choose_count = 0
    sandbox_drop_capture_once = False
    load_mode = "structured_completed"
    reconnect_mode = "structured_completed"

    def log_message(self, _format, *_args):
        pass

    def send_json(self, status: int, value: dict):
        body = json.dumps(value).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def valid_workbench_csrf(self) -> bool:
        expected = "workbench-csrf-secret-value-1234567890"
        cookie = self.headers.get("Cookie", "")
        valid = self.headers.get("X-Workbench-CSRF") == expected and f"claw_workbench_csrf={expected}" in cookie
        if not valid:
            FakeHandler.sandbox_csrf_failures += 1
            self.send_json(403, {"error": "workbench CSRF validation failed"})
        return valid

    def do_POST(self):
        parsed = urlsplit(self.path)
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        if parsed.path == "/api/workbench/session-ticket":
            self.send_json(200, {"data": {"ticket": "entry-secret-value"}})
        elif parsed.path == "/api/workbench/selections/choose":
            FakeHandler.selection_choose_count += 1
            request = json.loads(body)
            if request.get("selection_token") != "selection-secret-value":
                self.send_json(403, {"error": "invalid selection"})
            elif FakeHandler.selection_choose_count > 1:
                self.send_json(409, {"error": "selection already consumed"})
            else:
                self.send_json(200, {"data": {"redirect_url": "/workbench/auth/sso?ticket=sso-secret-value"}})
        elif parsed.path == "/api/workbench/agent-store/dynamic-claw/launch":
            self.send_json(200, {
                "success": True,
                "data": {
                    "redirect_url": "/workbench/auth/sso?ticket=agent-store-sso-secret",
                    "expires_at": "2026-08-11T00:00:00Z",
                },
            })
        elif parsed.path == "/workbench/load/message":
            request = json.loads(body)
            request_id = request.get("ClientRequestId")
            frames = [
                {
                    "Type": "workbench.turn",
                    "TurnId": "load-turn",
                    "ClientRequestId": request_id,
                    "Status": "submitted",
                },
                {
                    "Type": "response.output_text.delta",
                    "Delta": "ordinary model text containing the quoted word \"completed\"",
                },
            ]
            if FakeHandler.load_mode == "structured_completed":
                frames.append({"Type": "workbench.turn_status", "TurnId": "load-turn", "Status": "completed"})
            elif FakeHandler.load_mode == "structured_failed":
                frames.append({"Type": "workbench.turn_status", "TurnId": "load-turn", "Status": "failed_after_accept"})
            elif FakeHandler.load_mode == "wrong_turn_terminal":
                frames.append({"Type": "workbench.turn_status", "TurnId": "another-turn", "Status": "completed"})
            data = "".join(f"data: {json.dumps(frame)}\n\n" for frame in frames).encode()
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
        elif parsed.path == "/workbench/chat/message":
            FakeHandler.turn_posts += 1
            request = json.loads(body)
            if "ClientRequestId" not in request:
                self.send_json(400, {"error": "missing id"})
                return
            data = (
                f'id: 1\ndata: {{"Type":"workbench.turn","TurnId":"turn-secret","ClientRequestId":"{request["ClientRequestId"]}","ConversationId":"conversation-secret"}}\n\n'
                'id: 2\ndata: {"Type":"response.output_text.delta","Delta":"hello"}\n\n'
            ).encode()
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
        elif parsed.path == "/workbench/sandbox":
            if not self.valid_workbench_csrf():
                return
            request = json.loads(body)
            FakeHandler.sandbox_status = "running"
            if FakeHandler.sandbox_drop_capture_once:
                FakeHandler.sandbox_drop_capture_once = False
                self.send_json(201, {
                    "conversation_id": request.get("conversation_id"),
                    "status": "running",
                })
                return
            self.send_json(201, {
                "sandbox_id": FakeHandler.sandbox_id,
                "conversation_id": request.get("conversation_id"),
                "status": "running",
            })
        elif parsed.path == f"/workbench/sandbox/{FakeHandler.sandbox_id}/shell":
            if not self.valid_workbench_csrf():
                return
            self.send_json(200, {"stdout": "claw sandbox E2E", "stderr": "", "exit_code": 0})
        elif parsed.path == f"/workbench/sandbox/{FakeHandler.sandbox_id}/shell/stream":
            if not self.valid_workbench_csrf():
                return
            self.send_json(200, {"type": "stdout", "data": "claw sandbox E2E", "exit_code": 0})
        elif parsed.path in {
            f"/workbench/sandbox/{FakeHandler.sandbox_id}/code",
            f"/workbench/sandbox/{FakeHandler.sandbox_id}/pty",
        }:
            if not self.valid_workbench_csrf():
                return
            self.send_json(503, {"error": "provider_contract_unavailable"})
        elif parsed.path in {
            f"/workbench/sandbox/{FakeHandler.sandbox_id}/pause",
            f"/workbench/sandbox/{FakeHandler.sandbox_id}/resume",
            f"/workbench/sandbox/{FakeHandler.sandbox_id}/stop",
        }:
            if not self.valid_workbench_csrf():
                return
            if parsed.path.endswith("/stop"):
                FakeHandler.sandbox_stop_attempted = True
            action = parsed.path.rsplit("/", 1)[-1]
            FakeHandler.sandbox_status = {"pause": "paused", "resume": "running", "stop": "stopped"}[action]
            self.send_json(200, {"sandbox_id": FakeHandler.sandbox_id, "status": FakeHandler.sandbox_status})
        elif parsed.path == "/workbench/file/upload":
            self.send_json(422, {"error": "malware rejected"})
        else:
            self.send_json(404, {"error": "not found"})

    def do_GET(self):
        parsed = urlsplit(self.path)
        if parsed.path == "/playground/legacy":
            self.send_json(200, {"ok": True})
        elif parsed.path == "/api/workbench/entry":
            if FakeHandler.entry_used:
                self.send_json(410, {"error": "used"})
                return
            FakeHandler.entry_used = True
            self.send_response(302)
            self.send_header("Location", "/playground/select" if FakeHandler.selection_required else "/workbench/auth/sso?ticket=sso-secret-value")
            self.send_header("Set-Cookie", "claw_control_session=edge-secret; Path=/; HttpOnly")
            self.end_headers()
        elif parsed.path == "/workbench/auth/sso":
            if FakeHandler.sso_used:
                self.send_json(410, {"error": "used"})
                return
            FakeHandler.sso_used = True
            self.send_response(302)
            self.send_header("Location", "/workbench/")
            self.send_header("Set-Cookie", "adp_session=adp-secret; Path=/workbench; HttpOnly")
            self.send_header("Set-Cookie", "claw_workbench_csrf=workbench-csrf-secret-value-1234567890; Path=/workbench; Secure; SameSite=Strict")
            self.end_headers()
        elif parsed.path == "/workbench/":
            self.send_json(200, {"ok": True})
        elif parsed.path == "/workbench/account/info":
            self.send_json(200, {"data": {"UserId": "user-secret", "AgentId": "agent-secret"}})
        elif parsed.path == "/workbench/application/list":
            self.send_json(200, {"data": [{"ApplicationId": "app-secret"}]})
        elif parsed.path == "/api/workbench/selections":
            self.send_json(200, {"data": [{
                "selection_token": "selection-secret-value",
                "customer_code": "acceptance-customer",
                "app_selector": "primary",
            }]})
        elif parsed.path == "/workbench/sandbox":
            self.send_json(200, {
                "sandbox_id": FakeHandler.sandbox_id,
                "conversation_id": "conversation-secret",
                "status": FakeHandler.sandbox_status,
            })
        elif parsed.path == "/workbench/chat/turn/events":
            FakeHandler.reconnect_last_event_id = self.headers.get("Last-Event-ID", "")
            if FakeHandler.reconnect_mode == "structured_completed":
                data = 'id: 3\ndata: {"Type":"workbench.turn_status","TurnId":"turn-secret","Status":"completed"}\n\n'.encode()
            elif FakeHandler.reconnect_mode == "model_text_only":
                data = 'id: 3\ndata: {"Type":"response.output_text.delta","Delta":"model says completed"}\n\n'.encode()
            else:
                data = 'id: 3\ndata: {"Type":"workbench.turn_status","TurnId":"other-turn","Status":"completed"}\n\n'.encode()
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
        elif parsed.path == "/workbench/chat/messages":
            self.send_json(200, {"data": []})
        elif parsed.path == "/workbench/sandbox/config":
            self.send_json(200, {
                "sandbox_enabled": True,
                "shell_enabled": True,
                "files_enabled": True,
                "code_execution_enabled": False,
                "pty_enabled": False,
            })
        elif parsed.path == f"/workbench/sandbox/{FakeHandler.sandbox_id}":
            self.send_json(200, {"sandbox_id": FakeHandler.sandbox_id, "status": FakeHandler.sandbox_status})
        elif parsed.path == f"/workbench/sandbox/{FakeHandler.sandbox_id}/files":
            body = FakeHandler.sandbox_file
            self.send_response(200)
            self.send_header("Content-Type", "application/octet-stream")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        elif parsed.path == "/direct":
            self.send_json(403, {"error": "denied"})
        else:
            self.send_json(404, {"error": "not found"})

    def do_PUT(self):
        parsed = urlsplit(self.path)
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        if parsed.path == f"/workbench/sandbox/{FakeHandler.sandbox_id}/files":
            if not self.valid_workbench_csrf():
                return
            FakeHandler.sandbox_file = body
            self.send_json(200, {"written": True})
        else:
            self.send_json(404, {"error": "not found"})


class E2ESelfTest(unittest.TestCase):
    def setUp(self):
        FakeHandler.entry_used = False
        FakeHandler.sso_used = False
        FakeHandler.turn_posts = 0
        FakeHandler.reconnect_last_event_id = ""
        FakeHandler.sandbox_stop_attempted = False
        FakeHandler.sandbox_csrf_failures = 0
        FakeHandler.sandbox_file = b""
        FakeHandler.sandbox_status = "running"
        FakeHandler.selection_required = False
        FakeHandler.selection_choose_count = 0
        FakeHandler.sandbox_drop_capture_once = False
        FakeHandler.load_mode = "structured_completed"
        FakeHandler.reconnect_mode = "structured_completed"
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FakeHandler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.fixture = self.root / "turn.json"
        self.fixture.write_text('{"Prompt":"hello"}', encoding="utf-8")
        self.sandbox_create = self.root / "sandbox-create.json"
        self.sandbox_create.write_text('{"conversation_id":"${conversation_id}","timeout_seconds":60}', encoding="utf-8")
        self.sandbox_shell = self.root / "sandbox-shell.json"
        self.sandbox_shell.write_text('{"conversation_id":"${conversation_id}","command":"printf claw-sandbox-e2e","timeout_seconds":10}', encoding="utf-8")
        self.sandbox_file = self.root / "sandbox-file.json"
        self.sandbox_file.write_text('{"message":"claw sandbox E2E"}', encoding="utf-8")
        self.sandbox_lifecycle = self.root / "sandbox-lifecycle.json"
        self.sandbox_lifecycle.write_text('{"conversation_id":"${conversation_id}"}', encoding="utf-8")
        self.sandbox_code = self.root / "sandbox-code.json"
        self.sandbox_code.write_text('{"conversation_id":"${conversation_id}","language":"python","code":"print(1)","timeout_seconds":10}', encoding="utf-8")
        self.eicar = self.root / "eicar.txt"
        self.previous = os.environ.get("CLAW_TEST_USER_COOKIE")
        os.environ["CLAW_TEST_USER_COOKIE"] = "new_api_session=initial-session-secret"

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)
        self.temp.cleanup()
        if self.previous is None:
            os.environ.pop("CLAW_TEST_USER_COOKIE", None)
        else:
            os.environ["CLAW_TEST_USER_COOKIE"] = self.previous

    def config(self):
        base = f"http://127.0.0.1:{self.server.server_port}"
        path = self.root / "config.json"
        path.write_text(json.dumps({
            "version": 1,
            "base_url": base,
            "direct_adp_base_url": base,
            "secrets": {"user_a_session": "env:CLAW_TEST_USER_COOKIE"},
            "turn": {"request_fixture": f"file:{self.fixture}", "disconnect_after_events": 2},
            "eicar": {"file_ref": f"file:{self.eicar}"},
            "direct_adp_paths": ["/direct"],
        }), encoding="utf-8")
        return load_config(path, allow_http=True)

    def test_live_sso_replay_turn_resume_eicar_and_redaction(self):
        args = argparse.Namespace(execute=True, allow_mutations=True, allow_eicar=True, allow_provider_cost=True)
        runner = Runner(self.config(), args)
        self.assertTrue(runner.bootstrap("user_a"))
        self.assertTrue(runner.identity("user_a"))
        runner.minimal_turn()
        safe_eicar_file = Mock()
        safe_eicar_file.is_absolute.return_value = True
        safe_eicar_file.is_symlink.return_value = False
        safe_eicar_file.is_file.return_value = True
        safe_eicar_file.stat.return_value.st_size = 68
        safe_eicar_file.read_bytes.return_value = EICAR
        # Do not place the real EICAR signature on the developer workstation;
        # Windows Defender may quarantine it before the fake-server test runs.
        with patch("run_e2e.Path", return_value=safe_eicar_file):
            runner.eicar()
        runner.direct_adp()
        self.assertEqual(FakeHandler.turn_posts, 1)
        self.assertEqual(FakeHandler.reconnect_last_event_id, "2")
        by_name = {result.name: result for result in runner.results}
        self.assertEqual(by_name["user_a entry ticket replay rejected"].status, "pass")
        self.assertEqual(by_name["user_a ADP SSO replay rejected"].status, "pass")
        self.assertEqual(by_name["one POST plus Last-Event-ID reconnect"].status, "pass")
        self.assertEqual(by_name["malware upload rejected"].status, "pass", by_name["malware upload rejected"].message)
        report = self.root / "report"
        write_reports(report, run_id="test", started_at="start", finished_at="finish", target=runner.config["base_url"], mode="self-test", permissions={"provider_cost": True, "mutations": True, "eicar": True}, results=runner.results, redactor=runner.redactor)
        output = "\n".join(item.read_text(encoding="utf-8") for item in report.iterdir())
        for secret in ("initial-session-secret", "entry-secret-value", "sso-secret-value", "turn-secret", "conversation-secret", "user-secret", "agent-secret", "app-secret"):
            self.assertNotIn(secret, output)

    def test_minimal_turn_does_not_accept_model_text_as_terminal(self):
        args = argparse.Namespace(execute=True, allow_mutations=True, allow_eicar=False, allow_provider_cost=True)
        runner = Runner(self.config(), args)
        self.assertTrue(runner.bootstrap("user_a"))
        self.assertTrue(runner.identity("user_a"))
        FakeHandler.reconnect_mode = "model_text_only"
        runner.minimal_turn()
        result = next(item for item in runner.results if item.name == "one POST plus Last-Event-ID reconnect")
        self.assertEqual(result.status, "fail")
        self.assertFalse(result.evidence["terminal_seen"])

    def test_agent_store_launch_consumes_same_origin_sso_and_rejects_replay(self):
        args = argparse.Namespace(execute=True, allow_mutations=True, allow_eicar=False, allow_provider_cost=True)
        runner = Runner(self.config(), args)
        self.assertTrue(runner.bootstrap("user_a"))
        FakeHandler.sso_used = False
        passed = runner.generic_request("agent-store", {
            "name": "launch dynamic Claw",
            "requirement": "profile_claw_dynamic_v2",
            "actor": "user_a",
            "method": "POST",
            "path": "/api/workbench/agent-store/dynamic-claw/launch",
            "expect_status": [200],
            "consume_launch_redirect": True,
            "json_equals": {"/success": True},
            "json_types": {"/data/redirect_url": "string"},
            "json_absent": ["/data/app_id", "/data/app_key", "/data/customer_id", "/data/selection_token"],
        })
        self.assertTrue(passed)
        evidence = runner.results[-1].evidence
        self.assertTrue(evidence["launch_sso_replay_rejected"])
        self.assertTrue(evidence["launch_workbench_ready"])

    def sandbox_checks(self):
        return [
            {
                "name": "sandbox config",
                "actor": "user_a",
                "method": "GET",
                "path": "/workbench/sandbox/config",
                "expect_status": [200],
                "require_response_markers": ['"sandbox_enabled": true', '"code_execution_enabled": false'],
            },
            {
                "name": "sandbox create",
                "actor": "user_a",
                "method": "POST",
                "path": "/workbench/sandbox",
                "body_fixture": f"file:{self.sandbox_create}",
                "expect_status": [201],
                "capture": {"sandbox_id": "/sandbox_id"},
            },
            {
                "name": "sandbox query",
                "actor": "user_a",
                "method": "GET",
                "path": "/workbench/sandbox/${sandbox_id}?conversation_id=${conversation_id}",
                "expect_status": [200],
                "poll": {"pointer": "/status", "equals": "running", "forbid_values": ["failed", "provider_unknown"], "timeout_seconds": 1, "interval_seconds": 0.1},
            },
            {
                "name": "sandbox shell",
                "actor": "user_a",
                "method": "POST",
                "path": "/workbench/sandbox/${sandbox_id}/shell",
                "body_fixture": f"file:{self.sandbox_shell}",
                "expect_status": [200],
                "require_response_markers": ['"exit_code": 0'],
            },
            {
                "name": "sandbox shell stream",
                "actor": "user_a",
                "method": "POST",
                "path": "/workbench/sandbox/${sandbox_id}/shell/stream",
                "body_fixture": f"file:{self.sandbox_shell}",
                "expect_status": [200],
                "require_response_markers": ['"exit_code": 0'],
            },
            {
                "name": "sandbox file write",
                "actor": "user_a",
                "method": "PUT",
                "path": "/workbench/sandbox/${sandbox_id}/files?conversation_id=${conversation_id}&path=acceptance/result.json",
                "body_fixture": f"file:{self.sandbox_file}",
                "expect_status": [200],
            },
            {
                "name": "sandbox file read",
                "actor": "user_a",
                "method": "GET",
                "path": "/workbench/sandbox/${sandbox_id}/files?conversation_id=${conversation_id}&path=acceptance/result.json",
                "expect_status": [200],
                "require_response_markers": ["claw sandbox E2E"],
            },
            {
                "name": "sandbox code disabled",
                "actor": "user_a",
                "method": "POST",
                "path": "/workbench/sandbox/${sandbox_id}/code",
                "body_fixture": f"file:{self.sandbox_code}",
                "expect_status": [503],
                "require_response_markers": ["provider_contract_unavailable"],
            },
            {
                "name": "sandbox PTY disabled",
                "actor": "user_a",
                "method": "POST",
                "path": "/workbench/sandbox/${sandbox_id}/pty",
                "body_fixture": f"file:{self.sandbox_lifecycle}",
                "expect_status": [503],
                "require_response_markers": ["provider_contract_unavailable"],
            },
            *[
                item
                for action, expected_status in (("pause", "paused"), ("resume", "running"), ("stop", "stopped"))
                for item in (
                    {
                        "name": f"sandbox {action}",
                        "actor": "user_a",
                        "method": "POST",
                        "path": f"/workbench/sandbox/${{sandbox_id}}/{action}",
                        "body_fixture": f"file:{self.sandbox_lifecycle}",
                        "expect_status": [200],
                        "cleanup": action == "stop",
                    },
                    {
                        "name": f"sandbox {action} confirmed",
                        "actor": "user_a",
                        "method": "GET",
                        "path": "/workbench/sandbox/${sandbox_id}?conversation_id=${conversation_id}",
                        "expect_status": [200],
                        "poll": {"pointer": "/status", "equals": expected_status, "timeout_seconds": 1, "interval_seconds": 0.1},
                        "cleanup": action == "stop",
                    },
                )
            ],
        ]

    def test_managed_sandbox_acceptance_captures_ids_sends_csrf_and_cleans_up(self):
        args = argparse.Namespace(execute=True, allow_mutations=True, allow_eicar=False, allow_provider_cost=True)
        runner = Runner(self.config(), args)
        self.assertTrue(runner.bootstrap("user_a"))
        self.assertTrue(runner.identity("user_a"))
        runner.minimal_turn()
        self.assertEqual(runner.variables["conversation_id"], "conversation-secret")
        runner.fixture_phase(
            "19.3.11b managed sandbox",
            "managed sandbox",
            self.sandbox_checks(),
            mutations=True,
            provider_cost=True,
        )
        self.assertEqual(runner.variables["sandbox_id"], FakeHandler.sandbox_id)
        self.assertEqual(FakeHandler.sandbox_csrf_failures, 0)
        self.assertTrue(FakeHandler.sandbox_stop_attempted)
        sandbox_results = [item for item in runner.results if item.phase == "19.3.11b managed sandbox"]
        self.assertTrue(sandbox_results)
        self.assertTrue(all(item.status == "pass" for item in sandbox_results), sandbox_results)

        report = self.root / "sandbox-report"
        write_reports(report, run_id="sandbox", started_at="start", finished_at="finish", target=runner.config["base_url"], mode="self-test", permissions={"provider_cost": True, "mutations": True, "eicar": False}, results=runner.results, redactor=runner.redactor)
        output = "\n".join(item.read_text(encoding="utf-8") for item in report.iterdir())
        for secret in ("conversation-secret", FakeHandler.sandbox_id, "workbench-csrf-secret-value-1234567890"):
            self.assertNotIn(secret, output)

    def test_multi_context_entry_requires_exact_server_option_and_rejects_replay(self):
        FakeHandler.selection_required = True
        config = self.config()
        config["selection_targets"] = {
            "user_a": {"customer_code": "acceptance-customer", "app_selector": "primary"}
        }
        args = argparse.Namespace(execute=True, allow_mutations=False, allow_eicar=False, allow_provider_cost=False)
        runner = Runner(config, args)
        self.assertTrue(runner.bootstrap("user_a"))
        self.assertEqual(FakeHandler.selection_choose_count, 2)
        by_name = {result.name: result for result in runner.results}
        self.assertEqual(by_name["user_a context selection token replay rejected"].status, "pass")

    def test_managed_sandbox_phase_attempts_stop_after_middle_failure(self):
        args = argparse.Namespace(execute=True, allow_mutations=True, allow_eicar=False, allow_provider_cost=True)
        runner = Runner(self.config(), args)
        self.assertTrue(runner.bootstrap("user_a"))
        self.assertTrue(runner.identity("user_a"))
        runner.minimal_turn()
        checks = self.sandbox_checks()
        checks[3]["expect_status"] = [418]
        runner.fixture_phase("sandbox cleanup", "sandbox cleanup", checks, mutations=True, provider_cost=True)
        by_name = {result.name: result for result in runner.results}
        self.assertEqual(by_name["sandbox shell"].status, "fail")
        self.assertEqual(by_name["sandbox stop"].status, "pass")
        self.assertTrue(FakeHandler.sandbox_stop_attempted)

    def test_safe_fixture_rejects_secret_like_fields(self):
        fixture = self.root / "unsafe.json"
        fixture.write_text('{"Authorization":"Bearer should-not-be-here"}', encoding="utf-8")
        with self.assertRaises(ConfigError):
            load_json_fixture(f"file:{fixture}", {})
        fixture.write_text('{"model":"safe-model","max_tokens":8}', encoding="utf-8")
        self.assertEqual(load_json_fixture(f"file:{fixture}", {})["max_tokens"], 8)
        fixture.write_text('{"credential_profile_id":3,"secret_id_ref":"env://WORKBENCH_PROVIDER_CUSTOMER_A_ID","secret_key_ref":"env://WORKBENCH_PROVIDER_CUSTOMER_A_KEY"}', encoding="utf-8")
        self.assertEqual(load_json_fixture(f"file:{fixture}", {})["credential_profile_id"], 3)
        fixture.write_text('{"secret_key_ref":"raw-secret-must-not-pass"}', encoding="utf-8")
        with self.assertRaises(ConfigError):
            load_json_fixture(f"file:{fixture}", {})

    def test_required_live_phase_cannot_succeed_by_skipping_authorization(self):
        args = argparse.Namespace(execute=True, allow_mutations=False, allow_eicar=False, allow_provider_cost=False)
        runner = Runner(self.config(), args)
        runner.fixture_phase(
            "required",
            "required mutation",
            [{"name": "must run", "actor": "user_a", "method": "POST", "path": "/mutation", "expect_status": [200]}],
            mutations=True,
            required=True,
        )
        self.assertEqual(runner.results[-1].status, "blocker")

    def test_requirement_coverage_and_typed_assertions_are_fail_closed(self):
        dry_args = argparse.Namespace(execute=False, allow_mutations=False, allow_eicar=False, allow_provider_cost=False)
        runner = Runner(self.config(), dry_args)
        runner.fixture_phase(
            "coverage",
            "coverage",
            [{"name": "only one", "requirement": "first", "actor": "anonymous", "method": "GET", "path": "/", "expect_status": [200]}],
            requirements={"first", "second"},
        )
        self.assertEqual(runner.results[0].status, "blocker")
        self.assertEqual(runner.results[0].evidence["missing_requirements"], ["second"])

        live_args = argparse.Namespace(execute=True, allow_mutations=False, allow_eicar=False, allow_provider_cost=False)
        runner = Runner(self.config(), live_args)
        self.assertTrue(runner.bootstrap("user_a"))
        ok = runner.generic_request("typed", {
            "name": "typed response",
            "requirement": "capability_contract",
            "actor": "user_a",
            "method": "GET",
            "path": "/workbench/sandbox/config",
            "expect_status": [200],
            "json_equals": {"/sandbox_enabled": True, "/code_execution_enabled": False},
            "json_not_equals": {"/shell_enabled": False},
            "json_absent": ["/provider_locator", "/token"],
            "json_types": {"/sandbox_enabled": "boolean", "/shell_enabled": "boolean"},
            "header_equals": {"Content-Type": "application/json"},
            "capture_sha256": {"sandbox_enabled_hash": "/sandbox_enabled"},
        })
        self.assertTrue(ok)
        self.assertEqual(len(runner.variables["sandbox_enabled_hash"]), 64)
        report = self.root / "requirement-report"
        write_reports(report, run_id="requirements", started_at="start", finished_at="finish", target=runner.config["base_url"], mode="self-test", permissions={"provider_cost": False, "mutations": False, "eicar": False}, results=runner.results, redactor=runner.redactor)
        payload = json.loads((report / "results.json").read_text(encoding="utf-8"))
        self.assertEqual(payload["requirement_summary"]["capability_contract"]["status"], "pass")
        self.assertIn("json_equals", payload["requirement_summary"]["capability_contract"]["assertion_types"])

    def test_release_manifest_rejects_placeholders_and_accepts_bound_artifacts(self):
        args = argparse.Namespace(execute=False, allow_mutations=False, allow_eicar=False, allow_provider_cost=False)
        placeholder = self.config()
        placeholder["release_manifest"] = {
            "new_api_revision": "0" * 40,
            "claw_control_revision": "0" * 40,
            "adp_revision": "0" * 40,
            "new_api_image_digest": "sha256:" + "0" * 64,
            "claw_control_image_digest": "sha256:" + "0" * 64,
            "adp_image_digest": "sha256:" + "0" * 64,
            "config_sha256": "0" * 64,
            "control_migration": "replace-with-control-migration-head",
            "adp_migration": "replace-with-adp-migration-head",
            "caddy_version": "replace-with-caddy-version",
            "active_color": "green",
            "provider_region": "ap-guangzhou",
            "collected_at": "2026-08-10T00:00:00Z",
        }
        runner = Runner(placeholder, args)
        runner.legacy()
        self.assertEqual(runner.results[0].status, "blocker")

        bound = self.config()
        bound["release_manifest"] = {
            "new_api_revision": "1" * 40,
            "claw_control_revision": "2" * 40,
            "adp_revision": "3" * 40,
            "new_api_image_digest": "sha256:" + "4" * 64,
            "claw_control_image_digest": "sha256:" + "5" * 64,
            "adp_image_digest": "sha256:" + "6" * 64,
            "config_sha256": "7" * 64,
            "control_migration": "0015",
            "adp_migration": "20260809",
            "caddy_version": "2.10.0",
            "active_color": "green",
            "provider_region": "ap-guangzhou",
            "collected_at": "2026-08-10T00:00:00Z",
        }
        runner = Runner(bound, args)
        runner.legacy()
        self.assertEqual(runner.results[0].status, "pass")
        stale = {**bound["release_manifest"], "collected_at": "2000-01-01T00:00:00Z"}
        self.assertTrue(any("older than" in error for error in release_manifest_errors(stale, max_age_seconds=1800)))

    def test_runtime_config_contract_rejects_misspelled_assertion(self):
        base = f"http://127.0.0.1:{self.server.server_port}"
        path = self.root / "bad-config.json"
        path.write_text(json.dumps({
            "version": 1,
            "base_url": base,
            "secrets": {"user_a_session": "env:CLAW_TEST_USER_COOKIE"},
            "sandbox_checks": [{
                "name": "misspelled assertion",
                "actor": "user_a",
                "method": "GET",
                "path": "/workbench/sandbox/config",
                "expect_status": [200],
                "json_equal": {"/sandbox_enabled": True},
            }],
        }), encoding="utf-8")
        with self.assertRaisesRegex(ConfigError, "unknown fields: json_equal"):
            load_config(path, allow_http=True)

    def test_runtime_config_cannot_preseed_or_duplicate_captured_evidence(self):
        base = f"http://127.0.0.1:{self.server.server_port}"
        path = self.root / "preseeded-capture.json"
        request = {
            "name": "capture server resource",
            "actor": "user_a",
            "method": "GET",
            "path": "/workbench/resource",
            "expect_status": [200],
            "capture": {"resource_id": "/id"},
        }
        path.write_text(json.dumps({
            "version": 1,
            "base_url": base,
            "secrets": {"user_a_session": "env:CLAW_TEST_USER_COOKIE"},
            "variables": {"resource_id": "caller-controlled"},
            "isolation_checks": [request],
        }), encoding="utf-8")
        with self.assertRaisesRegex(ConfigError, "cannot pre-seed"):
            load_config(path, allow_http=True)

        path.write_text(json.dumps({
            "version": 1,
            "base_url": base,
            "secrets": {"user_a_session": "env:CLAW_TEST_USER_COOKIE"},
            "isolation_checks": [request, {**request, "name": "duplicate capture"}],
        }), encoding="utf-8")
        with self.assertRaisesRegex(ConfigError, "declared more than once"):
            load_config(path, allow_http=True)

    def test_selector_contract_requires_pending_stale_context_and_real_source_capture(self):
        fake_specs = [
            {
                "requirement": "selection_token_stale", "actor": "user_a_context_1",
                "method": "POST", "path": "/api/workbench/selections/choose",
                "expect_status": [409], "selection_token_source": "option",
                "selection_token_actor": "user_a_context_1",
                "selection_target": {"app_selector": "primary"},
            },
            {
                "requirement": "cross_context_idor", "actor": "user_a_context_2",
                "method": "GET", "path": "/workbench/resources/${context_1_resource_id}",
                "expect_status": [404],
            },
        ]
        errors = requirement_semantic_errors(
            "selector", fake_specs, {"selection_token_stale", "cross_context_idor"}
        )
        self.assertTrue(any("stale selection token rejection" in error or "invalidation" in error for error in errors))
        self.assertTrue(any("source-context Conversation creation" in error for error in errors))

        set_default = self.root / "set-default.json"
        set_default.write_text(
            '{"expected_target_version":2,"expected_current_default_version":3}',
            encoding="utf-8",
        )
        restore_default = self.root / "restore-default.json"
        restore_default.write_text(
            '{"expected_target_version":4,"expected_current_default_version":5}',
            encoding="utf-8",
        )
        create_conversation = self.root / "create-conversation.json"
        create_conversation.write_text('{"Title":"IDOR acceptance"}', encoding="utf-8")
        describe_conversation = self.root / "describe-conversation.json"
        describe_conversation.write_text(
            '{"ConversationId":"${context_1_resource_id}"}', encoding="utf-8"
        )
        valid_specs = [
            {
                "requirement": "selection_token_stale", "actor": "admin",
                "method": "POST", "path": "/api/admin/workbench/customers/${customer_id}/apps/secondary/default",
                "body_fixture": f"file:{set_default}", "expect_status": [200],
                "json_equals": {"/data/selector": "secondary", "/data/slot": "primary"},
            },
            {
                "requirement": "selection_token_stale", "actor": "user_a_stale_context",
                "method": "POST", "path": "/api/workbench/selections/choose",
                "expect_status": [403], "selection_token_source": "option",
                "selection_token_actor": "user_a_stale_context",
                "selection_target": {"app_selector": "secondary"},
                "require_response_markers": ["selection token context is stale"],
            },
            {
                "requirement": "selection_token_stale", "actor": "admin",
                "method": "POST", "path": "/api/admin/workbench/customers/${customer_id}/apps/primary/default",
                "body_fixture": f"file:{restore_default}", "expect_status": [200],
                "json_equals": {"/data/selector": "primary", "/data/slot": "primary"},
                "cleanup": True,
            },
            {
                "requirement": "cross_context_idor", "actor": "user_a_context_1",
                "method": "POST", "path": "/workbench/adp/CreateConversation",
                "body_fixture": f"file:{create_conversation}",
                "expect_status": [200],
                "capture": {"context_1_resource_id": "/Response/ConversationId"},
            },
            {
                "requirement": "cross_context_idor", "actor": "user_a_context_1",
                "method": "POST", "path": "/workbench/adp/DescribeConversation",
                "body_fixture": f"file:{describe_conversation}",
                "expect_status": [200],
                "json_equals": {"/Response/ConversationId": "${context_1_resource_id}"},
            },
            {
                "requirement": "cross_context_idor", "actor": "user_a_context_2",
                "method": "POST", "path": "/workbench/adp/DescribeConversation",
                "body_fixture": f"file:{describe_conversation}",
                "expect_status": [404],
            },
            {
                "requirement": "cross_context_idor", "actor": "user_a_context_1",
                "method": "POST", "path": "/workbench/chat/conversation/delete",
                "body_fixture": f"file:{describe_conversation}",
                "expect_status": [200], "json_equals": {"/Success": 1}, "cleanup": True,
            },
        ]
        self.assertEqual(
            requirement_semantic_errors(
                "selector", valid_specs, {"selection_token_stale", "cross_context_idor"}
            ),
            [],
        )

    def test_requirement_matrix_records_early_blocker(self):
        args = argparse.Namespace(execute=True, allow_mutations=False, allow_eicar=False, allow_provider_cost=False)
        runner = Runner(self.config(), args)
        runner.generic_request("early", {
            "name": "missing required actor",
            "requirement": "capability_contract",
            "actor": "user_b_other",
            "method": "GET",
            "path": "/workbench/sandbox/config",
            "expect_status": [200],
            "json_equals": {"/sandbox_enabled": True},
        })
        report = self.root / "early-blocker-report"
        write_reports(
            report,
            run_id="early-blocker",
            started_at="start",
            finished_at="finish",
            target=runner.config["base_url"],
            mode="self-test",
            permissions={"provider_cost": False, "mutations": False, "eicar": False},
            results=runner.results,
            redactor=runner.redactor,
        )
        payload = json.loads((report / "results.json").read_text(encoding="utf-8"))
        self.assertEqual(payload["requirement_summary"]["capability_contract"]["status"], "blocker")

    def test_live_preflight_rejects_incomplete_matrix_before_runner_network(self):
        errors = acceptance_preflight_errors(self.config())
        self.assertTrue(any(error.startswith("selector_checks:") for error in errors))
        self.assertTrue(any(error.startswith("sandbox_checks:") for error in errors))
        self.assertTrue(any(error.startswith("agent_store_checks:") for error in errors))
        self.assertTrue(any(error.startswith("app_migration_checks:") for error in errors))
        self.assertTrue(any(error.startswith("retention_checks:") for error in errors))
        self.assertTrue(any(error.startswith("integration_execution_mode:") for error in errors))
        self.assertTrue(any("sandbox_acceptance_token" in error for error in errors))

    def test_enabled_integration_mode_rejects_blocked_contract_masquerade(self):
        config = self.config()
        config["integration_execution_mode"] = "enabled"
        config["integration_execution_checks"] = [{
            "name": "blocked response cannot pass enabled mode",
            "requirement": "dependent_turn_blocked",
            "actor": "user_a",
            "method": "POST",
            "path": "/workbench/chat/message",
            "body_fixture": f"file:{self.fixture}",
            "expect_status": [503],
            "require_response_markers": ["execution blocked"],
        }]
        errors = acceptance_preflight_errors(config)
        integration_errors = [error for error in errors if error.startswith("integration_execution_checks:")]
        self.assertTrue(any("dependent_turn_completed" in error for error in integration_errors))
        self.assertTrue(any("unknown requirements dependent_turn_blocked" in error for error in integration_errors))

    def test_structured_sse_terminal_requires_bound_turn_and_exact_terminal(self):
        accepted = (
            'data: {"Type":"workbench.turn","TurnId":"turn-1","ClientRequestId":"request-1"}\n\n'
            'data: {"Type":"response.output_text.delta","Delta":"completed"}\n\n'
            'data: {"Type":"workbench.turn_status","TurnId":"turn-1","Status":"completed"}\n\n'
        ).encode()
        ok, evidence = verify_structured_sse_terminal(accepted, "request-1")
        self.assertTrue(ok)
        self.assertTrue(evidence["terminal_seen"])
        wrong_turn = accepted.replace(b'"TurnId":"turn-1","Status"', b'"TurnId":"turn-2","Status"')
        ok, evidence = verify_structured_sse_terminal(wrong_turn, "request-1")
        self.assertFalse(ok)
        self.assertFalse(evidence["terminal_seen"])
        model_text_only = (
            'data: {"Type":"workbench.turn","TurnId":"turn-1","ClientRequestId":"request-1"}\n\n'
            'data: {"Type":"response.output_text.delta","Delta":"completed"}\n\n'
        ).encode()
        self.assertFalse(verify_structured_sse_terminal(model_text_only, "request-1")[0])

    def test_runtime_config_accepts_only_exact_completed_sse_contract(self):
        base = f"http://127.0.0.1:{self.server.server_port}"
        turn = self.root / "integration-turn.json"
        turn.write_text('{"ClientRequestId":"request-1","Prompt":"hello"}', encoding="utf-8")
        value = {
            "version": 1,
            "base_url": base,
            "secrets": {"user_a_session": "env:CLAW_TEST_USER_COOKIE"},
            "integration_execution_mode": "enabled",
            "integration_execution_checks": [{
                "name": "real dependent Turn",
                "requirement": "dependent_turn_completed",
                "actor": "user_a",
                "method": "POST",
                "path": "/workbench/chat/message",
                "body_fixture": f"file:{turn}",
                "expect_status": [200],
                "sse_terminal": {"client_request_id_pointer": "/ClientRequestId", "status": "completed"},
            }],
        }
        path = self.root / "sse-contract.json"
        path.write_text(json.dumps(value), encoding="utf-8")
        load_config(path, allow_http=True)
        value["integration_execution_checks"][0]["sse_terminal"]["status"] = "failed_after_accept"
        path.write_text(json.dumps(value), encoding="utf-8")
        with self.assertRaisesRegex(ConfigError, "exact completed status"):
            load_config(path, allow_http=True)

    def test_new_non_http_requirements_can_only_be_satisfied_by_signed_observer_keys(self):
        observer_keys = {
            "agent_store.non_dynamic_zero_copy",
            "agent_store.dynamic_copy_single_request",
            "agent_store.dynamic_binding_unique",
            "app_migration.lineage_single_activation",
            "retention.delivery_receipt_completed",
            "retention.adp_exact_deletion",
            "retention.cos_exact_deletion",
            "retention.provider_revoked",
            "retention.control_records_preserved",
            "integration_execution.provider_binding_readback",
            "integration_execution.usage_limit_recorded",
        }
        for contract, requirements in (
            ("agent_store", {"non_dynamic_zero_copy", "dynamic_copy_single_request", "dynamic_binding_unique"}),
            ("app_migration", {"lineage_single_activation"}),
            ("retention", RETENTION_REQUIREMENTS.difference({"legal_hold_blocked", "policy_version_race_blocked", "delivery_enqueued"})),
            ("integration_execution", {"provider_binding_readback", "usage_limit_recorded"}),
        ):
            self.assertEqual(
                requirement_semantic_errors(contract, [], requirements, observer_keys=observer_keys),
                [],
            )

    def test_agent_store_profile_requires_one_ordered_launch_turn_history_chain(self):
        detail = {
            "name": "standard profile",
            "requirement": "profile_standard_v2",
            "actor": "admin",
            "method": "GET",
            "path": "/api/admin/workbench/agent-store/items/item-1",
            "expect_status": [200],
            "capture": {
                "standard_v2_slug": "/data/slug",
                "standard_v2_deployment_id": "/data/deployments/0/deployment_id",
            },
            "json_equals": {
                "/data/deployments/0/provider_app_mode": 1,
                "/data/deployments/0/runtime_profile": "standard_v2",
                "/data/deployments/0/execution_enabled": True,
            },
        }
        launch = {
            "name": "launch standard", "requirement": "profile_standard_v2", "actor": "user_a",
            "method": "POST", "path": "/api/workbench/agent-store/${standard_v2_slug}/launch",
            "expect_status": [200], "consume_launch_redirect": True,
            "launch_context_sha256": "standard_v2_app_context_sha256",
            "json_equals": {"/success": True}, "json_types": {"/data/redirect_url": "string"},
            "json_absent": ["/data/app_id", "/data/app_key", "/data/customer_id", "/data/selection_token"],
        }
        turn = {
            "name": "standard turn", "requirement": "profile_standard_v2", "actor": "user_a",
            "method": "POST", "path": "/workbench/chat/message", "expect_status": [200],
            "provider_cost": True,
            "assert_current_app_context_sha256": "standard_v2_app_context_sha256",
            "sse_terminal": {"client_request_id_pointer": "/ClientRequestId", "status": "completed", "capture_conversation_id": "standard_v2_conversation_id"},
        }
        history = {
            "name": "standard history", "requirement": "profile_standard_v2", "actor": "user_a",
            "method": "GET", "path": "/workbench/chat/messages?conversation_id=${standard_v2_conversation_id}",
            "expect_status": [200], "require_response_markers": ["completed"],
            "assert_current_app_context_sha256": "standard_v2_app_context_sha256",
        }
        specs = [detail, launch, turn, history]
        self.assertEqual(requirement_semantic_errors("agent_store", specs, {"profile_standard_v2"}), [])

        split = dict(detail)
        split["json_equals"] = dict(detail["json_equals"])
        split["json_equals"]["/data/deployments/1/runtime_profile"] = split["json_equals"].pop("/data/deployments/0/runtime_profile")
        errors = requirement_semantic_errors("agent_store", [split, launch, turn, history], {"profile_standard_v2"})
        self.assertTrue(any("one deployment array element" in error for error in errors))

        errors = requirement_semantic_errors("agent_store", [detail, turn, history], {"profile_standard_v2"})
        self.assertTrue(any("missing launch bound" in error for error in errors))

        interposed_launch = dict(launch)
        interposed_launch["requirement"] = "profile_claw_dynamic_v2"
        errors = requirement_semantic_errors(
            "agent_store", [detail, launch, interposed_launch, turn, history], {"profile_standard_v2"}
        )
        self.assertTrue(any("one ordered capture chain" in error for error in errors))

    def test_agent_store_disabled_deployment_must_reject_bound_launch(self):
        readback = {
            "name": "disabled readback", "requirement": "disabled_deployment_launch_rejected",
            "actor": "admin", "method": "GET", "path": "/api/admin/workbench/agent-store/items/disabled",
            "expect_status": [200], "capture": {"disabled_profile_slug": "/data/slug"},
            "json_equals": {"/data/deployments/0/execution_enabled": False},
        }
        rejected = {
            "name": "disabled launch", "requirement": "disabled_deployment_launch_rejected",
            "actor": "user_a", "method": "POST",
            "path": "/api/workbench/agent-store/${disabled_profile_slug}/launch", "expect_status": [409],
        }
        self.assertEqual(
            requirement_semantic_errors(
                "agent_store", [readback, rejected], {"disabled_deployment_launch_rejected"}
            ), []
        )

    def test_integration_revocation_readback_must_run_in_cleanup_order(self):
        unbind = self.root / "unbind.json"
        unbind.write_text('{"action":"unbind","kind":"skill","resource_id":"skill-1"}', encoding="utf-8")
        specs = [{
            "name": "unbind",
            "requirement": "revocation_readback",
            "actor": "user_a",
            "method": "POST",
            "path": "/workbench/integrations/bindings",
            "body_fixture": f"file:{unbind}",
            "expect_status": [200],
            "cleanup": True,
        }, {
            "name": "revoked readback",
            "requirement": "revocation_readback",
            "actor": "user_a",
            "method": "GET",
            "path": "/workbench/integrations",
            "expect_status": [200],
            "json_equals": {"/bindings/0/status": "revoked"},
        }]
        errors = requirement_semantic_errors("integration_execution", specs, {"revocation_readback"})
        self.assertTrue(any("post-unbind" in error for error in errors))
        specs[1]["cleanup"] = True
        self.assertEqual(
            requirement_semantic_errors("integration_execution", specs, {"revocation_readback"}),
            [],
        )

    def test_app_migration_prepare_requires_real_created_job(self):
        spec = {
            "name": "label-only prepare",
            "requirement": "migration_prepared",
            "actor": "admin_requester",
            "method": "POST",
            "path": "/api/admin/workbench/customers/1/app-migrations",
            "expect_status": [200],
            "require_response_markers": ["created"],
        }
        errors = requirement_semantic_errors("app_migration", [spec], {"migration_prepared"})
        self.assertTrue(any("captured job" in error for error in errors))

    def test_resource_bounds_evidence_requires_a_large_string_command(self):
        oversized = self.root / "oversized-list.json"
        oversized.write_text(json.dumps({"command": [0] * 65537}), encoding="utf-8")
        errors = requirement_semantic_errors(
            "sandbox",
            [{
                "name": "masquerading list",
                "requirement": "resource_bounds",
                "actor": "user_a",
                "method": "POST",
                "path": "/workbench/sandbox/${sandbox_id}/shell",
                "body_fixture": f"file:{oversized}",
                "body_min_bytes": 65537,
                "fixture_json_min_lengths": {"/command": 65537},
                "expect_status": [400],
                "require_response_markers": ["command_invalid"],
            }],
            {"resource_bounds"},
        )
        self.assertTrue(any("oversized Shell command-field rejection" in error for error in errors))

    def test_cleanup_contract_rejects_anonymous_500_placeholders(self):
        errors = requirement_semantic_errors(
            "sandbox",
            [
                {
                    "name": "fake stop", "requirement": "cleanup_terminal",
                    "actor": "anonymous", "method": "POST",
                    "path": "/workbench/sandbox/${sandbox_id}/stop",
                    "expect_status": [500], "cleanup": True,
                },
                {
                    "name": "fake terminal", "requirement": "cleanup_terminal",
                    "actor": "anonymous", "method": "GET",
                    "path": "/workbench/sandbox/${sandbox_id}?conversation_id=${conversation_id}",
                    "expect_status": [500],
                    "poll": {"pointer": "/status", "equals": "stopped", "forbid_values": ["failed", "provider_unknown"]},
                    "cleanup": True,
                },
            ],
            {"cleanup_terminal"},
        )
        self.assertTrue(any("finally stop mutation" in error for error in errors))
        self.assertTrue(any("finally stopped-state poll" in error for error in errors))

    def test_checked_in_example_satisfies_runtime_contract(self):
        value = json.loads((E2E_DIR / "config.example.json").read_text(encoding="utf-8"))
        validate_config_contract(value)
        value["acceptance_report_allowed_signers_file"] = "file:/run/secrets/e2e-allowed-signers"
        with self.assertRaisesRegex(ConfigError, "separate roles"):
            validate_config_contract(value)

    def test_load_report_must_bind_successful_same_release_acceptance(self):
        config = self.config()
        config["release_manifest"] = {
            "new_api_revision": "1" * 40,
            "claw_control_revision": "2" * 40,
            "adp_revision": "3" * 40,
            "new_api_image_digest": "sha256:" + "4" * 64,
            "claw_control_image_digest": "sha256:" + "5" * 64,
            "adp_image_digest": "sha256:" + "6" * 64,
            "config_sha256": "7" * 64,
            "control_migration": "0015",
            "adp_migration": "20260809",
            "caddy_version": "2.10.0",
            "active_color": "green",
            "provider_region": "ap-guangzhou",
            "collected_at": "2026-08-10T00:00:00Z",
        }
        run_id = str(__import__("uuid").uuid4())
        signing_key = self.root / "acceptance-signing-key"
        subprocess.run(
            ["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(signing_key)],
            check=True,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        public_key = (self.root / "acceptance-signing-key.pub").read_text(encoding="ascii").strip()
        allowed_signers = self.root / "acceptance-allowed-signers"
        allowed_signers.write_text(f"claw-workbench-e2e {public_key}\n", encoding="ascii")
        config["acceptance_report_allowed_signers_file"] = f"file:{allowed_signers}"
        requirements = sorted(set().union(
            SELECTOR_REQUIREMENTS,
            OAUTH_REQUIREMENTS,
            SCHEDULED_REQUIREMENTS,
            SANDBOX_REQUIREMENTS,
            BYOK_REQUIREMENTS,
            BILLING_IMPORT_REQUIREMENTS,
        ))
        results = [
            CheckResult(
                order=index,
                phase="acceptance",
                name=requirement,
                status="pass",
                message="verified",
                endpoint=config["base_url"] + "/workbench/check",
                http_status=200,
                duration_ms=1.0,
                evidence={"requirement_id": requirement, "assertion_types": ["http_status"]},
            )
            for index, requirement in enumerate(requirements, 1)
        ]
        report = self.root / "acceptance-report"
        write_reports(
            report,
            run_id=run_id,
            started_at=utc_now(),
            finished_at=utc_now(),
            target=config["base_url"],
            mode="live",
            permissions={"provider_cost": True, "mutations": True, "eicar": True},
            results=results,
            redactor=Redactor(),
            release_manifest=config["release_manifest"],
        )
        path = report / "results.json"
        sign_evidence_file(path, f"file:{signing_key}")
        self.assertEqual(load_acceptance_binding(str(path), config)[0], run_id)
        signed_bytes = path.read_bytes()
        path.write_text("{}", encoding="utf-8")
        with patch("load_sse.verify_evidence_signature", return_value=signed_bytes):
            self.assertEqual(load_acceptance_binding(str(path), config)[0], run_id)
        path.write_bytes(signed_bytes)

        wrong_identity = self.root / "wrong-identity-allowed-signers"
        wrong_identity.write_text(f"another-runner {public_key}\n", encoding="ascii")
        with self.assertRaisesRegex(ConfigError, "pin claw-workbench-e2e"):
            load_acceptance_binding(
                str(path),
                {**config, "acceptance_report_allowed_signers_file": f"file:{wrong_identity}"},
            )

        other_key = self.root / "other-signing-key"
        subprocess.run(
            ["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(other_key)],
            check=True,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        other_public_key = (self.root / "other-signing-key.pub").read_text(encoding="ascii").strip()
        wrong_signer = self.root / "wrong-signer-allowed-signers"
        wrong_signer.write_text(f"claw-workbench-e2e {other_public_key}\n", encoding="ascii")
        with self.assertRaisesRegex(ConfigError, "signature"):
            load_acceptance_binding(
                str(path),
                {**config, "acceptance_report_allowed_signers_file": f"file:{wrong_signer}"},
            )

        payload = json.loads(path.read_text(encoding="utf-8"))
        payload["summary"]["pass"] += 1
        path.write_text(json.dumps(payload), encoding="utf-8")
        with self.assertRaisesRegex(ConfigError, "signature"):
            load_acceptance_binding(str(path), config)

        mismatched = self.root / "mismatched-report"
        write_reports(
            mismatched,
            run_id=run_id,
            started_at=utc_now(),
            finished_at=utc_now(),
            target=config["base_url"],
            mode="live",
            permissions={"provider_cost": True, "mutations": True, "eicar": True},
            results=results,
            redactor=Redactor(),
            release_manifest={**config["release_manifest"], "active_color": "blue"},
        )
        sign_evidence_file(mismatched / "results.json", f"file:{signing_key}")
        with self.assertRaisesRegex(ConfigError, "release manifest differs"):
            load_acceptance_binding(str(mismatched / "results.json"), config)

        incomplete = self.root / "incomplete-report"
        write_reports(
            incomplete,
            run_id=run_id,
            started_at=utc_now(),
            finished_at=utc_now(),
            target=config["base_url"],
            mode="live",
            permissions={"provider_cost": True, "mutations": True, "eicar": True},
            results=results[:-1],
            redactor=Redactor(),
            release_manifest=config["release_manifest"],
        )
        sign_evidence_file(incomplete / "results.json", f"file:{signing_key}")
        with self.assertRaisesRegex(ConfigError, "requirement matrix is incomplete"):
            load_acceptance_binding(str(incomplete / "results.json"), config)

    def test_release_manifest_can_be_loaded_directly_from_collector_file(self):
        base_config = self.config()
        manifest = {
            "new_api_revision": "1" * 40,
            "claw_control_revision": "2" * 40,
            "adp_revision": "3" * 40,
            "new_api_image_digest": "sha256:" + "4" * 64,
            "claw_control_image_digest": "sha256:" + "5" * 64,
            "adp_image_digest": "sha256:" + "6" * 64,
            "config_sha256": "7" * 64,
            "control_migration": "0015",
            "adp_migration": "20260809",
            "caddy_version": "2.10.0",
            "active_color": "green",
            "provider_region": "ap-guangzhou",
            "collected_at": "2026-08-10T00:00:00Z",
        }
        manifest_path = self.root / "release-manifest.json"
        manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
        config_path = self.root / "manifest-config.json"
        config_path.write_text(json.dumps({
            "version": 1,
            "base_url": base_config["base_url"],
            "secrets": {"user_a_session": "env:CLAW_TEST_USER_COOKIE"},
            "release_manifest": f"file:{manifest_path}",
        }), encoding="utf-8")
        self.assertEqual(load_config(config_path, allow_http=True)["release_manifest"], manifest)

    def test_load_sse_uses_exact_bound_turn_terminal_events(self):
        source = HTTPClient(self.config()["base_url"], timeout=5)

        FakeHandler.load_mode = "content_only"
        content_only = run_one(source, 5, "/workbench/load/message", {"Prompt": "test"})
        self.assertFalse(content_only["completed"])
        self.assertEqual(content_only["terminal_outcome"], "none")

        FakeHandler.load_mode = "wrong_turn_terminal"
        wrong_turn = run_one(source, 5, "/workbench/load/message", {"Prompt": "test"})
        self.assertFalse(wrong_turn["completed"])
        self.assertEqual(wrong_turn["error_type"], "RuntimeError")

        FakeHandler.load_mode = "structured_failed"
        failed = run_one(source, 5, "/workbench/load/message", {"Prompt": "test"})
        self.assertTrue(failed["completed"])
        self.assertEqual(failed["terminal_outcome"], "failed_after_accept")

        FakeHandler.load_mode = "structured_completed"
        completed = run_one(source, 5, "/workbench/load/message", {"Prompt": "test"})
        self.assertTrue(completed["completed"])
        self.assertEqual(completed["terminal_outcome"], "completed")

    def test_external_load_evidence_is_file_bound_complete_and_fail_closed(self):
        now = datetime.now(timezone.utc)

        def timestamp(value):
            return value.isoformat().replace("+00:00", "Z")

        manifest = {"release": "exact"}
        scenario = {
            "concurrency": 50,
            "planned_requests": 50,
            "completed_requests": 50,
            "successful_requests": 50,
            "window_started_at": timestamp(now - timedelta(seconds=2)),
            "window_finished_at": timestamp(now - timedelta(seconds=1)),
        }
        evidence = {
            "schema_version": 1,
            "collector_id": "claw-load-observer/v1",
            "acceptance_run_id": "acceptance-run",
            "release_manifest": manifest,
            "concurrency": 50,
            "window_started_at": timestamp(now - timedelta(seconds=3)),
            "window_finished_at": timestamp(now),
            "collected_at": timestamp(now),
            "observed_requests": {"planned": 50, "terminal": 50, "successful": 50},
            "metrics": {
                "sample_count": 2,
                "active_connections_peak": {"edge": 50, "new_api": 0, "claw_control": 50, "adp": 50},
                "memory_peak_bytes": {"new_api": 1, "claw_control": 2, "adp": 3},
                "persisted_turn_events": {"before": 100, "after": 200, "delta": 100},
                "db_latency_ms": {"sample_count": 100, "p50": 1.0, "p95": 2.0, "p99": 3.0},
            },
        }
        path = self.root / "load-50.json"
        observer_key = self.root / "observer-key"
        subprocess.run(
            ["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(observer_key)],
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        public_key = observer_key.with_suffix(".pub").read_text(encoding="ascii").strip()
        observer_allowed = self.root / "observer-allowed-signers"
        observer_allowed.write_text(f"claw-load-observer {public_key}\n", encoding="ascii")
        write_signed_observer_evidence(
            path, json.dumps(evidence), f"file:{observer_key}",
        )
        loaded = wait_for_external_evidence(
            f"file:{path}",
            wait_seconds=1,
            allowed_signers_ref=f"file:{observer_allowed}",
            acceptance_run_id="acceptance-run",
            release_manifest=manifest,
            scenario=scenario,
        )
        self.assertEqual(loaded, evidence)
        self.assertEqual(
            external_evidence_refs(
                {"external_evidence": {"50": f"file:{path}", "100": f"file:{self.root / 'load-100.json'}"}},
                [50],
            ),
            {50: f"file:{path}"},
        )

        placeholder = json.loads(json.dumps(evidence))
        placeholder["metrics"]["active_connections_peak"]["edge"] = "not_collected"
        with self.assertRaisesRegex(ConfigError, "placeholder"):
            validate_external_evidence(
                placeholder,
                acceptance_run_id="acceptance-run",
                release_manifest=manifest,
                scenario=scenario,
            )

        wrong_release = {**evidence, "release_manifest": {"release": "other"}}
        with self.assertRaisesRegex(ConfigError, "exact release manifest"):
            validate_external_evidence(
                wrong_release,
                acceptance_run_id="acceptance-run",
                release_manifest=manifest,
                scenario=scenario,
            )

        inconsistent = json.loads(json.dumps(evidence))
        inconsistent["metrics"]["persisted_turn_events"]["delta"] = 99
        with self.assertRaisesRegex(ConfigError, "arithmetically inconsistent"):
            validate_external_evidence(
                inconsistent,
                acceptance_run_id="acceptance-run",
                release_manifest=manifest,
                scenario=scenario,
            )

    def test_json_null_assertions_require_pointer_presence(self):
        args = argparse.Namespace(execute=True, allow_mutations=False, allow_eicar=False, allow_provider_cost=False)
        runner = Runner(self.config(), args)
        self.assertTrue(runner.bootstrap("user_a"))
        ok = runner.generic_request("presence", {
            "name": "missing pointer is not null",
            "actor": "user_a",
            "method": "GET",
            "path": "/workbench/sandbox/config",
            "expect_status": [200],
            "json_equals": {"/missing": None},
        })
        self.assertFalse(ok)
        self.assertEqual(runner.results[-1].evidence["json_mismatch_count"], 1)

    def test_negative_csrf_modes_prove_server_enforcement(self):
        args = argparse.Namespace(execute=True, allow_mutations=True, allow_eicar=False, allow_provider_cost=True)
        runner = Runner(self.config(), args)
        self.assertTrue(runner.bootstrap("user_a"))
        runner.variables.update({"sandbox_id": FakeHandler.sandbox_id, "conversation_id": "conversation-secret"})
        for mode in ("omit", "invalid"):
            with self.subTest(mode=mode):
                ok = runner.generic_request("csrf", {
                    "name": f"csrf {mode}",
                    "actor": "user_a",
                    "method": "POST",
                    "path": "/workbench/sandbox/${sandbox_id}/shell",
                    "body_fixture": f"file:{self.sandbox_shell}",
                    "csrf_mode": mode,
                    "expect_status": [403],
                })
                self.assertTrue(ok)
        self.assertEqual(FakeHandler.sandbox_csrf_failures, 2)

    def test_poll_rejects_forbidden_intermediate_state(self):
        args = argparse.Namespace(execute=True, allow_mutations=False, allow_eicar=False, allow_provider_cost=False)
        runner = Runner(self.config(), args)
        self.assertTrue(runner.bootstrap("user_a"))
        runner.variables.update({"sandbox_id": FakeHandler.sandbox_id, "conversation_id": "conversation-secret"})
        FakeHandler.sandbox_status = "provider_unknown"
        ok = runner.generic_request("poll", {
            "name": "forbidden state",
            "actor": "user_a",
            "method": "GET",
            "path": "/workbench/sandbox/${sandbox_id}?conversation_id=${conversation_id}",
            "expect_status": [200],
            "poll": {"pointer": "/status", "equals": "running", "forbid_values": ["provider_unknown"], "timeout_seconds": 1, "interval_seconds": 0.1},
        })
        self.assertFalse(ok)
        self.assertTrue(runner.results[-1].evidence["poll_forbidden_state_seen"])

    def test_sandbox_requirement_label_cannot_masquerade_as_evidence(self):
        args = argparse.Namespace(execute=False, allow_mutations=False, allow_eicar=False, allow_provider_cost=False)
        runner = Runner(self.config(), args)
        runner.fixture_phase(
            "semantic",
            "semantic",
            [{
                "name": "unrelated success",
                "requirement": "capability_contract",
                "actor": "anonymous",
                "method": "GET",
                "path": "/workbench/sandbox/not-the-config",
                "expect_status": [200],
                "require_response_markers": ["ok"],
            }],
            requirements={"capability_contract"},
            contract="sandbox",
        )
        semantic = [result for result in runner.results if result.name.endswith("semantic contract")]
        self.assertEqual(len(semantic), 1)
        self.assertEqual(semantic[0].status, "blocker")

    def test_sandbox_cleanup_uses_read_only_lookup_after_capture_loss(self):
        args = argparse.Namespace(execute=True, allow_mutations=True, allow_eicar=False, allow_provider_cost=True)
        runner = Runner(self.config(), args)
        self.assertTrue(runner.bootstrap("user_a"))
        runner.variables["conversation_id"] = "conversation-secret"
        FakeHandler.sandbox_drop_capture_once = True
        checks = [
            {
                "name": "sandbox create with lost capture",
                "actor": "user_a",
                "method": "POST",
                "path": "/workbench/sandbox",
                "body_fixture": f"file:{self.sandbox_create}",
                "expect_status": [201],
                "capture": {"sandbox_id": "/sandbox_id"},
            },
            {
                "name": "sandbox stop in finally",
                "actor": "user_a",
                "method": "POST",
                "path": "/workbench/sandbox/${sandbox_id}/stop",
                "body_fixture": f"file:{self.sandbox_lifecycle}",
                "expect_status": [200],
                "cleanup": True,
            },
        ]
        runner.fixture_phase("cleanup recovery", "cleanup recovery", checks, mutations=True, provider_cost=True)
        self.assertEqual(runner.variables["sandbox_id"], FakeHandler.sandbox_id)
        self.assertTrue(FakeHandler.sandbox_stop_attempted)
        self.assertEqual(
            [result.status for result in runner.results if result.name.startswith("cleanup lookup recovery:")],
            ["pass"],
        )

    def test_missing_session_is_a_blocker_not_anonymous_fallback(self):
        config = self.config()
        config["secrets"]["user_a_session"] = "env:CLAW_TEST_MISSING_COOKIE"
        os.environ.pop("CLAW_TEST_MISSING_COOKIE", None)
        args = argparse.Namespace(execute=True, allow_mutations=False, allow_eicar=False, allow_provider_cost=False)
        runner = Runner(config, args)
        self.assertFalse(runner.bootstrap("user_a"))
        self.assertEqual(runner.results[-1].status, "blocker")
        self.assertEqual(FakeHandler.entry_used, False)

    def test_closed_phases_reject_labels_without_semantic_evidence(self):
        specs = [{
            "name": "label only", "requirement": "model_allowlist", "actor": "anonymous",
            "method": "GET", "path": "/healthz", "expect_status": [200],
        }]
        errors = closed_phase_semantic_errors("policy_checks", specs, {"model_allowlist"})
        self.assertTrue(any("machine-verifiable" in error for error in errors))
        self.assertTrue(any("real API path" in error for error in errors))

    def test_observer_requirements_replace_nonexistent_acceptance_routes(self):
        specs = [{
            "name": "task create", "requirement": "lifecycle", "actor": "user_a",
            "method": "POST", "path": "/workbench/scheduled-tasks", "expect_status": [201],
            "capture": {"scheduled_task_id": "/task_id"},
        }]
        errors = requirement_semantic_errors(
            "scheduled", specs, {"worker_failover", "provider_unknown_no_repost"},
            observer_keys={"scheduled.worker_failover", "scheduled.provider_unknown_no_repost"},
        )
        self.assertEqual(errors, [])
        self.assertFalse(any("/acceptance/" in str(spec.get("path")) for spec in specs))

    def test_enabled_sandbox_requires_exact_six_language_success(self):
        specs = []
        for language in ("python", "javascript", "typescript", "java", "r", "bash"):
            fixture = self.root / f"code-{language}.json"
            fixture.write_text(json.dumps({"conversation_id": "${conversation_id}", "language": language, "code": "ok", "timeout_seconds": 10}), encoding="utf-8")
            specs.append({
                "name": language, "requirement": "code_six_languages", "actor": "user_a",
                "method": "POST", "path": "/workbench/sandbox/${sandbox_id}/code",
                "body_fixture": f"file:{fixture}", "expect_status": [200],
                "json_equals": {"/exit_code": 0},
            })
        errors = requirement_semantic_errors("sandbox", specs, {"code_six_languages"})
        self.assertEqual(errors, [])
        specs.pop()
        errors = requirement_semantic_errors("sandbox", specs, {"code_six_languages"})
        self.assertTrue(any("six_languages" in error for error in errors))

    def test_bounded_websocket_rejects_oversized_server_frame(self):
        import socket
        left, right = socket.socketpair()
        try:
            client = BoundedWebSocket(left, timeout=1, max_frame_bytes=8)
            right.sendall(bytes((0x82, 9)) + b"123456789")
            with self.assertRaisesRegex(WebSocketContractError, "exceeds"):
                client.recv()
        finally:
            left.close()
            right.close()

    def test_pty_runner_executes_strict_events_and_finally_closes(self):
        config = self.config()
        config["sandbox_pty"] = {
            "actor": "user_a",
            "ticket_path": "/workbench/sandbox/${sandbox_id}/pty",
            "connect_path": "/workbench/sandbox/${sandbox_id}/pty/connect",
            "conversation_variable": "conversation_id",
            "sandbox_variable": "sandbox_id",
            "timeout_seconds": 5,
            "rows": 24,
            "cols": 80,
        }
        args = argparse.Namespace(execute=True, allow_mutations=True, allow_eicar=False, allow_provider_cost=True)
        runner = Runner(config, args)
        runner.variables.update({"conversation_id": "conv-safe", "sandbox_id": "sbx_" + "a" * 32})
        response = Mock(status=201)
        response.json.return_value = {"protocol": "claw-workbench-pty-v1", "ticket": "A" * 43}
        client = Mock()
        client.cookies = {}
        client.request.return_value = response
        client._cookie_header.return_value = "session=safe"
        runner.clients["user_a"] = client
        websocket = Mock()
        websocket.recv.side_effect = [
            (0x1, json.dumps({"type": "ready", "pty_session_id": "pty_" + "b" * 32}).encode()),
            (0x2, b"CLAW_PTY_INPUT_OK CLAW_PTY_RESIZE_OK 24 80 CLAW_PTY_CTRL_C_OK"),
        ]
        with patch("run_e2e.BoundedWebSocket.connect", return_value=websocket):
            runner.sandbox_pty_contract("sandbox", {"actor": "user_a"})
        self.assertEqual(runner.results[-1].status, "pass", runner.results[-1].message)
        websocket.close.assert_called_once()
        sent_payloads = [json.loads(call.args[1]) for call in websocket.send.call_args_list if call.args[0] == 0x1]
        self.assertIn({"type": "resize", "rows": 24, "cols": 80}, sent_payloads)
        self.assertTrue(any(value.get("data") == "\u0003" for value in sent_payloads if value.get("type") == "input"))

    @unittest.skipUnless(__import__("shutil").which("ssh-keygen"), "ssh-keygen required")
    def test_signed_acceptance_observer_binds_run_release_window_and_fact(self):
        key = self.root / "observer-key"
        subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(key)], check=True)
        public = key.with_suffix(".pub").read_text(encoding="ascii").strip()
        allowed = self.root / "observer-allowed"
        allowed.write_text(f"claw-acceptance-observer {public}\n", encoding="ascii")
        manifest = {"new_api_revision": "a" * 40}
        digest = __import__("hashlib").sha256(json.dumps(manifest, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
        started = (datetime.now(timezone.utc) - timedelta(seconds=2)).isoformat().replace("+00:00", "Z")
        now = datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")
        evidence = self.root / "evidence.json"
        evidence.write_text(json.dumps({
            "schema_version": 1,
            "acceptance_run_id": "observer-run",
            "release_manifest_sha256": digest,
            "requirement": "scheduled.worker_failover",
            "observed_from": started,
            "observed_until": now,
            "collected_at": now,
            "source": {"kind": "postgresql", "query_sha256": "b" * 64, "read_only": True, "row_count": 1},
            "facts": {"worker_claim_count": 1},
        }), encoding="utf-8")
        subprocess.run(["ssh-keygen", "-Y", "sign", "-f", str(key), "-n", "claw-acceptance-observer-v1", str(evidence)], check=True, stdout=subprocess.DEVNULL)
        config = self.config()
        config["release_manifest"] = manifest
        config["acceptance_observer"] = {
            "allowed_signers_file": f"file:{allowed}",
            "wait_seconds": 1,
            "max_age_seconds": 60,
            "evidence": {"scheduled.worker_failover": f"file:{evidence}"},
        }
        args = argparse.Namespace(execute=True, allow_mutations=True, allow_eicar=False, allow_provider_cost=True)
        runner = Runner(config, args, run_id="observer-run", started_at=started)
        runner.acceptance_observer_evidence("scheduled", "scheduled")
        self.assertEqual(runner.results[-1].status, "pass", runner.results[-1].message)
        self.assertIn("signed_observer_evidence", runner.results[-1].evidence["assertion_types"])

        evidence.with_suffix(evidence.suffix + ".sig").unlink()
        evidence.write_text(json.dumps({
            "schema_version": 1,
            "acceptance_run_id": "observer-run",
            "release_manifest_sha256": digest,
            "requirement": "agent_store.dynamic_binding_unique",
            "observed_from": started,
            "observed_until": now,
            "collected_at": now,
            "source": {"kind": "provider_audit", "query_sha256": "c" * 64, "read_only": True, "row_count": 1},
            "facts": {"active_binding_count": 1},
        }), encoding="utf-8")
        subprocess.run(["ssh-keygen", "-Y", "sign", "-f", str(key), "-n", "claw-acceptance-observer-v1", str(evidence)], check=True, stdout=subprocess.DEVNULL)
        config["acceptance_observer"]["evidence"] = {
            "agent_store.dynamic_binding_unique": f"file:{evidence}",
        }
        runner = Runner(config, args, run_id="observer-run", started_at=started)
        runner.acceptance_observer_evidence("agent-store", "agent_store")
        self.assertEqual(runner.results[-1].status, "blocker")
        self.assertIn("source is not", runner.results[-1].message)


if __name__ == "__main__":
    unittest.main()
