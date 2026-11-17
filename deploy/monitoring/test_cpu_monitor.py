import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("cpu_monitor", Path(__file__).with_name("newapi-cpu-monitor.py"))
monitor = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = monitor
spec.loader.exec_module(monitor)


class CpuMonitorTest(unittest.TestCase):
    def test_strict_threshold_and_repeated_alerts(self):
        state = monitor.AlertState()
        self.assertEqual(state.interval, 300)
        self.assertIsNone(state.observe(75))
        self.assertFalse(state.active)
        self.assertEqual(state.observe(75.01), "alert")
        self.assertEqual(state.interval, 5)
        self.assertEqual(state.observe(90), "alert")
        self.assertEqual(state.observe(80), "alert")

    def test_recovery_needs_ten_consecutive_samples(self):
        state = monitor.AlertState()
        state.observe(90)
        for count in range(1, 10):
            self.assertIsNone(state.observe(75))
            self.assertTrue(state.active)
            self.assertEqual(state.normal_samples, count)
            self.assertEqual(state.interval, 5)
        self.assertEqual(state.observe(74), "recovered")
        self.assertFalse(state.active)
        self.assertEqual(state.interval, 300)
        self.assertEqual(state.normal_samples, 0)
        self.assertEqual(state.observe(80), "alert")

    def test_any_high_sample_resets_recovery(self):
        state = monitor.AlertState(True)
        for _ in range(9):
            state.observe(40)
        self.assertEqual(state.observe(75.01), "alert")
        self.assertEqual(state.normal_samples, 0)
        self.assertIsNone(state.observe(40))
        self.assertTrue(state.active)
        self.assertEqual(state.normal_samples, 1)

    def test_persistence_and_restart_require_new_consecutive_samples(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "state"
            self.assertFalse(monitor.AlertState.load(path).active)
            monitor.AlertState(True, 9).save(path)
            self.assertEqual(path.read_text(), "alert 9")
            state = monitor.AlertState.load(path)
            self.assertTrue(state.active)
            self.assertEqual(state.normal_samples, 0)
            monitor.AlertState().save(path)
            self.assertEqual(path.read_text(), "ok")

    def test_guest_time_not_double_counted(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "stat"
            path.write_text("cpu 100 0 0 100 0 0 0 0 0 0\n")
            before = monitor.read_cpu(path)
            path.write_text("cpu 160 0 0 140 0 0 0 0 60 0\n")
            self.assertEqual(monitor.cpu_usage(before, monitor.read_cpu(path)), 60)
        with self.assertRaises(ValueError):
            monitor.cpu_usage((200, 100), (200, 100))

    @patch.object(monitor.urllib.request, "urlopen")
    def test_delivery_acknowledgement(self, urlopen):
        response = urlopen.return_value.__enter__.return_value
        for ack, expected in ((b'{"code": 0}', True), (b'{"code": 1}', False),
                              (b'<html>error</html>', False), (b'[]', False)):
            with self.subTest(ack=ack):
                response.read.return_value = ack
                self.assertEqual(monitor.send_feishu("https://example.invalid/test", "test"), expected)
        request = urlopen.call_args.args[0]
        self.assertEqual(json.loads(request.data)["content"]["text"], "test")

    @patch.object(monitor.urllib.request, "urlopen", side_effect=TimeoutError)
    def test_failed_delivery_does_not_change_alert_state(self, _):
        state = monitor.AlertState()
        self.assertEqual(state.observe(90), "alert")
        self.assertFalse(monitor.send_feishu("https://example.invalid/test", "test"))
        self.assertTrue(state.active)
        self.assertEqual(state.observe(90), "alert")

    def test_critical_and_unknown_services_are_not_given_stop_commands(self):
        for name in ("new-api-seedance-postgres-1", "new-api-seedance-new-api-1", "redis", "unknown"):
            with self.subTest(name=name):
                self.assertNotIn("docker stop", monitor.service_advice(name))
        self.assertIn("docker stop tokenops-worker", monitor.service_advice("tokenops-worker"))

    @patch.object(monitor, "diagnostics", return_value=(8, "containers", "processes", "advice"))
    def test_messages_preserve_robot_keyword_and_rules(self, _):
        text = monitor.notification_text("alert", 80, "2026-10-21T12:00:00+08:00")
        self.assertIn("服务器磁盘告警", text)
        self.assertIn("每 5 秒", text)
        self.assertIn("连续 10 次", text)
        self.assertIn("不会自动停止", text)
        self.assertIn("每 5 分钟", monitor.notification_text("recovered", 40, "now"))

    def test_diagnostics_unavailable_still_allows_host_alert(self):
        with patch.object(monitor.subprocess, "run", side_effect=monitor.subprocess.TimeoutExpired("docker", 3)):
            text = monitor.notification_text("alert", 90, "now")
        self.assertIn("90.00%", text)
        self.assertIn("未读取到容器", text)
        self.assertNotIn("docker stop", text)

    @patch.object(monitor, "Notifier")
    @patch.object(monitor, "read_cpu", side_effect=[(0, 0), (100, 20), (200, 20), (300, 40)])
    @patch.object(monitor.threading, "Event")
    def test_sampling_loop_does_not_wait_five_minutes_during_alert(self, event, _, notifier):
        # Two high samples; terminate after the second scheduling decision.
        event.return_value.is_set.side_effect = [False, False, True]
        event.return_value.wait.return_value = False
        with tempfile.TemporaryDirectory() as root, \
                patch.object(sys, "argv", ["monitor"]), \
                patch.dict(monitor.os.environ, {"WEBHOOK": "https://example.invalid/test", "STATE": str(Path(root) / "state")}), \
                patch.dict(sys.modules, {"fcntl": type("Fcntl", (), {"flock": staticmethod(lambda *_: None), "LOCK_EX": 1, "LOCK_NB": 2})}), \
                patch.object(monitor.signal, "signal"):
            self.assertEqual(monitor.main(), 0)
        self.assertEqual([call.args[0] for call in event.return_value.wait.call_args_list], [5, 0, 5, 0])
        self.assertEqual(notifier.return_value.submit.call_count, 2)

    @patch.object(monitor, "notification_text", return_value="test")
    @patch.object(monitor, "send_feishu", return_value=True)
    @patch.object(monitor, "read_cpu", side_effect=[(100, 0), (200, 50)])
    @patch.object(monitor.threading, "Event")
    def test_delivery_test_never_reads_or_changes_state(self, event, *_):
        event.return_value.wait.return_value = False
        with patch.object(sys, "argv", ["monitor", "--test"]), \
                patch.dict(monitor.os.environ, {"WEBHOOK": "https://example.invalid/test"}), \
                patch.object(monitor.AlertState, "load") as load, \
                patch.object(monitor.AlertState, "save") as save, \
                patch.object(monitor.signal, "signal"):
            self.assertEqual(monitor.main(), 0)
        load.assert_not_called()
        save.assert_not_called()


if __name__ == "__main__":
    unittest.main()
