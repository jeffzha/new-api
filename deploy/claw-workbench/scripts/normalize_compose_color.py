#!/usr/bin/env python3
"""Normalize the explicitly allowed workbench Blue/Green render differences."""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path


MAX_RENDER_BYTES = 16 * 1024 * 1024
CONTROL_SERVICES = frozenset({"claw-control-blue", "claw-control-green"})
ADP_SERVICES = frozenset({"adp-blue", "adp-green"})
SERVICE_NAMES = CONTROL_SERVICES | ADP_SERVICES
SERVICE_HEADER_RE = re.compile(r"^  ([A-Za-z0-9_.-]+):\s*(?:\r?\n)?$")
DIRECT_IMAGE_RE = re.compile(r"^    image:\s*\S.*?(\r?\n)?$")


class ComposeNormalizationError(ValueError):
    pass


def _line_ending(line: str) -> str:
    if line.endswith("\r\n"):
        return "\r\n"
    if line.endswith("\n"):
        return "\n"
    return ""


def normalize_rendered_compose(text: str, service_name: str) -> str:
    if service_name not in SERVICE_NAMES:
        raise ComposeNormalizationError("unsupported workbench color service")

    lines = text.splitlines(keepends=True)
    services_indexes = [
        index for index, line in enumerate(lines) if line.rstrip("\r\n") == "services:"
    ]
    if len(services_indexes) != 1:
        raise ComposeNormalizationError("rendered Compose must contain exactly one services mapping")

    services_index = services_indexes[0]
    service_indexes: list[int] = []
    for index in range(services_index + 1, len(lines)):
        line = lines[index]
        if line.strip() and not line.startswith((" ", "\t", "#")):
            break
        match = SERVICE_HEADER_RE.fullmatch(line)
        if match and match.group(1) == service_name:
            service_indexes.append(index)
    if len(service_indexes) != 1:
        raise ComposeNormalizationError(
            f"rendered Compose must contain exactly one {service_name} service"
        )

    service_index = service_indexes[0]
    service_end = len(lines)
    for index in range(service_index + 1, len(lines)):
        line = lines[index]
        if line.strip() and not line.startswith((" ", "\t", "#")):
            service_end = index
            break
        if SERVICE_HEADER_RE.fullmatch(line):
            service_end = index
            break

    image_indexes = [
        index
        for index in range(service_index + 1, service_end)
        if DIRECT_IMAGE_RE.fullmatch(lines[index])
    ]
    if len(image_indexes) != 1:
        raise ComposeNormalizationError(
            f"{service_name} must contain exactly one direct image field"
        )

    normalized = list(lines)
    service_family = "claw-control" if service_name in CONTROL_SERVICES else "adp"
    normalized[service_index] = f"  {service_family}-color:" + _line_ending(
        lines[service_index]
    )
    image_index = image_indexes[0]
    normalized[image_index] = (
        f"    image: __{service_family.upper().replace('-', '_')}_COLOR_IMAGE__"
        + _line_ending(lines[image_index])
    )

    if service_name in ADP_SERVICES:
        color = service_name.removeprefix("adp-")
        expected_instance = f"WORKBENCH_INSTANCE_ID: adp-{color}"
        expected_volume = f"adp_{color}_logs"
        instance_count = 0
        volume_count = 0
        for index, line in enumerate(normalized):
            content = line.strip()
            if content == expected_instance:
                prefix = line[: len(line) - len(line.lstrip())]
                normalized[index] = (
                    prefix
                    + "WORKBENCH_INSTANCE_ID: adp-color"
                    + _line_ending(line)
                )
                instance_count += 1
                continue
            if content in {
                f"source: {expected_volume}",
                f"{expected_volume}:",
                f"name: claw-workbench_{expected_volume}",
            }:
                prefix = line[: len(line) - len(line.lstrip())]
                replacement = content.replace(expected_volume, "adp_color_logs")
                normalized[index] = prefix + replacement + _line_ending(line)
                volume_count += 1
        if instance_count != 1:
            raise ComposeNormalizationError(
                f"{service_name} must contain exactly one stable WORKBENCH_INSTANCE_ID"
            )
        if volume_count < 2:
            raise ComposeNormalizationError(
                f"{service_name} must contain its dedicated log volume"
            )

        volumes_indexes = [
            index
            for index, line in enumerate(normalized)
            if line.rstrip("\r\n") == "volumes:"
        ]
        if len(volumes_indexes) != 1:
            raise ComposeNormalizationError(
                "rendered Compose must contain exactly one top-level volumes mapping"
            )
        volumes_index = volumes_indexes[0]
        volumes_end = len(normalized)
        for index in range(volumes_index + 1, len(normalized)):
            line = normalized[index]
            if line.strip() and not line.startswith((" ", "\t", "#")):
                volumes_end = index
                break
        volume_headers = [
            index
            for index in range(volumes_index + 1, volumes_end)
            if SERVICE_HEADER_RE.fullmatch(normalized[index])
        ]
        if not volume_headers:
            raise ComposeNormalizationError(
                "rendered Compose top-level volumes mapping is empty"
            )
        volume_blocks: list[list[str]] = []
        for position, start in enumerate(volume_headers):
            end = volume_headers[position + 1] if position + 1 < len(volume_headers) else volumes_end
            volume_blocks.append(normalized[start:end])
        volume_blocks.sort(key=lambda block: block[0].rstrip("\r\n"))
        normalized[volumes_index + 1 : volumes_end] = [
            line for block in volume_blocks for line in block
        ]
    return "".join(normalized)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--service", required=True, choices=sorted(SERVICE_NAMES))
    args = parser.parse_args(argv)

    try:
        raw = args.input.read_bytes()
        if not raw or len(raw) > MAX_RENDER_BYTES:
            raise ComposeNormalizationError(
                f"rendered Compose must contain between 1 and {MAX_RENDER_BYTES} bytes"
            )
        text = raw.decode("utf-8")
        normalized = normalize_rendered_compose(text, args.service)
    except (OSError, UnicodeError, ComposeNormalizationError) as error:
        print(f"compose color normalization: {error}", file=sys.stderr)
        return 1

    sys.stdout.write(normalized)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
