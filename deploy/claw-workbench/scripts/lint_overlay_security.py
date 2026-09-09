#!/usr/bin/env python3
"""Fail closed on dangerous constructs in the Claw deployment overlay."""

from __future__ import annotations

import argparse
import re
import sys
from dataclasses import dataclass
from pathlib import Path


SCANNED_NAMES = {"compose.yml", ".env.example", "new-api.env.example"}
SCANNED_SUFFIXES = {".sh", ".yml", ".yaml"}
SENSITIVE_KEY_RE = re.compile(
    r"(?i)(?:password|secret|token|api[_-]?key|app[_-]?key|access[_-]?key|private[_-]?key)"
)
ASSIGNMENT_RE = re.compile(
    r"^\s*(?:-\s*)?[\"']?([A-Za-z_][A-Za-z0-9_-]*)[\"']?\s*[:=]\s*(.*?)\s*$"
)
SAFE_SECRET_VALUE_RE = re.compile(
    r"^(?:|REPLACE(?:_[A-Z0-9_]+)?|<[^>]+>|/run/(?:secrets|workbench)/[^\s]+|"
    r"env(?:://|:)[A-Z0-9_]+|\$\(cat /run/(?:secrets|workbench)/[A-Za-z0-9_./-]+\))$"
)


@dataclass(frozen=True, order=True)
class Finding:
    code: str
    path: str
    line: int
    message: str

    def render(self) -> str:
        return f"{self.code} [{self.path}:{self.line}]: {self.message}"


def _relative_files(root: Path) -> list[Path]:
    files = []
    for path in root.rglob("*"):
        if not path.is_file() or "tests" in path.parts:
            continue
        relative = path.relative_to(root)
        if relative.parts and relative.parts[0] in {"backups", "state", "secrets"}:
            continue
        if path.name in SCANNED_NAMES or path.suffix.lower() in SCANNED_SUFFIXES or path.name.startswith("Dockerfile"):
            files.append(path)
    return sorted(files)


def scan(root: Path) -> list[Finding]:
    findings: list[Finding] = []
    for path in _relative_files(root):
        relative = path.relative_to(root).as_posix()
        try:
            lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
        except OSError:
            continue
        for line_number, line in enumerate(lines, 1):
            stripped = line.strip()
            if not stripped or stripped.startswith("#"):
                continue
            if path.suffix.lower() == ".sh" and re.search(
                r"(?:^|[;&|]\s*)(?:source|\.)\s+[^#\n]*\.env(?:\s|$|[;|&\"'])",
                line,
            ):
                findings.append(Finding("SOURCE_DOTENV", relative, line_number, "shell must not source a dotenv file"))
            if re.match(r"^\s*ports\s*:", line):
                findings.append(Finding("PUBLISHED_PORTS", relative, line_number, "host port publication is forbidden"))
            if re.match(r"^\s*privileged\s*:\s*(?:true|yes|on|1)\s*$", line, re.I):
                findings.append(Finding("PRIVILEGED_CONTAINER", relative, line_number, "privileged containers are forbidden"))
            if re.match(r"^\s*(?:network_mode|pid|ipc|uts)\s*:\s*[\"']?host[\"']?\s*$", line, re.I):
                findings.append(Finding("HOST_NAMESPACE", relative, line_number, "host namespaces are forbidden"))
            if re.search(r"/(?:var/)?run/docker\.sock(?:\b|$)", line):
                findings.append(Finding("DOCKER_SOCKET", relative, line_number, "Docker socket mounts/references are forbidden"))
            if re.match(r"^\s*user\s*:\s*[\"']?(?:root|0(?::[0-9]+)?)[\"']?\s*$", line, re.I) or re.match(
                r"^\s*USER\s+(?:root|0(?::[0-9]+)?)\s*$", line, re.I
            ):
                findings.append(Finding("ROOT_RUNTIME", relative, line_number, "runtime must not run as root"))
            if re.match(r"^\s*(?:user|privileged|network_mode|pid|ipc|uts)\s*:\s*.*\$\{", line):
                findings.append(Finding("DANGEROUS_INTERPOLATION", relative, line_number, "security boundary fields may not be interpolated"))
            if path.suffix.lower() in {".yml", ".yaml"} or path.name in {
                ".env.example",
                "new-api.env.example",
            }:
                without_secret_reads = re.sub(
                    r"\$\$\(cat /run/(?:secrets|workbench)/[A-Za-z0-9_./-]+\)",
                    "",
                    line,
                )
                has_command_substitution = "`" in without_secret_reads or re.search(
                    r"\$\([^)]*\)", without_secret_reads
                )
            else:
                has_command_substitution = False
            if has_command_substitution:
                findings.append(Finding("COMMAND_SUBSTITUTION", relative, line_number, "command substitution is forbidden in declarative deployment configuration"))

            match = ASSIGNMENT_RE.match(line)
            if path.name.startswith("Dockerfile"):
                match = re.match(
                    r"^\s*(?:ARG|ENV)\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?:=|\s)\s*(.*?)\s*$",
                    line,
                    re.I,
                ) or match
            elif path.suffix.lower() == ".sh":
                match = re.match(
                    r"^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)=(.*?)\s*$",
                    line,
                ) or match
            if match:
                key, raw_value = match.groups()
                value = raw_value.strip().strip("\"'")
                if (
                    SENSITIVE_KEY_RE.search(key)
                    and key.casefold() not in {"secret", "secrets"}
                    and not key.upper().endswith(
                        ("_FILE", "_REF", "_PATH", "_ID", "_COUNT", "_HASH", "_LENGTH", "_MODE")
                    )
                ):
                    shell_secret_reference = path.suffix.lower() == ".sh" and (
                        re.search(
                            r"(?:/run/(?:secrets|workbench)/|\$root/secrets/)",
                            value,
                        )
                        and "cat " in value
                        or re.fullmatch(r"\$\{[A-Z_][A-Z0-9_]*\}", value)
                    )
                    if "${" in value and not shell_secret_reference:
                        findings.append(Finding("SECRET_INTERPOLATION", relative, line_number, "secrets must use files, not environment interpolation"))
                    elif not shell_secret_reference and not SAFE_SECRET_VALUE_RE.fullmatch(value):
                        findings.append(Finding("PLAINTEXT_SECRET", relative, line_number, "credential-like value must be a secret-file or env reference"))

        if path.name.startswith("Dockerfile"):
            final_from = max(
                (index for index, line in enumerate(lines) if re.match(r"^\s*FROM\s+", line, re.I)),
                default=-1,
            )
            final_users = [
                (index + 1, line.strip().split(maxsplit=1)[1])
                for index, line in enumerate(lines[final_from + 1 :], final_from + 1)
                if re.match(r"^\s*USER\s+\S+", line, re.I)
            ]
            if final_from >= 0 and not final_users:
                findings.append(
                    Finding(
                        "ROOT_RUNTIME",
                        relative,
                        final_from + 1,
                        "final image stage must declare an explicit non-root USER",
                    )
                )
            elif final_users and "${" in final_users[-1][1]:
                findings.append(
                    Finding(
                        "DANGEROUS_INTERPOLATION",
                        relative,
                        final_users[-1][0],
                        "final runtime USER may not be interpolated",
                    )
                )
    return sorted(set(findings))


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1])
    args = parser.parse_args(argv)
    findings = scan(args.root.resolve())
    for finding in findings:
        print(f"overlay security: {finding.render()}", file=sys.stderr)
    return 1 if findings else 0


if __name__ == "__main__":
    raise SystemExit(main())
