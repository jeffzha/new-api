#!/usr/bin/env python3
"""Parse the deployment dotenv file without invoking a shell evaluator."""

from __future__ import annotations

import argparse
import base64
import ipaddress
import os
import re
import stat
import sys
from pathlib import Path
from urllib.parse import urlsplit


KEY_RE = re.compile(r"[A-Z_][A-Z0-9_]*")
ALLOWED_KEYS = frozenset(
    {
        "ADP_CPUS",
        "ADP_DB_NAME",
        "ADP_DB_USER",
        "ADP_MEMORY",
        "ADP_REVISION",
        "ADP_SOURCE_DIR",
        "ADP_WORKBENCH_IMAGE_BLUE",
        "ADP_WORKBENCH_IMAGE_GREEN",
        "BUN_BUILD_IMAGE",
        "CADDY_IMAGE",
        "CLAMAV_CPUS",
        "CLAMAV_FRESHCLAM_CHECKS",
        "CLAMAV_IMAGE",
        "CLAMAV_MAX_FILE_BYTES",
        "CLAMAV_MAX_SCAN_BYTES",
        "CLAMAV_MEMORY",
        "CLAMAV_STREAM_MAX_BYTES",
        "CLAW_ADMIN_SESSION_TTL",
        "CLAW_APP_CONTEXT_TTL",
        "CLAW_BUILD_LOCAL_IMAGES",
        "CLAW_CONTROL_CPUS",
        "CLAW_CONTROL_IMAGE_BLUE",
        "CLAW_CONTROL_IMAGE_GREEN",
        "CLAW_CONTROL_MEMORY",
        "CLAW_CONTROL_REVISION",
        "CLAW_CONTROL_SESSION_TTL",
        "CLAW_DB_CONN_MAX_LIFETIME",
        "CLAW_DB_MAX_IDLE_CONNS",
        "CLAW_DB_MAX_OPEN_CONNS",
        "CLAW_DB_NAME",
        "CLAW_DB_USER",
        "CLAW_DELIVERED_OUTBOX_RETENTION",
        "CLAW_RETENTION_COORDINATOR_INTERVAL",
        "CLAW_RETENTION_COORDINATOR_TIMEOUT",
        "CLAW_ENTRY_TICKET_TTL",
        "CLAW_ENVIRONMENT",
        "CLAW_EPHEMERAL_RETENTION",
        "CLAW_EVIDENCE_CLAMAV_TIMEOUT",
        "CLAW_EVIDENCE_MAX_BYTES",
        "CLAW_EVIDENCE_ROOT",
        "CLAW_INTERNAL_HMAC_TIME_SKEW",
        "CLAW_MAINTENANCE_INTERVAL",
        "CLAW_METRICS_ADDR",
        "CLAW_NEW_API_IDENTITY_STATUS_TIMEOUT",
        "CLAW_OUTBOX_INTERVAL",
        "CLAW_PERIOD_RECONCILE_INTERVAL",
        "CLAW_PROVIDER_VERIFICATION_TIMEOUT",
        "CLAW_REDIS_EVENT_CHANNEL",
        "CLAW_SSO_TICKET_TTL",
        "CLAW_TENCENT_BILLING_IMPORT_ENABLED",
        "CLAW_TENCENT_BILLING_IMPORT_INTERVAL",
        "CLAW_TENCENT_BILLING_LEASE_DURATION",
        "CLAW_TENCENT_BILLING_MAX_ATTEMPTS",
        "CLAW_TENCENT_BILLING_MAX_PAGES",
        "CLAW_TENCENT_BILLING_MAX_RECORDS",
        "CLAW_TENCENT_BILLING_MAX_RESPONSE_BYTES",
        "CLAW_TENCENT_BILLING_PAGE_SIZE",
        "CLAW_TENCENT_BILLING_PAYER_UIN",
        "CLAW_TENCENT_BILLING_TIMEOUT",
        "COMPOSE_PROJECT_NAME",
        "GO_BUILD_IMAGE",
        "GOPROXY",
        "LOG_LEVEL",
        "NEW_API_INTERNAL_UPSTREAM",
        "NEW_API_INTERNAL_ALLOWED_HOSTS",
        "NEW_API_REVISION",
        "NODE_BUILD_IMAGE",
        "NPM_REGISTRY",
        "PIP_INDEX_URL",
        "POSTGRES_IMAGE",
        "POSTGRES_MEMORY",
        "POSTGRES_TIMEZONE",
        "PYTHON_BUILD_IMAGE",
        "RATE_LIMIT",
        "REDIS_IMAGE",
        "REDIS_MEMORY",
        "SERVER_RESPONSE_TIMEOUT",
        "SSE_IDLE_TIMEOUT",
        "WORKBENCH_ALLOWED_CLOCK_SKEW_SECONDS",
        "WORKBENCH_APP_MIGRATION_WORKER_ENABLED",
        "WORKBENCH_APP_MIGRATION_POLL_SECONDS",
        "WORKBENCH_APP_MIGRATION_LEASE_SECONDS",
        "WORKBENCH_CANONICAL_ORIGIN",
        "WORKBENCH_CONTROL_EVENT_MAX_BACKOFF_SECONDS",
        "WORKBENCH_CONTROL_EVENT_MAX_BYTES",
        "WORKBENCH_CONTROL_EVENT_PROCESSED_TTL_SECONDS",
        "WORKBENCH_CONTROL_TIMEOUT_SECONDS",
        "WORKBENCH_EDGE_NETWORK",
        "WORKBENCH_FILES_ENABLED",
        "WORKBENCH_FILE_ABSOLUTE_MAX_BYTES",
        "WORKBENCH_FILE_COS_BUCKET",
        "WORKBENCH_FILE_COS_REGION",
        "WORKBENCH_FILE_LOCATOR_KEY_ID",
        "WORKBENCH_FILE_MAX_CONCURRENT_UPLOADS",
        "WORKBENCH_FILE_QUARANTINE_CAPACITY_BYTES",
        "WORKBENCH_FILE_SCANNER_MAX_BYTES",
        "WORKBENCH_FILE_SCANNER_PORT",
        "WORKBENCH_FILE_SCANNER_TIMEOUT_SECONDS",
        "WORKBENCH_FILE_URL_EXPIRE_SECONDS",
        "WORKBENCH_INTEGRATIONS_ENABLED",
        "WORKBENCH_CONNECTOR_TOKEN_KEY_ID",
        "WORKBENCH_OAUTH_STATE_KEY_ID",
        "WORKBENCH_OAUTH_STATE_TTL_SECONDS",
        "WORKBENCH_INTEGRATION_REAUTH_SECONDS",
        "WORKBENCH_OAUTH_HTTP_TIMEOUT_SECONDS",
        "WORKBENCH_OAUTH_MAX_RESPONSE_BYTES",
        "WORKBENCH_SANDBOX_ENABLED",
        "WORKBENCH_SANDBOX_CODE_ENABLED",
        "WORKBENCH_SANDBOX_CODE_WORKER_MEMORY_BYTES",
        "WORKBENCH_SANDBOX_PTY_ENABLED",
        "WORKBENCH_SANDBOX_PTY_TICKET_TTL_SECONDS",
        "WORKBENCH_SANDBOX_PTY_MAX_RUNTIME_SECONDS",
        "WORKBENCH_SANDBOX_PTY_REAUTH_SECONDS",
        "WORKBENCH_SANDBOX_PTY_HEARTBEAT_SECONDS",
        "WORKBENCH_SANDBOX_PTY_STALE_SECONDS",
        "WORKBENCH_SANDBOX_PTY_REAPER_SECONDS",
        "WORKBENCH_SANDBOX_PTY_GLOBAL_CAPACITY",
        "WORKBENCH_SANDBOX_PTY_CUSTOMER_CAPACITY",
        "WORKBENCH_SANDBOX_PTY_USER_CAPACITY",
        "WORKBENCH_SANDBOX_PTY_MAX_INPUT_FRAME_BYTES",
        "WORKBENCH_SANDBOX_PTY_MAX_OUTPUT_FRAME_BYTES",
        "WORKBENCH_SANDBOX_PTY_MAX_INPUT_BYTES",
        "WORKBENCH_SANDBOX_PTY_MAX_OUTPUT_BYTES",
        "WORKBENCH_SANDBOX_PTY_MAX_INPUT_FRAMES_PER_SECOND",
        "WORKBENCH_SANDBOX_PTY_OUTPUT_QUEUE_FRAMES",
        "WORKBENCH_SANDBOX_PROVIDER",
        "WORKBENCH_AGSX_REGION",
        "WORKBENCH_AGSX_DOMAIN",
        "WORKBENCH_AGSX_CONTROL_ENDPOINT",
        "WORKBENCH_AGSX_TOOL_ID",
        "WORKBENCH_AGSX_TOOL_NAME",
        "WORKBENCH_SANDBOX_NETWORK_MODE",
        "WORKBENCH_SANDBOX_AUTH_MODE",
        "WORKBENCH_SANDBOX_LEASE_SECONDS",
        "WORKBENCH_SANDBOX_STARTS_PER_USER_MINUTE",
        "WORKBENCH_SANDBOX_STARTS_PER_CUSTOMER_MINUTE",
        "WORKBENCH_SANDBOX_TERMINAL_RETENTION_DAYS",
        "WORKBENCH_SANDBOX_DEFAULT_TIMEOUT_SECONDS",
        "WORKBENCH_SANDBOX_HARD_MAX_RUNTIME_SECONDS",
        "WORKBENCH_SANDBOX_MAX_COMMAND_SECONDS",
        "WORKBENCH_SANDBOX_PROVIDER_TIMEOUT_SECONDS",
        "WORKBENCH_SANDBOX_MAX_CODE_CHARS",
        "WORKBENCH_SANDBOX_MAX_COMMAND_CHARS",
        "WORKBENCH_SANDBOX_MAX_OUTPUT_BYTES",
        "WORKBENCH_SANDBOX_HARD_MAX_FILE_BYTES",
        "WORKBENCH_METRICS_ENABLED",
        "WORKBENCH_METRICS_HOST",
        "WORKBENCH_METRICS_PORT",
        "WORKBENCH_SESSION_EXPIRE_MINUTES",
        "WORKBENCH_SCHEDULED_TASKS_ENABLED",
        "WORKBENCH_SCHEDULE_BATCH_SIZE",
        "WORKBENCH_SCHEDULE_DELEGATION_DAYS",
        "WORKBENCH_SCHEDULE_LEASE_SECONDS",
        "WORKBENCH_SCHEDULE_MAX_ATTACHMENTS",
        "WORKBENCH_SCHEDULE_MAX_DAILY_RUNS",
        "WORKBENCH_SCHEDULE_MAX_PROMPT_CHARS",
        "WORKBENCH_SCHEDULE_MAX_TASKS_PER_USER",
        "WORKBENCH_SCHEDULE_MIN_INTERVAL_MINUTES",
        "WORKBENCH_SCHEDULE_MISFIRE_GRACE_SECONDS",
        "WORKBENCH_SCHEDULE_REAUTH_SECONDS",
        "WORKBENCH_SCHEDULE_WORKER_INTERVAL_SECONDS",
        "WORKBENCH_STREAM_REAUTH_SECONDS",
        "WORKBENCH_USAGE_EVIDENCE_KEY_ID",
        "WORKBENCH_VENDOR_CACHE_IDLE_SECONDS",
        "WORKBENCH_VENDOR_CACHE_MAX_ENTRIES",
        "WORKBENCH_WORKSPACE_HOST_SUFFIXES",
        "WORKBENCH_WORKSPACE_LOCATOR_KEY_ID",
    }
)


