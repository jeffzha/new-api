#!/usr/bin/env python3
"""Fail-closed live acceptance primitives for the Claw Workbench edge."""

from __future__ import annotations

import base64
import json
import math
import os
import re
import shutil
import ssl
import stat
import subprocess
import tempfile
import time
import xml.etree.ElementTree as ET
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from email.message import Message
from http.cookies import SimpleCookie
from pathlib import Path
from typing import Any, BinaryIO, Iterable
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urljoin, urlsplit, urlunsplit
from urllib.request import HTTPRedirectHandler, HTTPSHandler, Request, build_opener


MAX_CONFIG_BYTES = 1024 * 1024
MAX_FIXTURE_BYTES = 1024 * 1024
MAX_RESPONSE_BYTES = 1024 * 1024
EICAR_SHA256 = "275a021bbfb6489e54d471899f7db9d1663fc695ec2fe2a2c4538aabf651fd0f"
SECRET_KEY_RE = re.compile(
    r"(?i)(authorization|cookie|ticket|token|secret|appkey|api[_-]?key|access[_-]?key|private[_-]?key|password|credential)"
)
SECRET_VALUE_RE = re.compile(r"(?i)(bearer\s+\S+|\bsk-[A-Za-z0-9_-]{8,}|[?&](?:ticket|token|key)=[^&\s]+)")
VARIABLE_RE = re.compile(r"\$\{([A-Za-z][A-Za-z0-9_]*)\}")
PROVIDER_REFERENCE_KEYS = {"app_key_secret_ref", "secret_id_ref", "secret_key_ref"}
PROVIDER_REFERENCE_RE = re.compile(r"env://WORKBENCH_PROVIDER_[A-Z0-9_]+")
NON_SECRET_CREDENTIAL_KEYS = {"credential_profile_id"}
ACCEPTANCE_SIGNER_IDENTITY = "claw-workbench-e2e"
ACCEPTANCE_SIGNATURE_NAMESPACE = "claw-workbench-e2e-v1"
MAX_SSH_KEY_BYTES = 64 * 1024
MAX_SSH_SIGNATURE_BYTES = 64 * 1024

TOP_LEVEL_CONFIG_KEYS = frozenset(
    {
        "$schema", "version", "base_url", "direct_adp_base_url", "timeout_seconds",
        "acceptance_report_signing_key_file", "acceptance_report_allowed_signers_file",
        "secrets", "variables", "selection_targets", "release_manifest", "paths",
        "lifecycle", "turn", "isolation_checks", "eicar", "policy_checks",
        "limit_checks", "selector_checks", "oauth_checks", "scheduled_task_checks",
        "sandbox_checks", "gate_scenarios", "rotation_checks", "byok_checks",
        "usage_audit_checks", "billing_import_checks", "smoke_checks",
        "security_checks", "direct_adp_paths", "load",
        "acceptance_observer", "sandbox_mode", "sandbox_pty",
    }
)
REQUEST_LIST_KEYS = frozenset(
    {
        "isolation_checks", "policy_checks", "limit_checks", "selector_checks",
        "oauth_checks", "scheduled_task_checks", "sandbox_checks", "rotation_checks",
        "byok_checks", "usage_audit_checks", "billing_import_checks", "smoke_checks",
        "security_checks",
    }
)
REQUEST_KEYS = frozenset(
    {
        "name", "requirement", "actor", "method", "path", "body_fixture",
        "expect_status", "provider_cost", "capture", "capture_sha256",
        "json_equals", "json_not_equals", "json_one_of", "json_absent", "json_types",
        "header_equals", "poll", "require_response_markers",
        "forbid_response_markers", "csrf_mode", "cleanup",
        "assert_variables_equal", "assert_variables_not_equal",
        "selection_token_source", "selection_token_actor", "selection_target",
        "body_min_bytes", "fixture_json_min_lengths",
    }
)
REQUEST_ACTORS = frozenset(
    {
        "admin", "admin_requester", "admin_approver", "user_a", "user_b_same",
        "user_b_other", "user_a_context_1", "user_a_context_2",
        "user_a_customer_2", "user_a_stale_context", "api_key", "anonymous",
    }
)
REQUEST_METHODS = frozenset({"GET", "POST", "PUT", "PATCH", "DELETE"})
JSON_TYPE_NAMES = frozenset({"string", "number", "integer", "boolean", "object", "array", "null"})
SCALAR_TYPES = (str, int, float, bool, type(None))


def is_secret_key(key: str) -> bool:
    normalized = key.lower().replace("-", "_")
    # Token *counts* are legitimate model request/usage fields, not
    # credentials. Singular token, access_token, refresh_token, etc. remain
    # secret-like and are rejected/redacted.
    if normalized.endswith("_tokens") or normalized in {"tokens", "token_count"}:
        return False
    return bool(SECRET_KEY_RE.search(key))


class ConfigError(RuntimeError):
    pass


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):  # noqa: ANN001
        return None


@dataclass
class StoredCookie:
    value: str
    path: str = "/"
    secure: bool = True


@dataclass
class HTTPResponse:
    status: int
    headers: Message
    body: bytes
    elapsed_ms: float
    endpoint: str

    def json(self) -> Any:
        return json.loads(self.body.decode("utf-8"))


@dataclass
class CheckResult:
    order: int
    phase: str
    name: str
    status: str
    message: str
    endpoint: str = ""
    http_status: int | None = None
    duration_ms: float = 0.0
    evidence: dict[str, Any] = field(default_factory=dict)


class Redactor:
    def __init__(self) -> None:
        self._values: set[str] = set()

    def add(self, value: str | bytes | None) -> None:
        if value is None:
            return
        text = value.decode("utf-8", "ignore") if isinstance(value, bytes) else str(value)
        if len(text) < 4:
            return
        self._values.add(text)
        self._values.add(quote(text, safe=""))
        self._values.add(base64.b64encode(text.encode()).decode())

    def clean(self, value: Any) -> Any:
        if isinstance(value, dict):
            return {
                str(key): "[REDACTED]" if is_secret_key(str(key)) else self.clean(item)
                for key, item in value.items()
            }
        if isinstance(value, list):
            return [self.clean(item) for item in value]
        if not isinstance(value, str):
            return value
        cleaned = value
        for secret in sorted(self._values, key=len, reverse=True):
            if secret:
                cleaned = cleaned.replace(secret, "[REDACTED]")
        cleaned = SECRET_VALUE_RE.sub("[REDACTED]", cleaned)
        return cleaned


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def origin(url: str) -> str:
    parsed = urlsplit(url)
    return urlunsplit((parsed.scheme, parsed.netloc, "", "", ""))


def sanitized_endpoint(url: str) -> str:
    parsed = urlsplit(url)
    return urlunsplit((parsed.scheme, parsed.netloc, parsed.path or "/", "", ""))


