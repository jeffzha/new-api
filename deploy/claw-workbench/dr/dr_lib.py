#!/usr/bin/env python3
"""Security and reporting primitives for Claw Workbench restic DR."""

from __future__ import annotations

import hashlib
import json
import os
import re
import stat
import subprocess
import tempfile
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from urllib.parse import urlsplit, urlunsplit


MAX_CONFIG_BYTES = 1024 * 1024
MAX_SECRET_BYTES = 64 * 1024
MAX_COMMAND_OUTPUT = 4 * 1024 * 1024
REF_RE = re.compile(r"^(?:env:[A-Z][A-Z0-9_]*|file:(?:/|[A-Za-z]:[\\/]).+)$")
BUCKET_RE = re.compile(r"^[a-z0-9][a-z0-9.-]{1,222}[a-z0-9]$")
REGION_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9-]{0,62}$")
PREFIX_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$")
SHA256_RE = re.compile(r"^[a-f0-9]{64}$")
SECRET_KEY_RE = re.compile(r"(?i)(secret|password|credential|access[_-]?key|session[_-]?token|authorization|cookie)")


class DRError(RuntimeError):
    pass


@dataclass
class CommandResult:
    returncode: int
    stdout: bytes
    stderr: bytes
    elapsed_seconds: float


@dataclass
class DRResult:
    phase: str
    target: str
    status: str
    message: str
    elapsed_seconds: float = 0.0
    evidence: dict[str, Any] = field(default_factory=dict)


class Redactor:
    def __init__(self) -> None:
        self.values: set[str] = set()

    def add(self, value: str | bytes | None) -> None:
        if value is None:
            return
        text = value.decode("utf-8", "ignore") if isinstance(value, bytes) else str(value)
        if len(text) >= 4:
            self.values.add(text)

    def clean(self, value: Any) -> Any:
        if isinstance(value, dict):
            return {
                str(key): "[REDACTED]" if SECRET_KEY_RE.search(str(key)) else self.clean(item)
                for key, item in value.items()
            }
        if isinstance(value, list):
            return [self.clean(item) for item in value]
        if not isinstance(value, str):
            return value
        result = value
        for secret in sorted(self.values, key=len, reverse=True):
            result = result.replace(secret, "[REDACTED]")
        return result


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def parse_utc(value: str) -> datetime:
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as error:
        raise DRError("manifest timestamp is not valid ISO-8601") from error
    if parsed.tzinfo is None:
        raise DRError("manifest timestamp must include a timezone")
    return parsed.astimezone(timezone.utc)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def read_bounded(path: Path, maximum: int) -> bytes:
    if not path.is_absolute() or path.is_symlink() or not path.is_file():
        raise DRError("file reference must be an absolute regular non-symlink file")
    if path.stat().st_size > maximum:
        raise DRError(f"file exceeds {maximum} bytes")
    return path.read_bytes()


def resolve_ref(ref: str) -> str:
    if ref.startswith("env:"):
        name = ref[4:]
        if not re.fullmatch(r"[A-Z][A-Z0-9_]*", name):
            raise DRError("invalid environment reference")
        value = os.environ.get(name, "")
        if not value:
            raise DRError(f"required environment value {name} is missing")
        return value
    if ref.startswith("file:"):
        path = Path(ref[5:])
        data = read_bounded(path, MAX_SECRET_BYTES)
        if os.name == "nt":
            raise DRError("secret file ACL validation fails closed on Windows; use env:NAME")
        if stat.S_IMODE(path.stat().st_mode) & 0o077:
            raise DRError("secret file must not grant group or other permissions")
        value = data.decode("utf-8").strip()
        if not value:
            raise DRError("secret file is empty")
        return value
    raise DRError("credentials and repository passwords must use env:NAME or file:/absolute/path")


def safe_endpoint(value: str, *, allow_http: bool) -> str:
    parsed = urlsplit(value)
    if parsed.username or parsed.password or parsed.query or parsed.fragment or not parsed.hostname:
        raise DRError("S3 endpoint cannot contain credentials, query, fragment, or an empty host")
    loopback = parsed.hostname in {"localhost", "127.0.0.1", "::1"}
    if parsed.scheme != "https" and not (allow_http and loopback and parsed.scheme == "http"):
        raise DRError("S3 endpoints require HTTPS; HTTP is allowed only for explicit loopback tests")
    if parsed.path not in {"", "/"}:
        raise DRError("S3 endpoint path must be empty; configure bucket and prefix separately")
    return urlunsplit((parsed.scheme, parsed.netloc.lower(), "", "", ""))


def canonical_path(value: str | Path) -> Path:
    path = Path(value)
    if not path.is_absolute():
        raise DRError("operational paths must be absolute")
    return path.resolve(strict=False)


