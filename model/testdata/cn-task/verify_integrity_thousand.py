"""Full independent Ops validation of the two actual native thousand-pair exports."""

import argparse
from collections import Counter
import hashlib
import json
from pathlib import Path
import sqlite3
import time

import cn_integrity_codec as codec
import cn_integrity_graph as graph
import cn_integrity_values as values
import cn_integrity_controls as controls
from cn_integrity_inventory import export_inventory
from verify_integrity_exports import read_frozen


def verify(root, approvals):
    origin = json.loads((approvals / "manifest.json").read_bytes())
    engine = next(row for row in origin["results"] if row["engine"] == "sqlite")
    assert origin["synthetic"] and origin["customerCount"] == 1000
    assert not origin["productionEligibility"] and origin["financialGrants"] == 0
    approved = (approvals / "sqlite.json").read_bytes()
    review = approvals / "sqlite.sqlite3"
    assert hashlib.sha256(approved).hexdigest() == engine["exportSha256"]
    assert hashlib.sha256(review.read_bytes()).hexdigest() == engine["databaseSha256"]
    with sqlite3.connect(review.as_uri() + "?mode=ro", uri=True) as db:
        assert codec.canonical(export_inventory(db, engine["tenantId"], engine["inventoryId"])) == approved
    results = []
    for name, lots, customers in (("distinct-1000-pairs", 1000, 1000), ("shared-1000-charges", 1, 1)):
        started = time.perf_counter()
        directory = root / name
        assert (directory / "ops-fixture-provenance.json").read_bytes() == (approvals / "manifest.json").read_bytes()
        assert (directory / "ops-approved-eligibility.json").read_bytes() == approved
        proof, manifest, collections, _, pages = read_frozen(directory, engine["gatewayId"])
        assert manifest["streamEpoch"] == engine["streamEpoch"] and manifest["integrityEpoch"] == engine["integrityEpoch"]
        assert manifest["status"] == "complete" and manifest["parentProofId"] is None
        wire_seconds = time.perf_counter() - started
        graph_start = time.perf_counter()
        graph.validate_core(manifest, collections)
        graph_seconds = time.perf_counter() - graph_start
        value_start = time.perf_counter()
        values.validate(manifest, collections)
        value_seconds = time.perf_counter() - value_start
        controls.validate(manifest, collections, authorizable=True)
        kinds = Counter(row["body"]["kind"] for row in collections["facts"])
        assert kinds == {"payment": lots, "funding_lot": lots, "charge": 1000, "funding_allocation": 1000, "recognized_revenue": 1000}
        assert len(collections["admissions"]) == customers
        index = codec.load((directory / "index.json").read_bytes())
        assert any(row["proofId"] == proof for row in index["items"])
        result = {"sample": name, "proofId": proof, "pages": pages, "factKinds": dict(kinds),
            "wireSeconds": wire_seconds, "graphAndFinancialSeconds": graph_seconds, "valueSeconds": value_seconds,
            "productionEligibility": False, "financialGrants": 0,
            "files": {path.name: hashlib.sha256(path.read_bytes()).hexdigest() for path in sorted(directory.glob("*.json"))}}
        results.append(result)
        print(name, proof, "full wire/graph/financial/values/control PASS", f"{time.perf_counter()-started:.3f}s", flush=True)
    return results


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--samples", type=Path, default=Path(__file__).with_name("integrity-v2-thousand"))
    parser.add_argument("--approvals", type=Path, required=True)
    parser.add_argument("--result", type=Path, required=True)
    args = parser.parse_args()
    results = verify(args.samples.resolve(), args.approvals.resolve())
    args.result.write_text(json.dumps(results, indent=2, sort_keys=True) + "\n", encoding="utf-8")
