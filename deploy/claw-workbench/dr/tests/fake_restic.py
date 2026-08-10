#!/usr/bin/env python3
"""Filesystem fake for restic orchestration; never opens a network socket."""

from __future__ import annotations

import hashlib
import json
import os
import shutil
import sys
import tempfile
from pathlib import Path


VERSION = "restic 0.99.0-fake"


def repository_root() -> Path:
    repository = os.environ.get("RESTIC_REPOSITORY", "")
    password = os.environ.get("RESTIC_PASSWORD", "")
    access = os.environ.get("AWS_ACCESS_KEY_ID", "")
    secret = os.environ.get("AWS_SECRET_ACCESS_KEY", "")
    if not repository or not password or not access or not secret:
        raise RuntimeError("fake restic requires repository credentials")
    digest = hashlib.sha256(repository.encode()).hexdigest()
    return Path(tempfile.gettempdir()) / "claw-fake-restic" / digest


def main() -> int:
    if len(sys.argv) == 2 and sys.argv[1] == "version":
        print(VERSION)
        return 0
    if len(sys.argv) < 2:
        return 2
    command = sys.argv[1]
    root = repository_root()
    if command == "init":
        if root.exists():
            return 1
        (root / "snapshots").mkdir(parents=True)
        return 0
    if not (root / "snapshots").is_dir():
        return 1
    if command == "backup":
        source = Path(sys.argv[-1])
        if not source.is_dir():
            return 1
        snapshot = hashlib.sha256((str(source) + os.environ["RESTIC_REPOSITORY"]).encode()).hexdigest()
        target = root / "snapshots" / snapshot
        if target.exists():
            shutil.rmtree(target)
        shutil.copytree(source, target / source.name)
        print(json.dumps({"message_type": "summary", "snapshot_id": snapshot}))
        return 0
    if command == "check":
        return 0 if any((root / "snapshots").iterdir()) else 1
    if command == "forget":
        return 0
    if command == "snapshots":
        values = [{"id": path.name} for path in (root / "snapshots").iterdir() if path.is_dir()]
        print(json.dumps(values))
        return 0
    if command == "restore":
        selector = sys.argv[2]
        try:
            target_index = sys.argv.index("--target")
            destination = Path(sys.argv[target_index + 1])
        except (ValueError, IndexError):
            return 2
        snapshots = sorted(path for path in (root / "snapshots").iterdir() if path.is_dir())
        if selector == "latest":
            source = snapshots[-1] if snapshots else None
        else:
            source = root / "snapshots" / selector
        if source is None or not source.is_dir():
            return 1
        for item in source.iterdir():
            shutil.copytree(item, destination / "restored" / item.name)
        return 0
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