class DotenvError(ValueError):
    pass


def validate_file(path: Path) -> None:
    try:
        metadata = path.lstat()
    except OSError as error:
        raise DotenvError(f"cannot stat dotenv file: {error}") from error
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
        raise DotenvError("dotenv file must be a regular file, not a symlink")
    if metadata.st_uid != os.geteuid():
        raise DotenvError("dotenv file must be owned by the current user")
    if stat.S_IMODE(metadata.st_mode) & 0o077:
        raise DotenvError("dotenv file must not be accessible by group or other users")


def parse_dotenv_text(text: str) -> dict[str, str]:
    values: dict[str, str] = {}
    for line_number, raw_line in enumerate(text.splitlines(), 1):
        if not raw_line.strip() or raw_line.lstrip().startswith("#"):
            continue
        if raw_line != raw_line.strip():
            raise DotenvError(f"line {line_number}: surrounding whitespace is forbidden")
        if "=" not in raw_line:
            raise DotenvError(f"line {line_number}: expected KEY=VALUE")
        key, value = raw_line.split("=", 1)
        if not KEY_RE.fullmatch(key):
            raise DotenvError(f"line {line_number}: invalid key")
        if key not in ALLOWED_KEYS:
            raise DotenvError(f"line {line_number}: unsupported key {key}")
        if key in values:
            raise DotenvError(f"line {line_number}: duplicate key {key}")
        if value != value.strip():
            raise DotenvError(f"line {line_number}: value has surrounding whitespace")
        if any(ord(character) < 32 or ord(character) == 127 for character in value):
            raise DotenvError(f"line {line_number}: control characters are forbidden")
        if "$" in value or "`" in value:
            raise DotenvError(
                f"line {line_number}: interpolation and command substitution are forbidden"
            )
        if value.startswith(("'", '"')) or value.endswith(("'", '"')):
            raise DotenvError(f"line {line_number}: quoted values are not supported")
        values[key] = value
    return values


