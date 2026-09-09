#!/usr/bin/env python3
"""Local-only replacement for pg_restore --list."""

from __future__ import annotations

import sys
from pathlib import Path


def main() -> int:
    if len(sys.argv) != 3 or sys.argv[1] != "--list":
        return 2
    path = Path(sys.argv[2])
    return 0 if path.is_file() and path.stat().st_size > 0 else 1


if __name__ == "__main__":
    raise SystemExit(main())
