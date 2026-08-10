#!/usr/bin/env python3
"""Create only deployment-owned Claw Workbench secrets, once and atomically."""

from __future__ import annotations

import argparse
import base64
import os
import secrets
import stat
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Callable


EXPECTED_OWNER_UID = 10001
SECRET_DIRECTORIES = ("provider", "oauth", "billing", "files", "sandbox")


class BootstrapError(RuntimeError):
    pass


@dataclass(frozen=True)
class GeneratedSecret:
    path: str
    content: Callable[[], bytes]


def _random_base64(byte_count: int) -> bytes:
    return base64.b64encode(secrets.token_bytes(byte_count)) + b"\n"


GENERATED_SECRETS = (
    GeneratedSecret("control_db_password", lambda: _random_base64(48)),
    GeneratedSecret("adp_db_password", lambda: _random_base64(48)),
    GeneratedSecret("redis_password", lambda: _random_base64(48)),
    GeneratedSecret("claw_admin_token", lambda: _random_base64(48)),
    GeneratedSecret("new_api_control_hmac", lambda: _random_base64(48)),
    GeneratedSecret("adp_control_hmac", lambda: _random_base64(48)),
    GeneratedSecret("new_api_identity_hmac", lambda: _random_base64(48)),
    GeneratedSecret("evidence_master_key", lambda: _random_base64(32)),
    GeneratedSecret("adp_usage_evidence_key", lambda: _random_base64(32)),
    GeneratedSecret("adp_file_locator_key", lambda: _random_base64(32)),
    GeneratedSecret("adp_file_locator_previous_keys.json", lambda: b"{}\n"),
    GeneratedSecret("adp_workspace_locator_key", lambda: _random_base64(32)),
    GeneratedSecret("adp_workspace_locator_previous_keys.json", lambda: b"{}\n"),
    GeneratedSecret("adp_connector_token_key", lambda: _random_base64(32)),
    GeneratedSecret("adp_connector_token_previous_keys.json", lambda: b"{}\n"),
    GeneratedSecret("adp_oauth_state_key", lambda: _random_base64(32)),
    GeneratedSecret("adp_oauth_state_previous_keys.json", lambda: b"{}\n"),
    GeneratedSecret("adp_session_secret", lambda: _random_base64(48)),
    GeneratedSecret("sandbox/client-token-hmac-key", lambda: _random_base64(48)),
)


def _validate_directory(path: Path, owner_uid: int | None) -> None:
    try:
        metadata = path.lstat()
    except OSError as error:
        raise BootstrapError(f"cannot inspect {path}") from error
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISDIR(metadata.st_mode):
        raise BootstrapError(f"{path} must be a non-symlink directory")
    if owner_uid is not None:
        if stat.S_IMODE(metadata.st_mode) & 0o077:
            raise BootstrapError(f"{path} must not be accessible by group or other")
        if metadata.st_uid not in {0, owner_uid}:
            raise BootstrapError(f"{path} has an unexpected owner")


def _ensure_directory(path: Path, owner_uid: int | None) -> None:
    try:
        path.mkdir(mode=0o700)
    except FileExistsError:
        pass
    except OSError as error:
        raise BootstrapError(f"cannot create {path}") from error
    _validate_directory(path, owner_uid)


def _validate_existing_secret(path: Path, owner_uid: int | None) -> None:
    try:
        metadata = path.lstat()
    except OSError as error:
        raise BootstrapError(f"cannot inspect existing secret {path.name}") from error
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
        raise BootstrapError(f"existing secret {path.name} is not a regular file")
    if metadata.st_size <= 0 or metadata.st_size > 65536:
        raise BootstrapError(f"existing secret {path.name} has an invalid size")
    if os.name == "posix":
        mode = stat.S_IMODE(metadata.st_mode)
        if mode & 0o077 or not mode & 0o400:
            raise BootstrapError(f"existing secret {path.name} has unsafe permissions")
    if owner_uid is not None:
        if metadata.st_uid != owner_uid:
            raise BootstrapError(f"existing secret {path.name} has an unexpected owner")


def _create_secret(path: Path, content: bytes, owner_uid: int | None) -> bool:
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
    flags |= getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(path, flags, 0o600)
    except FileExistsError:
        _validate_existing_secret(path, owner_uid)
        return False
    except OSError as error:
        raise BootstrapError(f"cannot create secret {path.name}") from error
    try:
        with os.fdopen(descriptor, "wb", closefd=True) as stream:
            stream.write(content)
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(path, 0o600)
        if owner_uid is not None:
            os.chown(path, owner_uid, owner_uid)
    except Exception:
        try:
            path.unlink()
        except OSError:
            pass
        raise
    return True


def bootstrap(secret_root: Path, *, owner_uid: int | None) -> tuple[int, int]:
    _ensure_directory(secret_root, owner_uid)
    for name in SECRET_DIRECTORIES:
        _ensure_directory(secret_root / name, owner_uid)
    generated = 0
    preserved = 0
    for specification in GENERATED_SECRETS:
        path = secret_root / specification.path
        if _create_secret(path, specification.content(), owner_uid):
            generated += 1
        else:
            preserved += 1
    return generated, preserved


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--secret-root",
        type=Path,
        default=Path(__file__).resolve().parents[1] / "secrets",
    )
    args = parser.parse_args()
    if os.name != "posix" or not hasattr(os, "geteuid") or os.geteuid() != 0:
        print("generated secret bootstrap must run as root on the Linux deployment host", file=sys.stderr)
        return 1
    try:
        generated, preserved = bootstrap(
            args.secret_root.resolve(), owner_uid=EXPECTED_OWNER_UID
        )
    except (BootstrapError, OSError) as error:
        print(f"generated secret bootstrap: {error}", file=sys.stderr)
        return 1
    print(f"generated secret bootstrap: generated={generated} preserved={preserved}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
