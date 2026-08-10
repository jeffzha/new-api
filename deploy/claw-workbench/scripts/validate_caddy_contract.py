#!/usr/bin/env python3
"""Validate the security-sensitive Caddy routing contract without credentials."""

from __future__ import annotations

import argparse
import sys
from pathlib import Path


class CaddyContractError(ValueError):
    pass


def _block(text: str, opening: str) -> str:
    lines = text.splitlines()
    starts = [index for index, line in enumerate(lines) if line.strip() == opening]
    if len(starts) != 1:
        raise CaddyContractError(f"expected exactly one {opening} block")
    start = starts[0]
    depth = 0
    captured: list[str] = []
    for line in lines[start:]:
        captured.append(line)
        depth += line.count("{") - line.count("}")
        if depth == 0:
            return "\n".join(captured)
    raise CaddyContractError(f"unterminated {opening} block")


def _require(text: str, token: str, label: str) -> None:
    if token not in text:
        raise CaddyContractError(f"missing {label}")


def validate(root: Path) -> None:
    public = (root / "caddy" / "Caddyfile.public.snippet").read_text(
        encoding="utf-8"
    )
    switch = (root / "caddy" / "Caddyfile.switch.template").read_text(
        encoding="utf-8"
    )
    disabled = (root / "caddy" / "Caddyfile.switch.disabled").read_text(
        encoding="utf-8"
    )

    if "/internal/metrics" in public:
        raise CaddyContractError("internal metrics must not be public")
    for header in (
        "X-Workbench-Service",
        "X-Workbench-Timestamp",
        "X-Workbench-Nonce",
        "X-Workbench-Signature",
        "X-Workbench-Principal",
        "X-Workbench-Customer",
        "X-Workbench-Auth-Timestamp",
        "X-Workbench-Auth-Signature",
    ):
        _require(public, f"request_header -{header}", f"edge removal of {header}")

    internal = _block(public, "handle @workbenchInternal {")
    _require(internal, "respond 404", "public internal-API denial")

    admin = _block(public, "handle @workbenchAdmin {")
    generic = _block(public, "handle @workbench {")
    if public.index("handle @workbenchAdmin {") > public.index("handle @workbench {"):
        raise CaddyContractError("admin route must precede the generic ADP route")
    _require(admin, "reverse_proxy claw-control-active:8080", "admin control upstream")
    if "forward_auth" in admin:
        raise CaddyContractError("admin static route must not use browser forward_auth")

    _require(generic, "forward_auth claw-control-active:8080", "browser forward_auth")
    _require(generic, "uri strip_prefix /workbench", "ADP prefix stripping")
    _require(generic, "reverse_proxy adp-chat-client:8000", "private ADP upstream")
    _require(generic, "flush_interval -1", "immediate SSE flushing")
    _require(generic, 'header_up Cookie "claw_', "control-cookie removal before ADP")
    for cleanup in (
        'header_up Cookie "^;',
        r'header_up Cookie ";\\s*;',
        r'header_up Cookie ";\\s*$',
    ):
        _require(generic, cleanup, "cookie delimiter cleanup")

    sso = _block(public, "handle @workbenchSSO {")
    _require(sso, "forward_auth claw-control-active:8080", "SSO forward_auth")
    _require(sso, "reverse_proxy adp-chat-client:8000", "SSO ADP upstream")
    _require(
        sso,
        'header_up Cookie "claw_sso_binding={http.request.cookie.claw_sso_binding}"',
        "SSO-only browser binding projection",
    )
    _require(sso, "flush_interval -1", "SSO immediate flushing")

    _require(
        public,
        "request_header @newApiWorkbenchTicket Cookie \"claw_",
        "control-cookie removal before new-api ticket endpoints",
    )
    _require(switch, "https://workbench-control.internal:8443", "internal control TLS")
    _require(
        switch,
        "tls /run/secrets/workbench_control_tls_cert /run/secrets/workbench_control_tls_key",
        "internal control certificate",
    )
    _require(switch, "reverse_proxy __CONTROL_UPSTREAM__", "switchable control upstream")
    _require(switch, "reverse_proxy __ADP_UPSTREAM__", "switchable ADP upstream")
    _require(switch, "flush_interval -1", "switch-level SSE flushing")
    internal_tls = _block(switch, "https://workbench-control.internal:8443 {")
    retention_path = "/api/internal/workbench/retention/intents"
    if internal_tls.count(retention_path) != 1:
        raise CaddyContractError("retention intent must have one exact private route")
    retention = _block(internal_tls, "handle @retentionIntent {")
    _require(internal_tls, "method POST", "retention POST-only matcher")
    _require(retention, "reverse_proxy __ADP_UPSTREAM__", "retention ADP upstream")
    if internal_tls.index("handle @retentionIntent {") > internal_tls.index("handle {"):
        raise CaddyContractError("retention intent must precede the control catch-all")

    for listener in (":8080 {", "https://workbench-control.internal:8443 {", ":8000 {"):
        block = _block(disabled, listener)
        _require(block, "respond 404", f"disabled response for {listener}")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1])
    args = parser.parse_args(argv)
    try:
        validate(args.root.resolve())
    except (OSError, UnicodeError, CaddyContractError) as error:
        print(f"Caddy contract: {error}", file=sys.stderr)
        return 1
    print("Caddy contract: private routes, cookie/header stripping, TLS and SSE flush passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
