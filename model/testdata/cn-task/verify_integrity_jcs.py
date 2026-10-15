"""Cross-check immutable wire vectors with rfc8785==0.1.4; no custom canonicalizer."""

import base64
import hashlib
import json
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
