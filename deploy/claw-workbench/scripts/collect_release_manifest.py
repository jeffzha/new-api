#!/usr/bin/env python3
"""Collect an authoritative, secret-free release manifest from a live deployment."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import stat
import subprocess
import sys
import tempfile
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Callable, Protocol, Sequence
from urllib.parse import urlsplit

from strict_dotenv import DotenvError, load_dotenv


REVISION_RE = re.compile(r"[0-9a-f]{40}")
DIGEST_RE = re.compile(r"sha256:[0-9a-f]{64}")
IMMUTABLE_IMAGE_RE = re.compile(
    r"(?P<repository>[a-z0-9][a-z0-9._:/-]*)@(?P<digest>sha256:[0-9a-f]{64})"
)
CONTAINER_ID_RE = re.compile(r"[0-9a-f]{64}")
CONTAINER_HOST_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,127}")
PROJECT_RE = re.compile(r"[a-z0-9][a-z0-9_-]{0,62}")
REGION_RE = re.compile(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?")
MIGRATION_RE = re.compile(r"[0-9]{4}_[a-z0-9_]{1,91}")
CADDY_VERSION_RE = re.compile(r"v?(\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?)")
PLACEHOLDER_MARKERS = ("replace", "placeholder", "change-me", "changeme", "example.invalid")
MAX_CONFIG_FILE_BYTES = 4 * 1024 * 1024
MAX_CONFIG_TOTAL_BYTES = 16 * 1024 * 1024
OCI_REVISION_LABEL = "org.opencontainers.image.revision"
OVERLAY_REVISION_LABEL = "com.nexus-reach.workbench.overlay-revision"
ADP_REQUIRED_TABLES = frozenset(
    {
        "account",
        "agent_config",
        "workbench_identity",
        "workbench_turn",
        "workbench_scheduled_task",
        "workbench_integration_binding",
        "workbench_sandbox",
    }
)


class CollectionError(RuntimeError):
    """A fail-closed collection error safe to show to an operator."""


@dataclass(frozen=True)
class CommandOutput:
    stdout: str


class CommandRunner(Protocol):
    def run(
        self, argv: Sequence[str], *, cwd: Path | None = None, purpose: str
    ) -> CommandOutput: ...


class SubprocessCommandRunner:
    """Run fixed argv commands without a shell and without echoing stderr."""

    def __init__(self, timeout_seconds: int = 30) -> None:
        self.timeout_seconds = timeout_seconds

    def run(
        self, argv: Sequence[str], *, cwd: Path | None = None, purpose: str
    ) -> CommandOutput:
        try:
            completed = subprocess.run(
                list(argv),
                cwd=cwd,
                check=False,
                capture_output=True,
                text=True,
                encoding="utf-8",
                errors="strict",
                timeout=self.timeout_seconds,
                shell=False,
            )
        except (OSError, subprocess.SubprocessError, UnicodeError) as error:
            raise CollectionError(f"{purpose} failed") from error
        if completed.returncode != 0:
            # Provider/database diagnostics can contain connection material. The
            # operator gets the failed stage and exit status, never raw stderr.
            raise CollectionError(f"{purpose} failed with exit status {completed.returncode}")
        return CommandOutput(stdout=completed.stdout)


def _is_placeholder(value: str) -> bool:
    normalized = value.strip().lower()
    return (
        not normalized
        or normalized.startswith(("<", "${"))
        or any(marker in normalized for marker in PLACEHOLDER_MARKERS)
        or (set(normalized) == {"0"})
    )


def _single_line(value: str, field: str) -> str:
    lines = [line.strip() for line in value.splitlines() if line.strip()]
    if len(lines) != 1:
        raise CollectionError(f"{field} must contain exactly one value")
    return lines[0]


def _regular_file(path: Path, field: str) -> os.stat_result:
    try:
        metadata = path.lstat()
    except OSError as error:
        raise CollectionError(f"cannot stat {field}") from error
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
        raise CollectionError(f"{field} must be a regular file, not a symlink")
    return metadata


def _framed_hash(domain: bytes, entries: Sequence[tuple[str, bytes]]) -> str:
    digest = hashlib.sha256(domain + b"\0")
    for name, content in entries:
        encoded_name = name.encode("utf-8")
        digest.update(len(encoded_name).to_bytes(8, "big"))
        digest.update(encoded_name)
        digest.update(len(content).to_bytes(8, "big"))
        digest.update(content)
    return digest.hexdigest()


def hash_release_config(deployment_root: Path) -> str:
    """Hash the exact runtime env plus the non-secret versioned configuration."""

    fixed = [
        deployment_root / ".env",
        deployment_root / "compose.yml",
        deployment_root / "new-api.env.example",
        deployment_root / "state" / "active-color",
        deployment_root / "state" / "Caddyfile.active",
    ]
    discovered: list[Path] = []
    for directory in ("caddy", "config", "observability"):
        root = deployment_root / directory
        if not root.exists():
            continue
        if root.is_symlink() or not root.is_dir():
            raise CollectionError(f"configuration directory {directory} is invalid")
        for path in root.rglob("*"):
            if path.is_symlink():
                relative = path.relative_to(deployment_root).as_posix()
                raise CollectionError(f"configuration path {relative} must not be a symlink")
            if not path.is_dir():
                discovered.append(path)

    unique: dict[str, Path] = {}
    resolved_root = deployment_root.resolve(strict=True)
    for path in fixed + discovered:
        relative = path.relative_to(deployment_root).as_posix()
        if relative in unique:
            raise CollectionError(f"duplicate configuration path {relative}")
        _regular_file(path, f"configuration file {relative}")
        try:
            resolved = path.resolve(strict=True)
            resolved.relative_to(resolved_root)
        except (OSError, ValueError) as error:
            raise CollectionError(f"configuration file {relative} escapes deployment root") from error
        unique[relative] = path

    total = 0
    entries: list[tuple[str, bytes]] = []
    for relative in sorted(unique):
        try:
            content = unique[relative].read_bytes()
        except OSError as error:
            raise CollectionError(f"cannot read configuration file {relative}") from error
        if len(content) > MAX_CONFIG_FILE_BYTES:
            raise CollectionError(f"configuration file {relative} exceeds size limit")
        total += len(content)
        if total > MAX_CONFIG_TOTAL_BYTES:
            raise CollectionError("configuration files exceed aggregate size limit")
        entries.append((relative, content))
    return _framed_hash(b"claw-workbench-release-config-v1", entries)


def write_manifest_atomic(path: Path, manifest: dict[str, str]) -> None:
    """Atomically replace a manifest with owner-only permissions."""

    parent = path.parent
    try:
        parent_metadata = parent.lstat()
    except OSError as error:
        raise CollectionError("manifest output directory does not exist") from error
    if stat.S_ISLNK(parent_metadata.st_mode) or not stat.S_ISDIR(parent_metadata.st_mode):
        raise CollectionError("manifest output directory must be a real directory")
    if path.exists() or path.is_symlink():
        _regular_file(path, "existing manifest output")

    payload = (json.dumps(manifest, ensure_ascii=False, sort_keys=True, indent=2) + "\n").encode(
        "utf-8"
    )
    descriptor = -1
    temporary_name = ""
    try:
        descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=parent)
        if hasattr(os, "fchmod"):
            os.fchmod(descriptor, 0o600)
        with os.fdopen(descriptor, "wb") as output:
            descriptor = -1
            output.write(payload)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary_name, path)
        temporary_name = ""
        os.chmod(path, 0o600)
        if os.name == "posix":
            directory_descriptor = os.open(parent, os.O_RDONLY)
            try:
                os.fsync(directory_descriptor)
            finally:
                os.close(directory_descriptor)
    except OSError as error:
        raise CollectionError("cannot atomically write release manifest") from error
    finally:
        if descriptor >= 0:
            os.close(descriptor)
        if temporary_name:
            try:
                os.unlink(temporary_name)
            except FileNotFoundError:
                pass


class ReleaseManifestCollector:
    def __init__(
        self,
        deployment_root: Path,
        runner: CommandRunner,
        *,
        clock: Callable[[], datetime] | None = None,
        env_loader: Callable[[Path], dict[str, str]] = load_dotenv,
    ) -> None:
        self.deployment_root = deployment_root.resolve(strict=True)
        self.runner = runner
        self.clock = clock or (lambda: datetime.now(timezone.utc))
        self.env_loader = env_loader
        self.env_file = self.deployment_root / ".env"
        self._project = ""

    def _run(
        self, argv: Sequence[str], *, purpose: str, cwd: Path | None = None
    ) -> str:
        return self.runner.run(argv, cwd=cwd, purpose=purpose).stdout

    def _git_revision(self, worktree: Path, component: str) -> str:
        if worktree.is_symlink() or not worktree.is_dir():
            raise CollectionError(f"{component} worktree is invalid")
        revision = _single_line(
            self._run(
                ["git", "-C", str(worktree), "rev-parse", "--verify", "HEAD^{commit}"],
                purpose=f"collect {component} Git revision",
            ),
            f"{component} Git revision",
        )
        if not REVISION_RE.fullmatch(revision) or _is_placeholder(revision):
            raise CollectionError(f"{component} Git revision is invalid")
        dirty = self._run(
            ["git", "-C", str(worktree), "status", "--porcelain=v1", "--untracked-files=all"],
            purpose=f"verify {component} Git worktree",
        )
        if dirty.strip():
            raise CollectionError(f"{component} Git worktree is dirty")
        return revision

    def _compose_argv(self, *arguments: str) -> list[str]:
        return [
            "docker",
            "compose",
            "--project-directory",
            str(self.deployment_root),
            "--env-file",
            str(self.env_file),
            "-f",
            str(self.deployment_root / "compose.yml"),
            "--profile",
            "claw-workbench",
            *arguments,
        ]

    def _inspect(self, target: str, purpose: str) -> dict[str, object]:
        raw = self._run(["docker", "inspect", target], purpose=purpose)
        try:
            payload = json.loads(raw)
        except (TypeError, json.JSONDecodeError) as error:
            raise CollectionError(f"{purpose} returned invalid JSON") from error
        if not isinstance(payload, list) or len(payload) != 1 or not isinstance(payload[0], dict):
            raise CollectionError(f"{purpose} must resolve exactly one container")
        return payload[0]

    def _require_running(self, inspected: dict[str, object], component: str) -> None:
        state = inspected.get("State")
        if not isinstance(state, dict) or state.get("Running") is not True:
            raise CollectionError(f"{component} container is not running")
        health = state.get("Health")
        if isinstance(health, dict) and health.get("Status") != "healthy":
            raise CollectionError(f"{component} container is not healthy")

    def _service_container(self, service: str) -> tuple[str, dict[str, object]]:
        raw_id = self._run(
            self._compose_argv("ps", "--all", "--quiet", service),
            purpose=f"resolve {service} container",
        )
        container_id = _single_line(raw_id, f"{service} container id")
        if not CONTAINER_ID_RE.fullmatch(container_id):
            raise CollectionError(f"{service} container id is invalid")
        inspected = self._inspect(container_id, f"inspect {service} container")
        if inspected.get("Id") != container_id:
            raise CollectionError(f"{service} container identity mismatch")
        self._require_running(inspected, service)
        config = inspected.get("Config")
        labels = config.get("Labels") if isinstance(config, dict) else None
        if not isinstance(labels, dict):
            raise CollectionError(f"{service} container labels are missing")
        if labels.get("com.docker.compose.project") != self._project:
            raise CollectionError(f"{service} compose project label mismatch")
        if labels.get("com.docker.compose.service") != service:
            raise CollectionError(f"{service} compose service label mismatch")
        return container_id, inspected

    def _container_image(
        self, inspected: dict[str, object], expected_revision: str, component: str
    ) -> str:
        config = inspected.get("Config")
        if not isinstance(config, dict):
            raise CollectionError(f"{component} container config is missing")
        labels = config.get("Labels")
        if not isinstance(labels, dict):
            raise CollectionError(f"{component} image labels are missing")
        label_revision = labels.get(OCI_REVISION_LABEL)
        if not isinstance(label_revision, str) or not REVISION_RE.fullmatch(label_revision):
            raise CollectionError(f"{component} OCI revision label is invalid")
        if label_revision != expected_revision:
            raise CollectionError(f"{component} OCI revision label does not match its worktree")

        image_reference = config.get("Image")
        if not isinstance(image_reference, str):
            raise CollectionError(f"{component} configured image reference is missing")
        matched = IMMUTABLE_IMAGE_RE.fullmatch(image_reference)
        if matched is None or _is_placeholder(image_reference):
            raise CollectionError(f"{component} image must use one immutable registry digest")
        local_image_id = inspected.get("Image")
        if not isinstance(local_image_id, str) or not DIGEST_RE.fullmatch(local_image_id):
            raise CollectionError(f"{component} local image id is invalid")

        raw_image = self._run(
            ["docker", "image", "inspect", local_image_id],
            purpose=f"inspect {component} image",
        )
        try:
            images = json.loads(raw_image)
        except (TypeError, json.JSONDecodeError) as error:
            raise CollectionError(f"inspect {component} image returned invalid JSON") from error
        if not isinstance(images, list) or len(images) != 1 or not isinstance(images[0], dict):
            raise CollectionError(f"inspect {component} image must resolve exactly one image")
        if images[0].get("Id") != local_image_id:
            raise CollectionError(f"{component} image identity mismatch")
        image_config = images[0].get("Config")
        image_labels = image_config.get("Labels") if isinstance(image_config, dict) else None
        if not isinstance(image_labels, dict) or image_labels.get(OCI_REVISION_LABEL) != expected_revision:
            raise CollectionError(f"{component} image OCI revision label mismatch")
        repo_digests = images[0].get("RepoDigests")
        if not isinstance(repo_digests, list) or repo_digests.count(image_reference) != 1:
            raise CollectionError(f"{component} configured digest is not attached to the running image")
        return matched.group("digest")

    def _container_revision(
        self, inspected: dict[str, object], component: str
    ) -> str:
        config = inspected.get("Config")
        labels = config.get("Labels") if isinstance(config, dict) else None
        revision = labels.get(OCI_REVISION_LABEL) if isinstance(labels, dict) else None
        if not isinstance(revision, str) or not REVISION_RE.fullmatch(revision):
            raise CollectionError(f"{component} OCI revision label is invalid")
        return revision

    def _require_overlay_revision(
        self, inspected: dict[str, object], expected_revision: str
    ) -> None:
        config = inspected.get("Config")
        labels = config.get("Labels") if isinstance(config, dict) else None
        revision = labels.get(OVERLAY_REVISION_LABEL) if isinstance(labels, dict) else None
        if not isinstance(revision, str) or not REVISION_RE.fullmatch(revision):
            raise CollectionError("ADP overlay revision label is invalid")
        if revision != expected_revision:
            raise CollectionError("ADP overlay revision does not match claw-control")

    def _active_color(self) -> str:
        color_file = self.deployment_root / "state" / "active-color"
        _regular_file(color_file, "active color state")
        try:
            color = _single_line(color_file.read_text(encoding="utf-8"), "active color")
        except (OSError, UnicodeError) as error:
            raise CollectionError("cannot read active color state") from error
        if color not in {"blue", "green"}:
            raise CollectionError("active color must be blue or green")

        caddy_file = self.deployment_root / "state" / "Caddyfile.active"
        _regular_file(caddy_file, "active Caddy state")
        try:
            caddy = caddy_file.read_text(encoding="utf-8")
        except (OSError, UnicodeError) as error:
            raise CollectionError("cannot read active Caddy state") from error
        other = "green" if color == "blue" else "blue"
        expected_counts = {
            f"claw-control-{color}:8090": 2,
            f"adp-{color}:8000": 2,
        }
        forbidden = (f"claw-control-{other}:8090", f"adp-{other}:8000")
        if any(caddy.count(value) != count for value, count in expected_counts.items()) or any(
            value in caddy for value in forbidden
        ):
            raise CollectionError("active color state does not match active Caddy routing")
        return color

    def _new_api_container(self, env: dict[str, str]) -> dict[str, object]:
        upstream = env.get("NEW_API_INTERNAL_UPSTREAM", "")
        try:
            hostname = urlsplit(upstream).hostname or ""
        except ValueError as error:
            raise CollectionError("new-api internal upstream is invalid") from error
        allowed = [value.strip() for value in env.get("NEW_API_INTERNAL_ALLOWED_HOSTS", "").split(",")]
        if (
            not CONTAINER_HOST_RE.fullmatch(hostname)
            or _is_placeholder(hostname)
            or allowed != [hostname]
        ):
            raise CollectionError("new-api routing must identify exactly one non-placeholder container")
        inspected = self._inspect(hostname, "inspect active new-api container")
        self._require_running(inspected, "new-api")

        names = {str(inspected.get("Name", "")).lstrip("/")}
        networks = inspected.get("NetworkSettings")
        network_map = networks.get("Networks") if isinstance(networks, dict) else None
        if isinstance(network_map, dict):
            for network in network_map.values():
                aliases = network.get("Aliases") if isinstance(network, dict) else None
                if isinstance(aliases, list):
                    names.update(str(alias) for alias in aliases if isinstance(alias, str))
        if hostname not in names:
            raise CollectionError("new-api upstream does not match the inspected container identity")
        return inspected

    def _provider_region(
        self, env: dict[str, str], adp_container: dict[str, object]
    ) -> str:
        if env.get("WORKBENCH_SANDBOX_PROVIDER") != "tencent_agsx":
            raise CollectionError("sandbox provider must be tencent_agsx")
        config = adp_container.get("Config")
        runtime_entries = config.get("Env") if isinstance(config, dict) else None
        if not isinstance(runtime_entries, list):
            raise CollectionError("ADP runtime environment is missing")
        runtime: dict[str, str] = {}
        wanted = {
            "WORKBENCH_SANDBOX_PROVIDER",
            "WORKBENCH_AGSX_REGION",
            "WORKBENCH_FILE_COS_REGION",
        }
        for entry in runtime_entries:
            if not isinstance(entry, str) or "=" not in entry:
                continue
            key, value = entry.split("=", 1)
            if key not in wanted:
                continue
            if key in runtime:
                raise CollectionError(f"ADP runtime contains multiple {key} values")
            runtime[key] = value
        if runtime.get("WORKBENCH_SANDBOX_PROVIDER") != "tencent_agsx":
            raise CollectionError("ADP runtime sandbox provider mismatch")
        pairs = [
            env.get("WORKBENCH_AGSX_REGION", ""),
            env.get("WORKBENCH_FILE_COS_REGION", ""),
            runtime.get("WORKBENCH_AGSX_REGION", ""),
            runtime.get("WORKBENCH_FILE_COS_REGION", ""),
        ]
        populated = [value for value in pairs if value]
        if not populated or len(set(populated)) != 1:
            raise CollectionError("Tencent provider region is missing or differs from ADP runtime")
        region = populated[0]
        if not REGION_RE.fullmatch(region) or _is_placeholder(region):
            raise CollectionError("Tencent provider region is invalid")
        return region

    def _control_migration(self) -> str:
        query = (
            "set -eu; PGPASSWORD=\"$(cat /run/secrets/control_db_password)\"; "
            "export PGPASSWORD; exec psql -X -v ON_ERROR_STOP=1 -U \"$POSTGRES_USER\" "
            "-d \"$POSTGRES_DB\" -Atqc "
            "'SELECT version FROM claw_schema_migrations ORDER BY version DESC LIMIT 1'"
        )
        head = _single_line(
            self._run(
                self._compose_argv("exec", "-T", "workbench-control-db", "sh", "-ec", query),
                purpose="collect control database migration head",
            ),
            "control database migration head",
        )
        if not MIGRATION_RE.fullmatch(head) or _is_placeholder(head):
            raise CollectionError("control database migration head is invalid")
        return head

    def _adp_migration(self) -> str:
        # The ADP fork currently uses SQLAlchemy create_all and has no migration
        # ledger. Its authoritative database head is therefore the SHA-256 of a
        # stable, sorted projection of the live public schema.
        sql = (
            "SELECT table_name || chr(31) || lpad(ordinal_position::text, 6, '0') || "
            "chr(31) || column_name || chr(31) || data_type || chr(31) || udt_name || "
            "chr(31) || is_nullable FROM information_schema.columns "
            "WHERE table_schema = 'public' ORDER BY table_name, ordinal_position"
        )
        query = (
            "set -eu; PGPASSWORD=\"$(cat /run/secrets/adp_db_password)\"; "
            "export PGPASSWORD; exec psql -X -v ON_ERROR_STOP=1 -U \"$POSTGRES_USER\" "
            f"-d \"$POSTGRES_DB\" -Atqc \"{sql}\""
        )
        output = self._run(
            self._compose_argv("exec", "-T", "workbench-adp-db", "sh", "-ec", query),
            purpose="collect ADP database schema head",
        )
        rows = [line for line in output.splitlines() if line]
        if not rows or rows != sorted(rows) or len(rows) != len(set(rows)):
            raise CollectionError("ADP database schema head is empty, unsorted, or ambiguous")
        if any(row.count(chr(31)) != 5 for row in rows):
            raise CollectionError("ADP database schema head has an invalid row")
        tables = {row.split(chr(31), 1)[0] for row in rows}
        if not ADP_REQUIRED_TABLES.issubset(tables):
            raise CollectionError("ADP database schema is missing required workbench tables")
        entries = [(f"row-{index:06d}", row.encode("utf-8")) for index, row in enumerate(rows)]
        return "schema-sha256:" + _framed_hash(b"adp-public-schema-v1", entries)

    def _caddy_version(self) -> str:
        raw = _single_line(
            self._run(
                self._compose_argv("exec", "-T", "workbench-switch", "caddy", "version"),
                purpose="collect Caddy version",
            ),
            "Caddy version",
        )
        matches = CADDY_VERSION_RE.findall(raw)
        if len(matches) != 1 or _is_placeholder(matches[0]):
            raise CollectionError("Caddy version is invalid or ambiguous")
        return matches[0]

    def collect(self) -> dict[str, str]:
        try:
            env = self.env_loader(self.env_file)
        except DotenvError as error:
            raise CollectionError("deployment dotenv validation failed") from error

        project = env.get("COMPOSE_PROJECT_NAME", "")
        if not PROJECT_RE.fullmatch(project) or _is_placeholder(project):
            raise CollectionError("Compose project name is invalid")
        self._project = project

        active_color = self._active_color()
        new_api = self._new_api_container(env)
        _, control = self._service_container(f"claw-control-{active_color}")
        _, adp = self._service_container(f"adp-{active_color}")
        build_local = env.get("CLAW_BUILD_LOCAL_IMAGES", "false")
        if build_local not in {"true", "false"}:
            raise CollectionError("CLAW_BUILD_LOCAL_IMAGES must be true or false")
        if build_local == "true":
            new_api_worktree = self.deployment_root.parent.parent
            adp_source = env.get("ADP_SOURCE_DIR", "")
            if not adp_source or _is_placeholder(adp_source):
                raise CollectionError("ADP source worktree is missing")
            adp_worktree = (self.deployment_root / adp_source).resolve(strict=True)
            new_api_revision = self._git_revision(
                new_api_worktree, "new-api/claw-control"
            )
            claw_control_revision = new_api_revision
            adp_revision = self._git_revision(adp_worktree, "ADP")
        else:
            new_api_revision = self._container_revision(new_api, "new-api")
            claw_control_revision = self._container_revision(control, "claw-control")
            if claw_control_revision != new_api_revision:
                raise CollectionError(
                    "new-api and claw-control OCI revisions do not match"
                )
            adp_revision = self._container_revision(adp, "ADP")
            self._require_overlay_revision(adp, new_api_revision)
        provider_region = self._provider_region(env, adp)
        self._service_container("workbench-control-db")
        self._service_container("workbench-adp-db")
        self._service_container("workbench-switch")

        now = self.clock()
        if now.tzinfo is None or now.utcoffset() is None:
            raise CollectionError("collector clock must be timezone-aware")
        collected_at = now.astimezone(timezone.utc).replace(microsecond=0).isoformat().replace(
            "+00:00", "Z"
        )

        return {
            "new_api_revision": new_api_revision,
            "claw_control_revision": claw_control_revision,
            "adp_revision": adp_revision,
            "new_api_image_digest": self._container_image(
                new_api, new_api_revision, "new-api"
            ),
            "claw_control_image_digest": self._container_image(
                control, new_api_revision, "claw-control"
            ),
            "adp_image_digest": self._container_image(adp, adp_revision, "ADP"),
            "config_sha256": hash_release_config(self.deployment_root),
            "control_migration": self._control_migration(),
            "adp_migration": self._adp_migration(),
            "caddy_version": self._caddy_version(),
            "active_color": active_color,
            "provider_region": provider_region,
            "collected_at": collected_at,
        }


def main(argv: Sequence[str] | None = None) -> int:
    default_root = Path(__file__).resolve().parents[1]
    parser = argparse.ArgumentParser(
        description="Collect a fail-closed release manifest from Git, Docker, Caddy, and PostgreSQL"
    )
    parser.add_argument("--deployment-root", type=Path, default=default_root)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args(argv)
    deployment_root = args.deployment_root.resolve()
    output = args.output or deployment_root / "state" / "release-manifest.json"
    try:
        output_parent = output.parent.resolve(strict=True)
        output_parent.relative_to(deployment_root.resolve(strict=True))
        manifest = ReleaseManifestCollector(
            deployment_root, SubprocessCommandRunner()
        ).collect()
        write_manifest_atomic(output_parent / output.name, manifest)
    except (CollectionError, OSError, ValueError) as error:
        print(f"release manifest collection failed: {error}", file=sys.stderr)
        return 1
    print(f"authoritative release manifest written to {output_parent / output.name}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
