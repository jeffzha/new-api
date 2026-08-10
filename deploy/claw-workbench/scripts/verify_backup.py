#!/usr/bin/env python3
"""Verify the exact, non-symlink source-backup contract before restore."""

from __future__ import annotations

import argparse
import hashlib
import re
import stat
import sys
from pathlib import Path


REQUIRED_FILES = frozenset(
    {"control-db.dump", "adp-db.dump", "redis-forensics.rdb", "manifest.txt"}
)
DR_MANIFEST_FILES = frozenset({"dr-manifest.json", "DR_MANIFEST.sha256"})
CHECKSUM_RE = re.compile(r"^([0-9a-f]{64})  ([A-Za-z0-9][A-Za-z0-9._-]*)$")
DR_CHECKSUM_RE = re.compile(r"^([0-9a-f]{64})  dr-manifest\.json$")
MAX_CHECKSUM_BYTES = 64 * 1024


class BackupVerificationError(ValueError):
    pass


def _regular_file(path: Path) -> bool:
    try:
        mode = path.lstat().st_mode
    except OSError:
        return False
    return stat.S_ISREG(mode) and not path.is_symlink()


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def verify_backup(directory: Path) -> dict[str, str]:
    root = directory.resolve(strict=True)
    if directory.is_symlink() or not directory.is_dir():
        raise BackupVerificationError("backup path must be a regular directory")

    children = list(root.iterdir())
    names = {child.name for child in children}
    base_files = REQUIRED_FILES | {"SHA256SUMS"}
    if names not in {base_files, base_files | DR_MANIFEST_FILES}:
        raise BackupVerificationError("backup directory has missing or unexpected entries")
    if any(not _regular_file(child) for child in children):
        raise BackupVerificationError("backup entries must be regular non-symlink files")

    checksum_path = root / "SHA256SUMS"
    if checksum_path.stat().st_size > MAX_CHECKSUM_BYTES:
        raise BackupVerificationError("SHA256SUMS exceeds the verification limit")
    try:
        checksum_text = checksum_path.read_text(encoding="ascii")
    except UnicodeError as error:
        raise BackupVerificationError("SHA256SUMS is not ASCII") from error

    declared: dict[str, str] = {}
    for line in checksum_text.splitlines():
        match = CHECKSUM_RE.fullmatch(line)
        if not match or match.group(2) in declared:
            raise BackupVerificationError("SHA256SUMS is malformed or duplicated")
        declared[match.group(2)] = match.group(1)
    if set(declared) != REQUIRED_FILES:
        raise BackupVerificationError("SHA256SUMS does not cover the exact backup file set")

    for name, expected in declared.items():
        if _sha256(root / name) != expected:
            raise BackupVerificationError(f"checksum mismatch for {name}")

    if DR_MANIFEST_FILES.issubset(names):
        dr_checksum_path = root / "DR_MANIFEST.sha256"
        if dr_checksum_path.stat().st_size > 256:
            raise BackupVerificationError("DR_MANIFEST.sha256 exceeds the verification limit")
        try:
            dr_checksum_text = dr_checksum_path.read_text(encoding="ascii").strip()
        except UnicodeError as error:
            raise BackupVerificationError("DR_MANIFEST.sha256 is not ASCII") from error
        match = DR_CHECKSUM_RE.fullmatch(dr_checksum_text)
        if not match or _sha256(root / "dr-manifest.json") != match.group(1):
            raise BackupVerificationError("DR manifest checksum mismatch")
    return declared


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("backup_directory", type=Path)
    args = parser.parse_args(argv)
    try:
        verify_backup(args.backup_directory)
    except (OSError, BackupVerificationError) as error:
        print(f"backup verification: {error}", file=sys.stderr)
        return 1
    print("backup verification: exact checksums and file types passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
