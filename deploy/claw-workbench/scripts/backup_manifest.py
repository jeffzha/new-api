#!/usr/bin/env python3
"""Render a secret-free backup provenance file from a live release manifest."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import stat
from datetime import datetime, timezone
from pathlib import Path
from typing import Callable, Sequence


RELEASE_FIELDS = frozenset(
    {
        "new_api_revision",
        "claw_control_revision",
        "adp_revision",
        "new_api_image_digest",
        "claw_control_image_digest",
        "adp_image_digest",
        "config_sha256",
        "control_migration",
        "adp_migration",
        "caddy_version",
        "active_color",
        "provider_region",
        "collected_at",
    }
)
REVISION_RE = re.compile(r"[0-9a-f]{40}")
DIGEST_RE = re.compile(r"sha256:[0-9a-f]{64}")
HASH_RE = re.compile(r"[0-9a-f]{64}")
MIGRATION_RE = re.compile(r"[0-9]{4}_[a-z0-9_]{1,91}")
SCHEMA_RE = re.compile(r"schema-sha256:[0-9a-f]{64}")
VERSION_RE = re.compile(r"[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?")
REGION_RE = re.compile(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?")
STAMP_RE = re.compile(r"[0-9]{8}T[0-9]{6}Z")
MAX_MANIFEST_BYTES = 64 * 1024


class BackupManifestError(ValueError):
    pass


def _read_regular_file(path: Path) -> bytes:
    try:
        metadata = path.lstat()
    except OSError as error:
        raise BackupManifestError("release manifest is unavailable") from error
    if path.is_symlink() or not stat.S_ISREG(metadata.st_mode):
        raise BackupManifestError("release manifest must be a regular non-symlink file")
    if metadata.st_size <= 0 or metadata.st_size > MAX_MANIFEST_BYTES:
        raise BackupManifestError("release manifest size is invalid")
    if os.name == "posix" and stat.S_IMODE(metadata.st_mode) & 0o077:
        raise BackupManifestError("release manifest must not be accessible by group or other")
    try:
        return path.read_bytes()
    except OSError as error:
        raise BackupManifestError("release manifest cannot be read") from error


def load_release_manifest(
    path: Path,
    *,
    clock: Callable[[], datetime] | None = None,
    max_age_seconds: int = 300,
) -> tuple[dict[str, str], str]:
    raw = _read_regular_file(path)
    try:
        payload = json.loads(raw.decode("utf-8"))
    except (UnicodeError, json.JSONDecodeError) as error:
        raise BackupManifestError("release manifest is not valid UTF-8 JSON") from error
    if not isinstance(payload, dict) or set(payload) != RELEASE_FIELDS:
        raise BackupManifestError("release manifest fields are incomplete or unknown")
    if any(
        not isinstance(value, str)
        or not value
        or len(value) > 256
        or "\n" in value
        or "\r" in value
        or "\0" in value
        for value in payload.values()
    ):
        raise BackupManifestError("release manifest contains an invalid value")
    for name in ("new_api_revision", "claw_control_revision", "adp_revision"):
        if not REVISION_RE.fullmatch(payload[name]):
            raise BackupManifestError(f"release manifest {name} is invalid")
    for name in (
        "new_api_image_digest",
        "claw_control_image_digest",
        "adp_image_digest",
    ):
        if not DIGEST_RE.fullmatch(payload[name]):
            raise BackupManifestError(f"release manifest {name} is invalid")
    if not HASH_RE.fullmatch(payload["config_sha256"]):
        raise BackupManifestError("release manifest config_sha256 is invalid")
    if not MIGRATION_RE.fullmatch(payload["control_migration"]):
        raise BackupManifestError("release manifest control_migration is invalid")
    if not SCHEMA_RE.fullmatch(payload["adp_migration"]):
        raise BackupManifestError("release manifest adp_migration is invalid")
    if not VERSION_RE.fullmatch(payload["caddy_version"]):
        raise BackupManifestError("release manifest caddy_version is invalid")
    if payload["active_color"] not in {"blue", "green"}:
        raise BackupManifestError("release manifest active_color is invalid")
    if not REGION_RE.fullmatch(payload["provider_region"]):
        raise BackupManifestError("release manifest provider_region is invalid")
    try:
        collected_at = datetime.fromisoformat(payload["collected_at"].replace("Z", "+00:00"))
    except ValueError as error:
        raise BackupManifestError("release manifest collected_at is invalid") from error
    if collected_at.tzinfo is None or collected_at.utcoffset() is None:
        raise BackupManifestError("release manifest collected_at must be timezone-aware")
    now = (clock or (lambda: datetime.now(timezone.utc)))()
    if now.tzinfo is None or now.utcoffset() is None:
        raise BackupManifestError("backup clock must be timezone-aware")
    age = (now.astimezone(timezone.utc) - collected_at.astimezone(timezone.utc)).total_seconds()
    if age < -5 or age > max_age_seconds:
        raise BackupManifestError("release manifest is stale or from the future")
    return payload, hashlib.sha256(raw).hexdigest()


def write_backup_manifest(
    output: Path,
    release: dict[str, str],
    release_sha256: str,
    backup_created_at: str,
) -> None:
    if not STAMP_RE.fullmatch(backup_created_at):
        raise BackupManifestError("backup timestamp is invalid")
    if not HASH_RE.fullmatch(release_sha256):
        raise BackupManifestError("release manifest hash is invalid")
    if output.exists() or output.is_symlink():
        raise BackupManifestError("backup manifest output already exists")
    requested_parent = output.parent.absolute()
    try:
        parent = output.parent.resolve(strict=True)
    except OSError as error:
        raise BackupManifestError("backup manifest directory is unavailable") from error
    if requested_parent != parent or not parent.is_dir():
        raise BackupManifestError("backup manifest directory is invalid")
    lines = [
        f"backup_created_at={backup_created_at}",
        f"release_manifest_sha256={release_sha256}",
    ]
    lines.extend(f"release.{name}={release[name]}" for name in sorted(RELEASE_FIELDS))
    payload = ("\n".join(lines) + "\n").encode("ascii")
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    descriptor = -1
    try:
        descriptor = os.open(output, flags, 0o600)
        with os.fdopen(descriptor, "wb") as stream:
            descriptor = -1
            stream.write(payload)
            stream.flush()
            os.fsync(stream.fileno())
    except OSError as error:
        raise BackupManifestError("backup manifest cannot be written") from error
    finally:
        if descriptor >= 0:
            os.close(descriptor)


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--release-manifest", type=Path, required=True)
    parser.add_argument("--backup-created-at", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        release, digest = load_release_manifest(args.release_manifest)
        write_backup_manifest(args.output, release, digest, args.backup_created_at)
    except (OSError, BackupManifestError) as error:
        print(f"backup manifest: {error}", file=os.sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
