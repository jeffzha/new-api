import importlib.util
import os
import stat
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "install_provider_secrets.py"
SPEC = importlib.util.spec_from_file_location("install_provider_secrets", SCRIPT)
installer = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(installer)


class ProviderSecretInstallerTest(unittest.TestCase):
    def test_normalizes_customer_code_for_environment_names(self) -> None:
        self.assertEqual("NEXUS_INTERNAL", installer.normalize_customer_code("NEXUS-INTERNAL"))
        self.assertEqual(
            {
                "app_key": "WORKBENCH_PROVIDER_NEXUS_INTERNAL_ADP_APP_KEY",
                "secret_id": "WORKBENCH_PROVIDER_NEXUS_INTERNAL_TENCENT_SECRET_ID",
                "secret_key": "WORKBENCH_PROVIDER_NEXUS_INTERNAL_TENCENT_SECRET_KEY",
            },
            installer.secret_names("nexus-internal"),
        )

    def test_rejects_unsafe_customer_codes_and_secret_values(self) -> None:
        for value in ("", "-nexus", "nexus/other", "nexus internal"):
            with self.subTest(value=value):
                with self.assertRaises(installer.ProviderSecretInstallError):
                    installer.normalize_customer_code(value)
        with self.assertRaises(installer.ProviderSecretInstallError):
            installer.validate_secret("line1\nline2", "secret")
        with self.assertRaises(installer.ProviderSecretInstallError):
            installer.validate_secret("x" * (installer.MAX_SECRET_BYTES + 1), "secret")

    @unittest.skipUnless(os.name == "posix", "POSIX ownership and mode contract")
    def test_installs_exact_values_atomically_and_refuses_overwrite(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            os.chmod(root, 0o700)
            targets, app_fingerprint, credential_fingerprint = installer.install_provider_secrets(
                root,
                "NEXUS-INTERNAL",
                "app-key-value",
                "secret-id-value",
                "secret-key-value",
                owner_uid=os.getuid(),
                owner_gid=os.getgid(),
            )
            self.assertEqual(b"app-key-value", targets["WORKBENCH_PROVIDER_NEXUS_INTERNAL_ADP_APP_KEY"].read_bytes())
            self.assertEqual(0o600, stat.S_IMODE(targets["WORKBENCH_PROVIDER_NEXUS_INTERNAL_ADP_APP_KEY"].stat().st_mode))
            self.assertRegex(app_fingerprint, r"^sha256:[0-9a-f]{64}$")
            self.assertRegex(credential_fingerprint, r"^sha256:[0-9a-f]{64}$")
            with self.assertRaisesRegex(installer.ProviderSecretInstallError, "refusing to overwrite"):
                installer.install_provider_secrets(
                    root,
                    "NEXUS-INTERNAL",
                    "replacement",
                    "replacement",
                    "replacement",
                    owner_uid=os.getuid(),
                    owner_gid=os.getgid(),
                )
            self.assertEqual(b"app-key-value", targets["WORKBENCH_PROVIDER_NEXUS_INTERNAL_ADP_APP_KEY"].read_bytes())


if __name__ == "__main__":
    unittest.main()
