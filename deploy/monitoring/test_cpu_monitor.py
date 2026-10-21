import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


class CpuMonitorTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.env = dict(os.environ, PATH=f"{self.bin}:{os.environ['PATH']}",
                        STATE=str(self.root / "state"), ENV_FILE="/nonexistent",
                        WEBHOOK="https://example.invalid/test", FIXTURE=str(self.root),
                        CONTAINER="tokenops", ACK='{"code": 0}', SAMPLE_SECONDS="0")
        self.stub("awk", '''if [[ "${!#}" == /proc/stat ]]; then
  args=("$@")
  if [[ -e "$FIXTURE/read" ]]; then file=after; else file=before; fi
  touch "$FIXTURE/read"
  args[${#args[@]}-1]="$FIXTURE/$file"
  exec /usr/bin/awk "${args[@]}"
fi
exec /usr/bin/awk "$@"
''')
        self.stub("docker", 'printf "%s\\t400%%\\n" "$CONTAINER"\n')
        self.stub("nproc", 'echo 8\n')
        self.stub("ps", 'printf "PID COMMAND %%CPU\\n123 worker 80\\n"\n')
        self.stub("curl", '''while (( $# )); do
  if [[ "$1" == -d ]]; then printf '%s' "$2" > "$FIXTURE/message"; fi
  shift
done
echo "$ACK"
''')

    def stub(self, name, body):
        path = self.bin / name
        path.write_text("#!/usr/bin/env bash\n" + body)
        path.chmod(0o700)

    def run_sample(self, busy=90, guest=0, args=()):
        (self.root / "read").unlink(missing_ok=True)
        (self.root / "before").write_text("cpu 100 0 0 100 0 0 0 0 0 0\n")
        (self.root / "after").write_text(
            f"cpu {100 + busy} 0 0 {200 - busy} 0 0 0 0 {guest} 0\n")
        return subprocess.run(["bash", str(Path(__file__).with_name("newapi-cpu-monitor.sh")), *args],
                              env=self.env, capture_output=True, text=True)

    def test_alert_dedup_and_rearm(self):
        result = self.run_sample()
        self.assertEqual(result.returncode, 0, result.stderr)
        text = json.loads((self.root / "message").read_text())["content"]["text"]
        self.assertIn("docker stop tokenops", text)
        self.assertIn("50.0%", text)
        (self.root / "message").unlink()
        self.assertEqual(self.run_sample().returncode, 0)
        self.assertFalse((self.root / "message").exists())
        self.assertEqual(self.run_sample(75).returncode, 0)
        self.assertEqual((self.root / "state").read_text(), "alert")
        self.assertEqual(self.run_sample(60).returncode, 0)
        self.assertEqual((self.root / "state").read_text(), "ok")
        self.assertEqual(self.run_sample().returncode, 0)
        self.assertTrue((self.root / "message").exists())

    def test_critical_service_has_no_stop_command(self):
        self.env["CONTAINER"] = "new-api-seedance-postgres-1"
        self.assertEqual(self.run_sample().returncode, 0)
        text = json.loads((self.root / "message").read_text())["content"]["text"]
        self.assertNotIn("docker stop", text)

    def test_rejected_or_invalid_ack_does_not_suppress_retry(self):
        for ack in ('{"code": 1}', '<html>error</html>'):
            with self.subTest(ack=ack):
                self.env["ACK"] = ack
                self.assertNotEqual(self.run_sample().returncode, 0)
                self.assertFalse((self.root / "state").exists())

    def test_guest_time_not_double_counted(self):
        result = self.run_sample(60, guest=60)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("cpu=60%", result.stdout)
        self.assertFalse((self.root / "message").exists())

    def test_docker_failure_still_reports_host(self):
        self.stub("docker", "exit 1\n")
        result = self.run_sample()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((self.root / "message").exists())

    def test_delivery_test_does_not_change_state(self):
        self.assertEqual(self.run_sample(30, args=("--test",)).returncode, 0)
        self.assertFalse((self.root / "state").exists())
        text = json.loads((self.root / "message").read_text())["content"]["text"]
        self.assertIn("不是超标告警", text)


if __name__ == "__main__":
    unittest.main()
