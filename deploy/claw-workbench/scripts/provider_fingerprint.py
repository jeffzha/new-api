#!/usr/bin/env python3
"""Print a canonical provider-secret fingerprint without printing the secret."""

from __future__ import annotations

import argparse
import hashlib
import re
import stat
import sys
from pathlib import Path


NAME_RE = re.compile(r"WORKBENCH_PROVIDER_[A-Z0-9_]+")
MAX_SECRET_BYTES = 4096


class FingerprintError(ValueError):
    pass


def _read(secrets_dir: Path, name: str) -> bytes:
    if not NAME_RE.fullmatch(name):
        raise FingerprintError("provider secret name is invalid")
    try:
        root_metadata = secrets_dir.lstat()
        if stat.S_ISLNK(root_metadata.st_mode) or not stat.S_ISDIR(root_metadata.st_mode):
            raise OSError("unsafe provider secret directory")
        root = secrets_dir.resolve(strict=True)
        candidate = root / name
        metadata = candidate.lstat()
        if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
            raise OSError("provider secret is not a regular file")
        path = candidate.resolve(strict=True)
        if path.parent != root:
            raise OSError("provider secret escapes its directory")
        value = path.read_bytes().rstrip(b"\r\n")
    except OSError as error:
        raise FingerprintError("provider secret is unavailable") from error
    if (
        not value
        or len(value) > MAX_SECRET_BYTES
        or any(byte < 32 or byte == 127 for byte in value)
    ):
        raise FingerprintError("provider secret is invalid")
    return value


def canonical(domain: bytes, *values: bytes) -> str:
    digest = hashlib.sha256()
    for value in (domain, *values):
        digest.update(str(len(value)).encode("ascii"))
        digest.update(b":")
        digest.update(value)
        digest.update(b"\n")
    return "sha256:" + digest.hexdigest()


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--secrets-dir",
        type=Path,
        default=Path(__file__).resolve().parents[1] / "secrets" / "provider",
    )
    subparsers = parser.add_subparsers(dest="kind", required=True)
    credential = subparsers.add_parser("credential")
    credential.add_argument("secret_id_name")
    credential.add_argument("secret_key_name")
    app_key = subparsers.add_parser("app-key")
    app_key.add_argument("app_key_name")
    subparsers.add_parser("validate")
    args = parser.parse_args(argv)
    try:
        if args.kind == "credential":
            result = canonical(
                b"claw-control/tencent-secret-pair/v1",
                _read(args.secrets_dir, args.secret_id_name),
                _read(args.secrets_dir, args.secret_key_name),
            )
        elif args.kind == "app-key":
            result = canonical(
                b"claw-control/tencent-app-key/v1",
                _read(args.secrets_dir, args.app_key_name),
            )
        else:
            for path in args.secrets_dir.iterdir():
                if path.name.startswith("."):
                    continue
                _read(args.secrets_dir, path.name)
            return 0
    except FingerprintError as error:
        print(f"provider fingerprint: {error}", file=sys.stderr)
        return 1
    print(result)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
