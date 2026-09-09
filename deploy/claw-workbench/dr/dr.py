#!/usr/bin/env python3
"""Fail-closed restic orchestration for two-region Claw Workbench backups."""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import sys
import tempfile
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from dr_lib import (
    CommandRunner,
    DRError,
    DRResult,
    Redactor,
    canonical_path,
    expand_command,
    is_within,
    load_config,
    minimal_env,
    parse_utc,
    sha256_file,
    target_env,
    utc_now,
    verify_dependencies,
    verify_source_backup,
    write_atomic,
    write_report,
)


REQUIRED_BACKUP_FILES = ["control-db.dump", "adp-db.dump", "redis-forensics.rdb", "manifest.txt"]


class Orchestrator:
    def __init__(self, config: dict[str, Any], args: argparse.Namespace, runner: CommandRunner | None = None) -> None:
        self.config = config
        self.args = args
        self.runner = runner or CommandRunner()
        self.redactor = Redactor()
        if args.snapshot != "latest":
            self.redactor.add(args.snapshot)
        self.results: list[DRResult] = []
        report_root = canonical_path(args.output)
        if any(is_within(report_root, canonical_path(root)) for root in config["protected_roots"]):
            raise DRError("report output cannot be inside a protected production root")
        self.run_id = str(uuid.uuid4())
        self.started_at = utc_now()
        self.payload: dict[str, Any] = {
            "schema_version": 1,
            "run_id": self.run_id,
            "action": args.action,
            "mode": "live" if self.is_live() else "dry-run",
            "started_at": self.started_at,
            "finished_at": self.started_at,
            "actual_restore_drill": False,
            "both_repositories_verified": False,
            "source_scope": ["control PostgreSQL", "ADP PostgreSQL", "Redis forensic RDB"],
            "objective_evaluation": self.not_exercised_objectives(),
        }

    def is_live(self) -> bool:
        return bool(self.args.execute or self.args.write or self.args.restore)

    def not_exercised_objectives(self) -> dict[str, Any]:
        objectives = self.config["objectives"]
        return {
            "rpo_target_seconds": objectives["rpo_seconds"],
            "rpo_observed_seconds": None,
            "rpo_status": "not_exercised",
            "rto_target_seconds": objectives["rto_seconds"],
            "rto_observed_seconds": None,
            "rto_status": "not_exercised",
            "rto_scope": "artifact retrieval and validation only; no production cutover",
        }

    def add(self, phase: str, target: str, status: str, message: str, *, elapsed: float = 0.0, evidence: dict[str, Any] | None = None) -> None:
        self.results.append(DRResult(phase, target, status, message, elapsed, evidence or {}))

    def command_env_for_backup(self) -> dict[str, str]:
        executable = self.config["backup"]["command"][0]
        env = minimal_env(executable)
        for name in self.config["backup"].get("environment_allowlist", ["HOME", "DOCKER_HOST", "XDG_RUNTIME_DIR"]):
            if not re.fullmatch(r"[A-Z][A-Z0-9_]*", name):
                raise DRError("backup environment_allowlist contains an invalid name")
            if name == "PATH":
                raise DRError("backup environment_allowlist cannot inherit PATH")
            if os.environ.get(name):
                env[name] = os.environ[name]
        return env

    def restic(self, target: dict[str, Any], arguments: list[str], *, timeout: int = 7200):
        command = self.config["restic"]["command"] + arguments
        env = target_env(target, self.redactor, self.config["restic"]["command"][0])
        return self.runner.run(command, env=env, timeout=timeout)

    def dependency_preflight(self) -> bool:
        if not self.is_live():
            self.add("preflight", "local", "skipped", "dry-run validates pins syntactically but does not execute dependencies")
            return False
        try:
            verify_dependencies(self.config, self.runner)
            # Resolve every target before any local backup or remote mutation.
            # A missing password/credential must not leave a new source backup
            # that operators could mistake for an off-host protected copy.
            for target in self.config["targets"]:
                target_env(target, self.redactor, self.config["restic"]["command"][0])
            self.add("preflight", "local", "pass", "all absolute dependencies and restic version match exact pins")
            return True
        except Exception as error:
            self.add("preflight", "local", "blocker", f"{type(error).__name__}: {error}")
            return False

    def plan(self) -> None:
        for target in self.config["targets"]:
            self.add(
                "plan", target["name"], "skipped", "dry-run; no repository connection or write performed",
                evidence={"endpoint_host": target["endpoint"].split("://", 1)[1], "bucket": target["bucket"], "region": target["region"], "prefix": target["prefix"]},
            )

    def init(self) -> None:
        if not self.dependency_preflight():
            return
        for target in self.config["targets"]:
            result = self.restic(target, ["init"])
            ok = result.returncode == 0
            self.add("repository-init", target["name"], "pass" if ok else "fail", "repository initialized" if ok else "repository initialization failed or repository already exists", elapsed=result.elapsed_seconds)

    def create_source_backup(self) -> tuple[Path, dict[str, str], str]:
        staging = canonical_path(self.config["backup_staging_root"])
        staging.mkdir(parents=True, exist_ok=True)
        if staging.is_symlink() or not staging.is_dir():
            raise DRError("backup_staging_root must be a regular directory")
        backup_id = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + self.run_id[:8]
        backup_dir = (staging / backup_id).resolve()
        if not is_within(backup_dir, staging) or backup_dir.exists():
            raise DRError("backup output must be a new directory under backup_staging_root")
        command = expand_command(self.config["backup"]["command"], {"backup_dir": str(backup_dir)})
        result = self.runner.run(command, env=self.command_env_for_backup(), timeout=int(self.config["backup"].get("timeout_seconds", 3600)))
        if result.returncode != 0:
            raise DRError("existing backup command failed")
        declared = verify_source_backup(backup_dir, REQUIRED_BACKUP_FILES)
        source_created = self.read_source_created_at(backup_dir / "manifest.txt")
        manifest = {
            "schema_version": 1,
            "backup_id": backup_id,
            "source_created_at": source_created,
            "source_backup_verified": True,
            "required_files": declared,
            "source_sha256sums_sha256": sha256_file(backup_dir / "SHA256SUMS"),
            "restic_expected_version": self.config["restic"]["expected_version"],
            "repository_targets": [target["name"] for target in self.config["targets"]],
        }
        manifest_path = backup_dir / "dr-manifest.json"
        write_atomic(manifest_path, json.dumps(manifest, ensure_ascii=False, sort_keys=True, indent=2) + "\n")
        manifest_hash = sha256_file(manifest_path)
        write_atomic(backup_dir / "DR_MANIFEST.sha256", f"{manifest_hash}  dr-manifest.json\n")
        self.add("source-backup", "local", "pass", "existing backup completed and exact source checksums verified", elapsed=result.elapsed_seconds, evidence={"backup_id": backup_id, "manifest_sha256": manifest_hash, "required_file_count": len(declared)})
        return backup_dir, declared, source_created

    @staticmethod
    def read_source_created_at(path: Path) -> str:
        for line in path.read_text(encoding="utf-8").splitlines():
            if line.startswith("created_at="):
                raw = line.split("=", 1)[1]
                if re.fullmatch(r"\d{8}T\d{6}Z", raw):
                    parsed = datetime.strptime(raw, "%Y%m%dT%H%M%SZ").replace(tzinfo=timezone.utc)
                    return parsed.isoformat().replace("+00:00", "Z")
        raise DRError("source manifest lacks a valid created_at timestamp")

    @staticmethod
    def snapshot_id(stdout: bytes) -> str:
        snapshot = ""
        for line in stdout.decode("utf-8", "replace").splitlines():
            try:
                item = json.loads(line)
            except json.JSONDecodeError:
                continue
            candidate = item.get("snapshot_id") if isinstance(item, dict) else None
            if candidate:
                snapshot = str(candidate)
        if not re.fullmatch(r"[a-f0-9]{8,64}", snapshot):
            raise DRError("restic backup did not return a valid snapshot_id")
        return snapshot

    @staticmethod
    def verify_dr_manifest(directory: Path) -> str:
        manifest = directory / "dr-manifest.json"
        checksum = directory / "DR_MANIFEST.sha256"
        if manifest.is_symlink() or checksum.is_symlink() or not manifest.is_file() or not checksum.is_file():
            raise DRError("DR manifest or its checksum is missing")
        match = re.fullmatch(r"([a-f0-9]{64})  dr-manifest\.json\n?", checksum.read_text(encoding="utf-8"))
        if not match or sha256_file(manifest) != match.group(1):
            raise DRError("DR manifest checksum mismatch")
        return match.group(1)

    def backup(self) -> None:
        if not self.dependency_preflight():
            return
        repositories_ready = True
        for target in self.config["targets"]:
            probe = self.restic(target, ["snapshots", "--json"])
            ok = probe.returncode == 0
            repositories_ready = repositories_ready and ok
            self.add("repository-preflight", target["name"], "pass" if ok else "blocker", "repository is initialized and readable" if ok else "repository is unavailable or not initialized", elapsed=probe.elapsed_seconds)
        if not repositories_ready:
            return
        try:
            backup_dir, _, source_created = self.create_source_backup()
        except Exception as error:
            self.add("source-backup", "local", "blocker", f"{type(error).__name__}: {error}")
            return
        snapshots: dict[str, str] = {}
        verified: list[str] = []
        for target in self.config["targets"]:
            try:
                verify_source_backup(backup_dir, REQUIRED_BACKUP_FILES)
                self.verify_dr_manifest(backup_dir)
            except DRError as error:
                self.add("pre-upload-verification", target["name"], "blocker", str(error))
                break
            result = self.restic(target, ["backup", "--json", "--tag", "claw-workbench-dr", "--tag", f"dr-run={self.run_id}", str(backup_dir)])
            if result.returncode != 0:
                self.add("encrypted-upload", target["name"], "fail", "restic backup failed", elapsed=result.elapsed_seconds)
                continue
            try:
                snapshot = self.snapshot_id(result.stdout)
            except DRError as error:
                self.add("encrypted-upload", target["name"], "fail", str(error), elapsed=result.elapsed_seconds)
                continue
            snapshots[target["name"]] = snapshot
            self.redactor.add(snapshot)
            self.add("encrypted-upload", target["name"], "pass", "encrypted snapshot written", elapsed=result.elapsed_seconds, evidence={"snapshot_recorded": True})
            check = self.restic(target, ["check", "--read-data"])
            ok = check.returncode == 0
            self.add("restic-check", target["name"], "pass" if ok else "fail", "full repository data check passed" if ok else "repository data check failed", elapsed=check.elapsed_seconds)
            if ok:
                verified.append(target["name"])
        both = len(verified) == 2
        self.payload["both_repositories_verified"] = both
        self.payload["snapshot_count"] = len(snapshots)
        if both:
            observed = max(0.0, (datetime.now(timezone.utc) - parse_utc(source_created)).total_seconds())
            target = self.config["objectives"]["rpo_seconds"]
            self.payload["objective_evaluation"].update({
                "rpo_observed_seconds": round(observed, 3),
                "rpo_status": "snapshot_freshness_within_target_not_continuity_proven" if observed <= target else "snapshot_freshness_not_met",
                "rto_status": "not_exercised",
            })
        else:
            self.payload["objective_evaluation"]["rpo_status"] = "not_verified"

    def check(self) -> None:
        if not self.dependency_preflight():
            return
        verified = 0
        for target in self.config["targets"]:
            result = self.restic(target, ["check", "--read-data"])
            ok = result.returncode == 0
            verified += int(ok)
            self.add("restic-check", target["name"], "pass" if ok else "fail", "full repository data check passed" if ok else "repository data check failed", elapsed=result.elapsed_seconds)
        self.payload["both_repositories_verified"] = verified == 2

    def retention(self) -> None:
        if not self.dependency_preflight():
            return
        policy = self.config["retention"]
        args = [
            "forget", "--prune", "--keep-daily", str(policy["daily"]),
            "--keep-weekly", str(policy["weekly"]), "--keep-monthly", str(policy["monthly"]),
            "--keep-yearly", str(policy["yearly"]), "--tag", "claw-workbench-dr",
        ]
        verified = 0
        for target in self.config["targets"]:
            forget = self.restic(target, args)
            if forget.returncode != 0:
                self.add("retention", target["name"], "fail", "forget/prune failed", elapsed=forget.elapsed_seconds)
                continue
            self.add("retention", target["name"], "pass", "retention and prune completed", elapsed=forget.elapsed_seconds, evidence={"daily": policy["daily"], "weekly": policy["weekly"], "monthly": policy["monthly"], "yearly": policy["yearly"]})
            check = self.restic(target, ["check", "--read-data"])
            ok = check.returncode == 0
            verified += int(ok)
            self.add("post-retention-check", target["name"], "pass" if ok else "fail", "full repository check passed" if ok else "repository check failed after retention", elapsed=check.elapsed_seconds)
        self.payload["both_repositories_verified"] = verified == 2
        if verified == 2:
            try:
                self.local_retention(policy["local_verified_backups"])
            except Exception as error:
                self.add("local-retention", "local", "fail", f"{type(error).__name__}: {error}")

    def local_retention(self, keep: int) -> None:
        staging = canonical_path(self.config["backup_staging_root"])
        if not staging.is_dir() or staging.is_symlink():
            raise DRError("local staging root is unavailable or a symlink")
        candidates = [item for item in staging.iterdir() if item.is_dir() and not item.is_symlink()]
        if any(not re.fullmatch(r"\d{8}T\d{6}Z-[a-f0-9]{8}", item.name) for item in candidates):
            raise DRError("local staging contains an unknown directory; nothing was deleted")
        candidates.sort(key=lambda item: item.name, reverse=True)
        for candidate in candidates[keep:]:
            if not is_within(candidate.resolve(), staging):
                raise DRError("local retention candidate escaped the staging root")
            verify_source_backup(candidate, REQUIRED_BACKUP_FILES)
            self.verify_dr_manifest(candidate)
        removed = 0
        for candidate in candidates[keep:]:
            shutil.rmtree(candidate)
            if candidate.exists():
                raise DRError("local verified backup could not be removed")
            removed += 1
        self.add("local-retention", "local", "pass", "local verified backup retention completed", evidence={"kept": min(keep, len(candidates)), "removed": removed})

    def restore_drill(self) -> None:
        if not self.dependency_preflight():
            return
        targets = {target["name"]: target for target in self.config["targets"]}
        if self.args.target not in targets:
            self.add("restore-drill", str(self.args.target), "blocker", "requested target is not configured")
            return
        target = targets[self.args.target]
        drill_root = canonical_path(self.config["drill_root"])
        drill_root.mkdir(parents=True, exist_ok=True)
        if drill_root.is_symlink() or not drill_root.is_dir():
            self.add("restore-drill", target["name"], "blocker", "drill_root is not a regular directory")
            return
        for protected in self.config["protected_roots"]:
            root = canonical_path(protected)
            if is_within(drill_root, root) or is_within(root, drill_root):
                self.add("restore-drill", target["name"], "blocker", "drill_root overlaps a protected production root")
                return
        temporary = Path(tempfile.mkdtemp(prefix="claw-drill-", dir=drill_root)).resolve()
        started = time.perf_counter()
        drill_started_at = datetime.now(timezone.utc)
        try:
            check = self.restic(target, ["check", "--read-data"])
            if check.returncode != 0:
                raise DRError("restore refused because full restic check failed")
            restore = self.restic(target, ["restore", self.args.snapshot, "--target", str(temporary)])
            if restore.returncode != 0:
                raise DRError("restic restore failed")
            manifests = list(temporary.rglob("dr-manifest.json"))
            if len(manifests) != 1:
                raise DRError("restored snapshot must contain exactly one dr-manifest.json")
            backup_dir = manifests[0].parent.resolve()
            if not is_within(backup_dir, temporary):
                raise DRError("restored backup escaped the isolated drill directory")
            hash_path = backup_dir / "DR_MANIFEST.sha256"
            if hash_path.is_symlink() or not hash_path.is_file():
                raise DRError("restored DR manifest hash is missing")
            match = re.fullmatch(r"([a-f0-9]{64})  dr-manifest\.json\n?", hash_path.read_text(encoding="utf-8"))
            if not match or sha256_file(manifests[0]) != match.group(1):
                raise DRError("restored DR manifest hash mismatch")
            manifest = json.loads(manifests[0].read_text(encoding="utf-8"))
            if manifest.get("source_backup_verified") is not True:
                raise DRError("restored manifest was not marked source-verified")
            verify_source_backup(backup_dir, REQUIRED_BACKUP_FILES)
            for index, validation in enumerate(self.config["restore_validation_commands"]):
                command = expand_command(validation["command"], {"backup_dir": str(backup_dir)})
                result = self.runner.run(command, env=minimal_env(command[0]), cwd=backup_dir, timeout=int(validation.get("timeout_seconds", 300)))
                if result.returncode != 0:
                    raise DRError(f"restore validation command {index + 1} failed")
            observed_rto = time.perf_counter() - started
            observed_rpo = (drill_started_at - parse_utc(str(manifest["source_created_at"]))).total_seconds()
            if observed_rpo < 0:
                raise DRError("restored manifest timestamp is in the future")
            shutil.rmtree(temporary)
            if temporary.exists():
                raise DRError("isolated restore directory could not be removed")
            objectives = self.config["objectives"]
            self.payload["actual_restore_drill"] = True
            self.payload["both_repositories_verified"] = False
            self.payload["objective_evaluation"] = {
                "rpo_target_seconds": objectives["rpo_seconds"],
                "rpo_observed_seconds": round(observed_rpo, 3),
                "rpo_status": "met" if observed_rpo <= objectives["rpo_seconds"] else "not_met",
                "rto_target_seconds": objectives["rto_seconds"],
                "rto_observed_seconds": round(observed_rto, 3),
                "rto_status": "partial_artifact_drill_only",
                "rto_scope": "artifact retrieval, hashes and configured dump validation; no production cutover/service recovery",
            }
            self.add("restore-drill", target["name"], "pass", "isolated artifact restore and configured validation completed", elapsed=observed_rto, evidence={"snapshot_selector": self.args.snapshot, "temporary_directory_removed": True, "validation_command_count": len(self.config["restore_validation_commands"])})
        except Exception as error:
            self.add("restore-drill", target["name"], "fail", f"{type(error).__name__}: {error}", elapsed=time.perf_counter() - started)
            self.payload["objective_evaluation"].update({"rpo_status": "not_verified", "rto_status": "not_verified"})
        finally:
            if temporary.exists() and is_within(temporary, drill_root) and temporary.name.startswith("claw-drill-"):
                try:
                    shutil.rmtree(temporary)
                except OSError as cleanup_error:
                    self.add("restore-cleanup", target["name"], "fail", f"isolated drill cleanup failed: {type(cleanup_error).__name__}")
                    self.payload["objective_evaluation"].update({"rpo_status": "not_verified", "rto_status": "not_verified"})

    def finish(self) -> int:
        self.payload["finished_at"] = utc_now()
        write_report(canonical_path(self.args.output), self.payload, self.results, self.redactor)
        summary = {status: sum(item.status == status for item in self.results) for status in ("pass", "fail", "blocker", "skipped")}
        print(json.dumps({"output": str(canonical_path(self.args.output)), "summary": summary}, ensure_ascii=False))
        return 1 if summary["fail"] or summary["blocker"] else 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["plan", "init", "backup", "check", "retention", "restore-drill"], nargs="?", default="plan")
    parser.add_argument("--config", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--allow-http", action="store_true", help="loopback fake-restic tests only")
    parser.add_argument("--execute", action="store_true", help="execute read-only repository checks")
    parser.add_argument("--write", action="store_true", help="permit repository initialization, backup, or retention writes")
    parser.add_argument("--restore", action="store_true", help="permit isolated restore drill")
    parser.add_argument("--confirm", default="", help="literal action-specific confirmation")
    parser.add_argument("--target", default="secondary", help="restore-drill target name")
    parser.add_argument("--snapshot", default="latest", help="restic snapshot ID or latest")
    return parser


def authorization_error(args: argparse.Namespace) -> str | None:
    if args.action == "plan":
        if args.execute or args.write or args.restore or args.confirm:
            return "plan rejects all execution/confirmation flags"
        return None
    if args.action == "check":
        return None if args.execute and not args.write and not args.restore and args.confirm == "CHECK_BOTH_REPOSITORIES" else "check requires --execute --confirm CHECK_BOTH_REPOSITORIES only"
    if args.action == "restore-drill":
        return None if args.restore and not args.execute and not args.write and args.confirm == "RESTORE_ISOLATED" else "restore-drill requires --restore --confirm RESTORE_ISOLATED only"
    expected = {"init": "INIT_BOTH_REPOSITORIES", "backup": "WRITE_BOTH_REGIONS", "retention": "APPLY_RETENTION_BOTH"}[args.action]
    return None if args.write and not args.execute and not args.restore and args.confirm == expected else f"{args.action} requires --write --confirm {expected} only"


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    error = authorization_error(args)
    if error:
        print(f"authorization error: {error}", file=sys.stderr)
        return 2
    if args.snapshot != "latest" and not re.fullmatch(r"[a-f0-9]{8,64}", args.snapshot):
        print("configuration error: snapshot must be latest or an exact hexadecimal snapshot ID", file=sys.stderr)
        return 2
    try:
        config = load_config(args.config, allow_http=args.allow_http)
        orchestrator = Orchestrator(config, args)
        getattr(orchestrator, args.action.replace("-", "_"))()
        return orchestrator.finish()
    except DRError as error:
        print(f"configuration error: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