def validate_deployment_values(values: dict[str, str]) -> None:
    canonical = values.get("WORKBENCH_CANONICAL_ORIGIN")
    if canonical is not None:
        try:
            parsed = urlsplit(canonical)
            port = parsed.port
        except ValueError as error:
            raise DotenvError("WORKBENCH_CANONICAL_ORIGIN is invalid") from error
        if (
            parsed.scheme != "https"
            or not parsed.hostname
            or parsed.username is not None
            or parsed.password is not None
            or parsed.path not in {"", "/"}
            or parsed.query
            or parsed.fragment
            or port not in {None, 443}
        ):
            raise DotenvError("WORKBENCH_CANONICAL_ORIGIN must be an exact HTTPS origin on port 443")

    upstream = values.get("NEW_API_INTERNAL_UPSTREAM")
    if upstream is not None:
        try:
            parsed = urlsplit(upstream)
            port = parsed.port
        except ValueError as error:
            raise DotenvError("NEW_API_INTERNAL_UPSTREAM is invalid") from error
        host = str(parsed.hostname or "").lower().rstrip(".")
        if (
            parsed.scheme not in {"http", "https"}
            or not host
            or parsed.username is not None
            or parsed.password is not None
            or parsed.path not in {"", "/"}
            or parsed.query
            or parsed.fragment
            or port is None
            or not 1 <= port <= 65535
        ):
            raise DotenvError("NEW_API_INTERNAL_UPSTREAM must be an exact internal HTTP(S) origin with an explicit port")
        if host in {"localhost", "localhost.localdomain", "ip6-localhost"}:
            raise DotenvError("NEW_API_INTERNAL_UPSTREAM cannot use a loopback hostname")
        try:
            address = ipaddress.ip_address(host)
        except ValueError:
            internal_host = (
                re.fullmatch(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?", host) is not None
                or host.endswith((".internal", ".local"))
            )
        else:
            internal_host = address.is_private and not address.is_loopback and not address.is_unspecified
        if not internal_host:
            raise DotenvError("NEW_API_INTERNAL_UPSTREAM must use a Docker/private-network hostname or private non-loopback IP")
        configured_hosts = values.get("NEW_API_INTERNAL_ALLOWED_HOSTS", "")
        allowed_hosts = [item.strip().lower().rstrip(".") for item in configured_hosts.split(",")]
        if (
            not configured_hosts
            or any(not item or item in {"localhost", "localhost.localdomain", "ip6-localhost"} for item in allowed_hosts)
            or len(allowed_hosts) != len(set(allowed_hosts))
            or host not in allowed_hosts
        ):
            raise DotenvError("NEW_API_INTERNAL_UPSTREAM host must appear exactly in NEW_API_INTERNAL_ALLOWED_HOSTS")


def load_dotenv(path: Path) -> dict[str, str]:
    validate_file(path)
    try:
        text = path.read_text(encoding="utf-8")
    except (OSError, UnicodeError) as error:
        raise DotenvError(f"cannot read dotenv file as UTF-8: {error}") from error
    values = parse_dotenv_text(text)
    validate_deployment_values(values)
    return values


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--file", required=True, type=Path)
    parser.add_argument("--get", choices=sorted(ALLOWED_KEYS))
    args = parser.parse_args(argv)
    try:
        values = load_dotenv(args.file)
    except DotenvError as error:
        print(f"strict dotenv: {error}", file=sys.stderr)
        return 1
    if args.get is not None:
        if args.get not in values:
            print(f"strict dotenv: missing key {args.get}", file=sys.stderr)
            return 1
        print(values[args.get])
        return 0
    for key, value in values.items():
        encoded = base64.b64encode(value.encode("utf-8")).decode("ascii")
        print(f"{key}\t{encoded}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
