#!/usr/bin/env python3
"""Host CPU monitoring only: no container mutations or automatic remediation."""

import argparse
from dataclasses import dataclass
from datetime import datetime
import json
import os
from pathlib import Path
import queue
import signal
import socket
import subprocess
import threading
import time
import urllib.request


THRESHOLD = 75
SAMPLE_SECONDS = 5
NORMAL_INTERVAL = 300
RECOVERY_SAMPLES = 10


@dataclass
class AlertState:
    active: bool = False
    normal_samples: int = 0

    @property
    def interval(self):
        return SAMPLE_SECONDS if self.active else NORMAL_INTERVAL

    def observe(self, usage):
        if usage > THRESHOLD:
            self.active = True
            self.normal_samples = 0
            return "alert"
        if self.active:
            self.normal_samples += 1
            if self.normal_samples >= RECOVERY_SAMPLES:
                self.active = False
                self.normal_samples = 0
                return "recovered"
        return None

    @classmethod
    def load(cls, path):
        try:
            value = path.read_text().split()
        except FileNotFoundError:
            return cls()
        # Recovery requires consecutive samples from this process, not an old run.
        return cls(active=bool(value and value[0] == "alert"))

    def save(self, path):
        temporary = path.with_suffix(".tmp")
        temporary.write_text(f"alert {self.normal_samples}" if self.active else "ok")
        temporary.replace(path)


def read_cpu(path=Path("/proc/stat")):
    with path.open() as source:
        fields = source.readline().split()
    if not fields or fields[0] != "cpu" or len(fields) < 9:
        raise ValueError("invalid aggregate CPU counters")
    # guest/guest_nice are already included in user/nice.
    values = [int(value) for value in fields[1:9]]
    return sum(values), values[3] + values[4]


def cpu_usage(before, after):
    total = after[0] - before[0]
    idle = after[1] - before[1]
    if total <= 0 or idle < 0 or idle > total:
        raise ValueError("invalid CPU counter delta")
    return 100.0 * (total - idle) / total


def service_advice(name):
    advice = (
        "先用 docker stats --no-stream 核查实时占用。数据库高占用：检查慢查询、连接数和批处理；"
        "网关高占用：检查请求量、日志及任务轮询，限制后台作业并发，持续饱和再评估扩容。"
        "不要直接停止数据库、Redis、网关、代理或未知服务。"
    )
    if name in {"tokenops", "tokenops-worker", "tokenops-cn-collector-1"}:
        advice += (
            f"\n最高占用为运营平台组件。负责人确认可以暂停后，人工执行：docker stop {name}"
            f"\n恢复命令：docker start {name}"
            "\n影响：对应运营页面、后台任务或采集中断；恢复后检查补采和账务同步。"
        )
    return advice


def diagnostics():
    cores = os.cpu_count() or 1
    services = []
    try:
        result = subprocess.run(
            ["docker", "stats", "--no-stream", "--format", "{{.Name}}\t{{.CPUPerc}}"],
            capture_output=True, text=True, timeout=3, check=True,
        )
        for line in result.stdout.splitlines():
            name, percent = line.split("\t", 1)
            services.append((float(percent.rstrip("%")), name))
        services.sort(reverse=True)
    except (OSError, ValueError, subprocess.SubprocessError):
        pass
    top_name = services[0][1] if services else "未读取到容器"
    details = "\n".join(
        f"{name}：Docker {percent:.1f}%，约整机 {percent / cores:.1f}%"
        for percent, name in services[:5]
    ) or "未读取到容器占用，请检查 Docker 状态"
    try:
        result = subprocess.run(
            ["ps", "-eo", "pid,comm,pcpu", "--sort=-pcpu"],
            capture_output=True, text=True, timeout=2, check=True,
        )
        processes = "\n".join(result.stdout.splitlines()[:6])
    except (OSError, subprocess.SubprocessError):
        processes = "未读取到进程占用"
    return cores, details, processes, service_advice(top_name)


