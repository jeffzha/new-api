from __future__ import annotations

import importlib.util
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "check_claw_constraints.py"
SPEC = importlib.util.spec_from_file_location("check_claw_constraints", SCRIPT)
assert SPEC and SPEC.loader
gate = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = gate
SPEC.loader.exec_module(gate)


class RepositoryFixture:
    def __init__(self) -> None:
        self._temporary = tempfile.TemporaryDirectory()
        self.root = Path(self._temporary.name)
        self.git("init", "-q")
        self.git("config", "user.name", "Constraint Test")
        self.git("config", "user.email", "constraint@example.invalid")

    def close(self) -> None:
        self._temporary.cleanup()

    def git(self, *args: str) -> str:
        result = subprocess.run(
            ["git", "-C", str(self.root), *args],
            check=True,
            text=True,
            encoding="utf-8",
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        return result.stdout.strip()

    def write(self, path: str, content: str) -> None:
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(content, encoding="utf-8")

    def baseline(self, files: dict[str, str] | None = None) -> str:
        for path, content in (files or {"README.md": "baseline\n"}).items():
            self.write(path, content)
        self.git("add", ".")
        self.git("commit", "-q", "-m", "baseline")
        return self.git("rev-parse", "HEAD")


class ClawConstraintTests(unittest.TestCase):
    def setUp(self) -> None:
        self.repo = RepositoryFixture()

    def tearDown(self) -> None:
        self.repo.close()

    @staticmethod
    def codes(report: object) -> set[str]:
        return {finding.code for finding in report.findings}

    def test_isolated_additive_component_is_allowed(self) -> None:
        baseline = self.repo.baseline()
        self.repo.write(
            "claw-control/internal/customer/service.go",
            "package customer\n",
        )

        report = gate.evaluate(self.repo.root, baseline, {})

        self.assertTrue(report.passed)
        self.assertEqual([], report.findings)

    def test_normal_new_api_business_path_is_denied(self) -> None:
        baseline = self.repo.baseline()
        self.repo.write("model/claw_customer.go", "package model\n")

        report = gate.evaluate(self.repo.root, baseline, {})

        self.assertFalse(report.passed)
        self.assertIn("PATH_ALLOWLIST", self.codes(report))
        self.assertIn("FORBIDDEN_CORE", self.codes(report))

    def test_allowed_bridge_path_cannot_import_relay_core(self) -> None:
        baseline = self.repo.baseline()
        self.repo.write(
            "service/workbenchbridge/bad.go",
            'package workbenchbridge\nimport "github.com/QuantumNous/new-api/relay"\n',
        )

        report = gate.evaluate(self.repo.root, baseline, {})

        self.assertFalse(report.passed)
        self.assertIn("CORE_DEPENDENCY", self.codes(report))

    def test_identity_bridge_user_read_exception_does_not_allow_writes(self) -> None:
        baseline = self.repo.baseline()
        self.repo.write(
            "controller/workbench_identity_bridge.go",
            'package controller\nimport "github.com/QuantumNous/new-api/model"\n'
            "func read() { _, _ = model.GetUserById(1, false) }\n",
        )
        allowed = gate.evaluate(self.repo.root, baseline, {})
        self.assertTrue(allowed.passed)

        self.repo.write(
            "controller/workbench_identity_bridge.go",
            'package controller\nimport "github.com/QuantumNous/new-api/model"\n'
            "func write() { model.UpdateUser(nil, false) }\n",
        )
        denied = gate.evaluate(self.repo.root, baseline, {})
        self.assertIn("IDENTITY_BRIDGE_WRITE", self.codes(denied))

    def test_original_existing_file_budget_is_still_enforced(self) -> None:
        files = {
            f"controller/workbench_{index}.go": "package controller\n"
            for index in range(gate.MAX_EXISTING_SOURCE_FILES + 1)
        }
        baseline = self.repo.baseline(files)
        for path in files:
            self.repo.write(path, files[path] + "// changed\n")

        report = gate.evaluate(self.repo.root, baseline, {})

        self.assertIn("MAX_EXISTING_FILES", self.codes(report))

    def test_exact_super_admin_sidebar_entry_is_an_approved_surface(self) -> None:
        baseline = self.repo.baseline(
            {"web/default/src/hooks/use-sidebar-data.ts": "export const items = []\n"}
        )
        self.repo.write(
            "web/default/src/hooks/use-sidebar-data.ts",
            "export const items = [{ path: '/workbench-admin' }]\n",
        )

        report = gate.evaluate(self.repo.root, baseline, {})

        self.assertNotIn("PATH_ALLOWLIST", self.codes(report))

    def test_agent_store_additive_frontend_surface_is_allowed(self) -> None:
        baseline = self.repo.baseline()
        self.repo.write(
            "web/default/src/features/agent-store/index.tsx",
            "export function AgentStore() { return null }\n",
        )
        self.repo.write(
            "web/default/src/routes/_authenticated/agent-store/index.tsx",
            "export const route = '/agent-store'\n",
        )

        report = gate.evaluate(self.repo.root, baseline, {})

        self.assertNotIn("PATH_ALLOWLIST", self.codes(report))

    def test_generated_route_tree_does_not_spend_manual_merge_budget(self) -> None:
        route_tree = "\n".join(f"export const route{index} = {index}" for index in range(200))
        baseline = self.repo.baseline(
            {"web/default/src/routeTree.gen.ts": route_tree + "\n"}
        )
        self.repo.write(
            "web/default/src/routeTree.gen.ts",
            route_tree + "\nexport const generated = true\n",
        )

        report = gate.evaluate(self.repo.root, baseline, {})

        self.assertTrue(report.passed)
        self.assertEqual(0, report.existing_changed_lines)

    def test_adr_is_not_considered_without_explicit_switch(self) -> None:
        baseline = self.repo.baseline()
        self.repo.write("model/claw_customer.go", "package model\n")
        self.repo.write(
            "docs/adp-claw-adr-boundary.md",
            self.valid_adr("PATH_ALLOWLIST, FORBIDDEN_CORE"),
        )

        report = gate.evaluate(
            self.repo.root,
            baseline,
            {gate.ADR_PATH_ENV: "docs/adp-claw-adr-boundary.md"},
        )

        self.assertFalse(report.passed)
        self.assertFalse(report.override_accepted)

    def test_one_line_or_incomplete_adr_cannot_bypass_gate(self) -> None:
        baseline = self.repo.baseline()
        self.repo.write("model/claw_customer.go", "package model\n")
        self.repo.write(
            "docs/adp-claw-adr-boundary.md",
            "Approved: yes, this exception is fine.\n",
        )

        report = gate.evaluate(
            self.repo.root,
            baseline,
            {
                gate.ADR_OVERRIDE_ENV: "true",
                gate.ADR_PATH_ENV: "docs/adp-claw-adr-boundary.md",
            },
        )

        self.assertFalse(report.passed)
        self.assertIn("ADR_INVALID", self.codes(report))

    def test_structured_approved_adr_must_cover_and_can_override_findings(self) -> None:
        baseline = self.repo.baseline()
        self.repo.write("model/claw_customer.go", "package model\n")
        adr_path = "docs/adp-claw-adr-boundary.md"
        self.repo.write(adr_path, self.valid_adr("PATH_ALLOWLIST, FORBIDDEN_CORE"))

        report = gate.evaluate(
            self.repo.root,
            baseline,
            {
                gate.ADR_OVERRIDE_ENV: "true",
                gate.ADR_PATH_ENV: adr_path,
            },
        )

        self.assertTrue(report.passed)
        self.assertTrue(report.override_accepted)
        self.assertEqual(adr_path, report.override_adr)

    @staticmethod
    def valid_adr(constraint_ids: str) -> str:
        return f"""# Approved Claw boundary exception

Status: approved
Approved-By: Project Owner
Approval-Date: 2026-08-09
Constraint-IDs: {constraint_ids}
Impact: This exception changes an upstream-owned boundary and raises merge risk.
Rollback: Disable the feature flag and revert every affected boundary file safely.
"""


if __name__ == "__main__":
    unittest.main()
