#!/usr/bin/env python3
"""Install an ADP provider secret set without exposing values in argv or logs."""

from __future__ import annotations

import argparse
import getpass
import os
import re
import stat
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from provider_fingerprint import canonical


MAX_SECRET_BYTES = 4096
CUSTOMER_CODE_RE = re.compile(r"[A-Z0-9]+(?:_[A-Z0-9]+)*")


class ProviderSecretInstallError(ValueError):
    pass


def normalize_customer_code(value: str) -> str:
    normalized = value.strip().upper().replace("-", "_")
    if not CUSTOMER_CODE_RE.fullmatch(normalized):
        raise ProviderSecretInstallError(
            "customer code must contain only letters, digits, hyphens, or underscores"
        )
    return normalized


def validate_secret(value: str, label: str) -> bytes:
    encoded = value.encode("utf-8")
    if not encoded or len(encoded) > MAX_SECRET_BYTES:
        raise ProviderSecretInstallError(
            f"{label} must contain between 1 and {MAX_SECRET_BYTES} bytes"
        )
    if any(byte < 32 or byte == 127 for byte in encoded):
        raise ProviderSecretInstallError(f"{label} contains a control character")
    return encoded


def secret_names(customer_code: str) -> dict[str, str]:
    prefix = f"WORKBENCH_PROVIDER_{normalize_customer_code(customer_code)}"
    return {
        "app_key": f"{prefix}_ADP_APP_KEY",
        "secret_id": f"{prefix}_TENCENT_SECRET_ID",
        "secret_key": f"{prefix}_TENCENT_SECRET_KEY",
    }


def _validate_directory(path: Path) -> Path:
    try:
        metadata = path.lstat()
    except OSError as error:
        raise ProviderSecretInstallError("provider secret directory is unavailable") from error
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISDIR(metadata.st_mode):
        raise ProviderSecretInstallError("provider secret directory must be a real directory")
    if stat.S_IMODE(metadata.st_mode) != 0o700:
        raise ProviderSecretInstallError("provider secret directory must have mode 0700")
    return path.resolve(strict=True)


def install_provider_secrets(
    secrets_dir: Path,
    customer_code: str,
    app_key: str,
    secret_id: str,
    secret_key: str,
    *,
    owner_uid: int = 10001,
    owner_gid: int = 10001,
) -> tuple[dict[str, Path], str, str]:
    root = _validate_directory(secrets_dir)
    names = secret_names(customer_code)
    values = {
        names["app_key"]: validate_secret(app_key, "ADP AppKey"),
        names["secret_id"]: validate_secret(secret_id, "Tencent SecretId"),
        names["secret_key"]: validate_secret(secret_key, "Tencent SecretKey"),
    }
    targets = {name: root / name for name in values}
    for target in targets.values():
        if target.exists() or target.is_symlink():
            raise ProviderSecretInstallError(
                f"refusing to overwrite existing provider secret {target.name}"
            )

    temporary_paths: list[Path] = []
    linked_paths: list[Path] = []
    directory_descriptor = -1
    try:
        for name, value in values.items():
            descriptor, temporary_name = tempfile.mkstemp(prefix=f".{name}.", dir=root)
            temporary = Path(temporary_name)
            temporary_paths.append(temporary)
            try:
                if hasattr(os, "fchmod"):
                    os.fchmod(descriptor, 0o600)
                if hasattr(os, "fchown"):
                    os.fchown(descriptor, owner_uid, owner_gid)
                with os.fdopen(descriptor, "wb") as output:
                    descriptor = -1
                    output.write(value)
                    output.flush()
                    os.fsync(output.fileno())
            finally:
                if descriptor >= 0:
                    os.close(descriptor)
            os.chmod(temporary, 0o600)

        for temporary, target in zip(temporary_paths, targets.values(), strict=True):
            os.link(temporary, target, follow_symlinks=False)
            linked_paths.append(target)
        if os.name == "posix":
            directory_descriptor = os.open(root, os.O_RDONLY)
            os.fsync(directory_descriptor)
    except (OSError, ProviderSecretInstallError) as error:
        for target in linked_paths:
            try:
                target.unlink()
            except FileNotFoundError:
                pass
        if isinstance(error, ProviderSecretInstallError):
            raise
        raise ProviderSecretInstallError("provider secrets could not be installed atomically") from error
    finally:
        if directory_descriptor >= 0:
            os.close(directory_descriptor)
        for temporary in temporary_paths:
            try:
                temporary.unlink()
            except FileNotFoundError:
                pass

    app_key_fingerprint = canonical(
        b"claw-control/tencent-app-key/v1", values[names["app_key"]]
    )
    credential_fingerprint = canonical(
        b"claw-control/tencent-secret-pair/v1",
        values[names["secret_id"]],
        values[names["secret_key"]],
    )
    return targets, app_key_fingerprint, credential_fingerprint


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--customer-code", required=True)
    parser.add_argument(
        "--secrets-dir",
        type=Path,
        default=Path(__file__).resolve().parents[1] / "secrets" / "provider",
    )
    args = parser.parse_args(argv)
    if os.name != "posix" or not hasattr(os, "geteuid") or os.geteuid() != 0:
        print("provider secret installer must run as root on a POSIX host", file=sys.stderr)
        return 1
    try:
        app_key = getpass.getpass("Rotated ADP AppKey: ")
        secret_id = getpass.getpass("Rotated Tencent SecretId: ")
        secret_key = getpass.getpass("Rotated Tencent SecretKey: ")
        targets, app_key_fingerprint, credential_fingerprint = install_provider_secrets(
            args.secrets_dir,
            args.customer_code,
            app_key,
            secret_id,
            secret_key,
        )
    except (ProviderSecretInstallError, EOFError, KeyboardInterrupt) as error:
        print(f"provider secret installer: {error}", file=sys.stderr)
        return 1
    print("Installed provider secret files:")
    for target in targets.values():
        print(f"  {target.name}")
    print(f"AppKey fingerprint: {app_key_fingerprint}")
    print(f"Credential fingerprint: {credential_fingerprint}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