def validate_base_url(value: str, *, allow_http: bool) -> str:
    parsed = urlsplit(value)
    if parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise ConfigError("base URLs cannot contain credentials, query strings, or fragments")
    if not parsed.hostname:
        raise ConfigError("base URL must have a host")
    loopback = parsed.hostname in {"localhost", "127.0.0.1", "::1"}
    if parsed.scheme != "https" and not (allow_http and loopback and parsed.scheme == "http"):
        raise ConfigError("HTTPS is required; HTTP is allowed only for explicit loopback self-tests")
    return value.rstrip("/")


def _read_file(path: Path, maximum: int, *, restricted: bool) -> bytes:
    if not path.is_absolute():
        raise ConfigError("referenced file must be an absolute, regular, non-symlink file")
    try:
        before = path.lstat()
    except OSError as error:
        raise ConfigError("referenced file cannot be inspected") from error
    if stat.S_ISLNK(before.st_mode) or not stat.S_ISREG(before.st_mode):
        raise ConfigError("referenced file must be an absolute, regular, non-symlink file")
    if before.st_size > maximum:
        raise ConfigError(f"referenced file exceeds {maximum} bytes")
    if restricted:
        if os.name == "nt":
            raise ConfigError("secret file references fail closed on Windows; use an env: reference")
        if stat.S_IMODE(before.st_mode) & 0o077:
            raise ConfigError("secret file permissions must not grant group or other access")
    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(path, flags)
    except OSError as error:
        raise ConfigError("referenced file cannot be opened safely") from error
    try:
        opened = os.fstat(descriptor)
        if not stat.S_ISREG(opened.st_mode) or (opened.st_dev, opened.st_ino) != (before.st_dev, before.st_ino):
            raise ConfigError("referenced file changed during validation")
        with os.fdopen(descriptor, "rb", closefd=False) as handle:
            data = handle.read(maximum + 1)
        if len(data) > maximum:
            raise ConfigError(f"referenced file exceeds {maximum} bytes")
        return data
    finally:
        os.close(descriptor)


def resolve_secret(ref: str) -> str:
    if ref.startswith("env:"):
        name = ref[4:]
        if not re.fullmatch(r"[A-Z][A-Z0-9_]*", name):
            raise ConfigError("invalid environment secret reference")
        value = os.environ.get(name, "")
        if not value:
            raise ConfigError(f"required environment value {name} is missing")
        return value
    if ref.startswith("file:"):
        value = _read_file(Path(ref[5:]), MAX_FIXTURE_BYTES, restricted=True).decode("utf-8").strip()
        if not value:
            raise ConfigError("secret file is empty")
        return value
    raise ConfigError("secret values must use env:NAME or file:/absolute/path")


def _read_file_reference(ref: str, *, maximum: int, restricted: bool) -> tuple[Path, bytes]:
    if not isinstance(ref, str) or not ref.startswith("file:"):
        raise ConfigError("acceptance report key material must use file:/absolute/path")
    path = Path(ref[5:])
    return path, _read_file(path, maximum, restricted=restricted)


def _ssh_keygen_path() -> str:
    executable = shutil.which("ssh-keygen")
    if not executable:
        raise ConfigError("ssh-keygen is required for acceptance report signatures")
    return executable


def _write_private_temp(path: Path, content: bytes) -> None:
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(descriptor, "wb", closefd=False) as handle:
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
    finally:
        os.close(descriptor)
    os.chmod(path, 0o600)


