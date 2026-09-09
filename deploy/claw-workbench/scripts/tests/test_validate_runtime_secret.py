from __future__ import annotations

import importlib.util
import os
import sys
import tempfile
import unittest
from contextlib import ExitStack
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).resolve().parents[1] / "validate_runtime_secret.py"
SPEC = importlib.util.spec_from_file_location("validate_runtime_secret", SCRIPT)
assert SPEC and SPEC.loader
runtime_secret = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = runtime_secret
SPEC.loader.exec_module(runtime_secret)


class RuntimeSecretValidationTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def write(self, value: bytes, name: str = "secret") -> Path:
        path = self.root / name
        path.write_bytes(value)
        if os.name == "posix":
            path.chmod(0o600)
        return path

    def validate(self, path: Path, minimum_length: int = 8) -> bytes:
        with ExitStack() as stack:
            stack.enter_context(
                mock.patch.object(runtime_secret, "EXPECTED_UID", path.lstat().st_uid)
            )
            if os.name != "posix":
                stack.enter_context(
                    mock.patch.object(runtime_secret.stat, "S_IMODE", return_value=0o600)
                )
            return runtime_secret.validate_runtime_secret(path, minimum_length)

    def test_accepts_private_regular_file_and_trims_only_trailing_line_endings(self) -> None:
        path = self.write(b"billing-secret-value\r\n")
        self.assertEqual(b"billing-secret-value", self.validate(path, 16))

    def test_rejects_short_oversized_and_control_character_values(self) -> None:
        cases = (
            (b"short\r\n", 8),
            (b"x" * (runtime_secret.MAX_SECRET_BYTES + 1), 8),
            (b"valid-prefix\x00suffix", 8),
            (b"valid-prefix\nsuffix", 8),
            (b"valid-prefix\tsuffix", 8),
            (b"valid-prefix\x7fsuffix", 8),
        )
        for index, (value, minimum) in enumerate(cases):
            with self.subTest(index=index):
                path = self.write(value, f"secret-{index}")
                with self.assertRaises(runtime_secret.RuntimeSecretError):
                    self.validate(path, minimum)

    def test_rejects_wrong_owner_and_non_regular_path(self) -> None:
        path = self.write(b"valid-secret-value")
        with mock.patch.object(
            runtime_secret, "EXPECTED_UID", path.lstat().st_uid + 1
        ):
            with self.assertRaises(runtime_secret.RuntimeSecretError):
                runtime_secret.validate_runtime_secret(path, 8)
        with self.assertRaises(runtime_secret.RuntimeSecretError):
            self.validate(self.root, 1)

    @unittest.skipUnless(os.name == "posix", "POSIX mode semantics are required")
    def test_rejects_group_or_world_access_and_missing_owner_read(self) -> None:
        path = self.write(b"valid-secret-value")
        for mode in (0o640, 0o604, 0o200):
            with self.subTest(mode=oct(mode)):
                path.chmod(mode)
                with self.assertRaises(runtime_secret.RuntimeSecretError):
                    self.validate(path, 8)

    def test_rejects_symlink(self) -> None:
        target = self.write(b"valid-secret-value", "target")
        link = self.root / "link"
        try:
            os.symlink(target, link)
        except (OSError, NotImplementedError) as error:
            self.skipTest(f"symlink creation unavailable: {error}")
        with self.assertRaises(runtime_secret.RuntimeSecretError):
            self.validate(link, 8)


if __name__ == "__main__":
    unittest.main()
