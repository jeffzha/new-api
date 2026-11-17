"""Opt-in synthetic acceptance using the real operator executable and Ops services."""

import argparse
from collections import Counter
from datetime import datetime, timedelta, timezone
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import time
import uuid


FLAGS = ("OPS_CN_FINANCE_V2_ENABLED", "OPS_CN_DURABLE_WALLET_ENABLED",
         "OPS_CN_DURABLE_TASK_ENABLED", "OPS_CN_WALLET_VALUE_ENABLED",
         "OPS_CN_INTEGRITY_CAPTURE_ENABLED")


def stamp(value):
    return value.strftime("%Y-%m-%dT%H:%M:%S.%fZ")


def save(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8", newline="\n")


class Acceptance:
    def __init__(self, args, engine):
        self.args, self.engine = args, engine
        self.work, self.output = args.work / engine, args.output / engine
        self.work.mkdir(parents=True, exist_ok=False)
        self.output.mkdir(parents=True, exist_ok=False)
        self.events = []
        self.env = {key: value for key, value in os.environ.items()
                    if not key.startswith("OPS_CN_") and key not in ("REDIS_CONN_STRING", "BATCH_UPDATE_ENABLED")}
        self.env.update(CN_OPERATOR_ACCEPTANCE_WORK=str(self.work), CN_OPERATOR_ACCEPTANCE_OUTPUT=str(self.output))
        if engine == "sqlite":
            database = self.work / "gateway.sqlite3"
            database.touch(exist_ok=False)
            self.env.update(SQL_DSN="local", SQLITE_PATH=str(database))
        else:
            key = "AGENCY_HUB_TEST_MYSQL_DSN" if engine == "mysql" else "AGENCY_HUB_TEST_OPERATOR_POSTGRES_DSN"
            self.env["SQL_DSN"] = os.environ[key]
            self.env["CN_OPERATOR_ACCEPTANCE_DATABASE"] = "s4_operator_" + uuid.uuid4().hex[:12]
            self.env["SQL_DSN"] = self.fixture("provision")["sqlDsn"]
        save(self.work / "database-env.json", {key: self.env[key] for key in ("SQL_DSN", "SQLITE_PATH") if key in self.env})

    def fixture(self, action):
        env = {**self.env, "CN_OPERATOR_ACCEPTANCE_ACTION": action}
        proc = subprocess.run([str(self.args.fixture), "-test.run=^TestCNIntegrityOperatorAcceptanceFixture$", "-test.v"],
                              env=env, cwd=self.args.gateway, text=True, capture_output=True, timeout=120)
        # The provision result stays private because a configured DSN can contain credentials.
        self.events.append({"fixtureAction": action, "exitCode": proc.returncode})
        (self.work / ("fixture-" + action + ".log")).write_text(proc.stdout + proc.stderr, encoding="utf-8")
        if proc.returncode:
            raise AssertionError(f"fixture {action} failed; inspect private fixture-{action}.log")
        return json.loads((self.work / "fixture-result.json").read_bytes())

    def cli(self, label, args, *, error=None, env=None):
        proc = subprocess.run([str(self.args.operator), *args], cwd=self.args.gateway,
                              env=self.env if env is None else env, text=True, capture_output=True, timeout=30)
        event = {"command": label, "arguments": args, "exitCode": proc.returncode,
                 "stdout": proc.stdout, "stderr": proc.stderr}
        self.events.append(event)
        assert proc.returncode == (1 if error else 0), event
        if error:
            assert json.loads(proc.stderr) == {"error": error}, event
            return None
        return json.loads(proc.stdout)

    def reject(self, label, args, error="control_or_parent_conflict", env=None):
        before = self.fixture("inspect")
        self.cli(label, args, error=error, env=env)
        after = self.fixture("inspect")
        self.events.append({"negativeStateCheck": label, "before": before, "after": after})
        assert after == before, (label, "state content changed during rejection")

    def inventory(self, seed, prepared):
        import app
        import platform_api
        import tenant_catalog
        import finance
        import migration_0008_cn_finance_v2 as migration8
        import migration_0009_cn_native_integrity as migration9
        import cn_integrity_inventory as inventory
        import cn_integrity_codec as codec
        import cn_finance_v2 as v2

        path = self.work / "ops-review.sqlite3"
        tenant, gateway = "operator-cli-synthetic", seed["gatewayId"]
        applicant = {"id": "synthetic-applicant", "role": "platform"}
        reviewer = {"id": "synthetic-independent-reviewer", "role": "platform"}
        with sqlite3.connect(path) as db:
            db.row_factory = sqlite3.Row
            db.executescript(app.SCHEMA)
            platform_api.init(db)
            tenant_catalog.init_schema(db)
            finance.init_schema(db)
            migration8.migrate(db)
            migration9.migrate(db)
            db.execute("INSERT INTO tenants(id,name,currency,gateway_key_hash,created_at) VALUES(?,'Synthetic CLI','USD','unused','2026-10-16')", (tenant,))
            db.execute("INSERT INTO gateways VALUES(?,?,'Synthetic CLI')", (gateway, tenant))
            for actor in (applicant, reviewer):
                db.execute("INSERT INTO actors VALUES(?,?,?,'platform',NULL,'unused','active','2026-10-16')", (actor["id"], actor["id"] + "@example.test", actor["id"]))
            config = {"direction": "pull", "sourceIdentity": gateway, "sourceUrl": "http://localhost", "schemaVersion": v2.SOURCE}
            db.execute("INSERT INTO gateway_control(tenant_id,gateway_id,version,legacy_retired,legacy_expires_at,collection_enabled,finance_posting_enabled,config_json) VALUES(?,?,1,0,'2099-01-01T00:00:00Z',1,1,?)", (tenant, gateway, v2.canonical(config)))
            frozen = inventory.freeze_inventory(db, tenant, {"operationId": "synthetic-cli-freeze", "gatewayId": gateway,
                "streamEpoch": seed["streamEpoch"], "integrityEpoch": prepared["epochId"],
                "customerKeys": ["1001"], "reason": "synthetic actual CLI acceptance"}, applicant)
            review = {"operationId": "synthetic-cli-review", "expectedVersion": 1,
                      "contentHash": frozen["contentHash"], "reason": "independent synthetic inventory review"}
            try:
                inventory.approve_inventory(db, tenant, frozen["id"], review, applicant)
            except v2.FinanceV2Error as exc:
                assert str(exc) == "applicant_cannot_approve"
            else:
                raise AssertionError("self-approval unexpectedly passed")
            approved = inventory.approve_inventory(db, tenant, frozen["id"], review, reviewer)
            exported = inventory.export_inventory(db, tenant, frozen["id"])
            assert approved["approver"] != approved["applicant"]
            assert exported["body"]["integrityEpoch"] == prepared["epochId"]
            assert exported["body"]["customers"][0]["decision"] == "eligible"
            row = db.execute("SELECT review FROM cn_integrity_inventory_approvals WHERE inventory_id=?", (frozen["id"],)).fetchone()
            (self.output / "ops-approved-review.json").write_bytes(bytes(row[0]))
            draft = db.execute("SELECT body FROM cn_integrity_inventory_snapshots WHERE inventory_id=?", (frozen["id"],)).fetchone()
            (self.output / "ops-frozen-draft.json").write_bytes(bytes(draft[0]))
            audit = [dict(row) for row in db.execute("SELECT * FROM control_audit WHERE tenant_id=? ORDER BY occurred_at,id", (tenant,))]
            save(self.output / "ops-audit.json", audit)
            assert {row["action"] for row in audit} >= {"integrity.inventory.freeze", "integrity.inventory.approve:" + frozen["id"]}
            for table in ("cn_authorities", "cn_integrity_owners", "cn_integrity_acceptances", "cn_postings"):
                assert db.execute(f"SELECT COUNT(*) FROM {table}").fetchone()[0] == 0
            db.commit()
        raw = codec.canonical(exported)
        with sqlite3.connect(path.as_uri() + "?mode=ro", uri=True) as db:
            db.row_factory = sqlite3.Row
            assert codec.canonical(inventory.export_inventory(db, tenant, frozen["id"])) == raw
        (self.work / "approved.json").write_bytes(raw)
        (self.output / "ops-approved-eligibility.json").write_bytes(raw)
        save(self.output / "ops-provenance.json", {"frozen": frozen, "approved": approved,
             "opsDatabase": str(path), "databaseSha256": hashlib.sha256(path.read_bytes()).hexdigest(),
             "syntheticIntegration": True, "productionEligibility": False, "financialGrants": 0})
        return exported

    def run(self):
        started = time.perf_counter()
        loop = None
        try:
            seed = self.fixture("seed")
            gateway = ["--gateway", seed["gatewayId"]]
            prepared = self.cli("prepare", ["prepare", *gateway, "--evidence", "synthetic-cli-prepare"])
            scope = [*gateway, "--epoch", prepared["epochId"]]
            self.status = ["status", *scope]
            assert prepared["generation"] == 1 and prepared["state"] == "prepared"
            approved = self.inventory(seed, prepared)
            private = self.work / "approved.json"
            bad = self.work / "invalid.json"
            bad.write_text("{invalid", encoding="ascii")
            imp = ["import", *scope, "--expected-generation", "1", "--evidence", "synthetic-cli-import", "--input", str(private)]
            self.reject("import-malformed", [*imp[:-1], str(bad)])
            stale = ["import", *scope, "--expected-generation", "2", "--evidence", "synthetic-cli-import", "--input", str(private)]
            self.reject("import-stale-generation", stale)
            self.cli("import", imp)
            self.cli("import-idempotent", imp)
            admit = ["admit", *scope, "--expected-generation", "1", "--evidence", "synthetic-cli-admit", "--inventory", approved["opsInventoryId"], "--user"]
            self.reject("admit-customer-outside-inventory", [*admit, "1002"], "operation_failed")
            self.reject("admit-stale-generation", ["admit", *scope, "--expected-generation", "2", "--evidence", "synthetic-cli-admit", "--inventory", approved["opsInventoryId"], "--user", "1001"])
            self.cli("admit", [*admit, "1001"])
            declaration = {"writers": [{"writerId": "operator-cli-writer", "buildId": "s4-cli-acceptance",
                "leaseUntil": stamp(datetime.now(timezone.utc) + timedelta(hours=1)), "protocolVersion": "native-mutation-receipt-v2",
                "capabilityPaths": ["quota_mutation", "source_switch", "task_reserve", "task_revision", "topup_credit", "wallet_final", "wallet_release", "wallet_reserve"]}],
                "attestation": {"inventoryId": approved["opsInventoryId"], "inventoryHash": approved["opsInventoryHash"],
                "operatorEvidenceId": "synthetic-cli-start", "oldWritersDrained": True, "redisDisabled": True,
                "batchDisabled": True, "externalSqlExcluded": True, "unguardedHandlesExcluded": True}}
            start_file = self.work / "start.json"
            save(start_file, declaration)
            (self.output / "writer-start.json").write_bytes(start_file.read_bytes())
            start = ["start", *scope, "--expected-generation", "1", "--evidence", "synthetic-cli-start", "--input", str(start_file)]
            self.reject("start-missing-explicit-flags", start, "operation_failed")
            self.env.update({key: "true" for key in FLAGS})
            self.env.update(OPS_CN_INTEGRITY_WRITER_ID="operator-cli-writer", OPS_CN_INTEGRITY_WRITER_BUILD="s4-cli-acceptance")
            self.reject("start-malformed", [*start[:-1], str(bad)], "operation_failed")
            invalid = json.loads(json.dumps(declaration))
            invalid["writers"][0]["protocolVersion"] = "invalid-protocol"
            save(bad, invalid)
            self.reject("start-invalid-writer", [*start[:-1], str(bad)], "operation_failed")
            invalid = json.loads(json.dumps(declaration))
            invalid["attestation"]["oldWritersDrained"] = False
            save(bad, invalid)
            self.reject("start-incomplete-attestation", [*start[:-1], str(bad)], "operation_failed")
            self.reject("start-redis-enabled", start, "operation_failed", {**self.env, "REDIS_CONN_STRING": "synthetic-not-connected"})
            self.reject("start-batch-enabled", start, "operation_failed", {**self.env, "BATCH_UPDATE_ENABLED": "true"})
            self.reject("start-stale-generation", ["start", *scope, "--expected-generation", "2", "--evidence", "synthetic-cli-start", "--input", str(start_file)])
            assert self.cli("start", start)["state"] == "active"
            self.reject("import-stale-after-start", imp)
            native = self.fixture("native")
            self.reject("stop-stale-generation", ["stop", *scope, "--expected-generation", "1", "--state", "paused", "--evidence", "synthetic-cli-stop"])
            from_time = stamp(datetime.now(timezone.utc) - timedelta(hours=1))
            loop_args = ["freeze-loop", *scope, "--expected-generation", "2", "--evidence", "synthetic-cli-loop",
                         "--from", from_time, "--interval", "1s", "--state-file", str(self.work / "loop.json")]
            loop = subprocess.Popen([str(self.args.operator), *loop_args], cwd=self.args.gateway,
                                    env=self.env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            deadline = time.monotonic() + 30
            while True:
                status = self.cli("periodic-status", self.status)
                if int(status["batchSequence"]) >= 2:
                    break
                assert loop.poll() is None and time.monotonic() < deadline, "periodic producer failed or timed out"
                time.sleep(0.15)
            stop = ["stop", *scope, "--expected-generation", "2", "--state", "paused", "--evidence", "synthetic-cli-stop"]
            assert self.cli("stop", stop)["state"] == "paused"
            stdout, stderr = loop.communicate(timeout=15)
            assert loop.returncode == 1 and json.loads(stderr) == {"error": "control_or_parent_conflict"}
            proofs = [json.loads(line) for line in stdout.splitlines()]
            assert len(proofs) >= 2
            self.events.append({"command": "freeze-loop", "arguments": loop_args, "exitCode": loop.returncode,
                                "stdout": stdout, "stderr": stderr, "expectedExit": "control generation changed by stop"})
            self.reject("loop-restart-stale-generation", loop_args)
            self.reject("stop-stale-repeat", stop)
            final = self.cli("final-status", self.status)
            assert final["state"] == "paused" and final["generation"] == 3
            self.fixture("export")
            from verify_integrity_exports import read_frozen
            import cn_integrity_graph as graph
            import cn_integrity_values as values
            import cn_integrity_controls as controls
            parent, parent_rows, verified = None, {}, []
            for batch in sorted(self.output.glob("batch-*"), key=lambda path: int(path.name.split("-")[1])):
                proof, manifest, rows, identities, pages = read_frozen(batch, seed["gatewayId"])
                for name, prior in parent_rows.items():
                    assert all(identities[name].get(identity) == hashed for identity, hashed in prior.items())
                assert manifest["parentProofId"] == parent and manifest["integrityEpoch"] == prepared["epochId"]
                assert manifest["status"] == "complete" and manifest["controlGeneration"] == "2"
                graph.validate_core(manifest, rows)
                values.validate(manifest, rows)
                controls.validate(manifest, rows, authorizable=True)
                assert Counter(row["body"]["kind"] for row in rows["facts"]) == {"payment": 1, "funding_lot": 1, "charge": 1, "funding_allocation": 1, "recognized_revenue": 1}
                parent = proof
                parent_rows = identities
                verified.append({"proofId": proof, "pages": pages})
            assert len(verified) == int(final["batchSequence"]) == len(proofs)
            save(self.output / "result.json", {"engine": self.engine, **seed, "prepared": prepared, "final": final,
                 "native": native, "verified": verified, "seconds": time.perf_counter() - started,
                 "databaseScope": self.env.get("CN_OPERATOR_ACCEPTANCE_DATABASE", self.env.get("SQLITE_PATH")),
                 "syntheticIntegration": True, "productionEligibility": False, "financialGrants": 0})
            print(self.engine, "CLI/import/admit/start/native/periodic/stop/negative + full wire/graph/value/control PASS", flush=True)
        finally:
            if loop is not None and loop.poll() is None:
                loop.kill()
                loop.communicate(timeout=10)
            save(self.output / "commands.json", self.events)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--ops-root", type=Path, required=True)
    parser.add_argument("--gateway", type=Path, default=Path(__file__).resolve().parents[3])
    parser.add_argument("--operator", type=Path, required=True)
    parser.add_argument("--fixture", type=Path, required=True)
    parser.add_argument("--work", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--engines", nargs="+", choices=("sqlite", "mysql", "postgres"), default=["sqlite"])
    args = parser.parse_args()
    for field in ("ops_root", "gateway", "operator", "fixture", "work", "output"):
        setattr(args, field, getattr(args, field).resolve())
    args.work.mkdir(parents=True, exist_ok=False)
    args.output.mkdir(parents=True, exist_ok=False)
    sys.path.insert(0, str(args.ops_root))
    for key in ("ADMIN_EMAIL", "ADMIN_PASSWORD"):
        os.environ.pop(key, None)
    source = {}
    for name, root in (("gateway", args.gateway), ("ops", args.ops_root)):
        proc = subprocess.run(["git", "-c", "safe.directory=" + root.as_posix(), "rev-parse", "HEAD"],
                              cwd=root, capture_output=True, text=True, check=True)
        source[name + "Head"] = proc.stdout.strip()
    source["opsModules"] = {name: hashlib.sha256((args.ops_root / name).read_bytes()).hexdigest()
                            for name in ("cn_integrity_inventory.py", "cn_integrity_codec.py", "cn_integrity_graph.py", "cn_integrity_values.py", "cn_integrity_controls.py")}
    source["binaries"] = {name: hashlib.sha256(path.read_bytes()).hexdigest()
                          for name, path in (("operator", args.operator), ("fixture", args.fixture))}
    source["qualificationSources"] = {name: hashlib.sha256((args.gateway / name).read_bytes()).hexdigest()
        for name in ("model/cn_integrity_operator_acceptance_test.go", "model/testdata/cn-task/verify_integrity_operator_cli.py")}
    save(args.output / "source-provenance.json", source)
    for engine in args.engines:
        Acceptance(args, engine).run()


if __name__ == "__main__":
    main()
