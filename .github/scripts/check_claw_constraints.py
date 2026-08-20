#!/usr/bin/env python3
"""Enforce the new-api-side Claw ownership boundary and merge budget.

The gate deliberately measures from the feature-start revision, rather than
trying to infer whether a change "looks like" Claw work.  This makes a release
candidate reproducible: every path after the baseline must be part of the
explicit extension surface.  Exceptions are fail-closed and require both an
environment switch and a structured, approved ADR naming every finding.
"""

from __future__ import annotations

import argparse
import datetime as dt
import fnmatch
import os
import re
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path


DEFAULT_BASELINE = "91f6b455dbd36858a7cff9d65b87e1d5a3fb4861"
BASELINE = os.environ.get("CLAW_NEW_API_BASELINE", DEFAULT_BASELINE)
MAX_EXISTING_SOURCE_FILES = 10
MAX_EXISTING_SOURCE_CHANGED_LINES = 100
MIN_ADDITIVE_SOURCE_RATIO = 0.90

SOURCE_SUFFIXES = {".go", ".js", ".jsx", ".py", ".sh", ".ts", ".tsx", ".vue"}
GENERATED_SOURCE_PATHS = {"web/src/routeTree.gen.ts"}
UPSTREAM_SOURCE_PREFIXES = (
    "common/",
    "constant/",
    "controller/",
    "dto/",
    "middleware/",
    "model/",
    "relay/",
    "router/",
    "service/",
    "setting/",
    "types/",
    "web/src/",
)

# This is intentionally a closed list.  Additions need an architecture review;
# a broad directory such as controller/** or web/src/** is never valid.
ALLOWED_PATH_PATTERNS = (
    "claw-control/**",
    "deploy/claw-workbench/**",
    "docs/adp-claw-*.md",
    "docs/adp-agent-store-*.md",
    "controller/workbench_*.go",
    "router/workbench_*.go",
    "service/workbenchbridge/**",
    "router/api-router.go",
    "web/src/features/workbench-entry/**",
    "web/src/features/agent-store/**",
    "web/src/hooks/use-sidebar-data.ts",
    "web/src/routes/_authenticated/playground/index.tsx",
    "web/src/routes/_authenticated/playground/legacy.tsx",
    "web/src/routes/_authenticated/playground/select.tsx",
    "web/src/routes/_authenticated/agent-store/**",
    "web/src/routes/_authenticated/workbench-admin.tsx",
    "web/src/routeTree.gen.ts",
    "web/src/i18n/locales/en.json",
    "web/src/i18n/locales/fr.json",
    "web/src/i18n/locales/ja.json",
    "web/src/i18n/locales/ru.json",
    "web/src/i18n/locales/vi.json",
    "web/src/i18n/locales/zh.json",
    "web/src/i18n/locales/zh-TW.json",
    ".github/scripts/check_claw_constraints.py",
    ".github/scripts/tests/test_check_claw_constraints.py",
    ".github/workflows/claw-control.yml",
    ".github/workflows/claw-workbench-release-images.yml",
)

FORBIDDEN_CORE_PREFIXES = (
    "common/",
    "constant/",
    "dto/",
    "middleware/",
    "model/",
    "relay/",
    "setting/",
    "types/",
)
FORBIDDEN_EXISTING_FRAGMENTS = (
    "billing",
    "channel_type",
    "model_ratio",
    "pre_consume",
    "quota",
    "settle",
)

ROOT_CORE_IMPORT_RE = re.compile(
    r'["\']github\.com/QuantumNous/new-api/'
    r'(?P<package>constant|dto|middleware|model|relay|setting|types)(?:["\']|/)',
)
ROOT_CORE_MODULE_RE = re.compile(
    r"(?m)^\s*(?:require|replace)\s+github\.com/QuantumNous/new-api(?:\s|$)"
)
DANGEROUS_CORE_SYMBOL_RE = re.compile(
    r"\b(?:model\.(?:Token|Channel|Ability|Log)|"
    r"(?:PreConsume|PostConsume|Quota|Relay|APIKey)[A-Za-z0-9_]*)\b"
)

SANCTIONED_CORE_IMPORTS = {
    # This is the minimal identity-truth exception required by design section
    # 18: an authenticated entry may read model.User but owns no Claw records.
    "controller/workbench_identity_bridge.go": {"model"},
    # Existing web-session/root authentication remains the router boundary.
    "router/workbench_identity_routes.go": {"middleware"},
}

