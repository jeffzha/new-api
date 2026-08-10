#!/usr/bin/env python3
"""Local-only replacement for backup.sh in Windows CI."""

from __future__ import annotations

import hashlib
import sys
from datetime import datetime, timezone
from pathlib import Path


def main() -> int:
    if len(sys.argv) != 2:
        return 2
    target = Path(sys.argv[1])
    target.mkdir(parents=True, exist_ok=False)
    files = {
        "control-db.dump": b"fake-control-postgres-custom-dump",
        "adp-db.dump": b"fake-adp-postgres-custom-dump",
        "redis-forensics.rdb": b"fake-redis-forensics-only",
        "manifest.txt": (
            "created_at=" + datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "\n"
            "new_api_revision=fake\nclaw_control_revision=fake\nadp_revision=fake\n"
        ).encode(),
    }
    for name, data in files.items():
        (target / name).write_bytes(data)
    lines = [f"{hashlib.sha256(data).hexdigest()}  {name}" for name, data in files.items()]
    (target / "SHA256SUMS").write_text("\n".join(lines) + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
