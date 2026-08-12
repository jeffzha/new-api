from __future__ import annotations

import base64
import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "validate_secret_keys.py"
SPEC = importlib.util.spec_from_file_location("validate_secret_keys", SCRIPT)
assert SPEC and SPEC.loader
keys = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = keys
SPEC.loader.exec_module(keys)


def encoded(byte: int, size: int = 32) -> str:
    return base64.b64encode(bytes([byte]) * size).decode("ascii")


class SecretKeyValidationTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        values = {
            "evidence_master_key": encoded(1),
            "provider_vault_master_key": encoded(16),
            "adp_usage_evidence_key": encoded(2),
            "adp_file_locator_key": encoded(3),
            "adp_workspace_locator_key": encoded(4),
            "adp_connector_token_key": encoded(12),
            "adp_oauth_state_key": encoded(13),
            "claw_admin_token": encoded(5, 48),
            "new_api_control_hmac": encoded(6, 48),
            "adp_control_hmac": encoded(7, 48),
            "new_api_identity_hmac": encoded(8, 48),
            "adp_session_secret": encoded(9, 48),
            "adp_file_locator_previous_keys.json": json.dumps({"v0": encoded(10)}),
            "adp_workspace_locator_previous_keys.json": json.dumps({"v0": encoded(11)}),
            "adp_connector_token_previous_keys.json": json.dumps({"v0": encoded(14)}),
            "adp_oauth_state_previous_keys.json": json.dumps({"v0": encoded(15)}),
        }
        for name, value in values.items():
            (self.root / name).write_text(value + "\n", encoding="ascii")

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def validate(self) -> None:
        keys.validate(self.root, "v1", "v1", "v1", "v1")

    def test_accepts_independent_active_and_historical_keys(self) -> None:
        self.validate()

    def test_rejects_malformed_previous_key_json_and_duplicate_kids(self) -> None:
        path = self.root / "adp_file_locator_previous_keys.json"
        for content in ("{", f'{{"v0":"{encoded(10)}","v0":"{encoded(12)}"}}'):
            with self.subTest(content=content):
                path.write_text(content, encoding="ascii")
                with self.assertRaises(keys.KeyValidationError):
                    self.validate()

    def test_rejects_active_kid_in_previous_map(self) -> None:
        (self.root / "adp_file_locator_previous_keys.json").write_text(
            json.dumps({"v1": encoded(10)}), encoding="ascii"
        )
        with self.assertRaisesRegex(keys.KeyValidationError, "active key id"):
            self.validate()

    def test_rejects_invalid_kid_base64_size_and_too_many_entries(self) -> None:
        path = self.root / "adp_file_locator_previous_keys.json"
        cases = (
            {"bad kid": encoded(10)},
            {"v0": "not-base64"},
            {"v0": encoded(10, 31)},
            {f"v{index + 2}": encoded((index % 200) + 20) for index in range(33)},
        )
        for value in cases:
            with self.subTest(value=list(value)[:2]):
                path.write_text(json.dumps(value), encoding="ascii")
                with self.assertRaises(keys.KeyValidationError):
                    self.validate()

    def test_rejects_raw_or_decoded_material_reuse_across_purposes(self) -> None:
        locator = (self.root / "adp_file_locator_key").read_text(encoding="ascii").strip()
        (self.root / "new_api_control_hmac").write_text(locator, encoding="ascii")
        with self.assertRaisesRegex(keys.KeyValidationError, "reused"):
            self.validate()

    def test_rejects_more_than_sixteen_integration_previous_keys(self) -> None:
        value = {f"v{index + 2}": encoded((index % 200) + 20) for index in range(17)}
        (self.root / "adp_oauth_state_previous_keys.json").write_text(
            json.dumps(value), encoding="ascii"
        )
        with self.assertRaisesRegex(keys.KeyValidationError, "more than 16"):
            self.validate()


if __name__ == "__main__":
    unittest.main()
