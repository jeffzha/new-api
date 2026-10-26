"""Exercise Caddy admission and failover without touching business routes."""

import argparse
import hashlib
import hmac
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import queue
import secrets
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def fetch(url, **kwargs):
    with urllib.request.urlopen(urllib.request.Request(url, **kwargs), timeout=3) as response:
        return response.read()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", required=True)
    args = parser.parse_args()
    actions = queue.Queue()
    entered = threading.Condition()
    state = {"probes": 0, "charges": 0}
    servers = []
    container = "new-api-isolated-health-" + secrets.token_hex(6)

    def handler(name):
        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                status, body = 200, name.encode()
                if self.path == "/healthz":
                    body = b'{"status":"ready"}'
                    if name == "primary":
                        with entered:
                            state["probes"] += 1
                            entered.notify_all()
                        try:
                            status, body = actions.get(timeout=4)
                        except queue.Empty:
                            status = 503
                self.send_response(status)
                self.end_headers()
                self.wfile.write(body)

            def do_POST(self):
                state["charges"] += 1
                self.close_connection = True
                self.connection.shutdown(socket.SHUT_RDWR)
        return Handler

    for name in ("primary", "zero_weight", "reserve"):
        server = ThreadingHTTPServer(("127.0.0.1", 0), handler(name))
        server.daemon_threads = True
        servers.append(server)
        threading.Thread(target=server.serve_forever, daemon=True).start()
    primary = f"127.0.0.1:{servers[0].server_port}"
    port, admin = free_port(), free_port()
    secret = secrets.token_hex(32)
    cookie = hmac.new(secret.encode(), primary.encode(), hashlib.sha256).hexdigest()
    headers = {"Cookie": "isolated_slot=" + cookie}
    upstreams = " ".join(f"127.0.0.1:{server.server_port}" for server in servers)
    config = f"""{{
    admin 127.0.0.1:{admin}
    persist_config off
    auto_https off
    metrics
}}
http://127.0.0.1:{port} {{
    reverse_proxy {upstreams} {{
        lb_policy cookie isolated_slot {secret} {{
            fallback weighted_round_robin 99 0 1
        }}
        health_uri /healthz
        health_body `"status":"ready"`
        health_interval 200ms
        health_timeout 5s
        health_status 200
        health_fails 3
        health_passes 2
        lb_try_duration 1s
        lb_try_interval 100ms
    }}
}}
"""
    base = f"http://127.0.0.1:{port}"

    def healthy():
        return fetch(base, headers=headers) == b"primary"

    def probe(status=200, body=b'{"status":"ready"}'):
        with entered:
            before = state["probes"]
        actions.put((status, body))
        with entered:
            assert entered.wait_for(lambda: state["probes"] > before, timeout=2), "health timer stalled"

    created = False
    try:
        with tempfile.TemporaryDirectory(prefix="new-api-health-") as temp:
            path = Path(temp) / "Caddyfile"
            path.write_text(config)
            subprocess.run([
                "docker", "run", "-d", "--name", container, "--network", "host",
                "--cpus", "0.25", "--memory", "128m", "--read-only",
                "--mount", f"type=bind,source={path},target=/check.Caddyfile,readonly",
                "--entrypoint", "caddy", args.image, "run",
                "--config", "/check.Caddyfile", "--adapter", "caddyfile",
            ], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
            created = True
            with entered:
                assert entered.wait_for(lambda: state["probes"] > 0, timeout=10), "Caddy did not start"
            assert fetch(base, headers=headers) == b"primary"
            probe(503)
            assert healthy(), "one failure removed a healthy backend"
            probe(503)
            assert healthy(), "two failures removed a healthy backend"
            probe(503)
            assert not healthy(), "three failures did not remove the backend"
            assert fetch(base, headers=headers) == b"reserve", "sticky client did not fail over"
            assert fetch(base) == b"reserve", "zero-weight backend was selected"
            probe()
            assert not healthy(), "one pass restored the backend too early"
            probe()
            assert healthy(), "two passes did not restore the backend"
            assert fetch(base, headers=headers) == b"primary"
            for _ in range(3):
                probe(200, b'{"status":"starting"}')
            assert not healthy(), "HTTP 200 with wrong body passed readiness"
            probe()
            probe()
            assert healthy()
            try:
                fetch(base + "/charge", headers=headers, method="POST", data=b"{}")
                raise AssertionError("connection failure unexpectedly succeeded")
            except urllib.error.HTTPError as error:
                assert error.code == 502
            assert state["charges"] == 1, "an accepted POST was replayed"
            print(json.dumps({
                "failures_before_removal": 3,
                "passes_after_removal_before_recovery": 2,
                "response_body_checked": True,
                "sticky_failed_backend_uses_reserve": True,
                "zero_weight_backend_not_selected": True,
                "accepted_post_not_replayed": True,
                "business_routes_changed": False,
            }))
    finally:
        if created:
            subprocess.run(["docker", "rm", "-f", container], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        for server in servers:
            server.shutdown()
            server.server_close()


if __name__ == "__main__":
    main()