def is_within(path: Path, root: Path) -> bool:
    try:
        path.relative_to(root)
        return True
    except ValueError:
        return False


def load_config(path: str | Path, *, allow_http: bool = False) -> dict[str, Any]:
    config_path = Path(path).resolve()
    raw = read_bounded(config_path, MAX_CONFIG_BYTES)
    try:
        config = json.loads(raw)
    except json.JSONDecodeError as error:
        raise DRError(f"configuration is invalid JSON at line {error.lineno}") from error
    if not isinstance(config, dict) or config.get("version") != 1:
        raise DRError("configuration version must be 1")
    allowed_top = {
        "$schema", "version", "backup_staging_root", "drill_root", "protected_roots",
        "restic", "backup", "restore_validation_commands", "targets", "retention", "objectives",
    }
    unknown_top = set(config) - allowed_top
    if unknown_top:
        raise DRError(f"unknown top-level configuration keys: {', '.join(sorted(unknown_top))}")
    targets = config.get("targets")
    if not isinstance(targets, list) or len(targets) != 2:
        raise DRError("exactly two DR targets are required")
    labels = [target.get("name") for target in targets if isinstance(target, dict)]
    if len(labels) != 2 or len(set(labels)) != 2:
        raise DRError("two unique target names are required")
    for target in targets:
        allowed_target = {
            "name", "endpoint", "bucket", "region", "prefix", "access_key_id_ref",
            "secret_access_key_ref", "session_token_ref", "repository_password_ref",
        }
        unknown_target = set(target) - allowed_target
        if unknown_target:
            raise DRError(f"target contains unknown keys: {', '.join(sorted(unknown_target))}")
        target["endpoint"] = safe_endpoint(str(target.get("endpoint", "")), allow_http=allow_http)
        if not BUCKET_RE.fullmatch(str(target.get("bucket", ""))):
            raise DRError(f"target {target.get('name')} has an invalid bucket")
        if not REGION_RE.fullmatch(str(target.get("region", ""))):
            raise DRError(f"target {target.get('name')} has an invalid region")
        prefix = str(target.get("prefix", "claw-workbench"))
        prefix_parts = prefix.strip("/").split("/")
        if not PREFIX_RE.fullmatch(prefix) or any(part in {"", ".", ".."} for part in prefix_parts):
            raise DRError(f"target {target.get('name')} has an invalid prefix")
        target["prefix"] = prefix.strip("/")
        for key in ("access_key_id_ref", "secret_access_key_ref", "repository_password_ref"):
            if not isinstance(target.get(key), str) or not REF_RE.fullmatch(target[key]):
                raise DRError(f"target {target.get('name')} {key} must be an env/file reference")
        if target.get("session_token_ref") and not REF_RE.fullmatch(str(target["session_token_ref"])):
            raise DRError(f"target {target.get('name')} session_token_ref must be an env/file reference")
    first, second = targets
    if first["endpoint"].lower() == second["endpoint"].lower():
        raise DRError("primary and secondary endpoints must differ")
    if first["bucket"].lower() == second["bucket"].lower():
        raise DRError("primary and secondary buckets must differ")
    if first["region"].lower() == second["region"].lower():
        raise DRError("primary and secondary regions must differ")
    if repository_url(first) == repository_url(second):
        raise DRError("primary and secondary repositories must differ")
    for key in ("backup_staging_root", "drill_root"):
        config[key] = str(canonical_path(config.get(key, "")))
    protected = config.get("protected_roots", [])
    if not isinstance(protected, list) or not protected:
        raise DRError("protected_roots must list production paths")
    config["protected_roots"] = [str(canonical_path(item)) for item in protected]
    if any(is_within(Path(config["drill_root"]), Path(root)) or is_within(Path(root), Path(config["drill_root"])) for root in config["protected_roots"]):
        raise DRError("drill_root must be isolated from every protected production root")
    validate_command_config(config.get("restic"), "restic")
    validate_command_config(config.get("backup"), "backup", require_version=False)
    validations = config.get("restore_validation_commands")
    if not isinstance(validations, list) or not validations:
        raise DRError("at least one restore_validation_command is required")
    for index, command in enumerate(validations):
        validate_command_config(command, f"restore_validation_commands[{index}]", require_version=False)
    retention = config.get("retention", {})
    if not isinstance(retention, dict) or not retention:
        raise DRError("retention policy is required")
    if set(retention) - {"daily", "weekly", "monthly", "yearly", "local_verified_backups"}:
        raise DRError("retention contains unknown keys")
    for key in ("daily", "weekly", "monthly", "yearly", "local_verified_backups"):
        value = retention.get(key)
        if not isinstance(value, int) or not 1 <= value <= 10000:
            raise DRError(f"retention.{key} must be between 1 and 10000")
    objectives = config.get("objectives", {})
    for key in ("rpo_seconds", "rto_seconds"):
        if not isinstance(objectives.get(key), int) or objectives[key] <= 0:
            raise DRError(f"objectives.{key} must be a positive integer")
    return config


