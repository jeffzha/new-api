"""Print conformance fixtures using the pinned mature Python implementation."""

import base64
import hashlib
import json

import rfc8785


samples = [
    ("numbers", r'{"numbers":[333333333.33333329,1E30,4.50,2e-3,1e-27,-0,1e-6,1e-7,1e20,1e21]}'),
    ("unicode-key-order", r'{"\u20ac":"euro","\r":"return","\ufb33":"hebrew","1":"one","\ud83d\ude00":"face","\u0080":"control","\u00f6":"latin"}'),
    ("strings", r'{"s":"\u2028<>&/\b\t\n\f\r\u0000\u4e2d\ud83d\ude00"}'),
    ("snapshots", r'{"paymentSnapshot":{"payment_reference":"receipt-1","currency_code":"USD","actual_money":"1.25"},"quotaConversionSnapshot":{"schema_version":1,"basis_field":"topup.amount","basis_value":"125","calculation":"identity","credited_quota":"125"}}'),
    ("sequence-strings", r'{"mutationThrough":"9007199254740993","publicationThrough":"9223372036854775807"}'),
    ("native-chain-wallet", r'["cn-native-chain-v2","gateway-cn","test-cn","196","wallet","request-A"]'),
    ("native-chain-unicode", r'["cn-native-chain-v2","gateway-cn","test-cn","196","topup","order-<>&-\u4e2d\ud83d\ude00"]'),
    ("native-lineage-wallet", r'["cn-native-lineage-v2","7b4865805493268eb6823b6acd230eeb192c36646708366006c3d5cfb93b12d2"]'),
]
vectors = []
for name, raw in samples:
    canonical = rfc8785.dumps(json.loads(raw))
    vectors.append({
        "id": name,
        "inputBase64": base64.b64encode(raw.encode("utf-8")).decode("ascii"),
        "canonicalBase64": base64.b64encode(canonical).decode("ascii"),
        "sha256": "sha256:" + hashlib.sha256(canonical).hexdigest(),
    })
print(json.dumps({
    "schema": "cn-integrity-jcs-vectors-v2",
    "goImplementation": "github.com/cyberphone/json-canonicalization v0.0.0-20261011152517-32a48195b9b2",
    "pythonImplementation": "rfc8785==0.1.4",
    "vectors": vectors,
}, indent=2))
