#!/usr/bin/env python3
"""Fail-closed validation for the optional Tencent AGSX managed sandbox."""

from __future__ import annotations

import argparse
import os
import re
import stat
import sys
from pathlib import Path


REGION_RE = re.compile(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?")
SECRET_NAMES = {
    "agsx-api-key",
    "cam-secret-id",
    "cam-secret-key",
    "client-token-hmac-key",
}
EXPECTED_SECRET_UID = 10001


class SandboxConfigError(ValueError):
    pass


def _safe_text(name: str, value: str, *, required: bool = True) -> str:
    value = value.strip()
    if required and not value:
        raise SandboxConfigError(f"{name} is required")
    if len(value) > 256 or any(ord(char) < 32 or ord(char) == 127 for char in value):
        raise SandboxConfigError(f"{name} is invalid")
    return value


def _validate_secret_dir(path: Path, enabled: bool) -> None:
    try:
        root_metadata = path.lstat()
    except OSError as error:
        raise SandboxConfigError("sandbox secret directory is unavailable") from error
    if stat.S_ISLNK(root_metadata.st_mode) or not stat.S_ISDIR(root_metadata.st_mode):
        raise SandboxConfigError("sandbox secret directory must be a non-symlink directory")
    root = path.resolve(strict=True)
    discovered: set[str] = set()
    for candidate in path.iterdir():
        if candidate.name == ".gitkeep":
            continue
        if candidate.name not in SECRET_NAMES:
            raise SandboxConfigError("sandbox secret directory contains an unknown file")
        metadata = candidate.lstat()
        if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
            raise SandboxConfigError("sandbox secret must be a non-symlink regular file")
        resolved = candidate.resolve(strict=True)
        if resolved.parent != root:
            raise SandboxConfigError("sandbox secret escapes its directory")
        if os.name == "posix":
            if metadata.st_uid != EXPECTED_SECRET_UID:
                raise SandboxConfigError("sandbox secrets must be owned by container UID 10001")
            mode = stat.S_IMODE(metadata.st_mode)
            if mode & 0o077 or not mode & 0o400:
                raise SandboxConfigError("sandbox secrets must be owner-readable and inaccessible to group/other")
        value = candidate.read_bytes().rstrip(b"\r\n")
        if not value or len(value) > 4096 or any(byte < 32 or byte == 127 for byte in value):
            raise SandboxConfigError("sandbox secret is empty, oversized, or contains control bytes")
        if candidate.name == "agsx-api-key" and (len(value) < 16 or not value.startswith(b"ark_")):
            raise SandboxConfigError("AGSX API key must use the ark_ prefix and contain at least 16 bytes")
        if candidate.name in {"cam-secret-id", "cam-secret-key"} and len(value) < 8:
            raise SandboxConfigError("CAM credentials must contain at least 8 bytes")
        if candidate.name == "client-token-hmac-key" and len(value) < 32:
            raise SandboxConfigError("sandbox ClientToken HMAC key must contain at least 32 bytes")
        discovered.add(candidate.name)
    if enabled and discovered != SECRET_NAMES:
        raise SandboxConfigError("all four sandbox secret files are required when enabled")


def validate(args: argparse.Namespace) -> None:
    if args.enabled not in {"true", "false"}:
        raise SandboxConfigError("enabled must be true or false")
    enabled = args.enabled == "true"
    _validate_secret_dir(args.secret_dir, enabled)
    if not enabled:
        return
    if args.provider != "tencent_agsx":
        raise SandboxConfigError("sandbox provider must be tencent_agsx")
    region = _safe_text("AGSX region", args.region).lower()
    if not REGION_RE.fullmatch(region):
        raise SandboxConfigError("AGSX region is invalid")
    if args.domain.strip().lower() != f"{region}.tencentags.com":
        raise SandboxConfigError("AGSX data-plane domain must exactly match <region>.tencentags.com")
    if args.control_endpoint.strip().lower() != "ags.tencentcloudapi.com":
        raise SandboxConfigError("AGSX control endpoint must be ags.tencentcloudapi.com")
    tool_id = _safe_text("AGSX ToolId", args.tool_id, required=False)
    tool_name = _safe_text("AGSX ToolName", args.tool_name, required=False)
    if not tool_id and not tool_name:
        raise SandboxConfigError("at least one AGSX ToolId or ToolName is required")
    if args.network_mode != "SANDBOX" or args.auth_mode != "TOKEN":
        raise SandboxConfigError("sandbox must use SANDBOX network mode and TOKEN authentication")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--enabled", required=True)
    parser.add_argument("--provider", required=True)
    parser.add_argument("--region", required=True)
    parser.add_argument("--domain", required=True)
    parser.add_argument("--control-endpoint", required=True)
    parser.add_argument("--tool-id", default="")
    parser.add_argument("--tool-name", default="")
    parser.add_argument("--network-mode", required=True)
    parser.add_argument("--auth-mode", required=True)
    parser.add_argument("--secret-dir", type=Path, required=True)
    args = parser.parse_args()
    try:
        validate(args)
    except (OSError, SandboxConfigError) as error:
        print(f"sandbox config: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