def validate_command_config(value: Any, name: str, *, require_version: bool = True) -> None:
    if not isinstance(value, dict):
        raise DRError(f"{name} command configuration is required")
    unknown = set(value) - {"command", "sha256", "expected_version", "environment_allowlist", "timeout_seconds"}
    if unknown:
        raise DRError(f"{name} contains unknown keys: {', '.join(sorted(unknown))}")
    command = value.get("command")
    hashes = value.get("sha256")
    if not isinstance(command, list) or not command or not all(isinstance(item, str) and item for item in command):
        raise DRError(f"{name}.command must be a non-empty string array")
    if not Path(command[0]).is_absolute():
        raise DRError(f"{name} executable must use an absolute path")
    if not isinstance(hashes, dict) or not hashes:
        raise DRError(f"{name}.sha256 pins are required")
    for dependency, expected in hashes.items():
        if not isinstance(dependency, str) or not Path(dependency).is_absolute():
            raise DRError(f"{name}.sha256 keys must be absolute dependency paths")
        if not isinstance(expected, str) or not SHA256_RE.fullmatch(expected):
            raise DRError(f"{name} has an invalid SHA-256 pin for {dependency}")
    for token in command:
        if "{" in token:
            continue
        looks_like_dependency = "/" in token or "\\" in token or token.lower().endswith((".py", ".sh", ".exe", ".cmd", ".bat"))
        if looks_like_dependency and not Path(token).is_absolute():
            raise DRError(f"{name} dependency paths must be absolute: {token}")
        if not Path(token).is_absolute():
            continue
        expected = hashes.get(token)
        if not isinstance(expected, str) or not SHA256_RE.fullmatch(expected):
            raise DRError(f"{name} absolute dependency {token} needs an exact SHA-256 pin")
    if require_version and (not isinstance(value.get("expected_version"), str) or not value["expected_version"]):
        raise DRError(f"{name}.expected_version is required")


def repository_url(target: dict[str, Any]) -> str:
    return f"s3:{target['endpoint']}/{target['bucket']}/{target['prefix']}"


def verify_dependencies(config: dict[str, Any], runner: "CommandRunner") -> None:
    groups = [config["restic"], config["backup"], *config["restore_validation_commands"]]
    checked: dict[str, str] = {}
    for group in groups:
        for path_text, expected in group["sha256"].items():
            path = canonical_path(path_text)
            if str(path) in checked:
                if checked[str(path)] != expected:
                    raise DRError(f"conflicting SHA-256 pins for dependency: {path}")
                continue
            if path.is_symlink() or not path.is_file():
                raise DRError(f"pinned dependency is missing or a symlink: {path}")
            if sha256_file(path) != expected:
                raise DRError(f"pinned dependency hash mismatch: {path}")
            checked[str(path)] = expected
    result = runner.run(config["restic"]["command"] + ["version"], env=minimal_env(config["restic"]["command"][0]))
    version = result.stdout.decode("utf-8", "replace").strip().splitlines()[0] if result.stdout else ""
    if result.returncode != 0 or version != config["restic"]["expected_version"]:
        raise DRError("restic version does not exactly match the pinned expected_version")


def minimal_env(executable: str) -> dict[str, str]:
    env = {"PATH": str(Path(executable).parent), "LANG": "C", "LC_ALL": "C"}
    if os.name == "nt":
        for key in ("SYSTEMROOT", "WINDIR", "TEMP", "TMP"):
            if os.environ.get(key):
                env[key] = os.environ[key]
    return env


def target_env(target: dict[str, Any], redactor: Redactor, executable: str) -> dict[str, str]:
    values = {
        "AWS_ACCESS_KEY_ID": resolve_ref(target["access_key_id_ref"]),
        "AWS_SECRET_ACCESS_KEY": resolve_ref(target["secret_access_key_ref"]),
        "RESTIC_PASSWORD": resolve_ref(target["repository_password_ref"]),
    }
    if target.get("session_token_ref"):
        values["AWS_SESSION_TOKEN"] = resolve_ref(target["session_token_ref"])
    for value in values.values():
        redactor.add(value)
    env = minimal_env(executable)
    env.update(values)
    env["AWS_DEFAULT_REGION"] = target["region"]
    env["AWS_REGION"] = target["region"]
    env["RESTIC_REPOSITORY"] = repository_url(target)
    return env


