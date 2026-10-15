"""Cross-check immutable wire vectors with rfc8785==0.1.4; no custom canonicalizer."""

import base64
import hashlib
import json
from decimal import Decimal
from pathlib import Path

import rfc8785


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON key")
        result[key] = value
    return result


vectors = json.loads(Path(__file__).with_name("integrity-v2-jcs-vectors.json").read_text(encoding="utf-8"))
for vector in vectors["vectors"]:
    raw = base64.b64decode(vector["inputBase64"], validate=True)
    body = json.loads(raw.decode("utf-8"), object_pairs_hook=unique_object)
    actual = rfc8785.dumps(body)
    assert actual == base64.b64decode(vector["canonicalBase64"], validate=True), vector["id"]
    assert "sha256:" + hashlib.sha256(actual).hexdigest() == vector["sha256"], vector["id"]
print(f"PASS Python rfc8785: {len(vectors['vectors'])} shared canonical bytes/hash vectors")
shared = json.loads(Path(__file__).with_name("integrity-v2-shared-jcs-vectors.json").read_text(encoding="utf-8"))
for vector in shared["vectors"]:
    body = json.loads(vector["inputJson"], object_pairs_hook=unique_object)
    assert rfc8785.dumps(body) == vector["canonical"].encode("utf-8"), vector["name"]
print(f"PASS Python rfc8785: {len(shared['vectors'])} unchanged S5 RFC vectors")

states = json.loads(Path(__file__).with_name("integrity-v2-value-state-vectors.json").read_text(encoding="utf-8"))
for state in states["cases"]:
    d = Decimal
    lot = state["lot"]
    valid = True
    available = d(state["unknownQuota"])
    if lot is not None:
        valid = d(lot["purchasedQuota"]) == sum(d(lot[k]) for k in ("remainingQuota", "heldQuota", "retiredQuota")) + d(state["allocationQuota"])
        valid &= d(lot["purchasedValue"]) == sum(d(lot[k]) for k in ("remainingValue", "heldValue", "retiredValue")) + d(state["allocationValue"])
        valid &= d(lot["heldQuota"]) == sum((d(h["quota"]) for h in state["holds"] if h["state"] == "held"), d(0))
        valid &= d(lot["heldValue"]) == sum((d(h["value"]) for h in state["holds"] if h["state"] == "held"), d(0))
        if not lot["isolated"]:
            available += d(lot["remainingQuota"])
    valid &= available == d(state["balance"]) + d(state["walletHeldQuota"]) + d(state["taskHeldQuota"])
    assert valid == state["valid"], state["id"]
print(f"PASS Python Decimal: {len(states['cases'])} native value-state arithmetic vectors")
