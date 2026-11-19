#!/usr/bin/env python3
"""Check versioned index rules and the OPS online index registry."""
from pathlib import Path
import re
import subprocess
import sys


def main():
    root = Path(__file__).resolve().parents[2]
    paths = ("AGENTS.md", "docs/operations/INDEX_CHANGE_AND_RELEASE_POLICY.md")
    errors = []
    for name in paths:
        tracked = subprocess.run(
            ["git", "ls-files", "--error-unmatch", "--", name], cwd=root,
            capture_output=True, text=True, check=False,
        )
        if tracked.returncode or not (root / name).is_file():
            errors.append(name + " must be present and tracked")
    agents = (root / paths[0]).read_text(encoding="utf-8") if (root / paths[0]).is_file() else ""
    for required in (
        "INDEX_CHANGE_AND_RELEASE_POLICY.md", "每个新增索引", "每次部署",
        "已有且定义正确", "缺失", "同名不同定义", "维护锁", "ready=true",
        "聚合只维护", "不记录连接密码或密钥",
    ):
        if required not in agents:
            errors.append("root AGENTS.md is missing index policy requirement: " + required)
    directory = root / "internal/db/feeindexes"
    if directory.is_dir():
        registered = re.findall(
            r'\{"([a-z0-9_]+)",\s*"([a-z0-9_]+)",\s*"([a-z0-9_]+\.sql)"\}',
            (directory / "indexes.go").read_text(encoding="utf-8"),
        )
        actual_files = {p.name for p in directory.glob("*.sql")}
        if not registered or {r[2] for r in registered} != actual_files:
            errors.append("every online index SQL file must be registered exactly once")
        if len({r[0] for r in registered}) != len(registered) or len({r[2] for r in registered}) != len(registered):
            errors.append("index names and SQL registrations must be unique")
        runbook = root / "docs/operations/FEE_INDEX_RELEASE_GATE.md"
        text = runbook.read_text(encoding="utf-8") if runbook.is_file() else ""
        for name, table, filename in registered:
            sql_file = directory / filename
            if not sql_file.is_file():
                continue
            definition = sql_file.read_text(encoding="utf-8")
            match = re.search(r"CREATE\s+INDEX\s+CONCURRENTLY\s+IF\s+NOT\s+EXISTS\s+(\w+)\s+ON\s+public\.(\w+)", definition, re.I)
            if not match or match.groups() != (name, table):
                errors.append(filename + " does not match its registered online index")
            if chr(96) + name + chr(96) not in text:
                errors.append(name + " must be documented in FEE_INDEX_RELEASE_GATE.md")
    if errors:
        print("Index policy check failed:\n" + "\n".join("- " + error for error in errors), file=sys.stderr)
        return 1
    print("Index policy check passed: tracked rules and applicable online index registry")
    return 0


if __name__ == "__main__":
    sys.exit(main())