ADR_OVERRIDE_ENV = "CLAW_CONSTRAINT_ADR_OVERRIDE"
ADR_PATH_ENV = "CLAW_CONSTRAINT_ADR"
ADR_PATH_RE = re.compile(r"^docs/adp-claw-adr-[a-z0-9][a-z0-9-]*\.md$")
ADR_FIELDS = (
    "Status",
    "Approved-By",
    "Approval-Date",
    "Constraint-IDs",
    "Impact",
    "Rollback",
)


@dataclass(frozen=True, order=True)
class Finding:
    code: str
    message: str
    path: str = ""

    def render(self) -> str:
        location = f" [{self.path}]" if self.path else ""
        return f"{self.code}{location}: {self.message}"


@dataclass
class GateReport:
    baseline: str
    changed_paths: list[str]
    existing_source: list[str]
    existing_changed_lines: int
    additive_ratio: float
    findings: list[Finding]
    override_accepted: bool = False
    override_adr: str = ""

    @property
    def passed(self) -> bool:
        return not self.findings or self.override_accepted


def run_git(repo: Path, *args: str, check: bool = True) -> str:
    result = subprocess.run(
        ["git", "-C", str(repo), *args],
        check=False,
        text=True,
        encoding="utf-8",
        errors="replace",
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    if check and result.returncode:
        raise RuntimeError(result.stderr.strip() or "git command failed")
    return result.stdout


def has_commit(repo: Path, revision: str) -> bool:
    result = subprocess.run(
        ["git", "-C", str(repo), "cat-file", "-e", f"{revision}^{{commit}}"],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    return result.returncode == 0


def normalize(path: str) -> str:
    return path.replace("\\", "/").removeprefix("./")


def path_matches(path: str, pattern: str) -> bool:
    path = normalize(path)
    if pattern.endswith("/**"):
        return path.startswith(pattern[:-3])
    return fnmatch.fnmatchcase(path, pattern)


def is_allowed_path(path: str) -> bool:
    return any(path_matches(path, pattern) for pattern in ALLOWED_PATH_PATTERNS)


def is_source(path: str) -> bool:
    path = normalize(path)
    if path in GENERATED_SOURCE_PATHS:
        return False
    if Path(path).suffix.lower() not in SOURCE_SUFFIXES:
        return False
    if path.endswith("_test.go") or "/test/" in path or "/tests/" in path:
        return False
    return path.startswith(UPSTREAM_SOURCE_PREFIXES) or path.startswith("claw-control/")


def read_text(repo: Path, path: str) -> str:
    try:
        return (repo / path).read_text(encoding="utf-8", errors="replace")
    except OSError:
        return ""


def line_count(repo: Path, path: str) -> int:
    return len(read_text(repo, path).splitlines())


def collect_changes(
    repo: Path, baseline: str
) -> tuple[set[str], set[str], dict[str, tuple[int, int]]]:
    baseline_paths = {
        normalize(path)
        for path in run_git(repo, "ls-tree", "-r", "--name-only", baseline).splitlines()
        if path
    }
    changed_paths: set[str] = set()
    for raw_line in run_git(
        repo, "diff", "--no-renames", "--name-status", baseline, "--", "."
    ).splitlines():
        fields = raw_line.split("\t")
        if len(fields) >= 2:
            changed_paths.update(normalize(path) for path in fields[1:] if path)
    changed_paths.update(
        normalize(path)
        for path in run_git(repo, "ls-files", "--others", "--exclude-standard").splitlines()
        if path
    )

    numstat: dict[str, tuple[int, int]] = {}
    for raw_line in run_git(
        repo, "diff", "--no-renames", "--numstat", baseline, "--", "."
    ).splitlines():
        fields = raw_line.split("\t")
        if len(fields) != 3 or fields[0] == "-" or fields[1] == "-":
            continue
        numstat[normalize(fields[2])] = (int(fields[0]), int(fields[1]))
    return baseline_paths, changed_paths, numstat


def dependency_findings(repo: Path, changed_paths: set[str]) -> list[Finding]:
    findings: list[Finding] = []
    source_paths = sorted(
        path
        for path in changed_paths
        if (repo / path).is_file()
        and (Path(path).suffix.lower() in SOURCE_SUFFIXES or path.endswith("go.mod"))
        and (
            path.startswith("claw-control/")
            or path.startswith("controller/workbench_")
            or path.startswith("router/workbench_")
            or path.startswith("service/workbenchbridge/")
            or path.startswith("web/src/features/workbench-entry/")
            or path.startswith("web/src/features/agent-store/")
            or path.startswith("web/src/routes/_authenticated/playground/")
            or path.startswith("web/src/routes/_authenticated/agent-store/")
            or path == "web/src/routes/_authenticated/workbench-admin.tsx"
        )
    )
    for path in source_paths:
        if path.endswith("_test.go") or "/test/" in path or "/tests/" in path:
            continue
        text = read_text(repo, path)
        sanctioned_imports = SANCTIONED_CORE_IMPORTS.get(path, set())
        root_imports = [
            match
            for match in ROOT_CORE_IMPORT_RE.finditer(text)
            if match.group("package") not in sanctioned_imports
        ]
        if root_imports or ROOT_CORE_MODULE_RE.search(text):
            findings.append(
                Finding(
                    "CORE_DEPENDENCY",
                    "Claw code imports or requires a new-api core package",
                    path,
                )
            )
        dangerous = (
            DANGEROUS_CORE_SYMBOL_RE.search(text)
            if not path.startswith("claw-control/")
            else None
        )
        if dangerous:
            findings.append(
                Finding(
                    "CORE_DEPENDENCY",
                    f"Claw code references forbidden core symbol {dangerous.group(0)!r}",
                    path,
                )
            )
        if path == "controller/workbench_identity_bridge.go" and re.search(
            r"\bmodel\.(?:DB|Update|Create|Delete|Insert|Add|Remove)[A-Za-z0-9_]*\b",
            text,
        ):
            findings.append(
                Finding(
                    "IDENTITY_BRIDGE_WRITE",
                    "the identity bridge may only read the existing user identity",
                    path,
                )
            )
    return findings


def parse_adr_fields(text: str) -> dict[str, str]:
    fields: dict[str, str] = {}
    for name in ADR_FIELDS:
        match = re.search(rf"(?mi)^\s*{re.escape(name)}\s*:\s*(.+?)\s*$", text)
        if match:
            fields[name] = match.group(1).strip()
    return fields


def validate_adr(repo: Path, adr_path: str, findings: list[Finding]) -> list[str]:
    errors: list[str] = []
    adr_path = normalize(adr_path.strip())
    if not ADR_PATH_RE.fullmatch(adr_path):
        return ["ADR path must match docs/adp-claw-adr-<slug>.md"]
    path = (repo / adr_path).resolve()
    try:
        path.relative_to(repo.resolve())
    except ValueError:
        return ["ADR path escapes the repository"]
    if not path.is_file():
        return ["ADR file does not exist"]

    text = path.read_text(encoding="utf-8", errors="replace")
    fields = parse_adr_fields(text)
    missing = [name for name in ADR_FIELDS if name not in fields]
    if missing:
        errors.append("missing required fields: " + ", ".join(missing))
        return errors
    if fields["Status"].casefold() != "approved":
        errors.append("Status must be approved")
    if len(fields["Approved-By"]) < 3 or fields["Approved-By"].casefold() in {
        "tbd",
        "todo",
        "unknown",
    }:
        errors.append("Approved-By must identify the approving project owner")
    try:
        approval_date = dt.date.fromisoformat(fields["Approval-Date"])
        if approval_date > dt.date.today():
            errors.append("Approval-Date cannot be in the future")
    except ValueError:
        errors.append("Approval-Date must use YYYY-MM-DD")
    if len(fields["Impact"]) < 20:
        errors.append("Impact must contain a substantive impact description")
    if len(fields["Rollback"]) < 20:
        errors.append("Rollback must contain a substantive rollback procedure")

    approved_codes = {
        code.strip() for code in re.split(r"[,\s]+", fields["Constraint-IDs"]) if code.strip()
    }
    required_codes = {finding.code for finding in findings}
    omitted = sorted(required_codes - approved_codes)
    if omitted:
        errors.append("Constraint-IDs does not cover: " + ", ".join(omitted))
    return errors


def evaluate(
    repo: Path,
    baseline: str = BASELINE,
    environ: dict[str, str] | None = None,
) -> GateReport:
    repo = repo.resolve()
    if not has_commit(repo, baseline):
        return GateReport(
            baseline,
            [],
            [],
            0,
            1.0,
            [Finding("BASELINE_UNAVAILABLE", f"baseline commit is unavailable: {baseline}")],
        )

    baseline_paths, changed_paths, numstat = collect_changes(repo, baseline)
    existing_source = sorted(
        path for path in changed_paths if is_source(path) and path in baseline_paths
    )
    existing_changed_lines = sum(
        sum(numstat.get(path, (0, 0))) for path in existing_source
    )

    added_source_lines = 0
    total_source_additions = 0
    for path in changed_paths:
        if not is_source(path):
            continue
        additions = numstat.get(path, (line_count(repo, path), 0))[0]
        total_source_additions += additions
        if path not in baseline_paths:
            added_source_lines += additions
    additive_ratio = (
        added_source_lines / total_source_additions if total_source_additions else 1.0
    )

    findings: list[Finding] = []
    for path in sorted(changed_paths):
        if not is_allowed_path(path):
            findings.append(
                Finding(
                    "PATH_ALLOWLIST",
                    "path is outside the approved Claw extension surface",
                    path,
                )
            )
        if path.startswith(FORBIDDEN_CORE_PREFIXES):
            findings.append(
                Finding("FORBIDDEN_CORE", "new-api core ownership path changed", path)
            )

    for path in existing_source:
        if any(fragment in path.casefold() for fragment in FORBIDDEN_EXISTING_FRAGMENTS):
            findings.append(
                Finding("FORBIDDEN_CORE", "billing/quota/settlement core path changed", path)
            )
    if len(existing_source) > MAX_EXISTING_SOURCE_FILES:
        findings.append(
            Finding(
                "MAX_EXISTING_FILES",
                f"modified {len(existing_source)} upstream-existing source files; maximum is {MAX_EXISTING_SOURCE_FILES}",
            )
        )
    if existing_changed_lines > MAX_EXISTING_SOURCE_CHANGED_LINES:
        findings.append(
            Finding(
                "MAX_EXISTING_LINES",
                f"changed {existing_changed_lines} lines in upstream-existing source files; maximum is {MAX_EXISTING_SOURCE_CHANGED_LINES}",
            )
        )
    if additive_ratio < MIN_ADDITIVE_SOURCE_RATIO:
        findings.append(
            Finding(
                "MIN_ADDITIVE_RATIO",
                f"additive Claw source ratio is {additive_ratio:.2%}; minimum is {MIN_ADDITIVE_SOURCE_RATIO:.0%}",
            )
        )
    findings.extend(dependency_findings(repo, changed_paths))
    findings = sorted(set(findings))

    report = GateReport(
        baseline,
        sorted(changed_paths),
        existing_source,
        existing_changed_lines,
        additive_ratio,
        findings,
    )
    env = os.environ if environ is None else environ
    if not findings or env.get(ADR_OVERRIDE_ENV, "").strip().casefold() != "true":
        return report
    adr_path = env.get(ADR_PATH_ENV, "").strip()
    adr_errors = validate_adr(repo, adr_path, findings)
    if adr_errors:
        report.findings.extend(
            Finding("ADR_INVALID", message, adr_path) for message in adr_errors
        )
        report.findings = sorted(set(report.findings))
        return report
    report.override_accepted = True
    report.override_adr = normalize(adr_path)
    return report


def print_report(report: GateReport) -> None:
    print(f"Claw baseline: {report.baseline}")
    print(
        f"Upstream-existing source files: {len(report.existing_source)}/"
        f"{MAX_EXISTING_SOURCE_FILES}"
    )
    for path in report.existing_source:
        print(f"  {path}")
    print(
        "Upstream-existing changed lines: "
        f"{report.existing_changed_lines}/{MAX_EXISTING_SOURCE_CHANGED_LINES}"
    )
    print(f"Additive Claw source ratio: {report.additive_ratio:.2%}")
    print(f"Changed paths checked against allowlist: {len(report.changed_paths)}")
    if report.override_accepted:
        print(
            f"WARNING: approved ADR override accepted: {report.override_adr}",
            file=sys.stderr,
        )
    elif report.findings:
        for finding in report.findings:
            print(f"ERROR: {finding.render()}", file=sys.stderr)
        print(
            f"An exception is disabled by default. Set {ADR_OVERRIDE_ENV}=true and "
            f"{ADR_PATH_ENV}=docs/adp-claw-adr-<slug>.md only after project-owner approval.",
            file=sys.stderr,
        )


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", type=Path, default=Path.cwd())
    parser.add_argument("--baseline", default=BASELINE)
    args = parser.parse_args(argv)
    report = evaluate(args.repo, args.baseline)
    print_report(report)
    return 0 if report.passed else (2 if any(f.code == "BASELINE_UNAVAILABLE" for f in report.findings) else 1)


if __name__ == "__main__":
    raise SystemExit(main())
