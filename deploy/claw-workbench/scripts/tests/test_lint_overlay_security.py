from __future__ import annotations

import importlib.util
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "lint_overlay_security.py"
SPEC = importlib.util.spec_from_file_location("lint_overlay_security", SCRIPT)
assert SPEC and SPEC.loader
lint = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = lint
SPEC.loader.exec_module(lint)


class OverlaySecurityLintTests(unittest.TestCase):
    def scan(self, path: str, content: str) -> set[str]:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            target = root / path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(content, encoding="utf-8")
            return {finding.code for finding in lint.scan(root)}

    def test_accepts_secret_files_non_root_and_internal_networks(self) -> None:
        codes = self.scan(
            "compose.yml",
            """services:
  app:
    user: 10001:10001
    environment:
      PASSWORD_FILE: /run/secrets/password
    networks: [internal]
""",
        )
        self.assertEqual(set(), codes)

    def test_rejects_shell_sourcing_dotenv(self) -> None:
        for command in ('. "$root/.env"', 'source "$root/.env"; echo ready'):
            with self.subTest(command=command):
                self.assertIn("SOURCE_DOTENV", self.scan("scripts/deploy.sh", command + "\n"))

    def test_rejects_plaintext_and_interpolated_secrets(self) -> None:
        plaintext = self.scan("compose.yml", "services:\n  app:\n    environment:\n      API_KEY: actual-secret-value\n")
        interpolated = self.scan("compose.yml", "services:\n  app:\n    environment:\n      API_KEY: ${API_KEY}\n")
        self.assertIn("PLAINTEXT_SECRET", plaintext)
        self.assertIn("SECRET_INTERPOLATION", interpolated)
        self.assertIn(
            "PLAINTEXT_SECRET",
            self.scan("docker/Dockerfile.app", "FROM alpine\nENV API_KEY=actual-secret-value\nUSER 10001\n"),
        )
        self.assertIn(
            "PLAINTEXT_SECRET",
            self.scan("scripts/start.sh", "API_KEY=actual-secret-value\n"),
        )

    def test_rejects_published_ports_privilege_root_and_host_escape(self) -> None:
        codes = self.scan(
            "compose.yml",
            """services:
  app:
    user: root
    privileged: true
    network_mode: host
    ports: ["8080:8080"]
    volumes: [/var/run/docker.sock:/var/run/docker.sock]
""",
        )
        self.assertTrue(
            {"ROOT_RUNTIME", "PRIVILEGED_CONTAINER", "HOST_NAMESPACE", "PUBLISHED_PORTS", "DOCKER_SOCKET"}.issubset(codes)
        )

    def test_rejects_security_field_and_command_interpolation(self) -> None:
        codes = self.scan(
            "compose.yml",
            "services:\n  app:\n    user: ${RUNTIME_USER}\n    command: $(id)\n",
        )
        self.assertIn("DANGEROUS_INTERPOLATION", codes)
        self.assertIn("COMMAND_SUBSTITUTION", codes)

    def test_rejects_implicit_root_final_image_stage(self) -> None:
        self.assertIn(
            "ROOT_RUNTIME",
            self.scan("docker/Dockerfile.app", "FROM alpine:3.20\nCMD [\"app\"]\n"),
        )


if __name__ == "__main__":
    unittest.main()
