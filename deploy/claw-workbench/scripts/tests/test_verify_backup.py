from __future__ import annotations

import hashlib
import importlib.util
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "verify_backup.py"
SPEC = importlib.util.spec_from_file_location("verify_backup", SCRIPT)
assert SPEC and SPEC.loader
verifier = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = verifier
SPEC.loader.exec_module(verifier)


class BackupVerificationTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name) / "backup"
        self.root.mkdir()
        for name in sorted(verifier.REQUIRED_FILES):
            (self.root / name).write_bytes(("contents:" + name).encode())
        self.write_checksums()

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def write_checksums(self) -> None:
        lines = []
        for name in sorted(verifier.REQUIRED_FILES):
            digest = hashlib.sha256((self.root / name).read_bytes()).hexdigest()
            lines.append(f"{digest}  {name}")
        (self.root / "SHA256SUMS").write_text("\n".join(lines) + "\n", encoding="ascii")

    def test_accepts_exact_regular_files_and_checksums(self) -> None:
        declared = verifier.verify_backup(self.root)
        self.assertEqual(verifier.REQUIRED_FILES, set(declared))

    def test_accepts_and_verifies_optional_dr_manifest_pair(self) -> None:
        manifest = self.root / "dr-manifest.json"
        manifest.write_text('{"schema_version":1}\n', encoding="utf-8")
        digest = hashlib.sha256(manifest.read_bytes()).hexdigest()
        (self.root / "DR_MANIFEST.sha256").write_text(
            f"{digest}  dr-manifest.json\n", encoding="ascii"
        )
        verifier.verify_backup(self.root)

        manifest.write_text('{"schema_version":2}\n', encoding="utf-8")
        with self.assertRaisesRegex(verifier.BackupVerificationError, "DR manifest"):
            verifier.verify_backup(self.root)

    def test_rejects_checksum_mismatch_missing_and_unexpected_files(self) -> None:
        (self.root / "control-db.dump").write_bytes(b"changed")
        with self.assertRaisesRegex(verifier.BackupVerificationError, "checksum mismatch"):
            verifier.verify_backup(self.root)

        (self.root / "control-db.dump").write_bytes(b"contents:control-db.dump")
        (self.root / "unexpected").write_bytes(b"unexpected")
        with self.assertRaisesRegex(verifier.BackupVerificationError, "missing or unexpected"):
            verifier.verify_backup(self.root)

    def test_rejects_path_traversal_and_duplicate_checksum_entries(self) -> None:
        checksum = self.root / "SHA256SUMS"
        checksum.write_text("0" * 64 + "  ../secret\n", encoding="ascii")
        with self.assertRaisesRegex(verifier.BackupVerificationError, "malformed"):
            verifier.verify_backup(self.root)

        self.write_checksums()
        checksum.write_text(checksum.read_text(encoding="ascii") * 2, encoding="ascii")
        with self.assertRaisesRegex(verifier.BackupVerificationError, "duplicated"):
            verifier.verify_backup(self.root)

    @unittest.skipIf(not hasattr(Path, "symlink_to"), "symlinks unsupported")
    def test_rejects_symlinked_backup_entry(self) -> None:
        target = self.root.parent / "outside.dump"
        target.write_bytes(b"contents:control-db.dump")
        (self.root / "control-db.dump").unlink()
        try:
            (self.root / "control-db.dump").symlink_to(target)
        except OSError as error:
            self.skipTest(f"symlink creation unavailable: {error}")
        with self.assertRaisesRegex(verifier.BackupVerificationError, "regular non-symlink"):
            verifier.verify_backup(self.root)


if __name__ == "__main__":
    unittest.main()
