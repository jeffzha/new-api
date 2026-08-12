#!/usr/bin/env python3
"""Validate deployment key files, rotation maps, and purpose separation."""

from __future__ import annotations

import argparse
import base64
import json
import re
import sys
from dataclasses import dataclass
from pathlib import Path


MAX_PREVIOUS_KEYS_BYTES = 64 * 1024
MAX_PREVIOUS_KEYS = 32
MAX_INTEGRATION_PREVIOUS_KEYS = 16
KID_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,63}")


class KeyValidationError(ValueError):
    pass


@dataclass(frozen=True)
class SecretMaterial:
    purpose: str
    representations: frozenset[bytes]


def _read_secret(path: Path) -> bytes:
    try:
        value = path.read_bytes().rstrip(b"\r\n")
    except OSError as error:
        raise KeyValidationError(f"cannot read {path.name}: {error}") from error
    if not value:
        raise KeyValidationError(f"{path.name} is empty")
    if len(value) > MAX_PREVIOUS_KEYS_BYTES:
        raise KeyValidationError(f"{path.name} exceeds the {MAX_PREVIOUS_KEYS_BYTES}-byte limit")
    if b"\x00" in value or b"\n" in value or b"\r" in value:
        raise KeyValidationError(f"{path.name} contains forbidden control characters")
    return value


def _canonical_base64(value: bytes, purpose: str, *, required_size: int | None) -> bytes:
    try:
        decoded = base64.b64decode(value, validate=True)
    except (ValueError, TypeError) as error:
        raise KeyValidationError(f"{purpose} is not canonical standard base64") from error
    if base64.b64encode(decoded) != value:
        raise KeyValidationError(f"{purpose} is not canonical standard base64")
    if required_size is not None and len(decoded) != required_size:
        raise KeyValidationError(f"{purpose} must decode to exactly {required_size} bytes")
    return decoded


def _representations(value: bytes) -> frozenset[bytes]:
    result = {value}
    try:
        decoded = base64.b64decode(value, validate=True)
    except (ValueError, TypeError):
        return frozenset(result)
    if base64.b64encode(decoded) == value:
        result.add(decoded)
    return frozenset(result)


def _json_object_without_duplicates(
    text: str, purpose: str, *, maximum_entries: int
) -> dict[str, str]:
    def pairs_hook(pairs: list[tuple[str, object]]) -> dict[str, object]:
        result: dict[str, object] = {}
        for key, value in pairs:
            if key in result:
                raise KeyValidationError(f"{purpose} contains duplicate key id {key}")
            result[key] = value
        return result

    try:
        value = json.loads(text, object_pairs_hook=pairs_hook)
    except KeyValidationError:
        raise
    except (TypeError, ValueError, json.JSONDecodeError) as error:
        raise KeyValidationError(f"{purpose} must contain a JSON object") from error
    if not isinstance(value, dict):
        raise KeyValidationError(f"{purpose} must contain a JSON object")
    if len(value) > maximum_entries:
        raise KeyValidationError(
            f"{purpose} contains more than {maximum_entries} historical keys"
        )
    if any(not isinstance(key, str) or not isinstance(item, str) for key, item in value.items()):
        raise KeyValidationError(f"{purpose} must map string key ids to base64 strings")
    return value


def _previous_materials(
    path: Path,
    *,
    domain: str,
    active_kid: str,
    maximum_entries: int = MAX_PREVIOUS_KEYS,
) -> list[SecretMaterial]:
    try:
        raw = path.read_bytes()
    except OSError as error:
        raise KeyValidationError(f"cannot read {path.name}: {error}") from error
    if len(raw) > MAX_PREVIOUS_KEYS_BYTES:
        raise KeyValidationError(
            f"{path.name} exceeds the {MAX_PREVIOUS_KEYS_BYTES}-byte limit"
        )
    try:
        text = raw.decode("utf-8")
    except UnicodeError as error:
        raise KeyValidationError(f"{path.name} is not UTF-8") from error
    entries = _json_object_without_duplicates(
        text, path.name, maximum_entries=maximum_entries
    )
    if active_kid in entries:
        raise KeyValidationError(f"{path.name} contains active key id {active_kid}")
    materials = []
    for kid, encoded in entries.items():
        if not KID_RE.fullmatch(kid):
            raise KeyValidationError(f"{path.name} contains invalid key id {kid!r}")
        encoded_bytes = encoded.encode("ascii", errors="strict")
        decoded = _canonical_base64(
            encoded_bytes,
            f"{domain} previous key {kid}",
            required_size=32,
        )
        materials.append(
            SecretMaterial(
                f"{domain} previous key {kid}",
                frozenset({encoded_bytes, decoded}),
            )
        )
    return materials