class CommandRunner:
    def run(self, command: list[str], *, env: dict[str, str], cwd: Path | None = None, timeout: int = 3600) -> CommandResult:
        import time

        started = time.perf_counter()
        try:
            process = subprocess.run(
                command,
                cwd=str(cwd) if cwd else None,
                env=env,
                stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=timeout,
                check=False,
            )
        except subprocess.TimeoutExpired as error:
            raise DRError("pinned command timed out") from error
        if len(process.stdout) > MAX_COMMAND_OUTPUT or len(process.stderr) > MAX_COMMAND_OUTPUT:
            raise DRError("pinned command output exceeded the capture limit")
        return CommandResult(process.returncode, process.stdout, process.stderr, time.perf_counter() - started)


def expand_command(command: list[str], variables: dict[str, str]) -> list[str]:
    expanded = []
    for token in command:
        result = token
        for key, value in variables.items():
            result = result.replace("{" + key + "}", value)
        if "{" in result or "}" in result:
            raise DRError("command contains an unknown placeholder")
        expanded.append(result)
    return expanded


def verify_source_backup(directory: Path, required_files: list[str]) -> dict[str, str]:
    if directory.is_symlink() or not directory.is_dir():
        raise DRError("backup command did not create a regular backup directory")
    sums_path = directory / "SHA256SUMS"
    if sums_path.is_symlink() or not sums_path.is_file():
        raise DRError("backup is unverified: SHA256SUMS is missing")
    allowed_files = set(required_files) | {"SHA256SUMS", "dr-manifest.json", "DR_MANIFEST.sha256"}
    actual_files = {item.name for item in directory.iterdir()}
    unexpected = actual_files - allowed_files
    if unexpected or any(not item.is_file() or item.is_symlink() for item in directory.iterdir()):
        raise DRError("backup is unverified: unexpected file, directory, or symlink is present")
    declared: dict[str, str] = {}
    for line in sums_path.read_text(encoding="utf-8").splitlines():
        match = re.fullmatch(r"([a-f0-9]{64})  ([A-Za-z0-9][A-Za-z0-9._-]*)", line)
        if not match or match.group(2) in declared:
            raise DRError("backup is unverified: malformed or duplicate SHA256SUMS entry")
        declared[match.group(2)] = match.group(1)
    if set(declared) != set(required_files):
        raise DRError("backup is unverified: SHA256SUMS does not cover the exact required file set")
    for name, expected in declared.items():
        path = directory / name
        if path.is_symlink() or not path.is_file() or sha256_file(path) != expected:
            raise DRError(f"backup is unverified: checksum mismatch for {name}")
    return declared


def write_atomic(path: Path, data: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temp_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        os.chmod(temp_name, 0o600)
        with os.fdopen(descriptor, "w", encoding="utf-8", newline="\n") as handle:
            handle.write(data)
        os.replace(temp_name, path)
        os.chmod(path, 0o600)
    finally:
        if os.path.exists(temp_name):
            os.unlink(temp_name)


def write_report(output: Path, payload: dict[str, Any], results: list[DRResult], redactor: Redactor) -> None:
    safe = redactor.clean({**payload, "results": [asdict(item) for item in results]})
    write_atomic(output / "dr-results.json", json.dumps(safe, ensure_ascii=False, indent=2) + "\n")
    rows = "\n".join(
        f"| {item['phase']} | {item['target']} | {item['status']} | {item['elapsed_seconds']:.3f} | {item['message']} | {json.dumps(item['evidence'], ensure_ascii=False, separators=(',', ':'))} |"
        for item in safe["results"]
    )
    objective = safe.get("objective_evaluation", {})
    markdown = f"""# Claw Workbench cross-region DR report

- Run ID: `{safe['run_id']}`
- Mode/action: `{safe['mode']}` / `{safe['action']}`
- Started/finished UTC: `{safe['started_at']}` / `{safe['finished_at']}`
- Actual restore drill performed: `{safe.get('actual_restore_drill', False)}`
- Backup verified in both repositories: `{safe.get('both_repositories_verified', False)}`
- Source scope: `{', '.join(safe.get('source_scope', []))}`

## RPO/RTO evidence

| Metric | Objective seconds | Observed seconds | Evaluation |
|---|---:|---:|---|
| RPO | {objective.get('rpo_target_seconds', '')} | {objective.get('rpo_observed_seconds', '')} | {objective.get('rpo_status', 'not_exercised')} |
| RTO | {objective.get('rto_target_seconds', '')} | {objective.get('rto_observed_seconds', '')} | {objective.get('rto_status', 'not_exercised')} |

`met` is emitted only for the observed RPO of a completed live restore drill.
A single backup reports snapshot freshness but does not prove schedule
continuity; full-service RTO is never marked met by this artifact-only tool.

## Results

| Phase | Target | Status | Seconds | Message | Sanitized evidence |
|---|---|---|---:|---|---|
{rows}

No repository password, S3 credential, session token, authorization value, raw
command line environment, or raw command output is included.
"""
    write_atomic(output / "dr-report.md", markdown)
