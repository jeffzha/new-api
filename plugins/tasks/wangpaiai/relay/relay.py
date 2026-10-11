#!/usr/bin/env python3
"""Local TLS-impersonating relay for the wangpaiai task upstream.

Why this exists
---------------
wangpaiai.com is fronted by a Cloudflare managed challenge. Requests that do
not present a browser TLS/HTTP2 fingerprint (Go's net/http, plain curl) are
answered with the "Just a moment..." interstitial instead of JSON, so the task
plugin cannot talk to the vendor API directly from the gateway container.

This helper accepts plain HTTP from the gateway, replays each request through
curl_cffi with Chrome impersonation, and returns the vendor response unchanged.

Routes
------
  GET  /healthz                 local liveness probe (no upstream call)
  GET  /_fetch?url=<absolute>   download a vendor artifact (host allowlist)
  *    everything else          forwarded to https://wangpaiai.com<path>

The vendor key is never stored here: the caller's Authorization header and
request body pass through untouched.
"""

from __future__ import annotations

import json
import os
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

from curl_cffi import requests as cffi_requests

UPSTREAM = os.environ.get("WP_RELAY_UPSTREAM", "https://wangpaiai.com").rstrip("/")
DEFAULT_HOSTS = "wangpaiai.com,www.wangpaiai.com"
ALLOWED_HOSTS = {
    host.strip().lower()
    for host in os.environ.get("WP_RELAY_ALLOWED_HOSTS", DEFAULT_HOSTS).split(",")
    if host.strip()
}
IMPERSONATE = os.environ.get("WP_RELAY_IMPERSONATE", "chrome")
TIMEOUT = (20, 600)
CHUNK = 65536

# Headers the relay owns or that must not be replayed verbatim.
DROP_REQUEST = {
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
    "content-length",
    "host",
    "accept-encoding",
}
DROP_RESPONSE = {
    "connection",
    "keep-alive",
    "transfer-encoding",
    "content-encoding",
    "content-length",
    "alt-svc",
    "server",
}
PASS_REQUEST = ("authorization", "content-type", "accept", "range")
PASS_RESPONSE = (
    "content-type",
    "content-range",
    "accept-ranges",
    "content-disposition",
    "cache-control",
    "etag",
    "last-modified",
)

_local = threading.local()


def session():
    """One curl_cffi session per worker thread (sessions are not thread safe)."""
    current = getattr(_local, "session", None)
    if current is None:
        current = cffi_requests.Session(impersonate=IMPERSONATE)
        _local.session = current
    return current


def host_allowed(url: str) -> bool:
    host = (urlparse(url).hostname or "").lower()
    if not host:
        return False
    if host in ALLOWED_HOSTS:
        return True
    return any(host.endswith("." + allowed) for allowed in ALLOWED_HOSTS)


class RelayHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "wp-relay/1.0"

    # -- helpers ---------------------------------------------------------
    def _read_body(self) -> bytes | None:
        raw_length = self.headers.get("Content-Length")
        if not raw_length:
            return None
        try:
            length = int(raw_length)
        except ValueError:
            return None
        if length <= 0:
            return None
        return self.rfile.read(length)

    def _json(self, status: int, payload: dict) -> None:
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _forward_headers(self) -> dict:
        headers = {}
        for header in PASS_REQUEST:
            value = self.headers.get(header)
            if value:
                headers[header] = value
        if "authorization" not in headers:
            for key, value in self.headers.items():
                if key.lower() == "authorization":
                    headers["authorization"] = value
                    break
        return headers

    # -- request handling ------------------------------------------------
    def _handle(self, method: str) -> None:
        parsed = urlparse(self.path)
        if parsed.path == "/healthz":
            self._json(200, {"ok": True, "upstream": UPSTREAM})
            return

        if parsed.path == "/_fetch":
            query = parse_qs(parsed.query)
            target = (query.get("url") or [""])[0]
            if not target or not host_allowed(target):
                self._json(400, {"error": "target_not_allowed"})
                return
            self._stream(method, target)
            return

        target = UPSTREAM + self.path
        self._buffered(method, target)

    def _buffered(self, method: str, target: str) -> None:
        body = self._read_body()
        headers = self._forward_headers()
        try:
            response = session().request(
                method,
                target,
                headers=headers,
                data=body,
                timeout=TIMEOUT,
                allow_redirects=False,
            )
        except Exception as exc:  # network / TLS / vendor error
            sys.stdout.write("relay error %s %s: %s\n" % (method, target, exc.__class__.__name__))
            sys.stdout.flush()
            self._json(502, {"error": "upstream_unreachable", "detail": exc.__class__.__name__})
            return

        payload = response.content or b""
        status = response.status_code
        # The vendor answers asynchronous submits with 202. The gateway's task
        # runner treats anything other than 200 as a failed submit, so accept
        # the vendor's 202 on submit paths and normalise it to 200.
        if status == 202 and method == "POST" and "/api/v1/generations" in target:
            status = 200
        self.send_response(status)
        for key, value in response.headers.items():
            if key.lower() in DROP_RESPONSE:
                continue
            self.send_header(key, value)
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        if method != "HEAD":
            self.wfile.write(payload)
        sys.stdout.write("%s %s -> %s (%d bytes)\n" % (method, target, response.status_code, len(payload)))
        sys.stdout.flush()

    def _stream(self, method: str, target: str) -> None:
        headers = self._forward_headers()
        try:
            response = session().get(
                target,
                headers=headers,
                timeout=TIMEOUT,
                allow_redirects=False,
                stream=True,
            )
        except Exception as exc:
            self._json(502, {"error": "artifact_unreachable", "detail": exc.__class__.__name__})
            return

        self.send_response(response.status_code)
        for key, value in response.headers.items():
            if key.lower() in DROP_RESPONSE:
                continue
            self.send_header(key, value)
        self.send_header("Connection", "close")
        self.close_connection = True
        self.end_headers()
        written = 0
        if method != "HEAD":
            for chunk in response.iter_content(chunk_size=CHUNK):
                if not chunk:
                    continue
                self.wfile.write(chunk)
                written += len(chunk)
        sys.stdout.write(
            "%s %s -> %s (streamed %d bytes)\n" % (method, target, response.status_code, written)
        )
        sys.stdout.flush()

    # -- HTTP verbs ------------------------------------------------------
    def do_GET(self) -> None:
        self._handle("GET")

    def do_HEAD(self) -> None:
        self._handle("HEAD")

    def do_POST(self) -> None:
        self._handle("POST")

    def do_PUT(self) -> None:
        self._handle("PUT")

    def do_PATCH(self) -> None:
        self._handle("PATCH")

    def do_DELETE(self) -> None:
        self._handle("DELETE")

    def log_message(self, fmt: str, *args) -> None:  # keep journal output tidy
        sys.stdout.write("%s %s\n" % (self.address_string(), fmt % args))
        sys.stdout.flush()


def main(argv: list[str]) -> int:
    host = os.environ.get("WP_RELAY_BIND", "172.30.250.1")
    port = int(os.environ.get("WP_RELAY_PORT", "8788"))
    if len(argv) >= 3 and argv[1] == "--bind":
        host = argv[2]
    if len(argv) >= 5 and argv[3] == "--port":
        port = int(argv[4])
    server = ThreadingHTTPServer((host, port), RelayHandler)
    sys.stdout.write(
        "wp-relay listening on %s:%d -> %s (impersonate=%s, hosts=%s)\n"
        % (host, port, UPSTREAM, IMPERSONATE, ",".join(sorted(ALLOWED_HOSTS)))
    )
    sys.stdout.flush()
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
