from __future__ import annotations

import base64
import importlib.util
import os
import stat
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "bootstrap_generated_secrets.py"
SPEC = importlib.util.spec_from_file_location("bootstrap_generated_secrets", SCRIPT)
assert SPEC and SPEC.loader
bootstrap_module = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = bootstrap_module
SPEC.loader.exec_module(bootstrap_module)


class GeneratedSecretBootstrapTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name) / "secrets"

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def test_creates_complete_internal_set_without_external_credentials(self) -> None:
        generated, preserved = bootstrap_module.bootstrap(self.root, owner_uid=None)

        self.assertEqual(len(bootstrap_module.GENERATED_SECRETS), generated)
        self.assertEqual(0, preserved)
        for directory in bootstrap_module.SECRET_DIRECTORIES:
            self.assertTrue((self.root / directory).is_dir())
        expected_paths = {
            item.path for item in bootstrap_module.GENERATED_SECRETS
        }
        actual_paths = {
            path.relative_to(self.root).as_posix()
            for path in self.root.rglob("*")
            if path.is_file()
        }
        self.assertEqual(expected_paths, actual_paths)
        self.assertFalse((self.root / "sandbox/agsx-api-key").exists())
        self.assertFalse((self.root / "files/cos_secret_id").exists())
        self.assertFalse(
            (self.root / "provider/WORKBENCH_PROVIDER_PLACEHOLDER").exists()
        )
        for name in (
            "evidence_master_key",
            "provider_vault_master_key",
            "adp_usage_evidence_key",
            "adp_file_locator_key",
            "adp_workspace_locator_key",
            "adp_connector_token_key",
            "adp_oauth_state_key",
        ):
            decoded = base64.b64decode(
                (self.root / name).read_bytes().strip(), validate=True
            )
            self.assertEqual(32, len(decoded))
        if os.name == "posix":
            for path in self.root.rglob("*"):
                self.assertEqual(0, stat.S_IMODE(path.stat().st_mode) & 0o077)

    def test_is_idempotent_and_never_rotates_existing_values(self) -> None:
        bootstrap_module.bootstrap(self.root, owner_uid=None)
        before = {
            item.path: (self.root / item.path).read_bytes()
            for item in bootstrap_module.GENERATED_SECRETS
        }

        generated, preserved = bootstrap_module.bootstrap(self.root, owner_uid=None)

        self.assertEqual(0, generated)
        self.assertEqual(len(before), preserved)
        self.assertEqual(
            before,
            {name: (self.root / name).read_bytes() for name in before},
        )

    def test_rejects_existing_insecure_secret_instead_of_replacing_it(self) -> None:
        self.root.mkdir(mode=0o700)
        for directory in bootstrap_module.SECRET_DIRECTORIES:
            (self.root / directory).mkdir(mode=0o700)
        target = self.root / "control_db_password"
        target.write_text("existing", encoding="utf-8")
        if os.name == "posix":
            target.chmod(0o644)
            with self.assertRaises(bootstrap_module.BootstrapError):
                bootstrap_module.bootstrap(self.root, owner_uid=None)
        else:
            target.write_text("", encoding="utf-8")
            with self.assertRaises(bootstrap_module.BootstrapError):
                bootstrap_module.bootstrap(self.root, owner_uid=None)

    def test_rejects_symlink_secret_root(self) -> None:
        target = Path(self.temporary.name) / "real"
        target.mkdir(mode=0o700)
        try:
            self.root.symlink_to(target, target_is_directory=True)
        except OSError:
            self.skipTest("symlink creation is unavailable")
        with self.assertRaises(bootstrap_module.BootstrapError):
            bootstrap_module.bootstrap(self.root, owner_uid=None)


if __name__ == "__main__":
    unittest.main()
