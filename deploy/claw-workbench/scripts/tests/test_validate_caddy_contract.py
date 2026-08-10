from __future__ import annotations

import importlib.util
import shutil
import sys
import tempfile
import unittest
from pathlib import Path


SOURCE_ROOT = Path(__file__).resolve().parents[2]
SCRIPT = SOURCE_ROOT / "scripts" / "validate_caddy_contract.py"
SPEC = importlib.util.spec_from_file_location("validate_caddy_contract", SCRIPT)
assert SPEC and SPEC.loader
validator = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = validator
SPEC.loader.exec_module(validator)


class CaddyContractTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        shutil.copytree(SOURCE_ROOT / "caddy", self.root / "caddy")

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def mutate(self, name: str, old: str, new: str) -> None:
        path = self.root / "caddy" / name
        text = path.read_text(encoding="utf-8")
        self.assertIn(old, text)
        path.write_text(text.replace(old, new, 1), encoding="utf-8")

    def test_checked_in_caddy_contract_is_complete(self) -> None:
        validator.validate(self.root)

    def test_rejects_missing_control_cookie_strip(self) -> None:
        self.mutate(
            "Caddyfile.public.snippet",
            'header_up Cookie "claw_[^=;\\\\s]+=[^;]*(;\\\\s*)?" ""',
            'header_up Cookie "unrelated_[^=;\\\\s]+=[^;]*" ""',
        )
        with self.assertRaisesRegex(validator.CaddyContractError, "control-cookie"):
            validator.validate(self.root)

    def test_rejects_missing_sse_flush_and_public_internal_route(self) -> None:
        self.mutate("Caddyfile.switch.template", "flush_interval -1", "flush_interval 1s")
        with self.assertRaisesRegex(validator.CaddyContractError, "SSE"):
            validator.validate(self.root)

        shutil.copy2(SOURCE_ROOT / "caddy" / "Caddyfile.switch.template", self.root / "caddy" / "Caddyfile.switch.template")
        self.mutate("Caddyfile.public.snippet", "respond 404", "respond 200")
        with self.assertRaisesRegex(validator.CaddyContractError, "internal-API"):
            validator.validate(self.root)

    def test_rejects_sso_cookie_overprojection_and_non_atomic_disabled_gate(self) -> None:
        self.mutate(
            "Caddyfile.public.snippet",
            'header_up Cookie "claw_sso_binding={http.request.cookie.claw_sso_binding}"',
            "header_up Cookie {http.request.header.Cookie}",
        )
        with self.assertRaisesRegex(validator.CaddyContractError, "SSO-only"):
            validator.validate(self.root)

        shutil.copy2(SOURCE_ROOT / "caddy" / "Caddyfile.public.snippet", self.root / "caddy" / "Caddyfile.public.snippet")
        self.mutate("Caddyfile.switch.disabled", ":8000 {\n\trespond 404", ":8000 {\n\treverse_proxy adp-blue:8000")
        with self.assertRaisesRegex(validator.CaddyContractError, "disabled response"):
            validator.validate(self.root)

    def test_rejects_missing_or_broad_retention_internal_route(self) -> None:
        self.mutate(
            "Caddyfile.switch.template",
            "/api/internal/workbench/retention/intents",
            "/api/internal/workbench/*",
        )
        with self.assertRaisesRegex(validator.CaddyContractError, "retention intent"):
            validator.validate(self.root)


if __name__ == "__main__":
    unittest.main()
