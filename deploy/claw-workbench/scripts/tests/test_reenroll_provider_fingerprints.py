from __future__ import annotations

import unittest
from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "reenroll-provider-fingerprints.sh"
CADDY = Path(__file__).resolve().parents[2] / "caddy" / "Caddyfile.public.snippet"


class ReenrollProviderFingerprintsTests(unittest.TestCase):
    def test_loopback_request_satisfies_emergency_admin_contract(self) -> None:
        script = SCRIPT.read_text(encoding="utf-8")

        self.assertIn('Authorization: Bearer %s', script)
        self.assertNotIn('X-Claw-Actor:', script)
        self.assertIn(r'{\"confirmation\":\"rebind-current-runtime-secrets\"}', script)
        self.assertIn('127.0.0.1 8090', script)
        self.assertNotIn('echo "$token"', script)

    def test_public_edge_blocks_maintenance_path_before_admin_wildcard(self) -> None:
        caddy = CADDY.read_text(encoding="utf-8")

        maintenance = caddy.index("@workbenchFingerprintReenroll path")
        public_admin = caddy.index("@workbenchBootstrapAdmin {")
        self.assertLess(maintenance, public_admin)
        block = caddy[caddy.index("handle @workbenchFingerprintReenroll {"):public_admin]
        self.assertIn("respond 404", block)


if __name__ == "__main__":
    unittest.main()
