from __future__ import annotations

import json
import os
import stat
import sys
import tempfile
import unittest
from datetime import datetime, timezone
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[1]
if str(SCRIPTS) not in sys.path:
    sys.path.insert(0, str(SCRIPTS))

import backup_manifest  # noqa: E402


class BackupManifestTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.release_path = self.root / "release-manifest.json"
        self.release = {
            "new_api_revision": "1" * 40,
            "claw_control_revision": "1" * 40,
            "adp_revision": "2" * 40,
            "new_api_image_digest": "sha256:" + "3" * 64,
            "claw_control_image_digest": "sha256:" + "4" * 64,
            "adp_image_digest": "sha256:" + "5" * 64,
            "config_sha256": "6" * 64,
            "control_migration": "0020_app_migration_lineage",
            "adp_migration": "schema-sha256:" + "7" * 64,
            "caddy_version": "2.10.2",
            "active_color": "green",
            "provider_region": "ap-guangzhou",
            "collected_at": "2026-08-10T01:02:03Z",
        }
        self.write_release()
        self.clock = lambda: datetime(2026, 8, 10, 1, 3, tzinfo=timezone.utc)

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def write_release(self) -> None:
        self.release_path.write_text(
            json.dumps(self.release, sort_keys=True) + "\n", encoding="utf-8"
        )
        os.chmod(self.release_path, 0o600)

    def test_renders_exact_release_provenance_without_config_values(self) -> None:
        release, digest = backup_manifest.load_release_manifest(
            self.release_path, clock=self.clock
        )
        output = self.root / "manifest.txt"
        backup_manifest.write_backup_manifest(
            output, release, digest, "20260810T010300Z"
        )
        lines = output.read_text(encoding="ascii").splitlines()
        self.assertEqual("backup_created_at=20260810T010300Z", lines[0])
        self.assertEqual(f"release_manifest_sha256={digest}", lines[1])
        self.assertIn("release.active_color=green", lines)
        self.assertIn("release.adp_revision=" + "2" * 40, lines)
        self.assertNotIn("NEW_API_INTERNAL_UPSTREAM", "\n".join(lines))
        if os.name == "posix":
            self.assertEqual(0o600, stat.S_IMODE(output.stat().st_mode))

    def test_rejects_stale_release_manifest(self) -> None:
        self.release["collected_at"] = "2026-08-09T01:02:03Z"
        self.write_release()
        with self.assertRaisesRegex(backup_manifest.BackupManifestError, "stale"):
            backup_manifest.load_release_manifest(self.release_path, clock=self.clock)

    def test_rejects_unknown_release_field(self) -> None:
        self.release["secret"] = "must-not-be-accepted"
        self.write_release()
        with self.assertRaisesRegex(backup_manifest.BackupManifestError, "incomplete or unknown"):
            backup_manifest.load_release_manifest(self.release_path, clock=self.clock)

    def test_rejects_overwriting_existing_output(self) -> None:
        release, digest = backup_manifest.load_release_manifest(
            self.release_path, clock=self.clock
        )
        output = self.root / "manifest.txt"
        output.write_text("existing\n", encoding="ascii")
        with self.assertRaisesRegex(backup_manifest.BackupManifestError, "already exists"):
            backup_manifest.write_backup_manifest(
                output, release, digest, "20260810T010300Z"
            )

    @unittest.skipUnless(os.name == "posix", "POSIX file mode enforcement")
    def test_rejects_group_readable_release_manifest(self) -> None:
        os.chmod(self.release_path, 0o640)
        with self.assertRaisesRegex(backup_manifest.BackupManifestError, "group or other"):
            backup_manifest.load_release_manifest(self.release_path, clock=self.clock)


if __name__ == "__main__":
    unittest.main()
