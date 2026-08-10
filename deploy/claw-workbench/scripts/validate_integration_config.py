#!/usr/bin/env python3
"""Validate mounted workbench integration metadata and OAuth secret files."""

from __future__ import annotations

import argparse
import json
import re
import stat
import sys
from pathlib import Path
from urllib.parse import urlsplit


MAX_CONFIG_BYTES = 1024 * 1024
ID_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._:-]{0,127}")
PROVIDER_RE = re.compile(r"[a-z0-9][a-z0-9_-]{0,63}")
SCOPE_RE = re.compile(r"[\x21-\x7e]{1,128}")


class IntegrationConfigError(ValueError):
    pass


def _pairs(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise IntegrationConfigError(f"duplicate JSON key {key!r}")
        result[key] = value
    return result


def _load(path: Path, label: str) -> dict[str, object]:
    try:
        metadata = path.lstat()
        if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
            raise OSError("not a regular file")
        raw = path.read_bytes()
    except OSError as error:
        raise IntegrationConfigError(f"{label} is unavailable: {error}") from error
    if len(raw) > MAX_CONFIG_BYTES:
        raise IntegrationConfigError(f"{label} exceeds {MAX_CONFIG_BYTES} bytes")
    try:
        value = json.loads(raw.decode("utf-8"), object_pairs_hook=_pairs)
    except IntegrationConfigError:
        raise
    except (UnicodeError, ValueError) as error:
        raise IntegrationConfigError(f"{label} is not valid UTF-8 JSON") from error
    if not isinstance(value, dict):
        raise IntegrationConfigError(f"{label} must contain a JSON object")
    return value


def _oauth_url(value: object, allowed_hosts: set[str], label: str) -> None:
    try:
        parsed = urlsplit(str(value or "").strip())
        port = parsed.port
    except (UnicodeError, ValueError) as error:
        raise IntegrationConfigError(f"{label} is invalid") from error
    host = str(parsed.hostname or "").lower().rstrip(".")
    if (
        parsed.scheme != "https"
        or host not in allowed_hosts
        or parsed.username is not None
        or parsed.password is not None
        or parsed.fragment
        or port not in {None, 443}
        or not parsed.path.startswith("/")
    ):
        raise IntegrationConfigError(f"{label} is invalid")


def _validate_secret(secret_dir: Path, reference: str) -> None:
    try:
        root_metadata = secret_dir.lstat()
        if stat.S_ISLNK(root_metadata.st_mode) or not stat.S_ISDIR(root_metadata.st_mode):
            raise OSError("unsafe OAuth secret directory")
        root = secret_dir.resolve(strict=True)
        candidate = root / reference
        metadata = candidate.lstat()
        if stat.S_ISLNK(metadata.st_mode):
            raise OSError("OAuth secret cannot be a symlink")
        path = candidate.resolve(strict=True)
        if (
            path.parent != root
            or not stat.S_ISREG(metadata.st_mode)
            or metadata.st_uid != 10001
            or stat.S_IMODE(metadata.st_mode) & 0o077
            or metadata.st_size > 4096
        ):
            raise OSError("unsafe OAuth secret file")
        secret = path.read_text(encoding="utf-8").strip()
    except (OSError, UnicodeError) as error:
        raise IntegrationConfigError(
            f"OAuth client secret {reference!r} is unavailable or unsafe"
        ) from error
    if len(secret) < 16 or len(secret) > 4096 or "\x00" in secret:
        raise IntegrationConfigError(f"OAuth client secret {reference!r} is invalid")


def validate(
    allowlist_path: Path,
    providers_path: Path,
    secret_dir: Path,
    *,
    enabled: bool = False,
) -> None:
    allowlist = _load(allowlist_path, "integration allowlist")
    providers_root = _load(providers_path, "OAuth provider metadata")
    if set(allowlist).difference({"applications"}) or not isinstance(
        allowlist.get("applications", {}), dict
    ):
        raise IntegrationConfigError("integration allowlist root is invalid")
    if set(providers_root).difference({"providers"}) or not isinstance(
        providers_root.get("providers", {}), dict
    ):
        raise IntegrationConfigError("OAuth provider metadata root is invalid")

    required_providers: set[str] = set()
    for application_id, application in allowlist.get("applications", {}).items():
        if not isinstance(application_id, str) or not ID_RE.fullmatch(application_id):
            raise IntegrationConfigError("integration application id is invalid")
        if not isinstance(application, dict) or set(application).difference(
            {"config_versions", "resources"}
        ):
            raise IntegrationConfigError("integration application entry is invalid")
        versions = application.get("config_versions", [])
        resources = application.get("resources", [])
        if (
            not isinstance(versions, list)
            or any(isinstance(item, bool) or not isinstance(item, int) or item <= 0 for item in versions)
            or len(versions) != len(set(versions))
            or not isinstance(resources, list)
            or len(resources) > 500
        ):
            raise IntegrationConfigError("integration versions or resources are invalid")
        seen: set[tuple[str, str, str]] = set()
        for resource in resources:
            if not isinstance(resource, dict) or set(resource).difference(
                {"kind", "id", "parent_id", "name_zh", "name_en", "provider_id", "requires_oauth"}
            ):
                raise IntegrationConfigError("integration resource is invalid")
            kind = str(resource.get("kind") or "").strip().lower()
            resource_id = str(resource.get("id") or "").strip()
            parent_id = str(resource.get("parent_id") or "").strip()
            provider_id = str(resource.get("provider_id") or "").strip().lower()
            requires_oauth = resource.get("requires_oauth", False)
            names = (
                str(resource.get("name_zh") or resource_id).strip(),
                str(resource.get("name_en") or resource_id).strip(),
            )
            if (
                kind not in {"skill", "plugin", "tool", "connector"}
                or not ID_RE.fullmatch(resource_id)
                or (parent_id and not ID_RE.fullmatch(parent_id))
                or any(not name or len(name) > 128 for name in names)
                or not isinstance(requires_oauth, bool)
                or (kind == "tool" and not parent_id)
                or (kind != "tool" and parent_id)
                or (kind == "connector" and not PROVIDER_RE.fullmatch(provider_id))
                or (kind != "connector" and provider_id)
                or (requires_oauth and kind != "connector")
            ):
                raise IntegrationConfigError("integration resource is invalid")
            key = (kind, resource_id, parent_id)
            if key in seen:
                raise IntegrationConfigError("integration resource is duplicated")
            seen.add(key)
            if requires_oauth:
                required_providers.add(provider_id)

    providers = providers_root.get("providers", {})
    if enabled and not allowlist.get("applications", {}):
        raise IntegrationConfigError(
            "integrations cannot be enabled with an empty application allowlist"
        )
    for provider_id, provider in providers.items():
        if not isinstance(provider_id, str) or not PROVIDER_RE.fullmatch(provider_id):
            raise IntegrationConfigError("OAuth provider id is invalid")
        allowed_fields = {
            "authorization_url", "token_url", "revocation_url", "client_id",
            "client_secret_ref", "token_auth_method", "allowed_scopes", "allowed_hosts",
        }
        if not isinstance(provider, dict) or set(provider).difference(allowed_fields):
            raise IntegrationConfigError(f"OAuth provider {provider_id!r} is invalid")
        method = str(provider.get("token_auth_method") or "client_secret_basic").strip()
        secret_ref = str(provider.get("client_secret_ref") or "").strip()
        scopes = provider.get("allowed_scopes")
        hosts = provider.get("allowed_hosts")
        client_id = str(provider.get("client_id") or "").strip()
        if (
            not client_id
            or len(client_id) > 512
            or method not in {"client_secret_basic", "client_secret_post", "none"}
            or (method != "none" and not PROVIDER_RE.fullmatch(secret_ref))
            or (method == "none" and secret_ref)
            or not isinstance(scopes, list)
            or not scopes
            or len(scopes) > 32
            or any(not isinstance(scope, str) or not SCOPE_RE.fullmatch(scope) for scope in scopes)
            or len(scopes) != len(set(scopes))
            or not isinstance(hosts, list)
            or not hosts
            or len(hosts) > 8
        ):
            raise IntegrationConfigError(f"OAuth provider {provider_id!r} is invalid")
        normalized_hosts = {str(host or "").strip().lower().rstrip(".") for host in hosts}
        if len(normalized_hosts) != len(hosts) or any(
            not host or len(host) > 253 or "*" in host or ":" in host
            for host in normalized_hosts
        ):
            raise IntegrationConfigError(f"OAuth provider {provider_id!r} hosts are invalid")
        _oauth_url(provider.get("authorization_url"), normalized_hosts, "authorization_url")
        _oauth_url(provider.get("token_url"), normalized_hosts, "token_url")
        if str(provider.get("revocation_url") or "").strip():
            _oauth_url(provider.get("revocation_url"), normalized_hosts, "revocation_url")
        if method != "none":
            _validate_secret(secret_dir, secret_ref)

    missing = required_providers.difference(providers)
    if missing:
        raise IntegrationConfigError(
            "OAuth metadata is missing providers: " + ", ".join(sorted(missing))
        )


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--allowlist", required=True, type=Path)
    parser.add_argument("--providers", required=True, type=Path)
    parser.add_argument("--secret-dir", required=True, type=Path)
    parser.add_argument("--enabled", choices=("true", "false"), required=True)
    args = parser.parse_args(argv)
    try:
        validate(
            args.allowlist,
            args.providers,
            args.secret_dir,
            enabled=args.enabled == "true",
        )
    except IntegrationConfigError as error:
        print(f"integration configuration: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
