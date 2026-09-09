from __future__ import annotations

import argparse
import importlib.util
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).resolve().parents[1] / "validate_sandbox_config.py"
SPEC = importlib.util.spec_from_file_location("validate_sandbox_config", SCRIPT)
assert SPEC and SPEC.loader
sandbox = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = sandbox
SPEC.loader.exec_module(sandbox)


class SandboxConfigTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def args(self, **overrides: object) -> argparse.Namespace:
        values: dict[str, object] = {
            "enabled": "true",
            "provider": "tencent_agsx",
            "region": "ap-guangzhou",
            "domain": "ap-guangzhou.tencentags.com",
            "control_endpoint": "ags.tencentcloudapi.com",
            "tool_id": "tool-1",
            "tool_name": "",
            "network_mode": "SANDBOX",
            "auth_mode": "TOKEN",
            "secret_dir": self.root,
        }
        values.update(overrides)
        return argparse.Namespace(**values)

    def write_secrets(self) -> None:
        values = {
            "agsx-api-key": b"ark_test-value-01",
            "cam-secret-id": b"secret-id-value",
            "cam-secret-key": b"secret-key-value",
            "client-token-hmac-key": b"x" * 32,
        }
        for name, value in values.items():
            path = self.root / name
            path.write_bytes(value)
            if os.name == "posix":
                path.chmod(0o400)

    def test_disabled_accepts_empty_directory(self) -> None:
        sandbox.validate(self.args(enabled="false", tool_id=""))

    def test_enabled_accepts_exact_fail_closed_contract(self) -> None:
        self.write_secrets()
        with mock.patch.object(sandbox, "EXPECTED_SECRET_UID", os.geteuid() if os.name == "posix" else 10001):
            sandbox.validate(self.args())

    def test_rejects_public_network_or_wrong_domain(self) -> None:
        self.write_secrets()
        with mock.patch.object(sandbox, "EXPECTED_SECRET_UID", os.geteuid() if os.name == "posix" else 10001):
            with self.assertRaises(sandbox.SandboxConfigError):
                sandbox.validate(self.args(network_mode="PUBLIC"))
            with self.assertRaises(sandbox.SandboxConfigError):
                sandbox.validate(self.args(domain="example.com"))

    def test_rejects_missing_or_weak_secrets(self) -> None:
        self.write_secrets()
        target = self.root / "client-token-hmac-key"
        if os.name == "posix":
            target.chmod(0o600)
        target.write_bytes(b"short")
        if os.name == "posix":
            target.chmod(0o400)
        with mock.patch.object(sandbox, "EXPECTED_SECRET_UID", os.geteuid() if os.name == "posix" else 10001):
            with self.assertRaises(sandbox.SandboxConfigError):
                sandbox.validate(self.args())

    def test_rejects_symlink_secret(self) -> None:
        if not hasattr(os, "symlink"):
            self.skipTest("symlink is unavailable")
        self.write_secrets()
        target = self.root / "outside"
        target.write_bytes(b"ark_outside")
        (self.root / "agsx-api-key").unlink()
        try:
            os.symlink(target, self.root / "agsx-api-key")
        except OSError as error:
            self.skipTest(f"symlink creation unavailable: {error}")
        with mock.patch.object(sandbox, "EXPECTED_SECRET_UID", os.geteuid() if os.name == "posix" else 10001):
            with self.assertRaises(sandbox.SandboxConfigError):
                sandbox.validate(self.args())


if __name__ == "__main__":
    unittest.main()