def validate(
    secrets_dir: Path,
    file_kid: str,
    workspace_kid: str,
    connector_kid: str,
    oauth_state_kid: str,
) -> None:
    for name, value in (
        ("file locator", file_kid),
        ("workspace locator", workspace_kid),
        ("connector token", connector_kid),
        ("OAuth state", oauth_state_kid),
    ):
        if not KID_RE.fullmatch(value):
            raise KeyValidationError(f"active {name} key id is invalid")

    materials: list[SecretMaterial] = []
    encoded_keys = (
        ("control evidence key", "evidence_master_key"),
        ("provider vault key", "provider_vault_master_key"),
        ("ADP usage evidence key", "adp_usage_evidence_key"),
        ("file locator active key", "adp_file_locator_key"),
        ("workspace locator active key", "adp_workspace_locator_key"),
        ("connector token active key", "adp_connector_token_key"),
        ("OAuth state active key", "adp_oauth_state_key"),
    )
    for purpose, filename in encoded_keys:
        encoded = _read_secret(secrets_dir / filename)
        decoded = _canonical_base64(encoded, purpose, required_size=32)
        materials.append(SecretMaterial(purpose, frozenset({encoded, decoded})))

    for purpose, filename in (
        ("Claw admin token", "claw_admin_token"),
        ("new-api control HMAC", "new_api_control_hmac"),
        ("ADP control HMAC", "adp_control_hmac"),
        ("new-api identity HMAC", "new_api_identity_hmac"),
        ("ADP session secret", "adp_session_secret"),
    ):
        value = _read_secret(secrets_dir / filename)
        if len(value) < 32:
            raise KeyValidationError(f"{purpose} must contain at least 32 bytes")
        materials.append(SecretMaterial(purpose, _representations(value)))

    materials.extend(
        _previous_materials(
            secrets_dir / "adp_file_locator_previous_keys.json",
            domain="file locator",
            active_kid=file_kid,
        )
    )
    materials.extend(
        _previous_materials(
            secrets_dir / "adp_workspace_locator_previous_keys.json",
            domain="workspace locator",
            active_kid=workspace_kid,
        )
    )
    materials.extend(
        _previous_materials(
            secrets_dir / "adp_connector_token_previous_keys.json",
            domain="connector token",
            active_kid=connector_kid,
            maximum_entries=MAX_INTEGRATION_PREVIOUS_KEYS,
        )
    )
    materials.extend(
        _previous_materials(
            secrets_dir / "adp_oauth_state_previous_keys.json",
            domain="OAuth state",
            active_kid=oauth_state_kid,
            maximum_entries=MAX_INTEGRATION_PREVIOUS_KEYS,
        )
    )

    for index, left in enumerate(materials):
        for right in materials[index + 1 :]:
            if left.representations.intersection(right.representations):
                raise KeyValidationError(
                    f"secret material is reused across {left.purpose} and {right.purpose}"
                )


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--secrets-dir", required=True, type=Path)
    parser.add_argument("--file-active-kid", required=True)
    parser.add_argument("--workspace-active-kid", required=True)
    parser.add_argument("--connector-active-kid", required=True)
    parser.add_argument("--oauth-state-active-kid", required=True)
    args = parser.parse_args(argv)
    try:
        validate(
            args.secrets_dir,
            args.file_active_kid,
            args.workspace_active_kid,
            args.connector_active_kid,
            args.oauth_state_active_kid,
        )
    except (KeyValidationError, UnicodeError) as error:
        print(f"secret key validation: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
