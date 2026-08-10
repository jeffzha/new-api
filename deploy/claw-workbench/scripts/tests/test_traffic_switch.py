from __future__ import annotations

import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


SOURCE_ROOT = Path(__file__).resolve().parents[2]


class TrafficSwitchTests(unittest.TestCase):
    def setUp(self) -> None:
        shell = shutil.which("sh")
        if not shell:
            self.skipTest("POSIX sh is unavailable")
        self.shell = shell
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        for directory in ("scripts", "state", "caddy"):
            (self.root / directory).mkdir()
        for name in ("switch-active.sh", "rollback.sh"):
            shutil.copy2(SOURCE_ROOT / "scripts" / name, self.root / "scripts" / name)
        (self.root / "state" / "Caddyfile.active").write_text(
            "reverse_proxy claw-control-blue:8090\nreverse_proxy adp-blue:8000\n",
            encoding="utf-8",
        )
        (self.root / "state" / "active-color").write_text("blue\n", encoding="utf-8")
        (self.root / "caddy" / "Caddyfile.switch.template").write_text(
            "reverse_proxy __CONTROL_UPSTREAM__\nreverse_proxy __ADP_UPSTREAM__\n",
            encoding="utf-8",
        )
        (self.root / "caddy" / "Caddyfile.switch.disabled").write_text(
            ":8080 { respond 404 }\n:8000 { respond 404 }\n",
            encoding="utf-8",
        )
        (self.root / "scripts" / "compose.sh").write_text(
            """#!/bin/sh
set -eu
root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
printf '%s\\n' "$*" >> "$root/state/compose-calls"
case "$*" in
  *"caddy reload"*)
    if [ "${FAKE_FAIL_RELOAD_ONCE:-0}" = 1 ] && [ ! -f "$root/state/reload-failed" ]; then
      : > "$root/state/reload-failed"
      exit 1
    fi
    ;;
  *"127.0.0.1:8080/readyz"*)
    [ "${FAKE_FAIL_POST_HEALTH:-0}" != 1 ] || exit 1
    ;;
esac
exit 0
""",
            encoding="utf-8",
        )

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def run_script(self, name: str, *arguments: str, **extra_env: str) -> subprocess.CompletedProcess[str]:
        environment = os.environ.copy()
        environment.update(extra_env)
        return subprocess.run(
            [self.shell, str(self.root / "scripts" / name), *arguments],
            text=True,
            capture_output=True,
            env=environment,
            timeout=20,
        )

    def assert_clean_transaction_files(self) -> None:
        self.assertFalse((self.root / "state" / ".traffic-switch.lock").exists())
        self.assertFalse((self.root / "state" / "Caddyfile.next").exists())
        self.assertFalse((self.root / "state" / "Caddyfile.before-switch").exists())
        self.assertFalse((self.root / "state" / "Caddyfile.before-rollback").exists())
        self.assertFalse((self.root / "state" / "Caddyfile.disabled.next").exists())

    def test_successful_switch_commits_route_then_active_color(self) -> None:
        result = self.run_script("switch-active.sh", "green")
        self.assertEqual(0, result.returncode, result.stderr)
        active = (self.root / "state" / "Caddyfile.active").read_text(encoding="utf-8")
        self.assertIn("claw-control-green:8090", active)
        self.assertIn("adp-green:8000", active)
        self.assertEqual("green\n", (self.root / "state" / "active-color").read_text(encoding="utf-8"))
        self.assert_clean_transaction_files()

    def test_reload_failure_restores_previous_route_and_color(self) -> None:
        original = (self.root / "state" / "Caddyfile.active").read_text(encoding="utf-8")
        result = self.run_script(
            "switch-active.sh", "green", FAKE_FAIL_RELOAD_ONCE="1"
        )
        self.assertNotEqual(0, result.returncode)
        self.assertEqual(original, (self.root / "state" / "Caddyfile.active").read_text(encoding="utf-8"))
        self.assertEqual("blue\n", (self.root / "state" / "active-color").read_text(encoding="utf-8"))
        self.assert_clean_transaction_files()

    def test_post_switch_health_failure_restores_previous_route(self) -> None:
        original = (self.root / "state" / "Caddyfile.active").read_text(encoding="utf-8")
        result = self.run_script(
            "switch-active.sh", "green", FAKE_FAIL_POST_HEALTH="1"
        )
        self.assertNotEqual(0, result.returncode)
        self.assertEqual(original, (self.root / "state" / "Caddyfile.active").read_text(encoding="utf-8"))
        self.assertEqual("blue\n", (self.root / "state" / "active-color").read_text(encoding="utf-8"))
        self.assert_clean_transaction_files()

    def test_concurrent_switch_is_rejected_without_mutation(self) -> None:
        original = (self.root / "state" / "Caddyfile.active").read_text(encoding="utf-8")
        (self.root / "state" / ".traffic-switch.lock").mkdir()
        result = self.run_script("switch-active.sh", "green")
        self.assertNotEqual(0, result.returncode)
        self.assertIn("already running", result.stderr)
        self.assertEqual(original, (self.root / "state" / "Caddyfile.active").read_text(encoding="utf-8"))

    def test_rollback_reload_failure_restores_live_route(self) -> None:
        original = (self.root / "state" / "Caddyfile.active").read_text(encoding="utf-8")
        result = self.run_script(
            "rollback.sh",
            "--confirm-new-api-disabled",
            FAKE_FAIL_RELOAD_ONCE="1",
        )
        self.assertNotEqual(0, result.returncode)
        self.assertEqual(original, (self.root / "state" / "Caddyfile.active").read_text(encoding="utf-8"))
        self.assert_clean_transaction_files()


if __name__ == "__main__":
    unittest.main()
