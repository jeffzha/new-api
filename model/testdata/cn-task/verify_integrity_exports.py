"""Verify Gateway raw samples with the independently implemented Ops consumer."""

import argparse
import hashlib
import json
from pathlib import Path
import sqlite3

import cn_integrity_codec as codec
import cn_integrity_wire as wire
import cn_integrity_graph as graph
import cn_integrity_values as values
import cn_integrity_controls as controls
import cn_finance_v2 as finance
from cn_integrity_import import _identity, _sort_key
from cn_integrity_inventory import export_inventory


def verify(root, approvals, include_source_switch=False):
    regression = root.with_name("integrity-v2-noncanonical-regression")
    try:
        codec.load((regression / "reconstructed-legacy.json").read_bytes())
    except finance.FinanceV2Error as error:
        assert str(error) == "integrity_json_not_canonical"
    else:
        raise AssertionError("old nested serialization must fail strict JCS")
    codec.load((regression / "reconstructed-fixed.json").read_bytes())
    provenance = json.loads((approvals / "manifest.json").read_bytes())
    assert provenance["synthetic"] and not provenance["productionEligibility"]
    assert provenance["financialGrants"] == 0
    results = []
    for engine in provenance["results"]:
        dialect = engine["engine"]
        approval = (approvals / (dialect + ".json")).read_bytes()
        database = approvals / (dialect + ".sqlite3")
        assert hashlib.sha256(approval).hexdigest() == engine["exportSha256"]
        assert hashlib.sha256(database.read_bytes()).hexdigest() == engine["databaseSha256"]
        with sqlite3.connect(database.as_uri() + "?mode=ro", uri=True) as db:
            exported = export_inventory(db, engine["tenantId"], engine["inventoryId"])
            assert codec.canonical(exported) == approval
        parent, parent_rows = None, {}
        for batch in sorted((root / dialect).glob("batch-*"), key=lambda path: int(path.name.split("-")[1])):
            raw = (batch / "manifest.json").read_bytes()
            proof = codec.digest(codec.load(raw))
            manifest = wire.manifest(raw, proof, engine["gatewayId"])
            assert manifest["parentProofId"] == parent
            assert manifest["streamEpoch"] == engine["streamEpoch"]
            assert manifest["integrityEpoch"] == engine["integrityEpoch"]
            assert (batch / "ops-approved-eligibility.json").read_bytes() == approval
            collections = {}
            page_count = 0
            for name in wire.COLLECTIONS:
                descriptor = manifest["collections"][name]
                fold = codec.FrozenRows(name, descriptor["count"], descriptor["root"])
                rows, previous = [], None
                for path in sorted(batch.glob(name + "-*.json"), key=lambda path: int(path.stem.rsplit("-", 1)[1])):
                    ordinal = path.stem.rsplit("-", 1)[1]
                    page = wire.page(path.read_bytes(), manifest, proof, name, ordinal)
                    page_count += 1
                    fold.consume(ordinal=page["ordinal"], rows=page["rows"], prefix_hash=page["prefixHash"], suffix_hash=page["suffixHash"], next_ordinal=page["nextOrdinal"])
                    for row in page["rows"]:
                        order = _sort_key(name, row)
                        assert previous is None or order > previous, (batch, name, "row order")
                        previous = order
                        rows.append(row)
                fold.require_complete()
                current = {_identity(name, row): codec.digest(row) for row in rows}
                assert len(current) == len(rows), (batch, name, "duplicate")
                for identity, hashed in parent_rows.get(name, {}).items():
                    assert current.get(identity) == hashed, (batch, name, "parent row changed", identity)
                parent_rows[name] = current
                collections[name] = rows
            graph.validate_core(manifest, collections)
            values.validate(manifest, collections)
            controls.validate(manifest, collections, authorizable=manifest["status"] == "complete")
            cutoffs = [row for row in collections["valueStates"] if row["triggerType"] == "cutoff" and row["batchSequence"] == manifest["batchSequence"]]
            assert cutoffs and all(row["mutationSequence"] == manifest["mutationThrough"] and row["controlGeneration"] == manifest["controlGeneration"] for row in cutoffs)
            index = codec.load((batch / "index.json").read_bytes())
            assert any(item["proofId"] == proof and item["status"] == manifest["status"] for item in index["items"])
            parent = proof
            results.append({"engine": dialect, "batch": manifest["batchSequence"], "status": manifest["status"],
                "proofId": proof, "pages": page_count, "collections": len(collections),
                "strictWire": True, "parentInclusion": True, "graphAndFinancial": True,
                "values": True, "controls": True, "productionEligibility": False, "financialGrants": 0,
                "files": {path.name: hashlib.sha256(path.read_bytes()).hexdigest() for path in sorted(batch.glob("*.json"))}})
            print(dialect, batch.name, manifest["status"], proof, "PASS", flush=True)
    switch_samples = sorted(root.parent.glob("s4-integrity-source-switch-*.json")) if include_source_switch else []
    for path in switch_samples:
        sample = codec.load(path.read_bytes())
        manifest, collections = sample["manifest"], sample["collections"]
        assert manifest["status"] == "gap" and sample["financialGrants"] == 0
        try:
            graph.validate_core(manifest, collections)
        except finance.FinanceV2Error as error:
            assert str(error) == "integrity_control_not_authorizable"
        else:
            raise AssertionError("broken source-switch epoch must not authorize")
        values.validate(manifest, collections)
        controls.validate(manifest, collections, authorizable=False)
        for row in collections["valueStates"]:
            if row["triggerType"] == "cutoff" and row["batchSequence"] == manifest["batchSequence"]:
                assert row["mutationSequence"] == manifest["mutationThrough"]
                assert row["controlGeneration"] == manifest["controlGeneration"]
        print(path.name, "zero-delta switch values/exact cutoff PASS; broken epoch authorization rejected", flush=True)
    return results


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--samples", type=Path, default=Path(__file__).with_name("integrity-v2-real"))
    parser.add_argument("--approvals", type=Path, required=True)
    parser.add_argument("--result", type=Path)
    parser.add_argument("--source-switch", action="store_true", help="also diagnose broken unsupported-source-switch samples")
    args = parser.parse_args()
    results = verify(args.samples.resolve(), args.approvals.resolve(), args.source_switch)
    if args.result:
        args.result.write_text(json.dumps(results, indent=2, sort_keys=True) + "\n", encoding="utf-8")
