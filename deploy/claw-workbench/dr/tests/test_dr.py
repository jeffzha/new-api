from __future__ import annotations

import hashlib
import json
import os
import shutil
import sys
import tempfile
import unittest
import uuid
from pathlib import Path


DR_DIR = Path(__file__).resolve().parents[1]
TEST_DIR = Path(__file__).resolve().parent
sys.path.insert(0, str(DR_DIR))

from dr import main  # noqa: E402
from dr_lib import DRError, load_config, repository_url, sha256_file  # noqa: E402


class DRTest(unittest.TestCase):
    secret_names = (
        "DR_TEST_P_AK", "DR_TEST_P_SK", "DR_TEST_P_PASSWORD",
        "DR_TEST_S_AK", "DR_TEST_S_SK", "DR_TEST_S_PASSWORD",
    )

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.staging = self.root / "staging"
        self.drills = self.root / "drills"
        self.protected = self.root / "production"
        self.protected.mkdir()
        self.config_path = self.root / "dr.json"
        self.previous = {name: os.environ.get(name) for name in self.secret_names}
        for index, name in enumerate(self.secret_names):
            os.environ[name] = f"fake-secret-value-{index}-{uuid.uuid4().hex}"
        python = str(Path(sys.executable).resolve())
        restic = str((TEST_DIR / "fake_restic.py").resolve())
        backup = str((TEST_DIR / "fake_backup.py").resolve())
        validate = str((TEST_DIR / "fake_validate.py").resolve())
        pins = {path: sha256_file(Path(path)) for path in (python, restic, backup, validate)}
        suffix = uuid.uuid4().hex[:12]
        self.config = {
            "version": 1,
            "backup_staging_root": str(self.staging),
            "drill_root": str(self.drills),
            "protected_roots": [str(self.protected)],
            "restic": {
                "command": [python, restic],
                "sha256": {python: pins[python], restic: pins[restic]},
                "expected_version": "restic 0.99.0-fake",
            },
            "backup": {
                "command": [python, backup, "{backup_dir}"],
                "sha256": {python: pins[python], backup: pins[backup]},
                "timeout_seconds": 30,
            },
            "restore_validation_commands": [
                {
                    "command": [python, validate, "--list", "{backup_dir}/control-db.dump"],
                    "sha256": {python: pins[python], validate: pins[validate]},
                    "timeout_seconds": 30,
                },
                {
                    "command": [python, validate, "--list", "{backup_dir}/adp-db.dump"],
                    "sha256": {python: pins[python], validate: pins[validate]},
                    "timeout_seconds": 30,
                },
            ],
            "targets": [
                {
                    "name": "primary", "endpoint": "http://127.0.0.1:19001",
                    "bucket": f"primary-{suffix}", "region": "test-primary",
                    "prefix": "workbench/restic", "access_key_id_ref": "env:DR_TEST_P_AK",
                    "secret_access_key_ref": "env:DR_TEST_P_SK",
                    "repository_password_ref": "env:DR_TEST_P_PASSWORD",
                },
                {
                    "name": "secondary", "endpoint": "http://127.0.0.1:19002",
                    "bucket": f"secondary-{suffix}", "region": "test-secondary",
                    "prefix": "workbench/restic", "access_key_id_ref": "env:DR_TEST_S_AK",
                    "secret_access_key_ref": "env:DR_TEST_S_SK",
                    "repository_password_ref": "env:DR_TEST_S_PASSWORD",
                },
            ],
            "retention": {"daily": 2, "weekly": 2, "monthly": 2, "yearly": 1, "local_verified_backups": 2},
            "objectives": {"rpo_seconds": 3600, "rto_seconds": 300},
        }
        self.write_config()

    def tearDown(self):
        for target in self.config.get("targets", []):
            try:
                digest = hashlib.sha256(repository_url(target).encode()).hexdigest()
                shutil.rmtree(Path(tempfile.gettempdir()) / "claw-fake-restic" / digest, ignore_errors=True)
            except Exception:
                pass
        self.temp.cleanup()
        for name, value in self.previous.items():
            if value is None:
                os.environ.pop(name, None)
            else:
                os.environ[name] = value

    def write_config(self):
        self.config_path.write_text(json.dumps(self.config), encoding="utf-8")

    def call(self, *arguments: str) -> int:
        return main([*arguments, "--config", str(self.config_path), "--allow-http"])

    def test_plan_is_offline_and_does_not_require_secrets(self):
        for name in self.secret_names:
            os.environ.pop(name, None)
        output = self.root / "plan-report"
        self.assertEqual(self.call("plan", "--output", str(output)), 0)
        payload = json.loads((output / "dr-results.json").read_text(encoding="utf-8"))
        self.assertEqual(payload["mode"], "dry-run")
        self.assertEqual(payload["objective_evaluation"]["rpo_status"], "not_exercised")
        self.assertFalse(self.staging.exists())

    def test_two_region_backup_check_and_isolated_restore(self):
        init_output = self.root / "init-report"
        self.assertEqual(self.call("init", "--output", str(init_output), "--write", "--confirm", "INIT_BOTH_REPOSITORIES"), 0)
        backup_output = self.root / "backup-report"
        self.assertEqual(self.call("backup", "--output", str(backup_output), "--write", "--confirm", "WRITE_BOTH_REGIONS"), 0)
        backup_payload = json.loads((backup_output / "dr-results.json").read_text(encoding="utf-8"))
        self.assertTrue(backup_payload["both_repositories_verified"])
        self.assertEqual(backup_payload["objective_evaluation"]["rpo_status"], "snapshot_freshness_within_target_not_continuity_proven")
        backup_dirs = [path for path in self.staging.iterdir() if path.is_dir()]
        self.assertEqual(len(backup_dirs), 1)
        self.assertTrue((backup_dirs[0] / "dr-manifest.json").is_file())
        self.assertTrue((backup_dirs[0] / "DR_MANIFEST.sha256").is_file())

        check_output = self.root / "check-report"
        self.assertEqual(self.call("check", "--output", str(check_output), "--execute", "--confirm", "CHECK_BOTH_REPOSITORIES"), 0)
        retention_output = self.root / "retention-report"
        self.assertEqual(self.call("retention", "--output", str(retention_output), "--write", "--confirm", "APPLY_RETENTION_BOTH"), 0)

        restore_output = self.root / "restore-report"
        self.assertEqual(self.call("restore-drill", "--output", str(restore_output), "--target", "secondary", "--snapshot", "latest", "--restore", "--confirm", "RESTORE_ISOLATED"), 0)
        restore_payload = json.loads((restore_output / "dr-results.json").read_text(encoding="utf-8"))
        self.assertTrue(restore_payload["actual_restore_drill"])
        self.assertEqual(restore_payload["objective_evaluation"]["rto_status"], "partial_artifact_drill_only")
        self.assertFalse(any(self.drills.iterdir()))
        combined = "\n".join(
            path.read_text(encoding="utf-8")
            for report_dir in (init_output, backup_output, check_output, retention_output, restore_output)
            for path in report_dir.iterdir()
            if path.is_file()
        )
        combined += "\n" + (backup_dirs[0] / "dr-manifest.json").read_text(encoding="utf-8")
        combined += "\n" + (backup_dirs[0] / "DR_MANIFEST.sha256").read_text(encoding="utf-8")
        for name in self.secret_names:
            self.assertNotIn(os.environ[name], combined)

    def test_missing_repository_password_blocks_before_source_backup(self):
        os.environ.pop("DR_TEST_S_PASSWORD")
        output = self.root / "missing-report"
        self.assertEqual(self.call("backup", "--output", str(output), "--write", "--confirm", "WRITE_BOTH_REGIONS"), 1)
        payload = json.loads((output / "dr-results.json").read_text(encoding="utf-8"))
        self.assertEqual(payload["results"][0]["status"], "blocker")
        self.assertFalse(self.staging.exists())

    def test_same_region_literal_secret_and_production_overlap_are_rejected(self):
        self.config["targets"][1]["region"] = self.config["targets"][0]["region"]
        self.write_config()
        with self.assertRaises(DRError):
            load_config(self.config_path, allow_http=True)
        self.config["targets"][1]["region"] = "test-secondary"
        self.config["targets"][0]["repository_password_ref"] = "literal-password"
        self.write_config()
        with self.assertRaises(DRError):
            load_config(self.config_path, allow_http=True)
        self.config["targets"][0]["repository_password_ref"] = "env:DR_TEST_P_PASSWORD"
        self.config["drill_root"] = str(self.protected / "restore-here")
        self.write_config()
        with self.assertRaises(DRError):
            load_config(self.config_path, allow_http=True)

    def test_floating_or_changed_dependency_is_a_live_blocker(self):
        restic_script = self.config["restic"]["command"][1]
        self.config["restic"]["sha256"][restic_script] = "0" * 64
        self.write_config()
        output = self.root / "pin-report"
        self.assertEqual(self.call("check", "--output", str(output), "--execute", "--confirm", "CHECK_BOTH_REPOSITORIES"), 1)
        payload = json.loads((output / "dr-results.json").read_text(encoding="utf-8"))
        self.assertEqual(payload["results"][0]["status"], "blocker")
        self.assertIn("hash mismatch", payload["results"][0]["message"])


if __name__ == "__main__":
    unittest.main()