def _validate_ed25519_private_key(
    source_path: Path,
    key_bytes: bytes,
    ssh_keygen: str,
    directory: Path,
) -> Path:
    # Windows OpenSSH validates the source NTFS ACL and rejects a byte-for-byte
    # copy whose inherited ACL is broader. The source has already passed the
    # bounded lstat/open/fstat non-symlink read. POSIX uses a private temp copy
    # so ssh-keygen cannot follow a later replacement of the configured path.
    key_path = source_path
    if os.name != "nt":
        key_path = directory / "acceptance_signing_key"
        _write_private_temp(key_path, key_bytes)
    try:
        result = subprocess.run(
            [ssh_keygen, "-y", "-f", str(key_path)],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=15,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        raise ConfigError("acceptance report signing key could not be validated") from error
    public_key = result.stdout.decode("ascii", errors="replace").strip()
    if result.returncode != 0 or not public_key.startswith("ssh-ed25519 "):
        raise ConfigError("acceptance report signing key must be an unencrypted OpenSSH Ed25519 private key")
    return key_path


def validate_acceptance_signing_key(ref: str) -> None:
    """Fail closed before live requests if the configured signer is unusable."""

    source_path, key_bytes = _read_file_reference(
        ref,
        maximum=MAX_SSH_KEY_BYTES,
        restricted=os.name != "nt",
    )
    ssh_keygen = _ssh_keygen_path()
    with tempfile.TemporaryDirectory(prefix="claw-e2e-key-") as directory:
        _validate_ed25519_private_key(source_path, key_bytes, ssh_keygen, Path(directory))


def sign_evidence_file(path_value: str | Path, signing_key_ref: str) -> Path:
    """Create an OpenSSH detached Ed25519 signature for an exact evidence file."""

    evidence_path = Path(path_value)
    evidence = load_evidence_file(str(evidence_path), maximum=MAX_CONFIG_BYTES)
    source_path, key_bytes = _read_file_reference(
        signing_key_ref,
        maximum=MAX_SSH_KEY_BYTES,
        restricted=os.name != "nt",
    )
    ssh_keygen = _ssh_keygen_path()
    with tempfile.TemporaryDirectory(prefix="claw-e2e-sign-") as directory:
        temp_root = Path(directory)
        key_path = _validate_ed25519_private_key(source_path, key_bytes, ssh_keygen, temp_root)
        message_path = temp_root / "results.json"
        _write_private_temp(message_path, evidence)
        try:
            result = subprocess.run(
                [
                    ssh_keygen,
                    "-Y", "sign",
                    "-f", str(key_path),
                    "-n", ACCEPTANCE_SIGNATURE_NAMESPACE,
                    str(message_path),
                ],
                stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=15,
                check=False,
            )
        except (OSError, subprocess.TimeoutExpired) as error:
            raise ConfigError("acceptance report signing failed") from error
        if result.returncode != 0:
            raise ConfigError("acceptance report signing failed")
        signature_bytes = _read_file(
            Path(str(message_path) + ".sig"),
            MAX_SSH_SIGNATURE_BYTES,
            restricted=False,
        )
    try:
        signature = signature_bytes.decode("ascii")
    except UnicodeDecodeError as error:
        raise ConfigError("acceptance report signature is not ASCII armored") from error
    if not signature.startswith("-----BEGIN SSH SIGNATURE-----\n"):
        raise ConfigError("acceptance report signature is not an OpenSSH signature")
    signature_path = evidence_path.with_name(evidence_path.name + ".sig")
    write_atomic(signature_path, signature if signature.endswith("\n") else signature + "\n")
    return signature_path


def verify_evidence_signature(
    path_value: str | Path,
    allowed_signers_ref: str,
) -> bytes:
    """Verify an exact evidence file using only the pinned OpenSSH public verifier."""

    evidence_path = Path(path_value)
    evidence = load_evidence_file(str(evidence_path), maximum=MAX_CONFIG_BYTES)
    signature = load_evidence_file(
        str(evidence_path.with_name(evidence_path.name + ".sig")),
        maximum=MAX_SSH_SIGNATURE_BYTES,
    )
    _, allowed_signers = _read_file_reference(
        allowed_signers_ref,
        maximum=MAX_SSH_KEY_BYTES,
        restricted=False,
    )
    try:
        active_lines = [
            line.strip()
            for line in allowed_signers.decode("utf-8").splitlines()
            if line.strip() and not line.lstrip().startswith("#")
        ]
    except UnicodeDecodeError as error:
        raise ConfigError("acceptance allowed_signers file is not valid UTF-8") from error
    if len(active_lines) != 1:
        raise ConfigError("acceptance allowed_signers must contain exactly one signer")
    fields = active_lines[0].split()
    if (
        len(fields) < 3
        or fields[0] != ACCEPTANCE_SIGNER_IDENTITY
        or "," in fields[0]
        or "ssh-ed25519" not in fields[1:]
    ):
        raise ConfigError(
            "acceptance allowed_signers must pin claw-workbench-e2e to an ssh-ed25519 key"
        )
    ssh_keygen = _ssh_keygen_path()
    with tempfile.TemporaryDirectory(prefix="claw-e2e-verify-") as directory:
        temp_root = Path(directory)
        allowed_path = temp_root / "allowed_signers"
        signature_path = temp_root / "results.json.sig"
        _write_private_temp(allowed_path, allowed_signers)
        _write_private_temp(signature_path, signature)
        try:
            result = subprocess.run(
                [
                    ssh_keygen,
                    "-Y", "verify",
                    "-f", str(allowed_path),
                    "-I", ACCEPTANCE_SIGNER_IDENTITY,
                    "-n", ACCEPTANCE_SIGNATURE_NAMESPACE,
                    "-s", str(signature_path),
                ],
                input=evidence,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=15,
                check=False,
            )
        except (OSError, subprocess.TimeoutExpired) as error:
            raise ConfigError("acceptance report signature could not be verified") from error
    if result.returncode != 0:
        raise ConfigError("acceptance report signature is missing or invalid")
    return evidence


def _reject_unknown(value: dict[str, Any], allowed: frozenset[str] | set[str], label: str) -> None:
    unknown = sorted(set(value).difference(allowed))
    if unknown:
        raise ConfigError(f"{label} contains unknown fields: {', '.join(unknown)}")


def _is_integer(value: Any) -> bool:
    return isinstance(value, int) and not isinstance(value, bool)


def _validate_path(value: Any, label: str) -> None:
    if not isinstance(value, str) or not value.startswith("/") or "\r" in value or "\n" in value:
        raise ConfigError(f"{label} must be a same-origin absolute path")


def _validate_file_ref(value: Any, label: str) -> None:
    if not isinstance(value, str) or not value.startswith("file:"):
        raise ConfigError(f"{label} must use file:/absolute/path")
    raw_path = value[5:]
    if not (raw_path.startswith("/") or re.match(r"^[A-Za-z]:[\\/]", raw_path)):
        raise ConfigError(f"{label} must use file:/absolute/path")


def _validate_secret_ref(value: Any, label: str) -> None:
    if not isinstance(value, str):
        raise ConfigError(f"{label} must be a secret reference")
    if value.startswith("env:") and re.fullmatch(r"env:[A-Z][A-Z0-9_]*", value):
        return
    if value.startswith("file:"):
        _validate_file_ref(value, label)
        return
    raise ConfigError(f"{label} must use env:NAME or file:/absolute/path")


def _validate_string_map(value: Any, label: str, *, pointer_values: bool = False) -> None:
    if not isinstance(value, dict) or any(not isinstance(key, str) or not isinstance(item, str) for key, item in value.items()):
        raise ConfigError(f"{label} must be an object with string keys and values")
    if pointer_values and any(not item.startswith("/") for item in value.values()):
        raise ConfigError(f"{label} values must be JSON pointers")


def _validate_request_spec(value: Any, label: str) -> None:
    if not isinstance(value, dict):
        raise ConfigError(f"{label} must be an object")
    _reject_unknown(value, REQUEST_KEYS, label)
    for required in ("name", "actor", "method", "path", "expect_status"):
        if required not in value:
            raise ConfigError(f"{label}.{required} is required")
    if not isinstance(value["name"], str) or not value["name"].strip():
        raise ConfigError(f"{label}.name must be a non-empty string")
    if value["actor"] not in REQUEST_ACTORS:
        raise ConfigError(f"{label}.actor is not supported")
    if value["method"] not in REQUEST_METHODS:
        raise ConfigError(f"{label}.method is not supported")
    _validate_path(value["path"], f"{label}.path")
    statuses = value["expect_status"]
    if not isinstance(statuses, list) or not statuses or any(not _is_integer(item) or not 100 <= item <= 599 for item in statuses):
        raise ConfigError(f"{label}.expect_status must contain HTTP status integers")
    requirement = value.get("requirement")
    if requirement is not None and (
        not isinstance(requirement, str)
        or not re.fullmatch(r"[a-z][a-z0-9_]{1,63}", requirement)
    ):
        raise ConfigError(f"{label}.requirement is invalid")
    if "body_fixture" in value:
        _validate_file_ref(value["body_fixture"], f"{label}.body_fixture")
    if "body_min_bytes" in value and (
        not _is_integer(value["body_min_bytes"])
        or not 1 <= value["body_min_bytes"] <= MAX_FIXTURE_BYTES
        or "body_fixture" not in value
    ):
        raise ConfigError(f"{label}.body_min_bytes requires a fixture and must be between 1 and {MAX_FIXTURE_BYTES}")
    if "fixture_json_min_lengths" in value:
        assertions = value["fixture_json_min_lengths"]
        if (
            "body_fixture" not in value
            or not isinstance(assertions, dict)
            or not assertions
            or any(
                not isinstance(pointer, str)
                or not pointer.startswith("/")
                or not _is_integer(minimum)
                or not 1 <= minimum <= MAX_FIXTURE_BYTES
                for pointer, minimum in assertions.items()
            )
        ):
            raise ConfigError(
                f"{label}.fixture_json_min_lengths must map JSON pointers to positive bounded lengths and requires a fixture"
            )
    for key in ("provider_cost", "cleanup"):
        if key in value and not isinstance(value[key], bool):
            raise ConfigError(f"{label}.{key} must be a boolean")
    if value.get("csrf_mode", "auto") not in {"auto", "omit", "invalid"}:
        raise ConfigError(f"{label}.csrf_mode must be auto, omit, or invalid")
    token_source = value.get("selection_token_source")
    if token_source is not None:
        if token_source not in {"consumed", "option"}:
            raise ConfigError(f"{label}.selection_token_source must be consumed or option")
        if value["method"] != "POST" or value["path"] != "/api/workbench/selections/choose":
            raise ConfigError(f"{label}.selection_token_source is allowed only on the selection choose endpoint")
        token_actor = value.get("selection_token_actor")
        if token_actor not in {"user_a", "user_a_context_1", "user_a_context_2", "user_a_customer_2", "user_a_stale_context", "user_b_same", "user_b_other"}:
            raise ConfigError(f"{label}.selection_token_actor is invalid")
        if value["actor"] != token_actor:
            raise ConfigError(f"{label}.actor must match selection_token_actor")
        if "body_fixture" in value:
            raise ConfigError(f"{label} cannot combine selection_token_source with body_fixture")
        target = value.get("selection_target")
        if token_source == "option":
            if not isinstance(target, dict) or not target:
                raise ConfigError(f"{label}.selection_target is required for option tokens")
            _reject_unknown(target, {"customer_code", "app_selector"}, f"{label}.selection_target")
            if any(not isinstance(item, str) or not item for item in target.values()):
                raise ConfigError(f"{label}.selection_target values must be non-empty strings")
        elif target is not None:
            raise ConfigError(f"{label}.selection_target is allowed only for option tokens")
    elif "selection_token_actor" in value or "selection_target" in value:
        raise ConfigError(f"{label}.selection_token_source is required with selection token fields")
    for key in ("capture", "capture_sha256"):
        if key in value:
            _validate_string_map(value[key], f"{label}.{key}", pointer_values=True)
    for key in ("assert_variables_equal", "assert_variables_not_equal"):
        if key in value:
            _validate_string_map(value[key], f"{label}.{key}")
    for key in ("json_equals", "json_not_equals"):
        if key in value:
            assertions = value[key]
            if not isinstance(assertions, dict) or any(
                not isinstance(pointer, str)
                or not pointer.startswith("/")
                or not isinstance(expected, SCALAR_TYPES)
                for pointer, expected in assertions.items()
            ):
                raise ConfigError(f"{label}.{key} must map JSON pointers to scalar values")
    if "json_one_of" in value:
        assertions = value["json_one_of"]
        if (
            not isinstance(assertions, dict)
            or not assertions
            or any(
                not isinstance(pointer, str)
                or not pointer.startswith("/")
                or not isinstance(allowed, list)
                or not allowed
                or any(not isinstance(item, SCALAR_TYPES) for item in allowed)
                or len({json.dumps(item, sort_keys=True) for item in allowed}) != len(allowed)
                for pointer, allowed in assertions.items()
            )
        ):
            raise ConfigError(f"{label}.json_one_of must map JSON pointers to unique non-empty scalar arrays")
    if "json_absent" in value and (
        not isinstance(value["json_absent"], list)
        or any(not isinstance(pointer, str) or not pointer.startswith("/") for pointer in value["json_absent"])
    ):
        raise ConfigError(f"{label}.json_absent must contain JSON pointers")
    if "json_types" in value:
        assertions = value["json_types"]
        if not isinstance(assertions, dict) or any(
            not isinstance(pointer, str)
            or not pointer.startswith("/")
            or expected not in JSON_TYPE_NAMES
            for pointer, expected in assertions.items()
        ):
            raise ConfigError(f"{label}.json_types is invalid")
    if "header_equals" in value:
        _validate_string_map(value["header_equals"], f"{label}.header_equals")
    for key in ("require_response_markers", "forbid_response_markers"):
        if key in value and (
            not isinstance(value[key], list)
            or any(not isinstance(item, str) or not item for item in value[key])
        ):
            raise ConfigError(f"{label}.{key} must contain non-empty strings")
    if "poll" in value:
        poll = value["poll"]
        if not isinstance(poll, dict):
            raise ConfigError(f"{label}.poll must be an object")
        _reject_unknown(poll, {"pointer", "equals", "timeout_seconds", "interval_seconds", "forbid_values"}, f"{label}.poll")
        if "pointer" not in poll or "equals" not in poll:
            raise ConfigError(f"{label}.poll requires pointer and equals")
        if not isinstance(poll["pointer"], str) or not poll["pointer"].startswith("/"):
            raise ConfigError(f"{label}.poll.pointer must be a JSON pointer")
        if not isinstance(poll["equals"], SCALAR_TYPES):
            raise ConfigError(f"{label}.poll.equals must be a scalar")
        timeout = poll.get("timeout_seconds", 60)
        interval = poll.get("interval_seconds", 1)
        if not _is_integer(timeout) or not 1 <= timeout <= 900:
            raise ConfigError(f"{label}.poll.timeout_seconds must be between 1 and 900")
        if isinstance(interval, bool) or not isinstance(interval, (int, float)) or not 0.1 <= interval <= 10:
            raise ConfigError(f"{label}.poll.interval_seconds must be between 0.1 and 10")
        forbidden = poll.get("forbid_values", [])
        if not isinstance(forbidden, list) or any(not isinstance(item, SCALAR_TYPES) for item in forbidden):
            raise ConfigError(f"{label}.poll.forbid_values must contain scalar values")


def _validate_request_list(value: Any, label: str) -> None:
    if not isinstance(value, list):
        raise ConfigError(f"{label} must be an array")
    for index, spec in enumerate(value):
        _validate_request_spec(spec, f"{label}[{index}]")


def validate_config_contract(config: Any) -> None:
    """Validate the executable config contract before resolving any secret or doing I/O."""

    if not isinstance(config, dict):
        raise ConfigError("configuration root must be an object")
    _reject_unknown(config, TOP_LEVEL_CONFIG_KEYS, "configuration")
    if config.get("version") != 1:
        raise ConfigError("configuration version must be 1")
    for required in ("base_url", "secrets"):
        if required not in config:
            raise ConfigError(f"configuration.{required} is required")
    for key in ("base_url", "direct_adp_base_url"):
        if key in config and not isinstance(config[key], str):
            raise ConfigError(f"configuration.{key} must be a string")
    for key in ("acceptance_report_signing_key_file", "acceptance_report_allowed_signers_file"):
        if key in config:
            _validate_file_ref(config[key], key)
    if (
        "acceptance_report_signing_key_file" in config
        and "acceptance_report_allowed_signers_file" in config
    ):
        raise ConfigError("acceptance signer and verifier files must be configured in separate roles")
    secrets = config["secrets"]
    if not isinstance(secrets, dict):
        raise ConfigError("secrets must be an object of references")
    if "user_a_session" not in secrets:
        raise ConfigError("secrets.user_a_session is required")
    for key, ref in secrets.items():
        if not isinstance(key, str) or not key:
            raise ConfigError("secret names must be non-empty strings")
        _validate_secret_ref(ref, f"secrets.{key}")
    variables = config.get("variables", {})
    if not isinstance(variables, dict) or any(
        not re.fullmatch(r"[A-Za-z][A-Za-z0-9_]*", str(key))
        or not isinstance(value, (str, int, bool))
        for key, value in variables.items()
    ):
        raise ConfigError("variables must map simple identifier keys to scalar values")
    timeout = config.get("timeout_seconds", 20)
    if not _is_integer(timeout) or not 1 <= timeout <= 900:
        raise ConfigError("timeout_seconds must be between 1 and 900")
    if "$schema" in config and not isinstance(config["$schema"], str):
        raise ConfigError("$schema must be a string")
    if "paths" in config:
        if not isinstance(config["paths"], dict):
            raise ConfigError("paths must be an object")
        for key, path in config["paths"].items():
            if not isinstance(key, str):
                raise ConfigError("path names must be strings")
            _validate_path(path, f"paths.{key}")
    if "selection_targets" in config:
        targets = config["selection_targets"]
        if not isinstance(targets, dict):
            raise ConfigError("selection_targets must be an object")
        for actor, target in targets.items():
            if not isinstance(actor, str) or not isinstance(target, dict) or not target:
                raise ConfigError("selection_targets entries must be non-empty objects")
            if actor not in {"user_a", "user_a_context_1", "user_a_context_2", "user_a_customer_2", "user_a_stale_context", "user_b_same", "user_b_other"}:
                raise ConfigError(f"selection_targets.{actor} is not a supported browser context")
            _reject_unknown(target, {"customer_code", "app_selector"}, f"selection_targets.{actor}")
            if any(not isinstance(item, str) or not item for item in target.values()):
                raise ConfigError(f"selection_targets.{actor} values must be non-empty strings")
    if "release_manifest" in config:
        manifest = config["release_manifest"]
        manifest_keys = {
            "new_api_revision", "claw_control_revision", "adp_revision",
            "new_api_image_digest", "claw_control_image_digest", "adp_image_digest",
            "config_sha256", "control_migration", "adp_migration", "caddy_version",
            "active_color", "provider_region", "collected_at",
        }
        if isinstance(manifest, str):
            _validate_file_ref(manifest, "release_manifest")
        else:
            if not isinstance(manifest, dict):
                raise ConfigError("release_manifest must be an object or file:/absolute/path")
            _reject_unknown(manifest, manifest_keys, "release_manifest")
            if set(manifest) != manifest_keys:
                raise ConfigError("release_manifest must contain every immutable release field")
            if any(not isinstance(value, str) or not value for value in manifest.values()):
                raise ConfigError("release_manifest values must be non-empty strings")
    if "lifecycle" in config:
        lifecycle = config["lifecycle"]
        lifecycle_keys = {"create_customer_members", "invalid_app_validation", "no_plan_gate", "activate_plan"}
        if not isinstance(lifecycle, dict):
            raise ConfigError("lifecycle must be an object")
        _reject_unknown(lifecycle, lifecycle_keys, "lifecycle")
        for key, specs in lifecycle.items():
            _validate_request_list(specs, f"lifecycle.{key}")
    for key in REQUEST_LIST_KEYS:
        if key in config:
            _validate_request_list(config[key], key)
    if "gate_scenarios" in config:
        scenarios = config["gate_scenarios"]
        if not isinstance(scenarios, list):
            raise ConfigError("gate_scenarios must be an array")
        for index, scenario in enumerate(scenarios):
            label = f"gate_scenarios[{index}]"
            if not isinstance(scenario, dict):
                raise ConfigError(f"{label} must be an object")
            _reject_unknown(scenario, {"name", "state", "transition", "write_probe", "read_probe", "restore"}, label)
            if set(scenario) != {"name", "state", "transition", "write_probe", "read_probe", "restore"}:
                raise ConfigError(f"{label} is incomplete")
            if not isinstance(scenario["name"], str) or not scenario["name"] or scenario["state"] not in {"suspended", "disabled", "expired"}:
                raise ConfigError(f"{label} name or state is invalid")
            for key in ("transition", "write_probe", "read_probe", "restore"):
                _validate_request_spec(scenario[key], f"{label}.{key}")
    if "turn" in config:
        turn = config["turn"]
        if not isinstance(turn, dict):
            raise ConfigError("turn must be an object")
        _reject_unknown(turn, {"request_fixture", "disconnect_after_events"}, "turn")
        if "request_fixture" not in turn:
            raise ConfigError("turn.request_fixture is required")
        _validate_file_ref(turn["request_fixture"], "turn.request_fixture")
        disconnect = turn.get("disconnect_after_events", 2)
        if not _is_integer(disconnect) or not 1 <= disconnect <= 20:
            raise ConfigError("turn.disconnect_after_events must be between 1 and 20")
    if "eicar" in config:
        eicar = config["eicar"]
        if not isinstance(eicar, dict):
            raise ConfigError("eicar must be an object")
        _reject_unknown(eicar, {"file_ref"}, "eicar")
        if "file_ref" in eicar:
            _validate_file_ref(eicar["file_ref"], "eicar.file_ref")
    if "direct_adp_paths" in config:
        paths = config["direct_adp_paths"]
        if not isinstance(paths, list):
            raise ConfigError("direct_adp_paths must be an array")
        for index, path in enumerate(paths):
            _validate_path(path, f"direct_adp_paths[{index}]")
    if "sandbox_mode" in config and config["sandbox_mode"] not in {"off", "enabled"}:
        raise ConfigError("sandbox_mode must be off or enabled")
    if "acceptance_observer" in config:
        observer = config["acceptance_observer"]
        if not isinstance(observer, dict):
            raise ConfigError("acceptance_observer must be an object")
        _reject_unknown(observer, {"allowed_signers_file", "evidence", "wait_seconds", "max_age_seconds"}, "acceptance_observer")
        if not {"allowed_signers_file", "evidence"}.issubset(observer):
            raise ConfigError("acceptance_observer requires allowed_signers_file and evidence")
        _validate_file_ref(observer["allowed_signers_file"], "acceptance_observer.allowed_signers_file")
        evidence = observer["evidence"]
        if not isinstance(evidence, dict) or not evidence:
            raise ConfigError("acceptance_observer.evidence must be a non-empty object")
        for requirement, ref in evidence.items():
            if not re.fullmatch(r"(?:scheduled|byok|billing_import)\.[a-z][a-z0-9_]{1,63}", str(requirement)):
                raise ConfigError("acceptance_observer evidence keys must be qualified requirement IDs")
            _validate_file_ref(ref, f"acceptance_observer.evidence.{requirement}")
        for key, default, minimum, maximum in (("wait_seconds", 60, 1, 300), ("max_age_seconds", 900, 1, 3600)):
            value = observer.get(key, default)
            if not _is_integer(value) or not minimum <= value <= maximum:
                raise ConfigError(f"acceptance_observer.{key} must be between {minimum} and {maximum}")
    if "sandbox_pty" in config:
        pty = config["sandbox_pty"]
        if not isinstance(pty, dict):
            raise ConfigError("sandbox_pty must be an object")
        _reject_unknown(pty, {"actor", "ticket_path", "connect_path", "conversation_variable", "sandbox_variable", "timeout_seconds", "rows", "cols"}, "sandbox_pty")
        required = {"actor", "ticket_path", "connect_path", "conversation_variable", "sandbox_variable"}
        if not required.issubset(pty):
            raise ConfigError("sandbox_pty is incomplete")
        if pty["actor"] not in REQUEST_ACTORS - {"anonymous", "api_key"}:
            raise ConfigError("sandbox_pty.actor is invalid")
        for key in ("ticket_path", "connect_path"):
            _validate_path(pty[key], f"sandbox_pty.{key}")
        for key in ("conversation_variable", "sandbox_variable"):
            if not isinstance(pty[key], str) or not re.fullmatch(r"[A-Za-z][A-Za-z0-9_]*", pty[key]):
                raise ConfigError(f"sandbox_pty.{key} is invalid")
        for key, default, minimum, maximum in (("timeout_seconds", 30, 5, 120), ("rows", 24, 2, 200), ("cols", 80, 10, 400)):
            value = pty.get(key, default)
            if not _is_integer(value) or not minimum <= value <= maximum:
                raise ConfigError(f"sandbox_pty.{key} must be between {minimum} and {maximum}")
    if "load" in config:
        load = config["load"]
        if not isinstance(load, dict):
            raise ConfigError("load must be an object")
        if "external_evidence_signing_key_file" in load:
            raise ConfigError("load runner configuration must never contain the observer private signing key")
        _reject_unknown(
            load,
            {
                "request_fixture", "turn_path", "timeout_seconds",
                "external_evidence", "external_evidence_wait_seconds",
                "external_evidence_allowed_signers_file",
            },
            "load",
        )
        if "request_fixture" not in load:
            raise ConfigError("load.request_fixture is required")
        _validate_file_ref(load["request_fixture"], "load.request_fixture")
        if "turn_path" in load:
            _validate_path(load["turn_path"], "load.turn_path")
        timeout = load.get("timeout_seconds", 900)
        if not _is_integer(timeout) or not 10 <= timeout <= 3600:
            raise ConfigError("load.timeout_seconds must be between 10 and 3600")
        external_evidence = load.get("external_evidence")
        if external_evidence is not None:
            if not isinstance(external_evidence, dict) or set(external_evidence) != {"50", "100"}:
                raise ConfigError("load.external_evidence must contain exact 50 and 100 file references")
            for level, ref in external_evidence.items():
                _validate_file_ref(ref, f"load.external_evidence.{level}")
        observer_verifier = load.get("external_evidence_allowed_signers_file")
        if external_evidence is not None and observer_verifier is None:
            raise ConfigError(
                "load.external_evidence_allowed_signers_file is required with external evidence"
            )
        if observer_verifier is not None:
            _validate_file_ref(
                observer_verifier, "load.external_evidence_allowed_signers_file",
            )
        evidence_wait = load.get("external_evidence_wait_seconds", 60)
        if not _is_integer(evidence_wait) or not 1 <= evidence_wait <= 300:
            raise ConfigError("load.external_evidence_wait_seconds must be between 1 and 300")

    captured_variables: dict[str, str] = {}

    def inspect_capture_specs(value: Any, location: str) -> None:
        if isinstance(value, dict):
            for capture_key in ("capture", "capture_sha256"):
                capture = value.get(capture_key)
                if isinstance(capture, dict):
                    for variable in capture:
                        if variable in variables:
                            raise ConfigError(
                                f"variables.{variable} cannot pre-seed a value that must be captured from a live response"
                            )
                        previous = captured_variables.get(variable)
                        if previous is not None:
                            raise ConfigError(
                                f"captured variable {variable} is declared more than once: {previous} and {location}"
                            )
                        captured_variables[variable] = location
            for key, item in value.items():
                inspect_capture_specs(item, f"{location}.{key}")
        elif isinstance(value, list):
            for index, item in enumerate(value):
                inspect_capture_specs(item, f"{location}[{index}]")

    inspect_capture_specs(config, "configuration")


def load_config(path: str | Path, *, allow_http: bool = False) -> dict[str, Any]:
    config_path = Path(path)
    raw = _read_file(Path(os.path.abspath(config_path)), MAX_CONFIG_BYTES, restricted=False)
    try:
        config = json.loads(raw)
    except json.JSONDecodeError as error:
        raise ConfigError(f"configuration is not valid JSON: line {error.lineno}") from error
    validate_config_contract(config)
    manifest_ref = config.get("release_manifest")
    manifest_source = "file" if isinstance(manifest_ref, str) else "inline" if isinstance(manifest_ref, dict) else "missing"
    if isinstance(manifest_ref, str):
        raw_manifest = _read_file(Path(manifest_ref[5:]), 64 * 1024, restricted=False)
        try:
            config["release_manifest"] = json.loads(raw_manifest)
        except json.JSONDecodeError as error:
            raise ConfigError(f"release manifest is not valid JSON: line {error.lineno}") from error
        validate_config_contract(config)
    config["_release_manifest_source"] = manifest_source
    base_value = str(config.get("base_url", ""))
    if base_value.startswith(("env:", "file:")):
        base_value = resolve_secret(base_value)
    config["base_url"] = validate_base_url(base_value, allow_http=allow_http)
    if config.get("direct_adp_base_url"):
        direct_value = str(config["direct_adp_base_url"])
        if direct_value.startswith(("env:", "file:")):
            direct_value = resolve_secret(direct_value)
        config["direct_adp_base_url"] = validate_base_url(
            direct_value, allow_http=allow_http
        )
    variables = config.get("variables", {})
    for key, value in variables.items():
        if isinstance(value, str) and value.startswith(("env:", "file:")):
            variables[key] = resolve_secret(value)
    return config


def expand_variables(value: Any, variables: dict[str, Any]) -> Any:
    if isinstance(value, dict):
        return {key: expand_variables(item, variables) for key, item in value.items()}
    if isinstance(value, list):
        return [expand_variables(item, variables) for item in value]
    if not isinstance(value, str):
        return value

    def replace(match: re.Match[str]) -> str:
        name = match.group(1)
        if name not in variables:
            raise ConfigError(f"fixture references unknown variable {name}")
        return str(variables[name])

    return VARIABLE_RE.sub(replace, value)


def _assert_safe_fixture(value: Any, path: str = "$") -> None:
    if isinstance(value, dict):
        for key, item in value.items():
            normalized = str(key).lower()
            if normalized in PROVIDER_REFERENCE_KEYS:
                if not isinstance(item, str) or not PROVIDER_REFERENCE_RE.fullmatch(item):
                    raise ConfigError(f"safe fixture contains an invalid provider reference at {path}.{key}")
                continue
            if normalized in NON_SECRET_CREDENTIAL_KEYS:
                _assert_safe_fixture(item, f"{path}.{key}")
                continue
            if is_secret_key(str(key)):
                raise ConfigError(f"safe fixture contains forbidden secret-like key at {path}.{key}")
            _assert_safe_fixture(item, f"{path}.{key}")
    elif isinstance(value, list):
        for index, item in enumerate(value):
            _assert_safe_fixture(item, f"{path}[{index}]")
    elif isinstance(value, str) and SECRET_VALUE_RE.search(value):
        raise ConfigError(f"safe fixture contains a secret-like value at {path}")


def load_json_fixture(ref: str, variables: dict[str, Any]) -> dict[str, Any]:
    if not ref.startswith("file:"):
        raise ConfigError("request bodies must use file:/absolute/path fixtures")
    raw = _read_file(Path(ref[5:]), MAX_FIXTURE_BYTES, restricted=False)
    try:
        value = json.loads(raw)
    except json.JSONDecodeError as error:
        raise ConfigError(f"request fixture is invalid JSON: line {error.lineno}") from error
    if not isinstance(value, dict):
        raise ConfigError("request fixture root must be a JSON object")
    value = expand_variables(value, variables)
    _assert_safe_fixture(value)
    return value


def load_evidence_file(path_value: str, *, maximum: int = 1024 * 1024) -> bytes:
    """Read a bounded evidence artifact without following filesystem links."""

    return _read_file(Path(path_value), maximum, restricted=False)


def load_json_document(path_value: str, *, maximum: int = 1024 * 1024) -> Any:
    """Load a bounded JSON evidence file without following filesystem links."""

    try:
        raw = load_evidence_file(path_value, maximum=maximum)
        return json.loads(raw)
    except UnicodeDecodeError as error:
        raise ConfigError("JSON evidence file is not valid UTF-8") from error
    except json.JSONDecodeError as error:
        raise ConfigError(f"JSON evidence file is invalid JSON: line {error.lineno}") from error


def find_key(value: Any, key: str) -> Any:
    if isinstance(value, dict):
        for current, item in value.items():
            if current.lower() == key.lower():
                return item
        for item in value.values():
            found = find_key(item, key)
            if found is not None:
                return found
    elif isinstance(value, list):
        for item in value:
            found = find_key(item, key)
            if found is not None:
                return found
    return None


def find_pointer(value: Any, pointer: str) -> Any:
    present, found = find_pointer_state(value, pointer)
    return found if present else None


def find_pointer_state(value: Any, pointer: str) -> tuple[bool, Any]:
    if not pointer.startswith("/"):
        found = find_key(value, pointer)
        return found is not None, found
    current = value
    for part in pointer.lstrip("/").split("/"):
        part = part.replace("~1", "/").replace("~0", "~")
        if isinstance(current, dict) and part in current:
            current = current[part]
        elif isinstance(current, list) and part.isdigit() and int(part) < len(current):
            current = current[int(part)]
        else:
            return False, None
    return True, current


class HTTPClient:
    def __init__(
        self,
        base_url: str,
        *,
        timeout: int,
        initial_cookie: str = "",
        bearer: str = "",
        ca_file: str = "",
    ) -> None:
        self.base_url = base_url
        self.timeout = timeout
        self.bearer = bearer
        self.cookies: dict[str, StoredCookie] = {}
        self.request_count = 0
        if initial_cookie:
            parsed = SimpleCookie()
            try:
                parsed.load(initial_cookie)
            except Exception as error:  # pragma: no cover - defensive parser boundary
                raise ConfigError("session cookie material is invalid") from error
            if not parsed:
                raise ConfigError("session cookie material contains no cookies")
            for name, morsel in parsed.items():
                self.cookies[name] = StoredCookie(morsel.value, "/", True)
        context = ssl.create_default_context(cafile=ca_file or None)
        self.opener = build_opener(NoRedirect(), HTTPSHandler(context=context))

    def _url(self, path: str) -> str:
        parsed = urlsplit(path)
        if parsed.scheme or parsed.netloc or not path.startswith("/") or "\r" in path or "\n" in path:
            raise ConfigError("request paths must be same-origin absolute paths")
        url = urljoin(self.base_url + "/", path.lstrip("/"))
        if origin(url) != origin(self.base_url):
            raise ConfigError("request escaped the configured origin")
        return url

    def _cookie_header(self, path: str, scheme: str) -> str:
        pairs = []
        for name, cookie in self.cookies.items():
            if cookie.secure and scheme != "https" and urlsplit(self.base_url).hostname not in {"localhost", "127.0.0.1", "::1"}:
                continue
            if path.startswith(cookie.path.rstrip("/") + "/") or path == cookie.path or cookie.path == "/":
                pairs.append(f"{name}={cookie.value}")
        return "; ".join(pairs)

    def _capture_cookies(self, headers: Message) -> None:
        for header in headers.get_all("Set-Cookie", []):
            parsed = SimpleCookie()
            try:
                parsed.load(header)
            except Exception:
                continue
            for name, morsel in parsed.items():
                if morsel["max-age"] == "0" or not morsel.value:
                    self.cookies.pop(name, None)
                    continue
                self.cookies[name] = StoredCookie(
                    morsel.value,
                    morsel["path"] or "/",
                    bool(morsel["secure"]),
                )

    def _request(self, method: str, path: str, body: bytes | None, headers: dict[str, str]) -> Request:
        url = self._url(path)
        parsed = urlsplit(url)
        safe_headers = {"Accept": "application/json", "User-Agent": "claw-e2e/1"}
        safe_headers.update(headers)
        cookie = self._cookie_header(parsed.path, parsed.scheme)
        if cookie:
            safe_headers["Cookie"] = cookie
        if self.bearer:
            safe_headers["Authorization"] = f"Bearer {self.bearer}"
        self.request_count += 1
        return Request(url, data=body, headers=safe_headers, method=method)

    def request(
        self,
        method: str,
        path: str,
        *,
        body: bytes | None = None,
        headers: dict[str, str] | None = None,
    ) -> HTTPResponse:
        request = self._request(method, path, body, headers or {})
        started = time.perf_counter()
        try:
            response = self.opener.open(request, timeout=self.timeout)
        except HTTPError as error:
            response = error
        except (URLError, TimeoutError, OSError) as error:
            raise RuntimeError(f"network request failed: {type(error).__name__}") from error
        self._capture_cookies(response.headers)
        body_bytes = response.read(MAX_RESPONSE_BYTES + 1)
        if len(body_bytes) > MAX_RESPONSE_BYTES:
            response.close()
            raise RuntimeError("response exceeded the safe capture limit")
        elapsed = (time.perf_counter() - started) * 1000
        result = HTTPResponse(response.status, response.headers, body_bytes, elapsed, sanitized_endpoint(request.full_url))
        response.close()
        return result

    def open_stream(
        self,
        method: str,
        path: str,
        *,
        body: bytes | None = None,
        headers: dict[str, str] | None = None,
    ) -> tuple[int, Message, BinaryIO, float, str]:
        request = self._request(method, path, body, headers or {})
        started = time.perf_counter()
        try:
            response = self.opener.open(request, timeout=self.timeout)
        except HTTPError as error:
            response = error
        except (URLError, TimeoutError, OSError) as error:
            raise RuntimeError(f"stream request failed: {type(error).__name__}") from error
        self._capture_cookies(response.headers)
        return response.status, response.headers, response, started, sanitized_endpoint(request.full_url)


def parse_location(base_url: str, location: str) -> str:
    target = urljoin(base_url + "/", location)
    if origin(target) != origin(base_url):
        raise RuntimeError("redirect left the configured origin")
    parsed = urlsplit(target)
    return urlunsplit(("", "", parsed.path, parsed.query, ""))


def json_bytes(value: dict[str, Any]) -> bytes:
    return json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def write_atomic(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temp_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        os.chmod(temp_name, 0o600)
        with os.fdopen(descriptor, "w", encoding="utf-8", newline="\n") as handle:
            handle.write(content)
        os.replace(temp_name, path)
        os.chmod(path, 0o600)
    finally:
        if os.path.exists(temp_name):
            os.unlink(temp_name)


def write_reports(
    output: str | Path,
    *,
    run_id: str,
    started_at: str,
    finished_at: str,
    target: str,
    mode: str,
    permissions: dict[str, bool],
    results: Iterable[CheckResult],
    redactor: Redactor,
    release_manifest: dict[str, Any] | None = None,
) -> None:
    output_path = Path(output)
    safe_results = [redactor.clean(asdict(result)) for result in results]
    safe_release_manifest = redactor.clean(release_manifest or {})
    summary = {status: sum(1 for item in safe_results if item["status"] == status) for status in ("pass", "fail", "blocker", "skipped")}
    requirement_summary: dict[str, dict[str, Any]] = {}
    status_rank = {"pass": 0, "skipped": 1, "fail": 2, "blocker": 3}
    for item in safe_results:
        requirement = str(item.get("evidence", {}).get("requirement_id", ""))
        if not requirement:
            continue
        current = requirement_summary.setdefault(
            requirement,
            {"status": "pass", "checks": [], "assertion_types": []},
        )
        current["checks"].append(item["order"])
        current["assertion_types"] = sorted(
            set(current["assertion_types"]).union(item.get("evidence", {}).get("assertion_types", []))
        )
        if status_rank[item["status"]] > status_rank[current["status"]]:
            current["status"] = item["status"]
    payload = {
        "schema_version": 1,
        "run_id": run_id,
        "started_at": started_at,
        "finished_at": finished_at,
        "target": sanitized_endpoint(target),
        "mode": mode,
        "permissions": permissions,
        "release_manifest": safe_release_manifest,
        "summary": summary,
        "requirement_summary": requirement_summary,
        "results": safe_results,
    }
    serialized_results = json.dumps(payload, ensure_ascii=False, indent=2) + "\n"
    write_atomic(output_path / "results.json", serialized_results)

    suite = ET.Element(
        "testsuite",
        name="claw-workbench-live-acceptance",
        tests=str(len(safe_results)),
        failures=str(summary["fail"]),
        errors=str(summary["blocker"]),
        skipped=str(summary["skipped"]),
    )
    for item in safe_results:
        case = ET.SubElement(
            suite,
            "testcase",
            classname=str(item["phase"]),
            name=str(item["name"]),
            time=f"{float(item['duration_ms']) / 1000:.3f}",
        )
        if item["status"] == "fail":
            ET.SubElement(case, "failure", message=str(item["message"])).text = str(item["message"])
        elif item["status"] == "blocker":
            ET.SubElement(case, "error", message=str(item["message"])).text = str(item["message"])
        elif item["status"] == "skipped":
            ET.SubElement(case, "skipped", message=str(item["message"]))
        ET.SubElement(case, "system-out").text = json.dumps(
            {"endpoint": item["endpoint"], "http_status": item["http_status"], "evidence": item["evidence"]},
            ensure_ascii=False,
        )
    write_atomic(output_path / "junit.xml", ET.tostring(suite, encoding="unicode") + "\n")

    rows = []
    for item in safe_results:
        evidence = json.dumps(item["evidence"], ensure_ascii=False, separators=(",", ":"))
        rows.append(
            f"| {item['order']} | {item['phase']} | {item['name']} | {item['status']} | "
            f"{item['http_status'] or ''} | {float(item['duration_ms']):.1f} ms | {evidence} |"
        )
    release_rows = [
        f"| {str(key).replace('|', '&#124;')} | {json.dumps(value, ensure_ascii=False).replace('|', '&#124;').replace('`', '&#96;')} |"
        for key, value in sorted(safe_release_manifest.items())
    ]
    if not release_rows:
        release_rows.append("| missing | blocker |")
    requirement_rows = [
        f"| {requirement} | {item['status']} | {','.join(str(check) for check in item['checks'])} | {','.join(item['assertion_types'])} |"
        for requirement, item in sorted(requirement_summary.items())
    ]
    if not requirement_rows:
        requirement_rows.append("| missing | blocker |  |  |")
    markdown = f"""# Claw Workbench acceptance report

- Run ID: `{run_id}`
- Started/finished (UTC): `{started_at}` / `{finished_at}`
- Target: `{sanitized_endpoint(target)}`
- Mode: `{mode}`
- Provider cost allowed: `{permissions['provider_cost']}`
- Mutations allowed: `{permissions['mutations']}`

## Immutable release binding

| Item | Exact value |
|---|---|
{os.linesep.join(release_rows)}

## Summary

| Passed | Failed | Blocked | Skipped |
|---:|---:|---:|---:|
| {summary['pass']} | {summary['fail']} | {summary['blocker']} | {summary['skipped']} |

## Section 19 ordered results

| # | Phase | Check | Result | HTTP | Duration | Sanitized evidence |
|---:|---|---|---|---:|---:|---|
{os.linesep.join(rows)}

## Requirement evidence matrix

| Requirement ID | Result | Check numbers | Assertion types |
|---|---|---|---|
{os.linesep.join(requirement_rows)}

## External performance evidence placeholders

- Active edge/backend connections: not collected by this runner.
- ADP/new-api/claw-control/container memory: not collected by this runner.
- Persisted Turn-event count/database latency: not collected by this runner.

No Cookie, ticket, secret, request body, raw response, or raw SSE event is included.
"""
    write_atomic(output_path / "report.md", markdown)


def percentile(values: list[float], percentile_value: float) -> float | None:
    if not values:
        return None
    ordered = sorted(values)
    position = (len(ordered) - 1) * percentile_value
    lower = math.floor(position)
    upper = math.ceil(position)
    if lower == upper:
        return ordered[lower]
    return ordered[lower] + (ordered[upper] - ordered[lower]) * (position - lower)
