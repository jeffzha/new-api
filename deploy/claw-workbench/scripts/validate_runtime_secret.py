#!/usr/bin/env python3
"""Validate a runtime Secret before it is exposed to a UID 10001 container."""

from __future__ import annotations

import argparse
import os
import stat
import sys
from pathlib import Path


EXPECTED_UID = 10001
MAX_SECRET_BYTES = 4096


class RuntimeSecretError(ValueError):
    pass


def _read_unchanged_regular_file(path: Path, expected: os.stat_result) -> bytes:
    flags = os.O_RDONLY
    if hasattr(os, "O_CLOEXEC"):
        flags |= os.O_CLOEXEC
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    try:
        descriptor = os.open(path, flags)
    except OSError as error:
        raise RuntimeSecretError(f"cannot open {path.name} safely: {error}") from error
    try:
        actual = os.fstat(descriptor)
        if (
            not stat.S_ISREG(actual.st_mode)
            or actual.st_dev != expected.st_dev
            or actual.st_ino != expected.st_ino
        ):
            raise RuntimeSecretError(f"{path.name} changed during validation")
        chunks: list[bytes] = []
        remaining = MAX_SECRET_BYTES + 1
        while remaining:
            chunk = os.read(descriptor, remaining)
            if not chunk:
                break
            chunks.append(chunk)
            remaining -= len(chunk)
        return b"".join(chunks)
    finally:
        os.close(descriptor)


def validate_runtime_secret(path: Path, minimum_length: int) -> bytes:
    if not 1 <= minimum_length <= MAX_SECRET_BYTES:
        raise RuntimeSecretError(
            f"minimum length must be between 1 and {MAX_SECRET_BYTES}"
        )
    try:
        metadata = path.lstat()
    except OSError as error:
        raise RuntimeSecretError(f"cannot stat {path.name}: {error}") from error
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
        raise RuntimeSecretError(f"{path.name} must be a regular non-symlink file")
    if metadata.st_uid != EXPECTED_UID:
        raise RuntimeSecretError(f"{path.name} must be owned by UID {EXPECTED_UID}")
    mode = stat.S_IMODE(metadata.st_mode)
    if not mode & stat.S_IRUSR:
        raise RuntimeSecretError(f"{path.name} must be readable by its owner")
    if mode & 0o077:
        raise RuntimeSecretError(
            f"{path.name} must not be accessible by group or other users"
        )
    if not 1 <= metadata.st_size <= MAX_SECRET_BYTES:
        raise RuntimeSecretError(
            f"{path.name} must contain between 1 and {MAX_SECRET_BYTES} bytes"
        )

    raw = _read_unchanged_regular_file(path, metadata)
    if not 1 <= len(raw) <= MAX_SECRET_BYTES:
        raise RuntimeSecretError(
            f"{path.name} must contain between 1 and {MAX_SECRET_BYTES} bytes"
        )
    value = raw.rstrip(b"\r\n")
    if len(value) < minimum_length:
        raise RuntimeSecretError(
            f"{path.name} must contain at least {minimum_length} bytes after trailing line endings are removed"
        )
    if any(byte < 32 or byte == 127 for byte in value):
        raise RuntimeSecretError(
            f"{path.name} contains NUL, a control character, or an internal line ending"
        )
    return value


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--minimum-length", required=True, type=int)
    parser.add_argument("path", type=Path)
    args = parser.parse_args(argv)
    try:
        validate_runtime_secret(args.path, args.minimum_length)
    except RuntimeSecretError as error:
        print(f"runtime secret validation: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
