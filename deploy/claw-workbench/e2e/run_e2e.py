#!/usr/bin/env python3
"""Ordered, fail-closed live acceptance runner for Claw Workbench."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from urllib.parse import quote, urlsplit

from acceptance_observer_signature import verify_acceptance_observer_evidence
from bounded_websocket import BoundedWebSocket, WebSocketContractError

from e2e_lib import (
    ConfigError,
    CheckResult,
    EICAR_SHA256,
    HTTPClient,
    Redactor,
    expand_variables,
    find_key,
    find_pointer,
    find_pointer_state,
    json_bytes,
    load_config,
    load_json_document,
    load_json_fixture,
    origin,
    parse_location,
    resolve_secret,
    sanitized_endpoint,
    sign_evidence_file,
    utc_now,
    validate_acceptance_signing_key,
    write_reports,
)


DEFAULT_PATHS = {
    "legacy": "/playground/legacy",
    "user_ticket": "/api/workbench/session-ticket",
    "admin_ticket": "/api/admin/workbench/session-ticket",
    "entry": "/api/workbench/entry",
    "selections": "/api/workbench/selections",
    "selection_choose": "/api/workbench/selections/choose",
    "workbench_root": "/workbench/",
    "account_info": "/workbench/account/info",
    "application_list": "/workbench/application/list",
    "turn": "/workbench/chat/message",
    "turn_events": "/workbench/chat/turn/events",
    "history": "/workbench/chat/messages",
    "file_upload": "/workbench/file/upload",
}

FORBIDDEN_TURN_KEYS = {
    "userid", "user_id", "customerid", "customer_id", "assetscopeid", "asset_scope_id",
    "agentid", "agent_id", "workbenchuserid", "workbench_user_id",
}
RELEASE_DIGEST_RE = re.compile(r"sha256:[0-9a-f]{64}")
RELEASE_REVISION_RE = re.compile(r"[0-9a-f]{40}")
SELECTOR_REQUIREMENTS = {
    "same_customer_multi_app", "same_user_multi_customer", "selection_token_replay",
    "selection_token_stale", "cross_context_idor",
}
OAUTH_REQUIREMENTS = {
    "pkce_s256", "state_replay", "disconnect_old_callback",
    "cross_scope_callback_rejected", "refresh_revoke", "execution_blocked",
}
SCHEDULED_REQUIREMENTS = {
    "lifecycle", "occurrence_idempotency", "worker_failover",
    "provider_unknown_no_repost", "offline_reauthorization", "limits", "agent_lock",
}
SANDBOX_REQUIREMENTS = {
    "capability_contract", "lifecycle_ownership", "shell_bounded",
    "shell_stream_bounded", "file_roundtrip", "code_fail_closed", "pty_fail_closed",
    "lifecycle_transitions", "cross_scope_idor", "rate_limits",
    "provider_unknown_no_duplicate", "resource_bounds", "csrf_rejected",
    "cleanup_terminal",
}
SANDBOX_ENABLED_REQUIREMENTS = (
    SANDBOX_REQUIREMENTS.difference({"code_fail_closed", "pty_fail_closed"})
    | {"code_six_languages", "pty_interactive"}
)
BYOK_REQUIREMENTS = {
    "platform_profile_sharing", "customer_profile_isolation",
    "additional_app_inherits_primary", "self_approval_rejected",
    "two_person_approval", "rotation", "rollback_retire",
    "readiness_fingerprint_mismatch", "public_reenroll_hidden", "secret_leak_scan",
}
BILLING_IMPORT_REQUIREMENTS = {
    "terminal_import", "idempotent_import", "invoice_immutable",
    "account_only_unattributed", "multipage_adjustment", "failed_retry",
    "coordinator_single_claim",
}
OBSERVER_FACTS = {
    "scheduled.worker_failover": ("worker_claim_count", 1),
    "scheduled.provider_unknown_no_repost": ("provider_submit_count", 1),
    "byok.secret_leak_scan": ("secret_matches", 0),
    "billing_import.coordinator_single_claim": ("worker_claim_count", 1),
}
PHASE_REQUIREMENTS = {
    "isolation_checks": {"same_customer_idor", "cross_customer_idor"},
    "policy_checks": {"model_allowlist", "skill_allowlist", "tool_allowlist", "connector_allowlist"},
    "limit_checks": {"request_rate", "concurrency", "storage", "turn"},
    "rotation_checks": {"credential_rotation", "app_rotation"},
    "usage_audit_checks": {"usage_reconciliation", "audit_attribution", "margin_snapshot"},
    "smoke_checks": {"chat_completion", "asset_video_route"},
    "security_checks": {"csrf", "xss", "forged_identity", "secret_redaction"},
}
LIFECYCLE_REQUIREMENTS = {
    "create_customer_members": {"customer_created", "member_roles"},
    "invalid_app_validation": {"invalid_app_rejected"},
    "no_plan_gate": {"no_plan_enable_rejected"},
    "activate_plan": {"plan_payment_invoice", "app_enabled"},
}
RELEASE_MANIFEST_FIELDS = {
    "new_api_revision", "claw_control_revision", "adp_revision",
    "new_api_image_digest", "claw_control_image_digest", "adp_image_digest",
    "config_sha256", "control_migration", "adp_migration", "caddy_version",
    "active_color", "provider_region", "collected_at",
}
WORKBENCH_TURN_EVENT_TYPE = "workbench.turn"
WORKBENCH_TERMINAL_EVENT_TYPE = "workbench.turn_status"
WORKBENCH_TERMINAL_STATUSES = {
    "completed", "failed_before_accept", "failed_after_accept",
    "cancel_confirmed", "provider_unknown",
}
MAX_SSE_EVENT_BYTES = 256 * 1024

CONTRACT_PATH_PREFIXES = {
    "selector": ("/api/workbench/", "/api/admin/workbench/customers/", "/playground/select", "/workbench/"),
    "oauth": ("/workbench/integration", "/workbench/chat/"),
    "scheduled": ("/workbench/scheduled",),
    "sandbox": (
        "/workbench/sandbox",
        "/workbench/adp/CreateConversation",
        "/workbench/chat/conversation/delete",
    ),
    "byok": (
        "/api/admin/workbench/credential-profiles", "/api/admin/workbench/approvals",
        "/api/admin/workbench/customers", "/api/admin/workbench/secret-fingerprints",
        "/readyz",
    ),
    "billing_import": (
        "/api/admin/workbench/tencent-billing-imports",
        "/api/admin/workbench/usage-audits",
        "/api/admin/workbench/customers",
    ),
}


def read_structured_sse_event(response: Any) -> tuple[str, dict[str, Any] | None] | None:
    """Read one bounded SSE event and decode an object payload when it is JSON."""
    data_lines: list[str] = []
    event_id = ""
    data_bytes = 0
    for _ in range(10000):
        line = response.readline(65537)
        if not line:
            if not data_lines and not event_id:
                return None
            break
        if len(line) > 65536:
            raise RuntimeError("SSE line exceeds 64 KiB")
        try:
            text = line.decode("utf-8").rstrip("\r\n")
        except UnicodeDecodeError as error:
            raise RuntimeError("SSE stream is not valid UTF-8") from error
        if not text:
            if data_lines or event_id:
                break
            continue
        if text.startswith(":"):
            continue
        field, separator, value = text.partition(":")
        if separator and value.startswith(" "):
            value = value[1:]
        if field == "id":
            if "\x00" in value:
                raise RuntimeError("SSE event id contains NUL")
            event_id = value
        elif field == "data":
            data_lines.append(value)
            data_bytes += len(value.encode("utf-8")) + (1 if len(data_lines) > 1 else 0)
            if data_bytes > MAX_SSE_EVENT_BYTES:
                raise RuntimeError("SSE event exceeds 256 KiB")
    else:
        raise RuntimeError("SSE event exceeds 10000 lines")
    if not data_lines:
        return event_id, None
    try:
        payload = json.loads("\n".join(data_lines))
    except json.JSONDecodeError:
        return event_id, None
    return event_id, payload if isinstance(payload, dict) else None


def _path_matches(spec: dict[str, Any], method: str, pattern: str) -> bool:
    return str(spec.get("method", "")).upper() == method and re.search(pattern, str(spec.get("path", ""))) is not None


def release_manifest_errors(manifest: Any, *, max_age_seconds: int | None = None) -> list[str]:
    if not isinstance(manifest, dict):
        return ["release_manifest is missing"]
    errors: list[str] = []
    missing = sorted(RELEASE_MANIFEST_FIELDS.difference(manifest))
    unknown = sorted(set(manifest).difference(RELEASE_MANIFEST_FIELDS))
    if missing:
        errors.append(f"missing fields: {', '.join(missing)}")
    if unknown:
        errors.append(f"unknown fields: {', '.join(unknown)}")
    if errors:
        return errors
    for key in ("new_api_revision", "claw_control_revision", "adp_revision"):
        value = str(manifest[key])
        if not RELEASE_REVISION_RE.fullmatch(value) or set(value) == {"0"}:
            errors.append(f"{key} is not a non-placeholder 40-hex revision")
    for key in ("new_api_image_digest", "claw_control_image_digest", "adp_image_digest"):
        value = str(manifest[key])
        if not RELEASE_DIGEST_RE.fullmatch(value) or set(value.removeprefix("sha256:")) == {"0"}:
            errors.append(f"{key} is not a non-placeholder sha256 digest")
    config_digest = str(manifest["config_sha256"])
    if not re.fullmatch(r"[0-9a-f]{64}", config_digest) or set(config_digest) == {"0"}:
        errors.append("config_sha256 is not a non-placeholder 64-hex digest")
    if manifest["active_color"] not in {"blue", "green"}:
        errors.append("active_color must be blue or green")
    for key in ("control_migration", "adp_migration", "caddy_version", "provider_region"):
        value = str(manifest[key]).strip()
        if not value or value.startswith(("replace-", "REPLACE_")):
            errors.append(f"{key} is empty or a placeholder")
        elif not re.fullmatch(r"[A-Za-z0-9._:-]{1,128}", value):
            errors.append(f"{key} contains unsupported characters")
    if not re.fullmatch(r"[a-z0-9]+(?:-[a-z0-9]+)+", str(manifest["provider_region"])):
        errors.append("provider_region is not a canonical region identifier")
    try:
        collected_at = datetime.fromisoformat(str(manifest["collected_at"]).replace("Z", "+00:00"))
        if collected_at.tzinfo is None:
            raise ValueError("timezone is missing")
        age_seconds = (datetime.now(timezone.utc) - collected_at.astimezone(timezone.utc)).total_seconds()
        if max_age_seconds is not None:
            if age_seconds < -300:
                errors.append("collected_at is more than five minutes in the future")
            if age_seconds > max_age_seconds:
                errors.append(f"collected_at is older than {max_age_seconds} seconds")
    except ValueError:
        errors.append("collected_at is not a timezone-aware ISO-8601 timestamp")
    return errors


def _has_strong_assertion(spec: dict[str, Any]) -> bool:
    statuses = {int(item) for item in spec.get("expect_status", [])}
    return bool(
        statuses.difference(range(200, 300))
        or any(
            spec.get(key)
            for key in (
                "capture", "capture_sha256", "json_equals", "json_not_equals",
                "json_absent", "json_types", "header_equals", "poll",
                "require_response_markers", "forbid_response_markers",
                "assert_variables_equal", "assert_variables_not_equal",
            )
        )
    )


def _matching(specs: list[dict[str, Any]], requirement: str, predicate) -> list[dict[str, Any]]:
    return [spec for spec in specs if spec.get("requirement") == requirement and predicate(spec)]


def _fixture_field(spec: dict[str, Any], field: str) -> Any:
    ref = str(spec.get("body_fixture", ""))
    if not ref.startswith("file:"):
        return None
    try:
        value = load_json_document(ref[5:])
    except ConfigError:
        return None
    return value.get(field) if isinstance(value, dict) else None


def _exact_status(spec: dict[str, Any], status: int) -> bool:
    return spec.get("expect_status") == [status]


def _exact_marker(spec: dict[str, Any], marker: str) -> bool:
    return marker in spec.get("require_response_markers", [])


def _default_app_selector(spec: dict[str, Any]) -> str:
    match = re.fullmatch(
        r"/api/admin/workbench/customers/[^/]+/apps/([^/]+)/default",
        str(spec.get("path", "")),
    )
    return match.group(1) if match else ""


def acceptance_preflight_errors(config: dict[str, Any]) -> list[str]:
    """Validate the complete live acceptance matrix before the first request."""

    errors: list[str] = []
    sandbox_requirements = SANDBOX_ENABLED_REQUIREMENTS if config.get("sandbox_mode", "off") == "enabled" else SANDBOX_REQUIREMENTS
    contracts = (
        ("selector", "selector_checks", SELECTOR_REQUIREMENTS),
        ("oauth", "oauth_checks", OAUTH_REQUIREMENTS),
        ("scheduled", "scheduled_task_checks", SCHEDULED_REQUIREMENTS),
        ("sandbox", "sandbox_checks", sandbox_requirements),
        ("byok", "byok_checks", BYOK_REQUIREMENTS),
        ("billing_import", "billing_import_checks", BILLING_IMPORT_REQUIREMENTS),
    )
    observer_keys = set(config.get("acceptance_observer", {}).get("evidence", {}))
    unknown_observer = sorted(observer_keys.difference(OBSERVER_FACTS))
    if unknown_observer:
        errors.append("acceptance_observer.evidence: unknown requirements " + ", ".join(unknown_observer))
    for contract, key, required in contracts:
        specs = config.get(key)
        if not isinstance(specs, list) or not specs:
            errors.append(f"{key}: no live acceptance fixtures configured")
            continue
        declared = {
            str(spec.get("requirement"))
            for spec in specs
            if isinstance(spec, dict) and isinstance(spec.get("requirement"), str)
        }.union({item.split(".", 1)[1] for item in observer_keys if item.startswith(contract + ".")})
        missing = sorted(required.difference(declared))
        unknown = sorted(declared.difference(required))
        if missing:
            errors.append(f"{key}: missing requirements {', '.join(missing)}")
        if unknown:
            errors.append(f"{key}: unknown requirements {', '.join(unknown)}")
        errors.extend(requirement_semantic_errors(contract, specs, required, observer_keys=observer_keys))

    for key, required in PHASE_REQUIREMENTS.items():
        if not isinstance(config.get(key), list) or not config[key]:
            errors.append(f"{key}: no live acceptance fixtures configured")
            continue
        errors.extend(closed_phase_semantic_errors(key, config[key], required))
    lifecycle = config.get("lifecycle")
    for key, required in LIFECYCLE_REQUIREMENTS.items():
        if not isinstance(lifecycle, dict) or not isinstance(lifecycle.get(key), list) or not lifecycle[key]:
            errors.append(f"lifecycle.{key}: no live acceptance fixtures configured")
            continue
        errors.extend(closed_phase_semantic_errors(f"lifecycle.{key}", lifecycle[key], required))
    if not isinstance(config.get("gate_scenarios"), list) or not config["gate_scenarios"]:
        errors.append("gate_scenarios: no live acceptance scenarios configured")
    else:
        states = [scenario.get("state") for scenario in config["gate_scenarios"] if isinstance(scenario, dict)]
        if set(states) != {"suspended", "disabled", "expired"} or len(states) != 3:
            errors.append("gate_scenarios: requires exactly one suspended, disabled, and expired scenario")
        for scenario in config["gate_scenarios"]:
            if not gate_scenario_semantically_closed(scenario):
                errors.append(f"gate_scenarios.{scenario.get('state', 'unknown')}: transition/write/read/restore contract is incomplete")
    if not config.get("turn", {}).get("request_fixture"):
        errors.append("turn.request_fixture: minimal provider Turn fixture is required")
    if not config.get("eicar", {}).get("file_ref"):
        errors.append("eicar.file_ref: standard EICAR fixture is required")
    if not config.get("direct_adp_base_url") or not config.get("direct_adp_paths"):
        errors.append("direct_adp_base_url and direct_adp_paths are required")
    if "sandbox_acceptance_token" not in config.get("secrets", {}):
        errors.append("secrets.sandbox_acceptance_token is required for sandbox fault evidence")
    if config.get("sandbox_mode", "off") == "enabled" and not isinstance(config.get("sandbox_pty"), dict):
        errors.append("sandbox_pty: enabled sandbox mode requires the dedicated WebSocket contract")
    return errors


def closed_phase_semantic_errors(key: str, specs: list[dict[str, Any]], required: set[str]) -> list[str]:
    declared = {str(spec.get("requirement")) for spec in specs if isinstance(spec, dict) and isinstance(spec.get("requirement"), str)}
    errors: list[str] = []
    missing = sorted(required.difference(declared))
    unknown = sorted(declared.difference(required))
    if missing:
        errors.append(f"{key}: missing requirements {', '.join(missing)}")
    if unknown:
        errors.append(f"{key}: unknown requirements {', '.join(unknown)}")
    for requirement in sorted(required.intersection(declared)):
        claimed = [spec for spec in specs if spec.get("requirement") == requirement]
        if not any(_has_strong_assertion(spec) for spec in claimed):
            errors.append(f"{key}.{requirement}: no machine-verifiable assertion")
        if not any(spec.get("actor") in REQUEST_ACTOR_NAMES and str(spec.get("path", "")).startswith(("/api/", "/workbench/", "/v1/")) for spec in claimed):
            errors.append(f"{key}.{requirement}: no supported actor on a real API path")
    return errors


REQUEST_ACTOR_NAMES = {
    "admin", "admin_requester", "admin_approver", "user_a", "user_b_same",
    "user_b_other", "user_a_context_1", "user_a_context_2", "user_a_customer_2",
    "user_a_stale_context", "api_key", "anonymous",
}


def gate_scenario_semantically_closed(scenario: dict[str, Any]) -> bool:
    if not isinstance(scenario, dict):
        return False
    transition, write_probe, read_probe, restore = (scenario.get(key, {}) for key in ("transition", "write_probe", "read_probe", "restore"))
    state = scenario.get("state")
    return bool(
        transition.get("actor") in {"admin", "admin_requester", "admin_approver"}
        and transition.get("method") in {"POST", "PATCH"}
        and _has_strong_assertion(transition)
        and write_probe.get("actor") == "user_a"
        and write_probe.get("method") in {"POST", "PUT", "PATCH", "DELETE"}
        and bool(set(write_probe.get("expect_status", [])).intersection({401, 403, 409, 423}))
        and read_probe.get("actor") == "user_a" and read_probe.get("method") == "GET"
        and read_probe.get("expect_status") == ([403] if state == "disabled" else [200])
        and restore.get("actor") in {"admin", "admin_requester", "admin_approver"}
        and restore.get("cleanup") is True and restore.get("method") in {"POST", "PATCH"}
        and _has_strong_assertion(restore)
    )


def requirement_semantic_errors(
    contract: str,
    specs: list[dict[str, Any]],
    requirements: set[str],
    *,
    observer_keys: set[str] | None = None,
) -> list[str]:
    """Reject label-only acceptance evidence before any live request is sent."""

    errors: list[str] = []
    prefixes = CONTRACT_PATH_PREFIXES.get(contract)
    if not prefixes:
        return [f"unknown requirement contract {contract}"]
    observer_keys = observer_keys or set()
    observer_requirements = {
        item.split(".", 1)[1] for item in observer_keys if item.startswith(contract + ".")
    }
    for requirement in sorted(requirements):
        claimed = [spec for spec in specs if spec.get("requirement") == requirement]
        if requirement in observer_requirements:
            continue
        if not claimed:
            continue
        if any(not str(spec.get("path", "")).startswith(prefixes) for spec in claimed):
            errors.append(f"{requirement}: endpoint is outside the {contract} contract")
        if not any(_has_strong_assertion(spec) for spec in claimed):
            errors.append(f"{requirement}: no machine-verifiable response assertion")

    def need(requirement: str, label: str, predicate) -> None:
        if requirement in requirements and requirement not in observer_requirements and not _matching(specs, requirement, predicate):
            errors.append(f"{requirement}: missing {label}")

    if contract == "selector":
        for actor, prefix in (("user_a_context_1", "context_1"), ("user_a_context_2", "context_2")):
            need(
                "same_customer_multi_app", f"{actor} identity hash capture",
                lambda spec, actor=actor, prefix=prefix: spec.get("actor") == actor
                and _path_matches(spec, "GET", r"^/workbench/account/info$")
                and all(
                    variable in spec.get("capture_sha256", {})
                    for variable in (f"{prefix}_user_hash", f"{prefix}_customer_hash", f"{prefix}_app_hash")
                ),
            )
        need(
            "same_customer_multi_app", "same user/customer and different App comparison",
            lambda spec: spec.get("actor") == "user_a_context_2"
            and spec.get("assert_variables_equal", {}).get("context_1_user_hash") == "context_2_user_hash"
            and spec.get("assert_variables_equal", {}).get("context_1_customer_hash") == "context_2_customer_hash"
            and spec.get("assert_variables_not_equal", {}).get("context_1_app_hash") == "context_2_app_hash",
        )
        need(
            "same_user_multi_customer", "same user and different customer comparison",
            lambda spec: spec.get("actor") == "user_a_customer_2"
            and spec.get("assert_variables_equal", {}).get("context_1_user_hash") == "customer_2_user_hash"
            and spec.get("assert_variables_not_equal", {}).get("context_1_customer_hash") == "customer_2_customer_hash",
        )
        need(
            "same_user_multi_customer", "alternate customer identity hash capture",
            lambda spec: spec.get("actor") == "user_a_customer_2"
            and _path_matches(spec, "GET", r"^/workbench/account/info$")
            and all(
                variable in spec.get("capture_sha256", {})
                for variable in ("customer_2_user_hash", "customer_2_customer_hash")
            ),
        )
        need(
            "selection_token_replay", "consumed selection token replay rejection",
            lambda spec: _path_matches(spec, "POST", r"^/api/workbench/selections/choose$")
            and spec.get("selection_token_source") == "consumed"
            and spec.get("selection_token_actor") == spec.get("actor")
            and bool(set(spec.get("expect_status", [])).intersection({403, 409, 410})),
        )
        need(
            "selection_token_stale", "stale selection token rejection",
            lambda spec: _path_matches(spec, "POST", r"^/api/workbench/selections/choose$")
            and spec.get("selection_token_source") == "option"
            and spec.get("selection_token_actor") == "user_a_stale_context"
            and bool(spec.get("selection_target", {}).get("app_selector"))
            and _exact_status(spec, 403)
            and _exact_marker(spec, "selection token context is stale"),
        )
        stale_specs = [spec for spec in specs if spec.get("requirement") == "selection_token_stale"]
        stale_targets = [
            spec.get("selection_target", {})
            for spec in stale_specs
            if spec.get("selection_token_actor") == "user_a_stale_context"
            and spec.get("selection_token_source") == "option"
        ]
        stale_target = stale_targets[0] if len(stale_targets) == 1 else {}
        stale_selector = str(stale_target.get("app_selector", ""))
        if len(stale_targets) != 1:
            errors.append("selection_token_stale: exactly one pending server-option target must be exercised")
        stale_rejections = [
            (index, spec)
            for index, spec in enumerate(specs)
            if spec in stale_specs
            and spec.get("selection_token_source") == "option"
            and spec.get("selection_token_actor") == "user_a_stale_context"
            and spec.get("selection_target") == stale_target
            and _exact_status(spec, 403)
            and _exact_marker(spec, "selection token context is stale")
        ]
        invalidations = [
            index
            for index, spec in enumerate(specs)
            if spec in stale_specs
            and spec.get("actor") in {"admin", "admin_requester", "admin_approver"}
            and bool(stale_selector)
            and _default_app_selector(spec) == stale_selector
            and _exact_status(spec, 200)
            and spec.get("json_equals", {}).get("/data/selector") == stale_selector
            and spec.get("json_equals", {}).get("/data/slot") == "primary"
            and isinstance(_fixture_field(spec, "expected_target_version"), int)
            and _fixture_field(spec, "expected_target_version") > 0
            and isinstance(_fixture_field(spec, "expected_current_default_version"), int)
            and _fixture_field(spec, "expected_current_default_version") > 0
            and not spec.get("cleanup")
        ]
        restores = [
            (index, spec)
            for index, spec in enumerate(specs)
            if spec in stale_specs
            if spec.get("actor") in {"admin", "admin_requester", "admin_approver"}
            and bool(_default_app_selector(spec))
            and _default_app_selector(spec) != stale_selector
            and _exact_status(spec, 200)
            and spec.get("json_equals", {}).get("/data/selector") == _default_app_selector(spec)
            and spec.get("json_equals", {}).get("/data/slot") == "primary"
            and isinstance(_fixture_field(spec, "expected_target_version"), int)
            and _fixture_field(spec, "expected_target_version") > 0
            and isinstance(_fixture_field(spec, "expected_current_default_version"), int)
            and _fixture_field(spec, "expected_current_default_version") > 0
            and spec.get("cleanup") is True
        ]
        ordered_stale_proof = any(
            invalidation < rejection < restore
            for rejection, _ in stale_rejections
            for invalidation in invalidations
            for restore, _ in restores
        )
        if stale_rejections and not ordered_stale_proof:
            errors.append(
                "selection_token_stale: requires an exact different-App default mutation, exact stale rejection, and exact original-App restore in that order"
            )
        need(
            "cross_context_idor", "source-context Conversation creation",
            lambda spec: spec.get("actor") == "user_a_context_1"
            and _path_matches(spec, "POST", r"^/workbench/adp/CreateConversation$")
            and _exact_status(spec, 200)
            and spec.get("capture", {}).get("context_1_resource_id") == "/Response/ConversationId",
        )
        need(
            "cross_context_idor", "source-context Conversation readback",
            lambda spec: spec.get("actor") == "user_a_context_1"
            and _path_matches(spec, "POST", r"^/workbench/adp/DescribeConversation$")
            and _fixture_field(spec, "ConversationId") == "${context_1_resource_id}"
            and _exact_status(spec, 200)
            and spec.get("json_equals", {}).get("/Response/ConversationId") == "${context_1_resource_id}",
        )
        need(
            "cross_context_idor", "different browser-context resource rejection",
            lambda spec: spec.get("actor") in {"user_a_context_2", "user_a_customer_2"}
            and _path_matches(spec, "POST", r"^/workbench/adp/DescribeConversation$")
            and _fixture_field(spec, "ConversationId") == "${context_1_resource_id}"
            and _exact_status(spec, 404),
        )
        need(
            "cross_context_idor", "source-context Conversation cleanup",
            lambda spec: spec.get("actor") == "user_a_context_1"
            and _path_matches(spec, "POST", r"^/workbench/chat/conversation/delete$")
            and _fixture_field(spec, "ConversationId") == "${context_1_resource_id}"
            and _exact_status(spec, 200)
            and spec.get("json_equals", {}).get("/Success") == 1
            and spec.get("cleanup") is True,
        )
        source_indices = [
            index
            for index, spec in enumerate(specs)
            if spec.get("requirement") == "cross_context_idor"
            and spec.get("actor") == "user_a_context_1"
            and _path_matches(spec, "POST", r"^/workbench/adp/CreateConversation$")
            and spec.get("capture", {}).get("context_1_resource_id") == "/Response/ConversationId"
            and _exact_status(spec, 200)
        ]
        readback_indices = [
            index
            for index, spec in enumerate(specs)
            if spec.get("requirement") == "cross_context_idor"
            and spec.get("actor") == "user_a_context_1"
            and _path_matches(spec, "POST", r"^/workbench/adp/DescribeConversation$")
            and _fixture_field(spec, "ConversationId") == "${context_1_resource_id}"
            and _exact_status(spec, 200)
            and spec.get("json_equals", {}).get("/Response/ConversationId") == "${context_1_resource_id}"
        ]
        rejection_indices = [
            index
            for index, spec in enumerate(specs)
            if spec.get("requirement") == "cross_context_idor"
            and spec.get("actor") in {"user_a_context_2", "user_a_customer_2"}
            and _path_matches(spec, "POST", r"^/workbench/adp/DescribeConversation$")
            and _fixture_field(spec, "ConversationId") == "${context_1_resource_id}"
            and _exact_status(spec, 404)
        ]
        cleanup_indices = [
            index
            for index, spec in enumerate(specs)
            if spec.get("requirement") == "cross_context_idor"
            and spec.get("actor") == "user_a_context_1"
            and _path_matches(spec, "POST", r"^/workbench/chat/conversation/delete$")
            and _fixture_field(spec, "ConversationId") == "${context_1_resource_id}"
            and _exact_status(spec, 200)
            and spec.get("cleanup") is True
        ]
        if rejection_indices and not any(
            source < readback < rejection < cleanup
            for source in source_indices
            for readback in readback_indices
            for rejection in rejection_indices
            for cleanup in cleanup_indices
        ):
            errors.append("cross_context_idor: requires create, owner readback, cross-context 404, and owner cleanup in that order")
        return errors

    if contract == "oauth":
        need(
            "pkce_s256", "OAuth start response containing S256 evidence",
            lambda spec: spec.get("actor") == "user_a"
            and _path_matches(spec, "POST", r"^/workbench/integrations/connectors/[^/]+/oauth/start$")
            and any("S256" in marker for marker in spec.get("require_response_markers", [])),
        )
        need(
            "state_replay", "callback replay rejection",
            lambda spec: _path_matches(spec, "GET", r"^/workbench/integrations/oauth/callback/[^/]+\?")
            and bool(set(spec.get("expect_status", [])).intersection({400, 403, 409, 410})),
        )
        need(
            "disconnect_old_callback", "connector disconnect mutation",
            lambda spec: _path_matches(spec, "POST", r"^/workbench/integrations/connectors/[^/]+/disconnect$"),
        )
        need(
            "disconnect_old_callback", "old callback rejection after disconnect",
            lambda spec: _path_matches(spec, "GET", r"^/workbench/integrations/oauth/callback/[^/]+\?")
            and bool(set(spec.get("expect_status", [])).intersection({400, 403, 409, 410})),
        )
        need(
            "cross_scope_callback_rejected", "different-customer callback rejection",
            lambda spec: spec.get("actor") == "user_b_other"
            and _path_matches(spec, "GET", r"^/workbench/integrations/oauth/callback/[^/]+\?")
            and bool(set(spec.get("expect_status", [])).intersection({403, 404, 409, 410})),
        )
        need(
            "refresh_revoke", "post-disconnect integration state assertion",
            lambda spec: _path_matches(spec, "GET", r"^/workbench/integrations$")
            and bool(spec.get("json_equals") or spec.get("require_response_markers")),
        )
        need(
            "execution_blocked", "connector-dependent Turn rejection",
            lambda spec: _path_matches(spec, "POST", r"^/workbench/chat/message$")
            and bool(set(spec.get("expect_status", [])).intersection({403, 409, 503})),
        )
        return errors

    if contract == "scheduled":
        need(
            "lifecycle", "task create with captured ID",
            lambda spec: _path_matches(spec, "POST", r"^/workbench/scheduled-tasks$")
            and any(str(variable).endswith("task_id") for variable in spec.get("capture", {})),
        )
        for method, suffix in (("PATCH", r"/\$\{scheduled_task_id\}$"), ("POST", r"/\$\{scheduled_task_id\}/pause$"), ("POST", r"/\$\{scheduled_task_id\}/resume$"), ("DELETE", r"/\$\{scheduled_task_id\}$")):
            need("lifecycle", f"{method} {suffix} lifecycle step", lambda spec, method=method, suffix=suffix: _path_matches(spec, method, suffix))
        need(
            "occurrence_idempotency", "dynamic occurrence identity comparison",
            lambda spec: bool(spec.get("assert_variables_equal"))
            and _path_matches(spec, "GET", r"/runs(\?|$)"),
        )
        need(
            "offline_reauthorization", "offline run blocked pending reauthorization",
            lambda spec: _path_matches(spec, "GET", r"/runs(\?|$)")
            and any("reauthor" in marker.lower() for marker in spec.get("require_response_markers", [])),
        )
        need(
            "limits", "scheduled-task 400/429 limit rejection",
            lambda spec: _path_matches(spec, "POST", r"^/workbench/scheduled-tasks$")
            and bool(set(spec.get("expect_status", [])).intersection({400, 429})),
        )
        need(
            "agent_lock", "Agent-changing update rejection",
            lambda spec: _path_matches(spec, "PATCH", r"^/workbench/scheduled-tasks/")
            and bool(set(spec.get("expect_status", [])).intersection({400, 409})),
        )
        return errors

    if contract == "byok":
        need("platform_profile_sharing", "platform profile listing assertion", lambda spec: _path_matches(spec, "GET", r"^/api/admin/workbench/credential-profiles") and bool(spec.get("json_equals") or spec.get("capture_sha256")))
        need("customer_profile_isolation", "customer-scoped profile isolation assertion", lambda spec: _path_matches(spec, "GET", r"^/api/admin/workbench/credential-profiles\?[^#]*customer_id=") and bool(spec.get("json_absent") or spec.get("json_equals")))
        need("additional_app_inherits_primary", "primary/additional App profile equality proof", lambda spec: _path_matches(spec, "GET", r"^/api/admin/workbench/customers/[^/]+/apps") and bool(spec.get("assert_variables_equal")))
        need("self_approval_rejected", "requester self-approval rejection", lambda spec: spec.get("actor") == "admin_requester" and _path_matches(spec, "POST", r"^/api/admin/workbench/approvals/[^/]+/approve$") and bool(set(spec.get("expect_status", [])).intersection({403, 409})))
        need("two_person_approval", "different approver approval", lambda spec: spec.get("actor") == "admin_approver" and _path_matches(spec, "POST", r"^/api/admin/workbench/approvals/[^/]+/approve$") and bool(spec.get("capture_sha256") or spec.get("assert_variables_not_equal")))
        need("rotation", "credential rotation mutation", lambda spec: _path_matches(spec, "POST", r"^/api/admin/workbench/credential-profiles/[^/]+/rotations$") and bool(spec.get("capture")))
        need("rollback_retire", "credential retire mutation", lambda spec: _path_matches(spec, "POST", r"^/api/admin/workbench/credential-profiles/[^/]+/retire$"))
        need("readiness_fingerprint_mismatch", "readiness 503 after fingerprint mismatch", lambda spec: _path_matches(spec, "GET", r"^/readyz$") and spec.get("expect_status") == [503])
        need("public_reenroll_hidden", "anonymous public re-enroll rejection", lambda spec: spec.get("actor") == "anonymous" and _path_matches(spec, "POST", r"^/api/admin/workbench/secret-fingerprints/re-enroll$") and set(spec.get("expect_status", [])).issubset({403, 404}))
        return errors

    if contract == "billing_import":
        need("terminal_import", "import create with captured ID", lambda spec: _path_matches(spec, "POST", r"^/api/admin/workbench/tencent-billing-imports$") and any(str(variable).endswith("import_id") for variable in spec.get("capture", {})))
        need("terminal_import", "terminal import poll", lambda spec: _path_matches(spec, "GET", r"^/api/admin/workbench/tencent-billing-imports/\$\{[^}]*import_id\}") and bool(spec.get("poll")))
        need("idempotent_import", "repeated import identity equality", lambda spec: bool(spec.get("assert_variables_equal")) and "/tencent-billing-imports" in str(spec.get("path", "")))
        need("invoice_immutable", "pre/post invoice digest equality", lambda spec: bool(spec.get("assert_variables_equal")) and re.search(r"/customers/[^/]+/invoices", str(spec.get("path", ""))) is not None)
        need("account_only_unattributed", "account-only audit has no customer", lambda spec: _path_matches(spec, "GET", r"^/api/admin/workbench/usage-audits") and "/customer_id" in spec.get("json_absent", []))
        need("multipage_adjustment", "multipage adjustment evidence", lambda spec: _path_matches(spec, "GET", r"^/api/admin/workbench/tencent-billing-imports/") and bool(spec.get("json_equals") or spec.get("require_response_markers")))
        need("failed_retry", "failed import retry", lambda spec: _path_matches(spec, "POST", r"^/api/admin/workbench/tencent-billing-imports/[^/]+/retry$") and bool(spec.get("capture") or spec.get("poll")))
        return errors

    if contract != "sandbox":
        return errors

    need(
        "capability_contract",
        "exact GET /workbench/sandbox/config capability assertions",
        lambda spec: _path_matches(spec, "GET", r"^/workbench/sandbox/config$")
        and all(
            spec.get("json_equals", {}).get(pointer) is expected
            for pointer, expected in {
                "/sandbox_enabled": True, "/shell_enabled": True, "/files_enabled": True,
                "/code_execution_enabled": "code_six_languages" in requirements,
                "/pty_enabled": "pty_interactive" in requirements,
            }.items()
        ),
    )
    need(
        "lifecycle_ownership",
        "id-capturing create",
        lambda spec: _path_matches(spec, "POST", r"^/workbench/sandbox$")
        and spec.get("capture", {}).get("sandbox_id") == "/sandbox_id"
        and _fixture_field(spec, "conversation_id") == "${conversation_id}",
    )
    need(
        "lifecycle_ownership",
        "running-state ownership poll",
        lambda spec: _path_matches(spec, "GET", r"^/workbench/sandbox/\$\{sandbox_id\}\?conversation_id=")
        and spec.get("poll", {}).get("pointer") == "/status"
        and spec.get("poll", {}).get("equals") == "running"
        and {"failed", "provider_unknown", "stopped"}.issubset(
            set(spec.get("poll", {}).get("forbid_values", []))
        ),
    )
    need(
        "shell_bounded", "bounded Shell POST with success marker",
        lambda spec: _path_matches(spec, "POST", r"/shell$")
        and bool(spec.get("body_fixture"))
        and (bool(spec.get("require_response_markers")) or spec.get("json_equals", {}).get("/exit_code") == 0),
    )
    need(
        "shell_stream_bounded", "bounded Shell stream POST with success marker",
        lambda spec: _path_matches(spec, "POST", r"/shell/stream$")
        and bool(spec.get("body_fixture"))
        and bool(spec.get("require_response_markers")),
    )
    need("file_roundtrip", "workspace file PUT", lambda spec: _path_matches(spec, "PUT", r"/files\?"))
    need(
        "file_roundtrip", "workspace file GET with content assertion",
        lambda spec: _path_matches(spec, "GET", r"/files\?") and bool(spec.get("require_response_markers")),
    )
    need(
        "code_fail_closed", "code 503 fail-closed assertion",
        lambda spec: _path_matches(spec, "POST", r"/code$") and 503 in spec.get("expect_status", []),
    )
    need(
        "pty_fail_closed", "PTY 503 fail-closed assertion",
        lambda spec: _path_matches(spec, "POST", r"/pty$") and 503 in spec.get("expect_status", []),
    )
    if "code_six_languages" in requirements:
        languages = {
            str(_fixture_field(spec, "language"))
            for spec in specs
            if spec.get("requirement") == "code_six_languages"
            and _path_matches(spec, "POST", r"/code$")
            and _exact_status(spec, 200)
            and (spec.get("json_equals", {}).get("/exit_code") == 0 or bool(spec.get("require_response_markers")))
        }
        expected_languages = {"python", "javascript", "typescript", "java", "r", "bash"}
        if languages != expected_languages:
            errors.append("code_six_languages: requires successful exact fixtures for python, javascript, typescript, java, r, and bash")
    if "pty_interactive" in requirements:
        need(
            "pty_interactive", "one-time PTY ticket creation",
            lambda spec: _path_matches(spec, "POST", r"/pty$")
            and _exact_status(spec, 201)
            and spec.get("capture", {}).get("pty_ticket") == "/ticket"
            and spec.get("json_equals", {}).get("/protocol") == "claw-workbench-pty-v1",
        )
    for action, state in (("pause", "paused"), ("resume", "running")):
        need(
            "lifecycle_transitions", f"{action} mutation",
            lambda spec, action=action: _path_matches(spec, "POST", rf"/{action}$"),
        )
        need(
            "lifecycle_transitions", f"{state} terminal poll",
            lambda spec, state=state: _path_matches(spec, "GET", r"^/workbench/sandbox/")
            and spec.get("poll", {}).get("equals") == state
            and {"failed", "provider_unknown", "stopped"}.issubset(
                set(spec.get("poll", {}).get("forbid_values", []))
            ),
        )
    need(
        "cross_scope_idor", "different-customer 403/404 query",
        lambda spec: spec.get("actor") == "user_b_other"
        and _path_matches(spec, "GET", r"^/workbench/sandbox/\$\{sandbox_id\}")
        and set(spec.get("expect_status", [])).issubset({403, 404}),
    )
    need(
        "rate_limits", "exact 429 create rejection",
        lambda spec: spec.get("actor") == "user_a"
        and _path_matches(spec, "POST", r"^/workbench/sandbox$")
        and spec.get("expect_status") == [429]
        and "sandbox_start_rate_limited" in spec.get("require_response_markers", []),
    )
    provider_unknown_query = (
        "?acceptance_run_id=${acceptance_run_id}"
        "&conversation_id=${provider_unknown_conversation_id}"
    )
    provider_unknown_specs = [
        (index, spec)
        for index, spec in enumerate(specs)
        if spec.get("requirement") == "provider_unknown_no_duplicate"
    ]
    provider_unknown_steps = {
        "conversation": [
            index for index, spec in provider_unknown_specs
            if spec.get("actor") == "user_a"
            and _path_matches(spec, "POST", r"^/workbench/adp/CreateConversation$")
            and _exact_status(spec, 200)
            and spec.get("capture", {}).get("provider_unknown_conversation_id") == "/Response/ConversationId"
        ],
        "fault": [
            index for index, spec in provider_unknown_specs
            if spec.get("actor") == "user_a"
            and _path_matches(spec, "POST", r"^/workbench/sandbox$")
            and _fixture_field(spec, "conversation_id") == "${provider_unknown_conversation_id}"
            and _exact_status(spec, 503)
            and _exact_marker(spec, "acceptance_provider_response_lost")
            and not spec.get("capture")
        ],
        "replay": [
            index for index, spec in provider_unknown_specs
            if spec.get("actor") == "user_a"
            and _path_matches(spec, "POST", r"^/workbench/sandbox$")
            and _fixture_field(spec, "conversation_id") == "${provider_unknown_conversation_id}"
            and _exact_status(spec, 201)
            and spec.get("json_equals", {}).get("/status") == "provider_unknown"
            and spec.get("capture", {}).get("provider_unknown_sandbox_id") == "/sandbox_id"
        ],
        "count": [
            index for index, spec in provider_unknown_specs
            if spec.get("actor") == "user_a"
            and spec.get("method") == "GET"
            and spec.get("path") == "/workbench/sandbox/acceptance/provider-start-count" + provider_unknown_query
            and _exact_status(spec, 200)
            and spec.get("json_equals", {}).get("/provider_start_count") == 1
        ],
        "instances": [
            index for index, spec in provider_unknown_specs
            if spec.get("actor") == "user_a"
            and spec.get("method") == "GET"
            and spec.get("path") == "/workbench/sandbox/acceptance/instances" + provider_unknown_query
            and _exact_status(spec, 200)
            and spec.get("json_equals", {}).get("/provider_start_count") == 1
            and spec.get("json_equals", {}).get("/instances/0/sandbox_id") == "${provider_unknown_sandbox_id}"
            and all(pointer in spec.get("json_absent", []) for pointer in ("/instances/0/provider_instance_id", "/instances/0/provider_locator"))
            and not spec.get("cleanup")
        ],
        "cleanup": [
            index for index, spec in provider_unknown_specs
            if spec.get("actor") == "user_a"
            and _path_matches(spec, "POST", r"^/workbench/sandbox/acceptance/cleanup$")
            and _fixture_field(spec, "acceptance_run_id") == "${acceptance_run_id}"
            and _fixture_field(spec, "conversation_id") == "${provider_unknown_conversation_id}"
            and _exact_status(spec, 200)
            and spec.get("json_equals", {}).get("/complete") is True
            and spec.get("json_equals", {}).get("/cleanup_failed") == 0
            and spec.get("cleanup") is True
        ],
        "terminal": [
            index for index, spec in provider_unknown_specs
            if spec.get("actor") == "user_a"
            and spec.get("method") == "GET"
            and spec.get("path") == "/workbench/sandbox/acceptance/instances" + provider_unknown_query
            and _exact_status(spec, 200)
            and spec.get("json_equals", {}).get("/provider_start_count") == 1
            and spec.get("json_equals", {}).get("/instances/0/sandbox_id") == "${provider_unknown_sandbox_id}"
            and spec.get("json_one_of", {}).get("/instances/0/cleanup_status") == ["not_found", "stopped"]
            and spec.get("cleanup") is True
        ],
        "conversation_cleanup": [
            index for index, spec in provider_unknown_specs
            if spec.get("actor") == "user_a"
            and _path_matches(spec, "POST", r"^/workbench/chat/conversation/delete$")
            and _fixture_field(spec, "ConversationId") == "${provider_unknown_conversation_id}"
            and _exact_status(spec, 200)
            and spec.get("json_equals", {}).get("/Success") == 1
            and spec.get("cleanup") is True
        ],
    }
    if "provider_unknown_no_duplicate" in requirements and not all(provider_unknown_steps.values()):
        errors.append(
            "provider_unknown_no_duplicate: requires owned Conversation creation, injected response loss, replayed local provider_unknown, exact one-start evidence, bounded instance evidence, and complete cleanup"
        )
    elif "provider_unknown_no_duplicate" in requirements and not any(
        conversation < fault < replay < count < instances < cleanup < terminal < conversation_cleanup
        for conversation in provider_unknown_steps["conversation"]
        for fault in provider_unknown_steps["fault"]
        for replay in provider_unknown_steps["replay"]
        for count in provider_unknown_steps["count"]
        for instances in provider_unknown_steps["instances"]
        for cleanup in provider_unknown_steps["cleanup"]
        for terminal in provider_unknown_steps["terminal"]
        for conversation_cleanup in provider_unknown_steps["conversation_cleanup"]
    ):
        errors.append("provider_unknown_no_duplicate: evidence and cleanup steps are not in the required order")
    need(
        "resource_bounds", "oversized Shell command-field rejection",
        lambda spec: str(spec.get("method")) == "POST"
        and re.search(r"/shell$", str(spec.get("path", ""))) is not None
        and spec.get("actor") == "user_a"
        and int(spec.get("body_min_bytes", 0)) >= 65537
        and int(spec.get("fixture_json_min_lengths", {}).get("/command", 0)) >= 65537
        and isinstance(_fixture_field(spec, "command"), str)
        and len(_fixture_field(spec, "command")) >= 65537
        and bool(set(spec.get("expect_status", [])).intersection({400, 413}))
        and "command_invalid" in spec.get("require_response_markers", []),
    )
    need(
        "csrf_rejected", "missing/invalid CSRF mutation rejected with 403",
        lambda spec: spec.get("actor") == "user_a"
        and str(spec.get("method")) != "GET"
        and str(spec.get("path", "")).startswith("/workbench/sandbox/")
        and spec.get("csrf_mode") in {"omit", "invalid"}
        and spec.get("expect_status") == [403]
        and any("CSRF" in marker.upper() for marker in spec.get("require_response_markers", [])),
    )
    need(
        "cleanup_terminal", "finally stop mutation",
        lambda spec: spec.get("actor") == "user_a"
        and spec.get("cleanup") is True
        and _path_matches(spec, "POST", r"^/workbench/sandbox/\$\{[^}]+sandbox_id\}/stop$")
        and _exact_status(spec, 200),
    )
    need(
        "cleanup_terminal", "finally stopped-state poll",
        lambda spec: spec.get("cleanup") is True
        and spec.get("actor") == "user_a"
        and _path_matches(spec, "GET", r"^/workbench/sandbox/")
        and _exact_status(spec, 200)
        and spec.get("poll", {}).get("equals") == "stopped"
        and {"failed", "provider_unknown"}.issubset(
            set(spec.get("poll", {}).get("forbid_values", []))
        ),
    )
    create_specs_by_variable: dict[str, list[dict[str, Any]]] = {}
    for spec in specs:
        if not _path_matches(spec, "POST", r"^/workbench/sandbox$"):
            continue
        for variable in spec.get("capture", {}):
            if str(variable).endswith("sandbox_id"):
                create_specs_by_variable.setdefault(str(variable), []).append(spec)
    for variable, create_specs in sorted(create_specs_by_variable.items()):
        if len(create_specs) != 1:
            errors.append(f"lifecycle_ownership: sandbox capture variable {variable} is reused by {len(create_specs)} create specs")
            continue
        create_spec = create_specs[0]
        conversation_ref = _fixture_field(create_spec, "conversation_id")
        if create_spec.get("actor") != "user_a" or not isinstance(conversation_ref, str) or not conversation_ref.startswith("${"):
            errors.append(f"lifecycle_ownership: captured {variable} is not bound to user_a and one captured Conversation")
            continue
        if variable == "provider_unknown_sandbox_id":
            continue
        stop_path = f"/workbench/sandbox/${{{variable}}}/stop"
        query_path = f"/workbench/sandbox/${{{variable}}}?conversation_id={conversation_ref}"
        if not any(
            spec.get("cleanup") is True
            and spec.get("actor") == "user_a"
            and spec.get("method") == "POST"
            and spec.get("path") == stop_path
            and _exact_status(spec, 200)
            and _fixture_field(spec, "conversation_id") == conversation_ref
            for spec in specs
        ):
            errors.append(f"cleanup_terminal: captured {variable} has no finally stop")
        if not any(
            spec.get("cleanup") is True
            and spec.get("actor") == "user_a"
            and spec.get("method") == "GET"
            and spec.get("path") == query_path
            and _exact_status(spec, 200)
            and spec.get("poll", {}).get("equals") == "stopped"
            and {"failed", "provider_unknown"}.issubset(set(spec.get("poll", {}).get("forbid_values", [])))
            for spec in specs
        ):
            errors.append(f"cleanup_terminal: captured {variable} has no stopped-state confirmation")
    return errors


def nested_forbidden(value: Any) -> str | None:
    if isinstance(value, dict):
        for key, item in value.items():
            if str(key).lower() in FORBIDDEN_TURN_KEYS:
                return str(key)
            found = nested_forbidden(item)
            if found:
                return found
    elif isinstance(value, list):
        for item in value:
            found = nested_forbidden(item)
            if found:
                return found
    return None


class Runner:
    def __init__(self, config: dict[str, Any], args: argparse.Namespace, *, run_id: str = "", started_at: str = "") -> None:
        self.config = config
        self.args = args
        self.paths = {**DEFAULT_PATHS, **config.get("paths", {})}
        self.variables = dict(config.get("variables", {}))
        self.run_id = run_id or str(uuid.uuid4())
        self.started_at = started_at or utc_now()
        self.variables["acceptance_run_id"] = self.run_id
        self.redactor = Redactor()
        self.results: list[CheckResult] = []
        self.order = 0
        self.secret_errors: dict[str, str] = {}
        self.secrets: dict[str, str] = {}
        self.clients: dict[str, HTTPClient] = {}
        self.identities: dict[str, dict[str, Any]] = {}
        self.consumed_selection_tokens: dict[str, str] = {}
        self.selection_option_tokens: dict[str, list[tuple[dict[str, str], str]]] = {}
        self.selection_pending_actors: set[str] = set()
        for value in self.variables.values():
            self.redactor.add(str(value))
        self._resolve_secrets()

    def acceptance_observer_evidence(self, phase: str, contract: str) -> None:
        observer = self.config.get("acceptance_observer", {})
        evidence_map = observer.get("evidence", {})
        allowed = observer.get("allowed_signers_file", "")
        manifest_digest = hashlib.sha256(
            json.dumps(self.config.get("release_manifest", {}), sort_keys=True, separators=(",", ":")).encode("utf-8")
        ).hexdigest()
        for qualified, ref in sorted(evidence_map.items()):
            if not qualified.startswith(contract + "."):
                continue
            requirement = qualified.split(".", 1)[1]
            name = f"observer evidence: {qualified}"
            if not self.args.execute:
                self.add(phase, name, "skipped", "dry-run: signed observer evidence was not read", evidence={"requirement_id": requirement})
                continue
            path = str(expand_variables(ref, self.variables))[5:]
            deadline = time.monotonic() + int(observer.get("wait_seconds", 60))
            while not Path(path).is_file() and time.monotonic() < deadline:
                time.sleep(0.2)
            try:
                raw = verify_acceptance_observer_evidence(path, str(allowed))
                value = json.loads(raw)
                if not isinstance(value, dict):
                    raise ConfigError("observer evidence root must be an object")
                expected_keys = {
                    "schema_version", "acceptance_run_id", "release_manifest_sha256",
                    "requirement", "observed_from", "observed_until", "collected_at",
                    "source", "facts",
                }
                if set(value) != expected_keys or value.get("schema_version") != 1:
                    raise ConfigError("observer evidence schema is not exact v1")
                if value.get("acceptance_run_id") != self.run_id:
                    raise ConfigError("observer evidence run_id does not match")
                if value.get("release_manifest_sha256") != manifest_digest:
                    raise ConfigError("observer evidence release digest does not match")
                if value.get("requirement") != qualified:
                    raise ConfigError("observer evidence requirement does not match")
                timestamps = []
                for field in ("observed_from", "observed_until", "collected_at"):
                    parsed = datetime.fromisoformat(str(value.get(field, "")).replace("Z", "+00:00"))
                    if parsed.tzinfo is None:
                        raise ValueError("timezone missing")
                    timestamps.append(parsed.astimezone(timezone.utc))
                run_started = datetime.fromisoformat(self.started_at.replace("Z", "+00:00")).astimezone(timezone.utc)
                observed_from, observed_until, collected_at = timestamps
                if not observed_from <= run_started <= observed_until <= collected_at:
                    raise ConfigError("observer evidence time window does not cover this acceptance run")
                age = (datetime.now(timezone.utc) - collected_at).total_seconds()
                if age < -300 or age > int(observer.get("max_age_seconds", 900)):
                    raise ConfigError("observer evidence is stale or from the future")
                source = value.get("source")
                if (not isinstance(source, dict) or set(source) != {"kind", "query_sha256", "read_only", "row_count"}
                        or source.get("kind") not in {"postgresql", "prometheus", "artifact_scan"}
                        or source.get("read_only") is not True
                        or not isinstance(source.get("row_count"), int) or source["row_count"] < 1
                        or not re.fullmatch(r"[0-9a-f]{64}", str(source.get("query_sha256", "")))):
                    raise ConfigError("observer evidence source is not a bounded read-only query")
                fact_name, expected = OBSERVER_FACTS[qualified]
                if value.get("facts") != {fact_name: expected}:
                    raise ConfigError("observer evidence fact is missing or not exact")
                self.add(
                    phase, name, "pass", "independently signed read-only evidence verified",
                    evidence={
                        "requirement_id": requirement,
                        "assertion_types": ["signed_observer_evidence", "run_binding", "release_binding", "time_window", "exact_fact"],
                        "source_kind": source["kind"],
                    },
                )
            except (OSError, ValueError, json.JSONDecodeError, ConfigError) as error:
                self.add(phase, name, "blocker", f"{type(error).__name__}: {error}", evidence={"requirement_id": requirement})

    def sandbox_pty_contract(self, phase: str, ticket_spec: dict[str, Any]) -> None:
        name = "bounded interactive PTY input, resize, Ctrl-C, and close"
        if not self.args.execute:
            self.add(phase, name, "skipped", "dry-run: WebSocket was not opened", evidence={"requirement_id": "pty_interactive"})
            return
        config = self.config.get("sandbox_pty", {})
        actor = str(config.get("actor", ticket_spec.get("actor", "user_a")))
        client = self.actor_client(actor)
        socket_client: BoundedWebSocket | None = None
        try:
            if client is None:
                raise ConfigError("PTY actor session is unavailable")
            conversation = self.variables[str(config["conversation_variable"])]
            sandbox = self.variables[str(config["sandbox_variable"])]
            rows, cols = int(config.get("rows", 24)), int(config.get("cols", 80))
            ticket_path = expand_variables(str(config["ticket_path"]), {**self.variables, "sandbox_id": sandbox})
            headers = {"Content-Type": "application/json"}
            csrf = client.cookies.get("claw_workbench_csrf")
            if csrf:
                headers["X-Workbench-CSRF"] = csrf.value
            response = client.request(
                "POST", ticket_path,
                body=json_bytes({"conversation_id": conversation, "rows": rows, "cols": cols, "timeout_seconds": int(config.get("timeout_seconds", 30))}),
                headers=headers,
            )
            payload = response.json()
            if response.status != 201 or not isinstance(payload, dict) or payload.get("protocol") != "claw-workbench-pty-v1":
                raise WebSocketContractError("PTY ticket response is invalid")
            ticket = str(payload.get("ticket", ""))
            if not re.fullmatch(r"[A-Za-z0-9_-]{32,128}", ticket):
                raise WebSocketContractError("PTY ticket format is invalid")
            self.redactor.add(ticket)
            connect_path = expand_variables(str(config["connect_path"]), {**self.variables, "sandbox_id": sandbox})
            base = urlsplit(self.config["base_url"])
            websocket_url = f"{'wss' if base.scheme == 'https' else 'ws'}://{base.netloc}{connect_path}"
            cookie = client._cookie_header(connect_path, base.scheme)
            socket_client = BoundedWebSocket.connect(
                websocket_url, origin=origin(self.config["base_url"]), cookie=cookie,
                protocols=["claw-workbench-pty-v1", "ticket." + ticket],
                timeout=int(config.get("timeout_seconds", 30)),
            )
            opcode, ready = socket_client.recv()
            ready_value = json.loads(ready) if opcode == 0x1 else None
            if not isinstance(ready_value, dict) or set(ready_value) != {"type", "pty_session_id"} or ready_value["type"] != "ready":
                raise WebSocketContractError("PTY ready control frame is invalid")
            frames = [
                {"type": "input", "data": "printf 'CLAW_PTY_INPUT_OK\\n'\r"},
                {"type": "resize", "rows": rows, "cols": cols},
                {"type": "input", "data": "stty size; printf 'CLAW_PTY_RESIZE_OK\\n'\r"},
                {"type": "input", "data": "sleep 30; printf 'CLAW_PTY_CTRL_C_FAILED\\n'\r"},
            ]
            for frame in frames:
                socket_client.send(0x1, json_bytes(frame))
            time.sleep(0.25)
            socket_client.send(0x1, json_bytes({"type": "input", "data": "\u0003"}))
            socket_client.send(0x1, json_bytes({"type": "input", "data": "printf 'CLAW_PTY_CTRL_C_OK\\n'\r"}))
            output = bytearray()
            deadline = time.monotonic() + int(config.get("timeout_seconds", 30))
            required_markers = (b"CLAW_PTY_INPUT_OK", b"CLAW_PTY_RESIZE_OK", b"CLAW_PTY_CTRL_C_OK", f"{rows} {cols}".encode())
            while time.monotonic() < deadline and not all(marker in output for marker in required_markers):
                opcode, data = socket_client.recv()
                if opcode == 0x2:
                    output.extend(data)
                    if len(output) > 256 * 1024:
                        raise WebSocketContractError("PTY acceptance output exceeded 256 KiB")
                elif opcode == 0x8:
                    raise WebSocketContractError("PTY closed before acceptance markers")
            if not all(marker in output for marker in required_markers) or b"CLAW_PTY_CTRL_C_FAILED" in output:
                raise WebSocketContractError("PTY input/resize/Ctrl-C markers did not prove the contract")
            socket_client.send(0x1, json_bytes({"type": "close"}))
            self.add(phase, name, "pass", "bounded PTY protocol completed", evidence={
                "requirement_id": "pty_interactive",
                "assertion_types": ["websocket_upgrade", "one_time_ticket", "ready_frame", "input", "resize", "ctrl_c", "bounded_output", "normal_close"],
                "output_bytes": len(output),
            })
        except (KeyError, ValueError, OSError, json.JSONDecodeError, ConfigError, WebSocketContractError) as error:
            self.add(phase, name, "blocker", f"{type(error).__name__}: {error}", evidence={"requirement_id": "pty_interactive"})
        finally:
            if socket_client is not None:
                socket_client.close()

    def _resolve_secrets(self) -> None:
        for name, ref in self.config.get("secrets", {}).items():
            try:
                value = resolve_secret(str(ref))
                self.secrets[name] = value
                self.redactor.add(value)
            except ConfigError as error:
                self.secret_errors[name] = str(error)

    def add(
        self,
        phase: str,
        name: str,
        status: str,
        message: str,
        response: Any = None,
        *,
        endpoint: str = "",
        evidence: dict[str, Any] | None = None,
        duration_ms: float = 0,
    ) -> None:
        self.order += 1
        self.results.append(
            CheckResult(
                self.order,
                phase,
                name,
                status,
                message,
                response.endpoint if response else sanitized_endpoint(endpoint) if endpoint else "",
                response.status if response else None,
                response.elapsed_ms if response else duration_ms,
                evidence or {},
            )
        )

    def actor_client(self, actor: str) -> HTTPClient | None:
        if actor in self.clients:
            return self.clients[actor]
        secret_name = {
            "admin": "admin_session",
            "admin_requester": "admin_requester_session",
            "admin_approver": "admin_approver_session",
            "user_a": "user_a_session",
            "user_b_same": "user_b_same_customer_session",
            "user_b_other": "user_b_other_customer_session",
            "user_a_context_1": "user_a_session",
            "user_a_context_2": "user_a_session",
            "user_a_customer_2": "user_a_session",
            "user_a_stale_context": "user_a_session",
            "api_key": "model_api_key",
        }.get(actor)
        if actor == "anonymous":
            client = HTTPClient(self.config["base_url"], timeout=self.config.get("timeout_seconds", 20))
        elif not secret_name or secret_name not in self.secrets:
            return None
        else:
            client = HTTPClient(
                self.config["base_url"],
                timeout=self.config.get("timeout_seconds", 20),
                initial_cookie=self.secrets[secret_name] if actor != "api_key" else "",
                bearer=self.secrets[secret_name] if actor == "api_key" else "",
            )
        self.clients[actor] = client
        return client

    def missing_actor(self, phase: str, name: str, actor: str) -> None:
        secret_name = {
            "admin": "admin_session", "admin_requester": "admin_requester_session",
            "admin_approver": "admin_approver_session", "user_a": "user_a_session",
            "user_b_same": "user_b_same_customer_session",
            "user_b_other": "user_b_other_customer_session", "api_key": "model_api_key",
            "user_a_context_1": "user_a_session", "user_a_context_2": "user_a_session",
            "user_a_customer_2": "user_a_session",
            "user_a_stale_context": "user_a_session",
        }.get(actor, actor)
        detail = self.secret_errors.get(secret_name, f"required {secret_name} reference is not configured")
        self.add(phase, name, "blocker", detail, evidence={"actor": actor})

    def dry(self, phase: str, name: str, why: str = "dry-run: no network or mutation performed") -> None:
        self.add(phase, name, "skipped", why)

    def generic_request(self, phase: str, spec: dict[str, Any], *, restore: bool = False, required: bool = True) -> bool:
        name = str(spec.get("name", "unnamed fixture"))
        actor = str(spec.get("actor", "anonymous"))
        method = str(spec.get("method", "GET")).upper()
        declared_assertions = sorted(
            (["http_status"] if spec.get("expect_status") else [])
            + [
                key
                for key in (
                    "capture", "capture_sha256", "json_equals", "json_not_equals",
                    "json_one_of", "json_absent", "json_types", "header_equals", "poll",
                    "require_response_markers", "forbid_response_markers",
                    "assert_variables_equal", "assert_variables_not_equal",
                    "fixture_json_min_lengths",
                )
                if spec.get(key)
            ]
        )
        declared_evidence: dict[str, Any] = {"assertion_types": declared_assertions}
        if spec.get("requirement"):
            declared_evidence["requirement_id"] = spec["requirement"]
        if not self.args.execute:
            self.dry(phase, name)
            return True
        if spec.get("provider_cost") and not self.args.allow_provider_cost:
            self.add(phase, name, "blocker" if required else "skipped", "provider-cost fixture requires --allow-provider-cost", evidence=declared_evidence)
            return not required
        if method != "GET" and not (self.args.allow_mutations or spec.get("provider_cost")):
            self.add(phase, name, "blocker" if required else "skipped", "non-GET fixture requires --allow-mutations or provider-cost authorization", evidence=declared_evidence)
            return not required
        client = self.actor_client(actor)
        if client is None:
            secret_name = {
                "admin": "admin_session", "admin_requester": "admin_requester_session",
                "admin_approver": "admin_approver_session", "user_a": "user_a_session",
                "user_b_same": "user_b_same_customer_session",
                "user_b_other": "user_b_other_customer_session", "api_key": "model_api_key",
                "user_a_context_1": "user_a_session", "user_a_context_2": "user_a_session",
                "user_a_customer_2": "user_a_session", "user_a_stale_context": "user_a_session",
            }.get(actor, actor)
            evidence = {**declared_evidence, "actor": actor}
            self.add(
                phase,
                name,
                "blocker",
                self.secret_errors.get(secret_name, f"required {secret_name} reference is not configured"),
                evidence=evidence,
            )
            return False
        try:
            path = expand_variables(str(spec["path"]), self.variables)
            body = None
            headers: dict[str, str] = {}
            if spec.get("body_fixture"):
                fixture_payload = load_json_fixture(str(spec["body_fixture"]), self.variables)
                for pointer, minimum in spec.get("fixture_json_min_lengths", {}).items():
                    present, value = find_pointer_state(fixture_payload, str(pointer))
                    if not present or not isinstance(value, str) or len(value) < int(minimum):
                        raise ConfigError(
                            f"request fixture field {pointer} must be a string at least {minimum} characters long"
                        )
                body = json_bytes(fixture_payload)
                headers["Content-Type"] = "application/json"
                if spec.get("body_min_bytes") and len(body) < int(spec["body_min_bytes"]):
                    raise ConfigError(
                        f"serialized request fixture is smaller than declared body_min_bytes={spec['body_min_bytes']}"
                    )
            if spec.get("selection_token_source"):
                token_actor = str(spec["selection_token_actor"])
                if spec["selection_token_source"] == "consumed":
                    token = self.consumed_selection_tokens.get(token_actor, "")
                else:
                    target = {str(key): str(value) for key, value in spec["selection_target"].items()}
                    selected_target = {
                        str(key): str(value)
                        for key, value in self.config.get("selection_targets", {}).get(token_actor, {}).items()
                    }
                    if token_actor == "user_a_stale_context":
                        if token_actor not in self.selection_pending_actors:
                            raise ConfigError("stale selection proof requires an unconsumed selection-pending browser context")
                        if target != selected_target:
                            raise ConfigError("stale selection target must be the server option captured for the pending context")
                    elif target == selected_target:
                        raise ConfigError("stale option target must differ from the actor's consumed selection target")
                    matches = [
                        token
                        for option, token in self.selection_option_tokens.get(token_actor, [])
                        if all(option.get(key) == value for key, value in target.items())
                    ]
                    if len(matches) != 1:
                        raise ConfigError("selection_target must match exactly one captured server option")
                    token = matches[0]
                if not token:
                    raise ConfigError("requested server-issued selection token is unavailable")
                body = json_bytes({"selection_token": token})
                headers["Content-Type"] = "application/json"
            csrf_mode = str(spec.get("csrf_mode", "auto"))
            if actor in {"admin", "admin_requester", "admin_approver"} and method != "GET":
                csrf = client.cookies.get("claw_admin_csrf")
                if csrf and csrf_mode == "auto":
                    headers["X-CSRF-Token"] = csrf.value
                elif csrf_mode == "invalid":
                    headers["X-CSRF-Token"] = "invalid-csrf-proof-value"
            elif actor.startswith("user_") and method != "GET":
                csrf = client.cookies.get("claw_workbench_csrf")
                if csrf and csrf_mode == "auto":
                    headers["X-Workbench-CSRF"] = csrf.value
                elif csrf_mode == "invalid":
                    headers["X-Workbench-CSRF"] = "invalid-csrf-proof-value"
            acceptance_evidence_path = path.startswith("/workbench/sandbox/acceptance/")
            acceptance_fault = (
                spec.get("requirement") == "provider_unknown_no_duplicate"
                and method == "POST"
                and path == "/workbench/sandbox"
                and _exact_status(spec, 503)
                and _exact_marker(spec, "acceptance_provider_response_lost")
            )
            if acceptance_evidence_path or acceptance_fault:
                acceptance_token = self.secrets.get("sandbox_acceptance_token", "")
                if not acceptance_token:
                    raise ConfigError(
                        self.secret_errors.get(
                            "sandbox_acceptance_token",
                            "secrets.sandbox_acceptance_token is required for isolated sandbox fault acceptance",
                        )
                    )
                public_origin = origin(self.config["base_url"])
                headers.update(
                    {
                        "Origin": public_origin,
                        "Referer": public_origin + "/workbench/",
                        "X-Workbench-Acceptance-Token": acceptance_token,
                        "X-Workbench-Acceptance-Run-Id": self.run_id,
                    }
                )
                if acceptance_fault:
                    headers["X-Workbench-Acceptance-Fault"] = "provider_start_response_lost"
            response = client.request(method, path, body=body, headers=headers)
            expected = [int(item) for item in spec.get("expect_status", [])]
            poll = expand_variables(spec.get("poll"), self.variables) if spec.get("poll") else None
            poll_attempts = 1
            poll_satisfied = poll is None
            poll_forbidden = False
            poll_states: list[Any] = []
            if poll is not None:
                if method != "GET":
                    raise ConfigError("poll is allowed only on GET fixtures")
                deadline = time.monotonic() + float(poll.get("timeout_seconds", 60))
                interval = float(poll.get("interval_seconds", 1))
                while True:
                    try:
                        present, current_state = find_pointer_state(response.json(), str(poll["pointer"]))
                        if present:
                            poll_states.append(current_state)
                        poll_forbidden = present and current_state in poll.get("forbid_values", [])
                        poll_satisfied = present and response.status in expected and current_state == poll.get("equals")
                    except Exception:
                        poll_satisfied = False
                    if poll_satisfied or poll_forbidden or time.monotonic() >= deadline:
                        break
                    time.sleep(interval)
                    response = client.request(method, path, headers=headers)
                    poll_attempts += 1
            ok = response.status in expected
            required_markers = spec.get("require_response_markers", [])
            forbidden_markers = spec.get("forbid_response_markers", [])
            text = response.body.decode("utf-8", "ignore")
            missing = [marker for marker in required_markers if marker not in text]
            forbidden = [marker for marker in forbidden_markers if marker in text]
            json_mismatches = 0
            json_absence_mismatches = 0
            json_type_mismatches = 0
            header_mismatches = 0
            json_equals = expand_variables(spec.get("json_equals", {}), self.variables)
            json_not_equals = expand_variables(spec.get("json_not_equals", {}), self.variables)
            json_one_of = expand_variables(spec.get("json_one_of", {}), self.variables)
            json_absent = spec.get("json_absent", [])
            json_types = spec.get("json_types", {})
            if json_equals or json_not_equals or json_one_of or json_absent or json_types:
                try:
                    payload = response.json()
                    for pointer, expected_value in json_equals.items():
                        present, actual = find_pointer_state(payload, str(pointer))
                        json_mismatches += not present or actual != expected_value
                    for pointer, unexpected_value in json_not_equals.items():
                        present, actual = find_pointer_state(payload, str(pointer))
                        json_mismatches += not present or actual == unexpected_value
                    for pointer, allowed_values in json_one_of.items():
                        present, actual = find_pointer_state(payload, str(pointer))
                        json_mismatches += not present or actual not in allowed_values
                    json_absence_mismatches = sum(
                        find_pointer_state(payload, str(pointer))[0]
                        for pointer in json_absent
                    )
                    type_names = {
                        type(None): "null", bool: "boolean", str: "string",
                        int: "integer", float: "number", dict: "object", list: "array",
                    }
                    for pointer, expected_type in json_types.items():
                        present, value = find_pointer_state(payload, str(pointer))
                        actual_type = type_names.get(type(value), "unknown") if present else "missing"
                        if expected_type == "number" and actual_type == "integer":
                            actual_type = "number"
                        json_type_mismatches += actual_type != expected_type
                except Exception:
                    json_mismatches = len(json_equals) + len(json_not_equals) + len(json_one_of)
                    json_absence_mismatches = len(json_absent)
                    json_type_mismatches = len(json_types)
            for header, expected_value in expand_variables(spec.get("header_equals", {}), self.variables).items():
                header_mismatches += str(response.headers.get(str(header), "")) != str(expected_value)
            variable_mismatches = 0
            for left, right in spec.get("assert_variables_equal", {}).items():
                variable_mismatches += left not in self.variables or right not in self.variables or self.variables[left] != self.variables[right]
            for left, right in spec.get("assert_variables_not_equal", {}).items():
                variable_mismatches += left not in self.variables or right not in self.variables or self.variables[left] == self.variables[right]
            if missing or forbidden or json_mismatches or json_absence_mismatches or json_type_mismatches or header_mismatches or variable_mismatches or not poll_satisfied or poll_forbidden:
                ok = False
            assertion_types = declared_assertions
            evidence = {
                "expected_status": expected,
                "response_bytes": len(response.body),
                "poll_attempts": poll_attempts,
                "assertion_types": assertion_types,
            }
            if spec.get("requirement"):
                evidence["requirement_id"] = spec["requirement"]
            if not poll_satisfied:
                evidence["poll_satisfied"] = False
            if poll_states:
                evidence["poll_state_sequence"] = poll_states
            if poll_forbidden:
                evidence["poll_forbidden_state_seen"] = True
            if json_mismatches:
                evidence["json_mismatch_count"] = json_mismatches
            if json_absence_mismatches:
                evidence["json_absence_mismatch_count"] = json_absence_mismatches
            if json_type_mismatches:
                evidence["json_type_mismatch_count"] = json_type_mismatches
            if header_mismatches:
                evidence["header_mismatch_count"] = header_mismatches
            if variable_mismatches:
                evidence["variable_mismatch_count"] = variable_mismatches
            if missing:
                evidence["missing_required_marker_count"] = len(missing)
            if forbidden:
                evidence["forbidden_marker_count"] = len(forbidden)
            if ok and spec.get("capture"):
                payload = response.json()
                for variable, pointer in spec["capture"].items():
                    captured = find_pointer(payload, str(pointer))
                    if captured is None:
                        raise RuntimeError(f"capture {variable} was not present")
                    self.variables[str(variable)] = captured
                    self.redactor.add(str(captured))
                evidence["captured_variable_count"] = len(spec["capture"])
            if ok and spec.get("capture_sha256"):
                payload = response.json()
                captured_hashes: dict[str, str] = {}
                for variable, pointer in spec["capture_sha256"].items():
                    present, captured = find_pointer_state(payload, str(pointer))
                    if not present:
                        raise RuntimeError(f"capture_sha256 {variable} was not present")
                    canonical = json.dumps(captured, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
                    digest = hashlib.sha256(canonical).hexdigest()
                    self.variables[str(variable)] = digest
                    captured_hashes[str(variable)] = digest
                evidence["captured_sha256_count"] = len(spec["capture_sha256"])
                evidence["captured_sha256"] = captured_hashes
            self.add(phase, name, "pass" if ok else "fail", "expected response" if ok else "unexpected response", response, evidence=evidence)
            return ok
        except Exception as error:
            self.add(
                phase,
                name,
                "fail" if restore else "blocker",
                f"{type(error).__name__}: {error}",
                evidence=declared_evidence,
            )
            return False

    def fixture_phase(
        self,
        phase: str,
        name: str,
        specs: list[dict[str, Any]] | None,
        *,
        mutations: bool = False,
        required: bool = True,
        provider_cost: bool = False,
        requirements: set[str] | None = None,
        contract: str = "",
        semantic_key: str = "",
    ) -> None:
        if not specs:
            self.add(phase, name, "blocker" if required else "skipped", "no explicit acceptance fixture configured")
            return
        if requirements:
            observer_keys = set(self.config.get("acceptance_observer", {}).get("evidence", {}))
            declared = {
                str(spec.get("requirement"))
                for spec in specs
                if isinstance(spec.get("requirement"), str)
            }.union({item.split(".", 1)[1] for item in observer_keys if item.startswith(contract + ".")})
            missing = sorted(requirements.difference(declared))
            unknown = sorted(declared.difference(requirements))
            if missing or unknown:
                self.add(
                    phase,
                    f"{name} coverage contract",
                    "blocker",
                    "acceptance requirement coverage is incomplete or contains unknown IDs",
                    evidence={"missing_requirements": missing, "unknown_requirements": unknown},
                )
            semantic_errors = (
                closed_phase_semantic_errors(semantic_key, specs, requirements)
                if semantic_key else
                requirement_semantic_errors(contract, specs, requirements, observer_keys=observer_keys)
            )
            if semantic_errors:
                self.add(
                    phase,
                    f"{name} semantic contract",
                    "blocker",
                    "acceptance requirement labels are not backed by the required actor/method/path/assertion evidence",
                    evidence={"semantic_errors": semantic_errors},
                )
            if missing or unknown or semantic_errors:
                return
        if mutations and not self.args.allow_mutations:
            self.add(phase, name, "blocker" if self.args.execute and required else "skipped", "requires --allow-mutations")
            return
        if provider_cost and not self.args.allow_provider_cost:
            self.add(phase, name, "blocker" if self.args.execute and required else "skipped", "requires --allow-provider-cost")
            return
        pty_specs = [spec for spec in specs if spec.get("requirement") == "pty_interactive"]
        normal_specs = [spec for spec in specs if not spec.get("cleanup") and spec.get("requirement") != "pty_interactive"]
        cleanup_specs = [spec for spec in specs if spec.get("cleanup")]
        recovery_specs = [
            spec
            for spec in normal_specs
            if str(spec.get("method", "")).upper() == "POST"
            and spec.get("path") == "/workbench/sandbox"
            and isinstance(spec.get("capture"), dict)
            and any(str(variable).endswith("sandbox_id") for variable in spec["capture"])
        ]
        try:
            for spec in normal_specs:
                effective_spec = {**spec, "provider_cost": True} if provider_cost else spec
                self.generic_request(phase, effective_spec, required=required)
            if pty_specs:
                if len(pty_specs) != 1:
                    self.add(phase, "PTY execution contract", "blocker", "exactly one PTY ticket spec is required", evidence={"requirement_id": "pty_interactive"})
                else:
                    self.sandbox_pty_contract(phase, pty_specs[0])
        finally:
            # A create may have reached the provider while its response was lost.
            # Recover by the exact owner/conversation scope through a read-only
            # lookup. Replaying POST here could create a second provider instance
            # if either side ever regressed its idempotency contract.
            for spec in recovery_specs:
                captured_variables = list(spec.get("capture", {}))
                if any(variable not in self.variables for variable in captured_variables):
                    conversation_ref = _fixture_field(spec, "conversation_id")
                    if not isinstance(conversation_ref, str) or not conversation_ref.startswith("${"):
                        self.add(
                            phase,
                            f"cleanup lookup recovery: {spec.get('name', 'sandbox create')}",
                            "fail",
                            "sandbox create fixture is not bound to a captured conversation",
                        )
                        continue
                    recovery = {
                        "name": f"cleanup lookup recovery: {spec.get('name', 'sandbox create')}",
                        "requirement": spec.get("requirement"),
                        "actor": spec.get("actor"),
                        "method": "GET",
                        "path": f"/workbench/sandbox?conversation_id={conversation_ref}",
                        "expect_status": [200],
                        "capture": spec.get("capture", {}),
                    }
                    self.generic_request(phase, recovery, restore=True, required=True)
            for spec in cleanup_specs:
                effective_spec = {**spec, "provider_cost": True} if provider_cost else spec
                self.generic_request(phase, effective_spec, restore=True, required=True)
        if contract:
            self.acceptance_observer_evidence(phase, contract)

    def legacy(self) -> None:
        phase = "19.3.1 legacy/preflight"
        manifest = self.config.get("release_manifest")
        manifest_validation_errors = release_manifest_errors(
            manifest,
            max_age_seconds=1800 if self.args.execute else None,
        )
        manifest_ok = not manifest_validation_errors
        self.add(
            phase,
            "acceptance report is bound to immutable release metadata",
            "pass" if manifest_ok else "blocker",
            "release metadata is complete" if manifest_ok else "release_manifest is missing or invalid",
            evidence={"required_field_count": len(RELEASE_MANIFEST_FIELDS), "validation_errors": manifest_validation_errors},
        )
        if not self.args.execute:
            self.dry(phase, "legacy playground remains reachable")
            return
        client = self.actor_client("user_a")
        if not client:
            self.missing_actor(phase, "legacy playground remains reachable", "user_a")
            return
        try:
            response = client.request("GET", self.paths["legacy"])
            self.add(phase, "legacy playground remains reachable", "pass" if response.status == 200 else "fail", "legacy route reachable" if response.status == 200 else "legacy route regression", response, evidence={"expected_status": [200]})
        except Exception as error:
            self.add(phase, "legacy playground remains reachable", "fail", f"{type(error).__name__}: {error}")

    def bootstrap(self, actor: str, *, admin: bool = False, selection_only: bool = False) -> bool:
        phase = "19.3.6 SSO and identity"
        client = self.actor_client(actor)
        if not client:
            self.missing_actor(phase, f"{actor} session bootstrap", actor)
            return False
        if not self.args.execute:
            self.dry(phase, f"{actor} session bootstrap and one-time replay")
            return False
        try:
            ticket_path = self.paths["admin_ticket" if admin else "user_ticket"]
            headers = {"Origin": origin(self.config["base_url"]), "Referer": origin(self.config["base_url"]) + "/"}
            ticket_response = client.request("POST", ticket_path, body=b"{}", headers={**headers, "Content-Type": "application/json"})
            if ticket_response.status not in {200, 201}:
                self.add(phase, f"{actor} session ticket", "fail", "ticket issue failed", ticket_response, evidence={"expected_status": [200, 201]})
                return False
            ticket = find_key(ticket_response.json(), "ticket")
            if not ticket:
                raise RuntimeError("ticket response omitted ticket")
            self.redactor.add(str(ticket))
            entry_path = f"{self.paths['entry']}?ticket={quote(str(ticket), safe='')}"
            entry = client.request("GET", entry_path)
            if entry.status not in {302, 303, 307, 308}:
                self.add(phase, f"{actor} entry", "fail", "entry did not redirect", entry)
                return False
            location = entry.headers.get("Location", "")
            redirect_path = parse_location(self.config["base_url"], location)
            query = urlsplit(redirect_path).query
            self.redactor.add(query)
            entry_replay = client.request("GET", entry_path)
            replay_ok = entry_replay.status in {400, 401, 403, 404, 409, 410}
            self.add(phase, f"{actor} entry ticket replay rejected", "pass" if replay_ok else "fail", "one-time entry ticket enforced" if replay_ok else "entry ticket replay accepted", entry_replay, evidence={"expected_reject": True})
            if admin:
                self.add(phase, f"{actor} admin session bootstrap", "pass", "admin edge session established", entry)
                return True
            if urlsplit(redirect_path).path == "/playground/select":
                options_response = client.request("GET", self.paths["selections"])
                if options_response.status != 200:
                    self.add(phase, f"{actor} context selection", "fail", "selection list failed", options_response, evidence={"expected_status": [200]})
                    return False
                options = find_key(options_response.json(), "data")
                if not isinstance(options, list) or not options:
                    self.add(phase, f"{actor} context selection", "fail", "selection list was empty or malformed", options_response)
                    return False
                captured_options: list[tuple[dict[str, str], str]] = []
                for option in options:
                    if not isinstance(option, dict):
                        continue
                    option_token = str(option.get("selection_token", ""))
                    if not option_token:
                        continue
                    self.redactor.add(option_token)
                    captured_options.append((
                        {
                            "customer_code": str(option.get("customer_code", "")),
                            "app_selector": str(option.get("app_selector", "")),
                        },
                        option_token,
                    ))
                self.selection_option_tokens[actor] = captured_options
                target = self.config.get("selection_targets", {}).get(actor, {})
                matches = [
                    option for option in options
                    if isinstance(option, dict)
                    and all(str(option.get(key, "")) == str(value) for key, value in target.items())
                ]
                if len(matches) != 1:
                    self.add(
                        phase,
                        f"{actor} context selection",
                        "blocker",
                        "selection target must match exactly one authorized option",
                        options_response,
                        evidence={"option_count": len(options), "match_count": len(matches), "target_field_count": len(target)},
                    )
                    return False
                selection_token = str(matches[0].get("selection_token", ""))
                if not selection_token:
                    self.add(phase, f"{actor} context selection", "fail", "selected option omitted its opaque token", options_response)
                    return False
                self.redactor.add(selection_token)
                if selection_only:
                    self.selection_pending_actors.add(actor)
                    self.add(
                        phase,
                        f"{actor} pending context selection captured",
                        "pass",
                        "server-issued selection options captured before the controlled auth-epoch change",
                        options_response,
                        evidence={"option_count": len(captured_options), "target_field_count": len(target)},
                    )
                    return True
                self.consumed_selection_tokens[actor] = selection_token
                choice_body = json_bytes({"selection_token": selection_token})
                choice = client.request("POST", self.paths["selection_choose"], body=choice_body, headers={"Content-Type": "application/json"})
                if choice.status != 200:
                    self.add(phase, f"{actor} context selection", "fail", "context selection failed", choice, evidence={"expected_status": [200]})
                    return False
                redirect_url = find_key(choice.json(), "redirect_url")
                if not isinstance(redirect_url, str) or not redirect_url:
                    self.add(phase, f"{actor} context selection", "fail", "selection response omitted its local SSO redirect", choice)
                    return False
                redirect_path = parse_location(self.config["base_url"], redirect_url)
                self.redactor.add(urlsplit(redirect_path).query)
                replay = client.request("POST", self.paths["selection_choose"], body=choice_body, headers={"Content-Type": "application/json"})
                selection_ok = replay.status in {400, 401, 403, 404, 409, 410}
                self.add(
                    phase,
                    f"{actor} context selection token replay rejected",
                    "pass" if selection_ok else "fail",
                    "one-time context selection enforced" if selection_ok else "selection token replay accepted",
                    replay,
                    evidence={"option_count": len(options), "target_field_count": len(target), "expected_reject": True},
                )
                if not selection_ok:
                    return False
            sso = client.request("GET", redirect_path)
            if sso.status not in {302, 303, 307, 308}:
                self.add(phase, f"{actor} ADP SSO", "fail", "ADP SSO did not redirect", sso)
                return False
            sso_replay = client.request("GET", redirect_path)
            replay_ok = sso_replay.status in {400, 401, 403, 404, 409, 410}
            self.add(phase, f"{actor} ADP SSO replay rejected", "pass" if replay_ok else "fail", "one-time ADP SSO enforced" if replay_ok else "ADP SSO replay accepted", sso_replay, evidence={"expected_reject": True})
            root = client.request("GET", self.paths["workbench_root"])
            root_ok = root.status == 200
            self.add(phase, f"{actor} workbench entry", "pass" if root_ok else "fail", "authenticated workbench available" if root_ok else "workbench entry failed", root)
            return root_ok
        except Exception as error:
            self.add(phase, f"{actor} session bootstrap", "blocker", f"{type(error).__name__}: {error}")
            return False

    def identity(self, actor: str) -> bool:
        phase = "19.3.6 SSO and identity"
        client = self.actor_client(actor)
        if not client:
            self.missing_actor(phase, f"{actor} identity snapshot", actor)
            return False
        try:
            account = client.request("GET", self.paths["account_info"])
            apps = client.request("GET", self.paths["application_list"])
            ok = account.status == 200 and apps.status == 200
            identity_fields_present = 0
            if ok:
                account_payload, app_payload = account.json(), apps.json()
                self.identities[actor] = {
                    "account": find_key(account_payload, "UserId") or find_key(account_payload, "id"),
                    "agent": find_key(account_payload, "AgentId"),
                    "application": find_key(app_payload, "ApplicationId") or find_key(app_payload, "id"),
                }
                for item in self.identities[actor].values():
                    self.redactor.add(str(item) if item is not None else None)
                identity_fields_present = sum(value is not None for value in self.identities[actor].values())
                ok = identity_fields_present == 3
            self.add(phase, f"{actor} identity snapshot", "pass" if ok else "fail", "complete identity endpoints succeeded" if ok else "identity endpoint failed or omitted account/Agent/Application identity", account, evidence={"application_list_status": apps.status, "identity_fields_present": identity_fields_present})
            return ok
        except Exception as error:
            self.add(phase, f"{actor} identity snapshot", "blocker", f"{type(error).__name__}: {error}")
            return False

    def minimal_turn(self) -> None:
        phase = "19.3.7 minimal Turn/reconnect"
        turn = self.config.get("turn")
        if not turn or not turn.get("request_fixture"):
            self.add(phase, "one POST plus Last-Event-ID reconnect", "blocker", "minimal Turn fixture is not configured")
            return
        if not self.args.execute:
            self.dry(phase, "one POST plus Last-Event-ID reconnect")
            return
        if not self.args.allow_provider_cost:
            self.add(phase, "one POST plus Last-Event-ID reconnect", "blocker", "real Turn requires --allow-provider-cost")
            return
        client = self.actor_client("user_a")
        if not client or "user_a" not in self.identities:
            self.add(phase, "one POST plus Last-Event-ID reconnect", "blocker", "user A SSO and identity must succeed first")
            return
        response = None
        try:
            payload = load_json_fixture(str(turn["request_fixture"]), self.variables)
            forbidden = nested_forbidden(payload)
            if forbidden:
                raise ConfigError(f"Turn fixture must not provide trusted identity field {forbidden}")
            payload["ClientRequestId"] = str(uuid.uuid4())
            client_request_id = payload["ClientRequestId"]
            application = self.identities["user_a"].get("application")
            if application and not find_key(payload, "ApplicationId"):
                payload["ApplicationId"] = application
            status, _, response, started, endpoint = client.open_stream("POST", self.paths["turn"], body=json_bytes(payload), headers={"Content-Type": "application/json", "Accept": "text/event-stream"})
            if status not in {200, 201}:
                body = response.read(4096)
                response.close()
                self.add(phase, "minimal Turn accepted", "fail", "Turn POST failed", endpoint=endpoint, duration_ms=(time.perf_counter() - started) * 1000, evidence={"http_status": status, "response_bytes": len(body)})
                return
            disconnect_after = int(turn.get("disconnect_after_events", 2))
            data_events = 0
            last_event_id = ""
            turn_id = ""
            conversation_id = ""
            while data_events < disconnect_after:
                frame = read_structured_sse_event(response)
                if frame is None:
                    break
                event_id, event = frame
                data_events += 1
                if event_id:
                    last_event_id = event_id
                    self.redactor.add(last_event_id)
                if event is None:
                    continue
                if event.get("Type") == WORKBENCH_TURN_EVENT_TYPE:
                    observed_request_id = event.get("ClientRequestId")
                    observed_turn_id = event.get("TurnId")
                    if observed_request_id != client_request_id:
                        raise RuntimeError("workbench.turn ClientRequestId does not match the submitted request")
                    if not isinstance(observed_turn_id, str) or not observed_turn_id:
                        raise RuntimeError("workbench.turn omitted a valid TurnId")
                    if turn_id and turn_id != observed_turn_id:
                        raise RuntimeError("initial SSE changed TurnId")
                    turn_id = observed_turn_id
                observed_conversation_id = find_key(event, "ConversationId")
                if isinstance(observed_conversation_id, str) and observed_conversation_id:
                    if conversation_id and conversation_id != observed_conversation_id:
                        raise RuntimeError("initial SSE changed ConversationId")
                    conversation_id = observed_conversation_id
            response.close()
            self.redactor.add(turn_id)
            self.redactor.add(conversation_id)
            if not turn_id or not last_event_id:
                raise RuntimeError("initial SSE did not yield TurnId and event id before disconnect")
            if conversation_id:
                self.variables["conversation_id"] = conversation_id
            reconnect_path = f"{self.paths['turn_events']}?TurnId={quote(turn_id, safe='')}"
            reconnect_status, _, reconnect, reconnect_started, reconnect_endpoint = client.open_stream("GET", reconnect_path, headers={"Accept": "text/event-stream", "Last-Event-ID": last_event_id})
            terminal = False
            terminal_status = ""
            replay_events = 0
            if reconnect_status == 200:
                for _ in range(10000):
                    frame = read_structured_sse_event(reconnect)
                    if frame is None:
                        break
                    _, event = frame
                    replay_events += 1
                    if event is None or event.get("Type") != WORKBENCH_TERMINAL_EVENT_TYPE:
                        continue
                    if event.get("TurnId") != turn_id:
                        raise RuntimeError("workbench terminal event is not bound to the submitted Turn")
                    status_value = event.get("Status")
                    if status_value not in WORKBENCH_TERMINAL_STATUSES:
                        raise RuntimeError("workbench terminal event has an unknown Status")
                    terminal = True
                    terminal_status = str(status_value)
                    break
            reconnect.close()
            history_status = None
            if conversation_id:
                history = client.request("GET", f"{self.paths['history']}?ConversationId={quote(conversation_id, safe='')}")
                history_status = history.status
            ok = reconnect_status == 200 and terminal_status == "completed" and history_status in {None, 200}
            self.add(phase, "one POST plus Last-Event-ID reconnect", "pass" if ok else "fail", "Turn resumed without duplicate POST" if ok else "Turn reconnect/history contract failed", endpoint=reconnect_endpoint, duration_ms=(time.perf_counter() - reconnect_started) * 1000, evidence={"turn_post_count": 1, "initial_events": data_events, "replayed_events": replay_events, "terminal_seen": terminal, "history_status": history_status, "reconnect_status": reconnect_status})
        except Exception as error:
            if response:
                try:
                    response.close()
                except Exception:
                    pass
            self.add(phase, "one POST plus Last-Event-ID reconnect", "blocker", f"{type(error).__name__}: {error}")

    def isolation(self) -> None:
        phase = "19.3.8 identity/tenant isolation"
        for actor in ("user_b_same", "user_b_other"):
            if self.bootstrap(actor):
                self.identity(actor)
        a = self.identities.get("user_a")
        same = self.identities.get("user_b_same")
        other = self.identities.get("user_b_other")
        if a and same:
            ok = a.get("application") == same.get("application") and a.get("account") != same.get("account")
            self.add(phase, "same customer shares App but isolates user/Agent", "pass" if ok else "fail", "expected same-customer identity relationship" if ok else "same-customer identity relationship violated", evidence={"same_application": a.get("application") == same.get("application"), "different_account": a.get("account") != same.get("account"), "different_agent": a.get("agent") != same.get("agent")})
        else:
            self.add(phase, "same customer shares App but isolates user/Agent", "blocker", "user A and same-customer user B identity snapshots are required")
        if a and other:
            ok = a.get("application") != other.get("application") and a.get("account") != other.get("account")
            self.add(phase, "different customers isolate App/user/Agent", "pass" if ok else "fail", "cross-customer identity separation observed" if ok else "cross-customer identity overlap detected", evidence={"different_application": a.get("application") != other.get("application"), "different_account": a.get("account") != other.get("account"), "different_agent": a.get("agent") != other.get("agent")})
        else:
            self.add(phase, "different customers isolate App/user/Agent", "blocker", "user A and other-customer user B identity snapshots are required")
        self.fixture_phase(phase, "configured IDOR rejection", self.config.get("isolation_checks"), required=True, requirements=PHASE_REQUIREMENTS["isolation_checks"], semantic_key="isolation_checks")

    def eicar(self) -> None:
        phase = "19.3.9 EICAR fail-closed"
        config = self.config.get("eicar", {})
        if not config.get("file_ref"):
            self.add(phase, "malware upload rejected", "blocker", "EICAR file reference is not configured")
            return
        if not self.args.execute:
            self.dry(phase, "malware upload rejected")
            return
        if not (self.args.allow_mutations and self.args.allow_eicar):
            self.add(phase, "malware upload rejected", "blocker", "requires --allow-mutations and --allow-eicar")
            return
        client = self.actor_client("user_a")
        if not client:
            self.missing_actor(phase, "malware upload rejected", "user_a")
            return
        try:
            ref = str(config["file_ref"])
            if not ref.startswith("file:"):
                raise ConfigError("EICAR must use a file:/absolute/path reference")
            path = Path(ref[5:])
            if not path.is_absolute() or path.is_symlink() or not path.is_file():
                raise ConfigError("EICAR reference must be an absolute regular non-symlink file")
            if path.stat().st_size != 68:
                raise ConfigError("EICAR file must be exactly 68 bytes")
            payload = path.read_bytes()
            if hashlib.sha256(payload).hexdigest() != EICAR_SHA256:
                raise ConfigError("file is not the standard EICAR test file")
            boundary = "----claw-e2e-" + uuid.uuid4().hex
            body = (
                f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"eicar.com.txt\"\r\n"
                "Content-Type: application/octet-stream\r\n\r\n"
            ).encode() + payload + f"\r\n--{boundary}--\r\n".encode()
            response = client.request("POST", self.paths["file_upload"], body=body, headers={"Content-Type": f"multipart/form-data; boundary={boundary}"})
            returned_handle = False
            try:
                parsed = response.json()
                returned_handle = bool(find_key(parsed, "WorkbenchFileId") or find_key(parsed, "url"))
            except Exception:
                pass
            ok = response.status not in range(200, 300) and not returned_handle
            self.add(phase, "malware upload rejected", "pass" if ok else "fail", "scanner failed closed" if ok else "malware was accepted or yielded a file handle", response, evidence={"expected_reject": True, "returned_handle": returned_handle})
        except Exception as error:
            self.add(phase, "malware upload rejected", "blocker", f"{type(error).__name__}: {error}")

    def gate_scenarios(self) -> None:
        phase = "19.3.12/14 lifecycle gates"
        scenarios = self.config.get("gate_scenarios", [])
        if not scenarios:
            self.add(phase, "suspended/disabled/expired read-only gates", "blocker", "no gate scenarios configured")
            return
        if not self.args.allow_mutations:
            self.add(phase, "suspended/disabled/expired read-only gates", "blocker", "requires --allow-mutations")
            return
        for scenario in scenarios:
            transitioned = False
            try:
                transitioned = self.generic_request(phase, scenario["transition"])
                if transitioned:
                    self.generic_request(phase, scenario["write_probe"])
                    self.generic_request(phase, scenario["read_probe"])
            finally:
                restore = dict(scenario["restore"])
                restore["name"] = f"{scenario['name']} restore (mandatory)"
                if not self.generic_request(phase, restore, restore=True):
                    self.add(phase, f"{scenario['name']} restore invariant", "fail", "restore failed; acceptance customer may remain gated")

    def direct_adp(self) -> None:
        phase = "19.4 direct ADP/header spoofing"
        base = self.config.get("direct_adp_base_url")
        paths = self.config.get("direct_adp_paths", [])
        if not base or not paths:
            self.add(phase, "direct ADP rejects unauthenticated/forged requests", "blocker", "direct_adp_base_url and paths are required")
            return
        if not self.args.execute:
            self.dry(phase, "direct ADP rejects unauthenticated/forged requests")
            return
        client = HTTPClient(base, timeout=self.config.get("timeout_seconds", 20))
        for path in paths:
            try:
                response = client.request("GET", str(path), headers={"X-Forwarded-User": "forged", "X-Forwarded-Email": "forged@example.invalid", "X-Workbench-User": "forged"})
                ok = response.status in {401, 403}
                self.add(phase, f"direct ADP rejects {path}", "pass" if ok else "fail", "known direct API rejected forged access" if ok else "direct check did not prove an authentication rejection", response, evidence={"expected_status": [401, 403]})
            except Exception as error:
                self.add(phase, f"direct ADP rejects {path}", "blocker", f"{type(error).__name__}: {error}", endpoint=base + str(path))

    def edge_security(self) -> None:
        phase = "19.4 edge security"
        if not self.args.execute:
            self.dry(phase, "anonymous and forged forwarded identity rejected")
        else:
            client = self.actor_client("anonymous")
            try:
                response = client.request(
                    "GET", self.paths["account_info"],
                    headers={"X-Forwarded-User": "forged", "X-Forwarded-Email": "forged@example.invalid", "X-Workbench-User": "forged"},
                )
                ok = response.status in {401, 403, 404}
                self.add(phase, "anonymous and forged forwarded identity rejected", "pass" if ok else "fail", "edge ignored forged identity" if ok else "edge trusted forged identity", response, evidence={"expected_status": [401, 403, 404]})
            except Exception as error:
                self.add(phase, "anonymous and forged forwarded identity rejected", "blocker", f"{type(error).__name__}: {error}")
            authenticated = self.actor_client("user_a")
            if authenticated:
                try:
                    response = authenticated.request("POST", self.paths["user_ticket"], body=b"{}", headers={"Content-Type": "application/json"})
                    ok = response.status in {400, 401, 403}
                    self.add(phase, "session-ticket CSRF request rejected", "pass" if ok else "fail", "missing origin/referer rejected" if ok else "missing origin/referer accepted", response, evidence={"expected_status": [400, 401, 403]})
                except Exception as error:
                    self.add(phase, "session-ticket CSRF request rejected", "blocker", f"{type(error).__name__}: {error}")
            else:
                self.missing_actor(phase, "session-ticket CSRF request rejected", "user_a")
        self.fixture_phase(phase, "configured CSRF/XSS/security checks", self.config.get("security_checks"), required=True, requirements=PHASE_REQUIREMENTS["security_checks"], semantic_key="security_checks")

    def admin_actor_separation(self) -> None:
        phase = "20.3 BYOK"
        requester = self.secrets.get("admin_requester_session", "")
        approver = self.secrets.get("admin_approver_session", "")
        if not requester or not approver:
            self.add(
                phase,
                "two-person administrator sessions are configured",
                "blocker",
                "admin_requester_session and admin_approver_session are both required",
            )
            return
        if requester == approver:
            self.add(
                phase,
                "two-person administrator sessions are distinct",
                "blocker",
                "requester and approver session material is identical",
            )
            return
        self.add(
            phase,
            "two-person administrator sessions are distinct",
            "pass",
            "requester and approver use distinct session material; audit actor assertions remain required",
        )

    def run(self) -> list[CheckResult]:
        self.legacy()
        lifecycle = self.config.get("lifecycle", {})
        self.fixture_phase("19.3.2 customer/member", "create acceptance customer and members", lifecycle.get("create_customer_members"), mutations=True, requirements=LIFECYCLE_REQUIREMENTS["create_customer_members"], semantic_key="lifecycle.create_customer_members")
        self.fixture_phase("19.3.3 invalid App", "reject invalid App configuration", lifecycle.get("invalid_app_validation"), mutations=True, requirements=LIFECYCLE_REQUIREMENTS["invalid_app_validation"], semantic_key="lifecycle.invalid_app_validation")
        self.fixture_phase("19.3.4 no-plan gate", "reject enable without active plan", lifecycle.get("no_plan_gate"), mutations=True, requirements=LIFECYCLE_REQUIREMENTS["no_plan_gate"], semantic_key="lifecycle.no_plan_gate")
        self.fixture_phase("19.3.5 activation", "plan/payment/invoice/App enable", lifecycle.get("activate_plan"), mutations=True, requirements=LIFECYCLE_REQUIREMENTS["activate_plan"], semantic_key="lifecycle.activate_plan")
        if self.bootstrap("user_a"):
            self.identity("user_a")
        self.minimal_turn()
        self.isolation()
        self.eicar()
        self.fixture_phase("19.3.10 allowlists", "model/Skill/Tool/connector allowlists", self.config.get("policy_checks"), required=True, requirements=PHASE_REQUIREMENTS["policy_checks"], semantic_key="policy_checks")
        self.fixture_phase("19.3.11 limits", "request/concurrency/storage/turn limits", self.config.get("limit_checks"), required=True, requirements=PHASE_REQUIREMENTS["limit_checks"], semantic_key="limit_checks")
        for actor in ("user_a_context_1", "user_a_context_2", "user_a_customer_2"):
            if actor in self.config.get("selection_targets", {}) and self.bootstrap(actor):
                self.identity(actor)
        if "user_a_stale_context" in self.config.get("selection_targets", {}):
            self.bootstrap("user_a_stale_context", selection_only=True)
        self.fixture_phase("20.3 selector", "multi-App and multi-customer selection invariants", self.config.get("selector_checks"), mutations=True, required=True, requirements=SELECTOR_REQUIREMENTS, contract="selector")
        self.fixture_phase("20.2 OAuth", "OAuth PKCE, replay, disconnect and execution-blocking invariants", self.config.get("oauth_checks"), mutations=True, required=True, requirements=OAUTH_REQUIREMENTS, contract="oauth")
        self.fixture_phase("20.3 scheduled tasks", "scheduled task lifecycle, idempotency and offline reauthorization", self.config.get("scheduled_task_checks"), mutations=True, required=True, provider_cost=True, requirements=SCHEDULED_REQUIREMENTS, contract="scheduled")
        self.fixture_phase(
            "19.3.11b managed sandbox",
            "managed sandbox lifecycle, Shell, files and fail-closed surfaces",
            self.config.get("sandbox_checks"),
            mutations=True,
            required=True,
            provider_cost=True,
            requirements=SANDBOX_ENABLED_REQUIREMENTS if self.config.get("sandbox_mode", "off") == "enabled" else SANDBOX_REQUIREMENTS,
            contract="sandbox",
        )
        self.gate_scenarios()
        self.fixture_phase("19.3.13 rotation", "credential and App rotation", self.config.get("rotation_checks"), mutations=True, required=True, requirements=PHASE_REQUIREMENTS["rotation_checks"], semantic_key="rotation_checks")
        self.admin_actor_separation()
        self.fixture_phase("20.3 BYOK", "BYOK scope, inheritance, two-person approval and rotation", self.config.get("byok_checks"), mutations=True, required=True, requirements=BYOK_REQUIREMENTS, contract="byok")
        self.fixture_phase("19.3.15 usage audit", "usage, audit and margin reconciliation", self.config.get("usage_audit_checks"), required=True, requirements=PHASE_REQUIREMENTS["usage_audit_checks"], semantic_key="usage_audit_checks")
        self.fixture_phase("20.2 billing import", "Tencent billing draft import and invoice immutability", self.config.get("billing_import_checks"), mutations=True, required=True, requirements=BILLING_IMPORT_REQUIREMENTS, contract="billing_import")
        self.fixture_phase("19.3.16 regression smoke", "existing model and asset routing", self.config.get("smoke_checks"), required=True, provider_cost=True, requirements=PHASE_REQUIREMENTS["smoke_checks"], semantic_key="smoke_checks")
        self.edge_security()
        self.direct_adp()
        return self.results


def parser() -> argparse.ArgumentParser:
    value = argparse.ArgumentParser(description=__doc__)
    value.add_argument("--config", required=True, help="JSON configuration path")
    value.add_argument("--output", default="claw-e2e-report", help="report directory")
    value.add_argument("--execute", action="store_true", help="perform live network checks")
    value.add_argument("--allow-mutations", action="store_true", help="allow configured lifecycle mutations")
    value.add_argument("--allow-eicar", action="store_true", help="allow standard EICAR upload")
    value.add_argument("--allow-provider-cost", action="store_true", help="allow real provider-cost requests")
    value.add_argument("--allow-http", action="store_true", help="allow HTTP only for loopback self-tests")
    return value


def main(argv: list[str] | None = None) -> int:
    args = parser().parse_args(argv)
    if args.allow_eicar and not args.allow_mutations:
        print("error: --allow-eicar also requires --allow-mutations", file=sys.stderr)
        return 2
    try:
        config = load_config(args.config, allow_http=args.allow_http)
    except ConfigError as error:
        print(f"configuration error: {error}", file=sys.stderr)
        return 2
    if args.execute:
        preflight_errors = acceptance_preflight_errors(config)
        if preflight_errors:
            print(
                "configuration error: live acceptance preflight failed: "
                + "; ".join(preflight_errors),
                file=sys.stderr,
            )
            return 2
    if "acceptance_report_allowed_signers_file" in config:
        print(
            "configuration error: run_e2e accepts only acceptance_report_signing_key_file; "
            "the public verifier belongs in the separate load configuration",
            file=sys.stderr,
        )
        return 2
    signing_key_ref = config.get("acceptance_report_signing_key_file")
    if args.execute:
        if config.get("_release_manifest_source") != "file":
            print(
                "configuration error: live execution requires release_manifest as a collector file:/absolute/path reference",
                file=sys.stderr,
            )
            return 2
        manifest_validation_errors = release_manifest_errors(config.get("release_manifest"), max_age_seconds=1800)
        if manifest_validation_errors:
            print(
                "configuration error: live execution requires an immutable release manifest: "
                + "; ".join(manifest_validation_errors),
                file=sys.stderr,
            )
            return 2
        if not signing_key_ref:
            print(
                "configuration error: live execution requires acceptance_report_signing_key_file",
                file=sys.stderr,
            )
            return 2
        try:
            validate_acceptance_signing_key(str(signing_key_ref))
        except ConfigError as error:
            print(f"configuration error: {error}", file=sys.stderr)
            return 2
    started = utc_now()
    run_id = str(uuid.uuid4())
    runner = Runner(config, args, run_id=run_id, started_at=started)
    results = runner.run()
    finished = utc_now()
    write_reports(
        args.output,
        run_id=run_id,
        started_at=started,
        finished_at=finished,
        target=config["base_url"],
        mode="live" if args.execute else "dry-run",
        permissions={"provider_cost": args.allow_provider_cost, "mutations": args.allow_mutations, "eicar": args.allow_eicar},
        results=results,
        redactor=runner.redactor,
        release_manifest=config.get("release_manifest"),
    )
    if args.execute:
        try:
            sign_evidence_file(Path(args.output) / "results.json", str(signing_key_ref))
        except ConfigError as error:
            print(f"acceptance report signing error: {error}", file=sys.stderr)
            return 2
    summary = {status: sum(item.status == status for item in results) for status in ("pass", "fail", "blocker", "skipped")}
    print(json.dumps({"output": str(Path(args.output).resolve()), "summary": summary}, ensure_ascii=False))
    return 1 if summary["fail"] or summary["blocker"] else 0


if __name__ == "__main__":
    raise SystemExit(main())
