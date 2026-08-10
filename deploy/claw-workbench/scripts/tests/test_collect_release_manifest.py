from __future__ import annotations

import json
import os
import stat
import sys
import tempfile
import unittest
from datetime import datetime, timezone
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[1]
if str(SCRIPTS) not in sys.path:
    sys.path.insert(0, str(SCRIPTS))

import collect_release_manifest as release  # noqa: E402
import strict_dotenv  # noqa: E402


ROOT_REVISION = "1" * 40
ADP_REVISION = "2" * 40
NEW_API_CONTAINER_ID = "a" * 64
CONTROL_CONTAINER_ID = "b" * 64
ADP_CONTAINER_ID = "c" * 64
CONTROL_DB_CONTAINER_ID = "d" * 64
ADP_DB_CONTAINER_ID = "e" * 64
SWITCH_CONTAINER_ID = "f" * 64


class FakeRunner:
    def __init__(self, repository: Path, adp: Path) -> None:
        self.repository = repository
        self.adp = adp
        self.dirty_repository = False
        self.stopped: set[str] = set()
        self.revision_labels = {
            "gateway-production-green": ROOT_REVISION,
            CONTROL_CONTAINER_ID: ROOT_REVISION,
            ADP_CONTAINER_ID: ADP_REVISION,
        }
        self.overlay_revision = ROOT_REVISION
        self.image_references = {
            "gateway-production-green": "registry.local/new-api@sha256:" + "3" * 64,
            CONTROL_CONTAINER_ID: "registry.local/claw-control@sha256:" + "4" * 64,
            ADP_CONTAINER_ID: "registry.local/adp@sha256:" + "5" * 64,
        }
        self.local_image_ids = {
            "gateway-production-green": "sha256:" + "6" * 64,
            CONTROL_CONTAINER_ID: "sha256:" + "7" * 64,
            ADP_CONTAINER_ID: "sha256:" + "8" * 64,
        }
        self.service_ids = {
            "claw-control-green": CONTROL_CONTAINER_ID,
            "adp-green": ADP_CONTAINER_ID,
            "workbench-control-db": CONTROL_DB_CONTAINER_ID,
            "workbench-adp-db": ADP_DB_CONTAINER_ID,
            "workbench-switch": SWITCH_CONTAINER_ID,
        }
        self.control_head = "0015_provider_secret_fingerprints\n"
        self.adp_rows = self._adp_schema_rows()
        self.runtime_region = "ap-guangzhou"

    @staticmethod
    def _adp_schema_rows() -> str:
        rows = [
            chr(31).join((table, "000001", "id", "bigint", "int8", "NO"))
            for table in sorted(release.ADP_REQUIRED_TABLES)
        ]
        return "\n".join(rows) + "\n"

    @staticmethod
    def _service_from_container_id(container_id: str) -> str:
        return {
            CONTROL_CONTAINER_ID: "claw-control-green",
            ADP_CONTAINER_ID: "adp-green",
            CONTROL_DB_CONTAINER_ID: "workbench-control-db",
            ADP_DB_CONTAINER_ID: "workbench-adp-db",
            SWITCH_CONTAINER_ID: "workbench-switch",
        }[container_id]

    def _container(self, target: str) -> dict[str, object]:
        if target == "gateway-production-green":
            config = {
                "Image": self.image_references[target],
                "Labels": {release.OCI_REVISION_LABEL: self.revision_labels[target]},
            }
            return {
                "Id": NEW_API_CONTAINER_ID,
                "Name": "/gateway-production-green-container",
                "State": {
                    "Running": target not in self.stopped,
                    "Health": {"Status": "healthy"},
                },
                "Image": self.local_image_ids[target],
                "Config": config,
                "NetworkSettings": {
                    "Networks": {
                        "new-api-production": {"Aliases": ["gateway-production-green"]}
                    }
                },
            }

        service = self._service_from_container_id(target)
        labels: dict[str, str] = {
            "com.docker.compose.project": "claw-workbench",
            "com.docker.compose.service": service,
        }
        config: dict[str, object] = {"Image": "postgres:16", "Labels": labels}
        image = "sha256:" + "9" * 64
        if target in self.image_references:
            labels[release.OCI_REVISION_LABEL] = self.revision_labels[target]
            config["Image"] = self.image_references[target]
            image = self.local_image_ids[target]
        if target == ADP_CONTAINER_ID:
            labels[release.OVERLAY_REVISION_LABEL] = self.overlay_revision
            config["Env"] = [
                "WORKBENCH_SANDBOX_PROVIDER=tencent_agsx",
                f"WORKBENCH_AGSX_REGION={self.runtime_region}",
                f"WORKBENCH_FILE_COS_REGION={self.runtime_region}",
            ]
        return {
            "Id": target,
            "Name": "/claw-workbench-" + service + "-1",
            "State": {
                "Running": target not in self.stopped,
                "Health": {"Status": "healthy"},
            },
            "Image": image,
            "Config": config,
            "NetworkSettings": {"Networks": {}},
        }

    def run(
        self,
        argv: list[str] | tuple[str, ...],
        *,
        cwd: Path | None = None,
        purpose: str,
    ) -> release.CommandOutput:
        args = list(argv)
        if args[:2] == ["git", "-C"]:
            worktree = Path(args[2])
            if "rev-parse" in args:
                revision = ROOT_REVISION if worktree == self.repository else ADP_REVISION
                return release.CommandOutput(revision + "\n")
            if "status" in args:
                dirty = self.dirty_repository and worktree == self.repository
                return release.CommandOutput(" M tracked.go\n" if dirty else "")
        if args[:2] == ["docker", "inspect"]:
            return release.CommandOutput(json.dumps([self._container(args[2])]))
        if args[:3] == ["docker", "image", "inspect"]:
            local_id = args[3]
            targets = [
                target for target, value in self.local_image_ids.items() if value == local_id
            ]
            references = [self.image_references[target] for target in targets]
            labels = {release.OCI_REVISION_LABEL: self.revision_labels[targets[0]]}
            return release.CommandOutput(
                json.dumps(
                    [
                        {
                            "Id": local_id,
                            "RepoDigests": references,
                            "Config": {"Labels": labels},
                        }
                    ]
                )
            )
        if args[:2] == ["docker", "compose"]:
            if "ps" in args:
                service = args[-1]
                value = self.service_ids[service]
                if isinstance(value, list):
                    return release.CommandOutput("\n".join(value) + "\n")
                return release.CommandOutput(value + "\n")
            if "exec" in args:
                service = args[args.index("exec") + 2]
                if service == "workbench-control-db":
                    return release.CommandOutput(self.control_head)
                if service == "workbench-adp-db":
                    return release.CommandOutput(self.adp_rows)
                if service == "workbench-switch":
                    return release.CommandOutput("v2.10.0 h1:fixture\n")
        raise AssertionError(f"unexpected command for {purpose}: {args}")


class ReleaseManifestCollectorTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        temporary = Path(self.temporary.name)
        self.repository = temporary / "repo"
        self.root = self.repository / "deploy" / "claw-workbench"
        self.adp = temporary / "adp"
        self.adp.mkdir(parents=True)
        (self.root / "state").mkdir(parents=True)
        (self.root / "caddy").mkdir()
        (self.root / "config").mkdir()
        (self.root / "observability").mkdir()

        env = self.root / ".env"
        env.write_text(
            "\n".join(
                (
                    "COMPOSE_PROJECT_NAME=claw-workbench",
                    "CLAW_BUILD_LOCAL_IMAGES=true",
                    "NEW_API_INTERNAL_UPSTREAM=http://gateway-production-green:3000",
                    "NEW_API_INTERNAL_ALLOWED_HOSTS=gateway-production-green",
                    "ADP_SOURCE_DIR=../../../adp",
                    "WORKBENCH_SANDBOX_PROVIDER=tencent_agsx",
                    "WORKBENCH_AGSX_REGION=ap-guangzhou",
                    "WORKBENCH_FILE_COS_REGION=ap-guangzhou",
                )
            )
            + "\n",
            encoding="utf-8",
        )
        os.chmod(env, 0o600)
        (self.root / "compose.yml").write_text("services: {}\n", encoding="utf-8")
        (self.root / "new-api.env.example").write_text(
            "WORKBENCH_ENABLED=false\n", encoding="utf-8"
        )
        (self.root / "state" / "active-color").write_text("green\n", encoding="utf-8")
        (self.root / "state" / "Caddyfile.active").write_text(
            "reverse_proxy claw-control-green:8090\n"
            "reverse_proxy claw-control-green:8090\n"
            "reverse_proxy adp-green:8000\n",
            encoding="utf-8",
        )
        (self.root / "caddy" / "Caddyfile.public.snippet").write_text(
            "handle /workbench/* {}\n", encoding="utf-8"
        )
        (self.root / "config" / "integration-allowlist.json").write_text(
            '{"enabled":false}\n', encoding="utf-8"
        )
        (self.root / "observability" / "prometheus.yml").write_text(
            "scrape_configs: []\n", encoding="utf-8"
        )
        self.runner = FakeRunner(self.repository, self.adp)
        self.clock = lambda: datetime(2026, 8, 10, 1, 2, 3, tzinfo=timezone.utc)

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def collect(self) -> dict[str, str]:
        return release.ReleaseManifestCollector(
            self.root,
            self.runner,
            clock=self.clock,
            env_loader=lambda path: strict_dotenv.parse_dotenv_text(
                path.read_text(encoding="utf-8")
            ),
        ).collect()

    def test_collects_authoritative_manifest_from_live_sources(self) -> None:
        manifest = self.collect()
        self.assertEqual(
            {
                "new_api_revision",
                "claw_control_revision",
                "adp_revision",
                "new_api_image_digest",
                "claw_control_image_digest",
                "adp_image_digest",
                "config_sha256",
                "control_migration",
                "adp_migration",
                "caddy_version",
                "active_color",
                "provider_region",
                "collected_at",
            },
            set(manifest),
        )
        self.assertEqual(ROOT_REVISION, manifest["new_api_revision"])
        self.assertEqual(ROOT_REVISION, manifest["claw_control_revision"])
        self.assertEqual(ADP_REVISION, manifest["adp_revision"])
        self.assertEqual("sha256:" + "3" * 64, manifest["new_api_image_digest"])
        self.assertEqual("sha256:" + "4" * 64, manifest["claw_control_image_digest"])
        self.assertEqual("sha256:" + "5" * 64, manifest["adp_image_digest"])
        self.assertRegex(manifest["config_sha256"], r"^[0-9a-f]{64}$")
        self.assertEqual("0015_provider_secret_fingerprints", manifest["control_migration"])
        self.assertRegex(manifest["adp_migration"], r"^schema-sha256:[0-9a-f]{64}$")
        self.assertEqual("2.10.0", manifest["caddy_version"])
        self.assertEqual("green", manifest["active_color"])
        self.assertEqual("ap-guangzhou", manifest["provider_region"])
        self.assertEqual("2026-08-10T01:02:03Z", manifest["collected_at"])

    def test_atomic_output_is_owner_only_and_contains_no_config_values(self) -> None:
        output = self.root / "state" / "release-manifest.json"
        release.write_manifest_atomic(output, self.collect())
        payload = output.read_text(encoding="utf-8")
        self.assertNotIn("gateway-production-green:3000", payload)
        self.assertNotIn("WORKBENCH_AGSX_REGION", payload)
        if os.name == "posix":
            self.assertEqual(0o600, stat.S_IMODE(output.stat().st_mode))

    def test_rejects_dirty_source_worktree(self) -> None:
        self.runner.dirty_repository = True
        with self.assertRaisesRegex(release.CollectionError, "worktree is dirty"):
            self.collect()

    def test_immutable_production_collection_uses_oci_revisions_without_git(self) -> None:
        env = self.root / ".env"
        env.write_text(
            env.read_text(encoding="utf-8").replace(
                "CLAW_BUILD_LOCAL_IMAGES=true", "CLAW_BUILD_LOCAL_IMAGES=false"
            ),
            encoding="utf-8",
        )
        os.chmod(env, 0o600)
        self.runner.dirty_repository = True
        manifest = self.collect()
        self.assertEqual(ROOT_REVISION, manifest["new_api_revision"])
        self.assertEqual(ROOT_REVISION, manifest["claw_control_revision"])
        self.assertEqual(ADP_REVISION, manifest["adp_revision"])

    def test_immutable_production_collection_rejects_overlay_revision_mismatch(self) -> None:
        env = self.root / ".env"
        env.write_text(
            env.read_text(encoding="utf-8").replace(
                "CLAW_BUILD_LOCAL_IMAGES=true", "CLAW_BUILD_LOCAL_IMAGES=false"
            ),
            encoding="utf-8",
        )
        os.chmod(env, 0o600)
        self.runner.overlay_revision = "9" * 40
        with self.assertRaisesRegex(release.CollectionError, "overlay revision does not match"):
            self.collect()

    def test_rejects_stopped_active_container(self) -> None:
        self.runner.stopped.add(CONTROL_CONTAINER_ID)
        with self.assertRaisesRegex(release.CollectionError, "not running"):
            self.collect()

    def test_rejects_revision_label_mismatch(self) -> None:
        self.runner.revision_labels[CONTROL_CONTAINER_ID] = "9" * 40
        with self.assertRaisesRegex(release.CollectionError, "does not match"):
            self.collect()

    def test_rejects_mutable_image_tag(self) -> None:
        self.runner.image_references[ADP_CONTAINER_ID] = "registry.local/adp:latest"
        with self.assertRaisesRegex(release.CollectionError, "immutable registry digest"):
            self.collect()

    def test_rejects_multiple_compose_containers(self) -> None:
        self.runner.service_ids["adp-green"] = [ADP_CONTAINER_ID, "0" * 64]
        with self.assertRaisesRegex(release.CollectionError, "exactly one value"):
            self.collect()

    def test_rejects_active_color_and_caddy_disagreement(self) -> None:
        (self.root / "state" / "Caddyfile.active").write_text(
            "reverse_proxy claw-control-blue:8090\nreverse_proxy adp-blue:8000\n",
            encoding="utf-8",
        )
        with self.assertRaisesRegex(release.CollectionError, "does not match"):
            self.collect()

    def test_rejects_placeholder_region(self) -> None:
        env = self.root / ".env"
        content = env.read_text(encoding="utf-8").replace("ap-guangzhou", "replace-region")
        env.write_text(content, encoding="utf-8")
        os.chmod(env, 0o600)
        self.runner.runtime_region = "replace-region"
        with self.assertRaisesRegex(release.CollectionError, "region is invalid"):
            self.collect()

    def test_rejects_region_that_differs_from_active_adp_runtime(self) -> None:
        self.runner.runtime_region = "ap-shanghai"
        with self.assertRaisesRegex(release.CollectionError, "differs from ADP runtime"):
            self.collect()

    def test_rejects_missing_adp_schema_table(self) -> None:
        self.runner.adp_rows = "\n".join(
            row
            for row in self.runner.adp_rows.splitlines()
            if not row.startswith("workbench_turn" + chr(31))
        ) + "\n"
        with self.assertRaisesRegex(release.CollectionError, "missing required"):
            self.collect()

    def test_config_hash_changes_without_disclosing_content(self) -> None:
        first = release.hash_release_config(self.root)
        config = self.root / "config" / "integration-allowlist.json"
        config.write_text('{"enabled":true,"token":"do-not-print"}\n', encoding="utf-8")
        second = release.hash_release_config(self.root)
        self.assertNotEqual(first, second)
        self.assertNotIn("do-not-print", second)

    def test_subprocess_failure_does_not_echo_stderr(self) -> None:
        runner = release.SubprocessCommandRunner(timeout_seconds=5)
        with self.assertRaises(release.CollectionError) as raised:
            runner.run(
                [
                    sys.executable,
                    "-c",
                    "import sys; sys.stderr.write('database-password'); raise SystemExit(7)",
                ],
                purpose="query database",
            )
        self.assertNotIn("database-password", str(raised.exception))
        self.assertIn("exit status 7", str(raised.exception))


if __name__ == "__main__":
    unittest.main()
