from __future__ import annotations

import importlib.util
import sys
import unittest
from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "normalize_compose_color.py"
SPEC = importlib.util.spec_from_file_location("normalize_compose_color", SCRIPT)
assert SPEC and SPEC.loader
normalizer = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = normalizer
SPEC.loader.exec_module(normalizer)


def rendered(service: str, image: str) -> str:
    return f"""name: claw-workbench
services:
  database:
    image: postgres:16
  {service}:
    build:
      context: ../..
    environment:
      RELEASE_POLICY: immutable
    image: {image}
    networks:
      backend: null
networks:
  backend:
    name: claw-workbench_backend
"""


def rendered_adp(color: str, image: str, dependency: str = "database") -> str:
    return f"""name: claw-workbench
services:
  {dependency}:
    image: postgres:16
  adp-{color}:
    depends_on:
      {dependency}:
        condition: service_healthy
    environment:
      WORKBENCH_INSTANCE_ID: adp-{color}
    image: {image}
    volumes:
      - type: volume
        source: adp_{color}_logs
        target: /app/logs
volumes:
  adp_{color}_logs:
    name: claw-workbench_adp_{color}_logs
"""


class ComposeColorNormalizationTests(unittest.TestCase):
    def test_blue_and_green_differing_only_by_service_and_image_normalize_equal(self) -> None:
        blue = normalizer.normalize_rendered_compose(
            rendered("claw-control-blue", "registry/control:blue-deadbeefdead"),
            "claw-control-blue",
        )
        green = normalizer.normalize_rendered_compose(
            rendered("claw-control-green", "registry/control:green-acdeacdeacde"),
            "claw-control-green",
        )
        self.assertEqual(blue, green)
        self.assertIn("  database:\n    image: postgres:16\n", blue)
        self.assertIn("    image: __CLAW_CONTROL_COLOR_IMAGE__\n", blue)

    def test_only_service_header_and_direct_image_are_normalized(self) -> None:
        source = rendered(
            "claw-control-blue", "registry/control:blue-deadbeefdead"
        ).replace("RELEASE_POLICY: immutable", "COLOR_LABEL: claw-control-blue")
        normalized = normalizer.normalize_rendered_compose(
            source, "claw-control-blue"
        )
        self.assertIn("COLOR_LABEL: claw-control-blue", normalized)
        self.assertIn("image: postgres:16", normalized)
        self.assertNotIn("registry/control:blue-deadbeefdead", normalized)

    def test_missing_or_duplicate_direct_image_fails(self) -> None:
        missing = rendered("claw-control-blue", "image").replace(
            "    image: image\n", ""
        )
        duplicate = rendered("claw-control-blue", "first").replace(
            "    image: first\n", "    image: first\n    image: second\n"
        )
        for source in (missing, duplicate):
            with self.subTest(source=source), self.assertRaises(
                normalizer.ComposeNormalizationError
            ):
                normalizer.normalize_rendered_compose(source, "claw-control-blue")

    def test_missing_or_duplicate_target_service_fails(self) -> None:
        missing = rendered("other-service", "registry/other:deadbeefdead")
        duplicate = rendered(
            "claw-control-blue", "registry/control:blue-deadbeefdead"
        ).replace(
            "\nnetworks:\n",
            "\n  claw-control-blue:\n    image: registry/control:duplicate-deadbeefdead\nnetworks:\n",
            1,
        )
        for source in (missing, duplicate):
            with self.subTest(source=source), self.assertRaises(
                normalizer.ComposeNormalizationError
            ):
                normalizer.normalize_rendered_compose(source, "claw-control-blue")

    def test_adp_colors_normalize_only_instance_image_and_log_volume(self) -> None:
        blue = normalizer.normalize_rendered_compose(
            rendered_adp("blue", "registry/adp@sha256:" + "1" * 64),
            "adp-blue",
        )
        green = normalizer.normalize_rendered_compose(
            rendered_adp("green", "registry/adp@sha256:" + "2" * 64),
            "adp-green",
        )
        self.assertEqual(blue, green)
        self.assertIn("WORKBENCH_INSTANCE_ID: adp-color", blue)
        self.assertIn("source: adp_color_logs", blue)
        self.assertIn("name: claw-workbench_adp_color_logs", blue)

    def test_adp_parity_keeps_dependency_differences_visible(self) -> None:
        blue = normalizer.normalize_rendered_compose(
            rendered_adp("blue", "registry/adp@sha256:" + "1" * 64),
            "adp-blue",
        )
        green = normalizer.normalize_rendered_compose(
            rendered_adp(
                "green",
                "registry/adp@sha256:" + "2" * 64,
                dependency="adp-blue",
            ),
            "adp-green",
        )
        self.assertNotEqual(blue, green)

    def test_adp_top_level_volume_order_is_canonicalized_after_color_rename(self) -> None:
        blue_source = rendered_adp(
            "blue", "registry/adp@sha256:" + "1" * 64
        ).replace(
            "volumes:\n  adp_blue_logs:\n    name: claw-workbench_adp_blue_logs\n",
            "volumes:\n  adp_blue_logs:\n    name: claw-workbench_adp_blue_logs\n"
            "  adp_db_data:\n    name: claw-workbench_adp_db_data\n",
        )
        green_source = rendered_adp(
            "green", "registry/adp@sha256:" + "2" * 64
        ).replace(
            "volumes:\n  adp_green_logs:\n    name: claw-workbench_adp_green_logs\n",
            "volumes:\n  adp_db_data:\n    name: claw-workbench_adp_db_data\n"
            "  adp_green_logs:\n    name: claw-workbench_adp_green_logs\n",
        )
        blue = normalizer.normalize_rendered_compose(blue_source, "adp-blue")
        green = normalizer.normalize_rendered_compose(green_source, "adp-green")
        self.assertEqual(blue, green)

    def test_adp_requires_stable_instance_and_dedicated_log_volume(self) -> None:
        source = rendered_adp("blue", "registry/adp@sha256:" + "1" * 64)
        missing_instance = source.replace(
            "      WORKBENCH_INSTANCE_ID: adp-blue\n", ""
        )
        missing_volume = source.replace("adp_blue_logs", "shared_logs")
        for candidate in (missing_instance, missing_volume):
            with self.subTest(candidate=candidate), self.assertRaises(
                normalizer.ComposeNormalizationError
            ):
                normalizer.normalize_rendered_compose(candidate, "adp-blue")


if __name__ == "__main__":
    unittest.main()
