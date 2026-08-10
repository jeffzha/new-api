"""Minimal RFC 6455 client used only by the live PTY acceptance contract."""

from __future__ import annotations

import base64
import hashlib
import os
import socket
import ssl
import struct
from urllib.parse import urlsplit


class WebSocketContractError(RuntimeError):
    pass


class BoundedWebSocket:
    def __init__(self, sock: socket.socket, *, timeout: int, max_frame_bytes: int = 65536, initial: bytes = b"") -> None:
        self.sock = sock
        self.sock.settimeout(timeout)
        self.max_frame_bytes = max_frame_bytes
        self.closed = False
        self._buffer = bytearray(initial)

    @classmethod
    def connect(cls, url: str, *, origin: str, cookie: str, protocols: list[str], timeout: int):
        parsed = urlsplit(url)
        if parsed.scheme not in {"ws", "wss"} or not parsed.hostname or parsed.username or parsed.password or parsed.fragment:
            raise WebSocketContractError("WebSocket URL is invalid")
        port = parsed.port or (443 if parsed.scheme == "wss" else 80)
        raw = socket.create_connection((parsed.hostname, port), timeout=timeout)
        if parsed.scheme == "wss":
            raw = ssl.create_default_context().wrap_socket(raw, server_hostname=parsed.hostname)
        key = base64.b64encode(os.urandom(16)).decode("ascii")
        target = parsed.path or "/"
        if parsed.query:
            target += "?" + parsed.query
        host = parsed.hostname if parsed.port is None else f"{parsed.hostname}:{parsed.port}"
        lines = [
            f"GET {target} HTTP/1.1", f"Host: {host}", "Upgrade: websocket",
            "Connection: Upgrade", f"Sec-WebSocket-Key: {key}", "Sec-WebSocket-Version: 13",
            f"Origin: {origin}", f"Sec-WebSocket-Protocol: {', '.join(protocols)}",
        ]
        if cookie:
            lines.append(f"Cookie: {cookie}")
        raw.sendall(("\r\n".join(lines) + "\r\n\r\n").encode("ascii"))
        response = b""
        while b"\r\n\r\n" not in response:
            chunk = raw.recv(4096)
            if not chunk or len(response) + len(chunk) > 16384:
                raw.close()
                raise WebSocketContractError("bounded WebSocket handshake failed")
            response += chunk
        head, extra = response.split(b"\r\n\r\n", 1)
        text = head.decode("ascii", "strict")
        rows = text.split("\r\n")
        if not rows[0].startswith("HTTP/1.1 101 "):
            raw.close()
            raise WebSocketContractError("WebSocket handshake was rejected")
        headers = {}
        for row in rows[1:]:
            name, separator, value = row.partition(":")
            if not separator:
                raw.close()
                raise WebSocketContractError("malformed WebSocket handshake header")
            headers[name.lower()] = value.strip()
        expected = base64.b64encode(hashlib.sha1((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode("ascii")).digest()).decode("ascii")
        if headers.get("sec-websocket-accept") != expected or headers.get("sec-websocket-protocol") != protocols[0]:
            raw.close()
            raise WebSocketContractError("WebSocket accept or selected protocol is invalid")
        return cls(raw, timeout=timeout, initial=extra)

    def _read_exact(self, size: int) -> bytes:
        value = bytearray()
        if self._buffer:
            take = min(size, len(self._buffer))
            value.extend(self._buffer[:take])
            del self._buffer[:take]
        while len(value) < size:
            chunk = self.sock.recv(size - len(value))
            if not chunk:
                raise WebSocketContractError("WebSocket closed unexpectedly")
            value.extend(chunk)
        return bytes(value)

    def send(self, opcode: int, payload: bytes) -> None:
        if self.closed or len(payload) > self.max_frame_bytes:
            raise WebSocketContractError("outbound WebSocket frame is invalid")
        first = 0x80 | opcode
        mask = os.urandom(4)
        length = len(payload)
        if length < 126:
            header = bytes((first, 0x80 | length))
        elif length <= 65535:
            header = bytes((first, 0x80 | 126)) + struct.pack("!H", length)
        else:
            header = bytes((first, 0x80 | 127)) + struct.pack("!Q", length)
        masked = bytes(value ^ mask[index % 4] for index, value in enumerate(payload))
        self.sock.sendall(header + mask + masked)

    def recv(self) -> tuple[int, bytes]:
        first, second = self._read_exact(2)
        if not first & 0x80 or first & 0x70 or second & 0x80:
            raise WebSocketContractError("fragmented, reserved, or masked server frame")
        opcode = first & 0x0F
        length = second & 0x7F
        if length == 126:
            length = struct.unpack("!H", self._read_exact(2))[0]
        elif length == 127:
            length = struct.unpack("!Q", self._read_exact(8))[0]
        if length > self.max_frame_bytes:
            raise WebSocketContractError("inbound WebSocket frame exceeds limit")
        payload = self._read_exact(length)
        if opcode == 0x9:
            self.send(0xA, payload)
            return self.recv()
        if opcode == 0x8:
            self.closed = True
        if opcode not in {0x1, 0x2, 0x8, 0xA}:
            raise WebSocketContractError("unsupported WebSocket opcode")
        return opcode, payload

    def close(self) -> None:
        if not self.closed:
            try:
                self.send(0x8, struct.pack("!H", 1000))
            except OSError:
                pass
        self.closed = True
        self.sock.close()
