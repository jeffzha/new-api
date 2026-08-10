from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "validate_integration_config.py"
SPEC = importlib.util.spec_from_file_location("validate_integration_config", SCRIPT)
assert SPEC and SPEC.loader
config = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = config
SPEC.loader.exec_module(config)


class IntegrationConfigValidationTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.allowlist = self.root / "allowlist.json"
        self.providers = self.root / "providers.json"
        self.secrets = self.root / "oauth"
        self.secrets.mkdir()

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def write(self, allowlist: object, providers: object) -> None:
        self.allowlist.write_text(json.dumps(allowlist), encoding="utf-8")
        self.providers.write_text(json.dumps(providers), encoding="utf-8")

    def validate(self) -> None:
        config.validate(self.allowlist, self.providers, self.secrets)

    def test_accepts_empty_fail_closed_metadata(self) -> None:
        self.write({"applications": {}}, {"providers": {}})
        self.validate()

    def test_rejects_enabling_an_empty_application_allowlist(self) -> None:
        self.write({"applications": {}}, {"providers": {}})
        with self.assertRaisesRegex(config.IntegrationConfigError, "cannot be enabled"):
            config.validate(
                self.allowlist, self.providers, self.secrets, enabled=True
            )

    def test_rejects_duplicate_json_keys(self) -> None:
        self.allowlist.write_text(
            '{"applications":{},"applications":{}}', encoding="utf-8"
        )
        self.providers.write_text('{"providers":{}}', encoding="utf-8")
        with self.assertRaisesRegex(config.IntegrationConfigError, "duplicate"):
            self.validate()

    def test_rejects_missing_oauth_provider_for_connector(self) -> None:
        self.write(
            {
                "applications": {
                    "app-1": {
                        "resources": [
                            {
                                "kind": "connector",
                                "id": "mail",
                                "provider_id": "mail-provider",
                                "requires_oauth": True,
                            }
                        ]
                    }
                }
            },
            {"providers": {}},
        )
        with self.assertRaisesRegex(config.IntegrationConfigError, "missing providers"):
            self.validate()

    def test_rejects_private_or_unallowlisted_oauth_endpoint(self) -> None:
        self.write(
            {"applications": {}},
            {
                "providers": {
                    "mail-provider": {
                        "authorization_url": "https://127.0.0.1/authorize",
                        "token_url": "https://oauth.example/token",
                        "client_id": "client",
                        "client_secret_ref": "",
                        "token_auth_method": "none",
                        "allowed_scopes": ["read"],
                        "allowed_hosts": ["oauth.example"],
                    }
                }
            },
        )
        with self.assertRaisesRegex(config.IntegrationConfigError, "authorization_url"):
            self.validate()


if __name__ == "__main__":
    unittest.main()