def notification_text(kind, usage, sampled_at):
    titles = {
        "alert": "【服务器 CPU 超标】[new-api]",
        "recovered": "【服务器 CPU 已恢复】[new-api]",
        "test": "【CPU 告警通路测试】[new-api] 不是超标告警，不改变告警状态",
    }
    mode = (
        "告警模式：每 5 秒检查，仍超过 75% 就继续发送；连续 10 次不超过 75% 才恢复。"
        if kind == "alert" else
        "正常模式：每 5 分钟检查；严格超过 75% 才触发告警。"
    )
    cores, details, processes, advice = diagnostics()
    return (
        f"{titles[kind]}\n"
        "告警通道：服务器磁盘告警（同一机器人新增 CPU 监控）\n"
        f"服务器：{socket.gethostname()}\n采样结束时间：{sampled_at}\n"
        f"整机 CPU：{usage:.2f}%（阈值 >75%，{cores} 核，采样 5 秒）\n"
        f"{mode}\n中途再次超标，连续正常计数清零。\n\n"
        "容器占用排名（Docker 100% 约等于一个核，不是整机 100%；以下为发送时辅助采样）：\n"
        f"{details}\n\n进程参考（CPU 为进程存活期均值，不含命令参数）：\n{processes}\n\n"
        f"处理建议：\n{advice}\n\n只告警，不会自动停止或重启服务。"
    )


def send_feishu(webhook, text):
    payload = json.dumps({"msg_type": "text", "content": {"text": text}}).encode()
    request = urllib.request.Request(
        webhook, data=payload, headers={"Content-Type": "application/json"}, method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=3) as response:
            acknowledgement = json.loads(response.read(65536))
        if not isinstance(acknowledgement, dict) or acknowledgement.get("code") != 0:
            raise ValueError("Feishu rejected notification")
    except Exception as error:
        # HTTP error messages may contain the secret webhook URL.
        print(f"CPU notification failed: {type(error).__name__}", flush=True)
        return False
    print("CPU notification sent", flush=True)
    return True


class Notifier:
    def __init__(self, webhook):
        self.webhook = webhook
        self.pending = queue.Queue(maxsize=1)
        self.worker = threading.Thread(target=self.run, daemon=True)
        self.worker.start()

    def submit(self, kind, usage, sampled_at):
        event = (kind, usage, sampled_at)
        try:
            self.pending.put_nowait(event)
        except queue.Full:
            # Do not accumulate stale alerts during a slow or unavailable network.
            try:
                self.pending.get_nowait()
            except queue.Empty:
                pass
            self.pending.put_nowait(event)
            print("CPU notification backlog: kept latest sample", flush=True)

    def run(self):
        while True:
            kind, usage, sampled_at = self.pending.get()
            try:
                send_feishu(self.webhook, notification_text(kind, usage, sampled_at))
            except Exception as error:
                print(f"CPU notification worker failed: {type(error).__name__}", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--test", action="store_true", help="Send a live sample without changing alert state")
    args = parser.parse_args()
    webhook = os.environ.get("WEBHOOK")
    if not webhook:
        parser.error("WEBHOOK is not configured")
    stop = threading.Event()
    signal.signal(signal.SIGTERM, lambda *_: stop.set())
    signal.signal(signal.SIGINT, lambda *_: stop.set())
    if args.test:
        before = read_cpu()
        if stop.wait(SAMPLE_SECONDS):
            return 0
        usage = cpu_usage(before, read_cpu())
        print(f"CPU test sample: cpu={usage:.2f}%", flush=True)
        return 0 if send_feishu(webhook, notification_text("test", usage, datetime.now().astimezone().isoformat())) else 1

    import fcntl

    path = Path(os.environ.get("STATE", "/var/lib/newapi-cpu-monitor/state"))
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.with_suffix(".lock").open("w") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            print("CPU monitor already running", flush=True)
            return 0
        state = AlertState.load(path)
        notifier = Notifier(webhook)
        while not stop.is_set():
            before = read_cpu()
            if stop.wait(SAMPLE_SECONDS):
                break
            usage = cpu_usage(before, read_cpu())
            sampled_at = datetime.now().astimezone().isoformat()
            sampled_end = time.monotonic()
            kind = state.observe(usage)
            state.save(path)
            print(
                f"CPU sample: cpu={usage:.2f}% active={state.active} "
                f"normal_samples={state.normal_samples}/{RECOVERY_SAMPLES} next={state.interval}s",
                flush=True,
            )
            if kind:
                notifier.submit(kind, usage, sampled_at)
            delay = max(0, sampled_end + state.interval - SAMPLE_SECONDS - time.monotonic())
            if stop.wait(delay):
                break
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
