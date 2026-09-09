from __future__ import annotations

import hashlib
import importlib.util
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "provider_fingerprint.py"
SPEC = importlib.util.spec_from_file_location("provider_fingerprint", SCRIPT)
assert SPEC and SPEC.loader
fingerprint = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = fingerprint
SPEC.loader.exec_module(fingerprint)


class ProviderFingerprintTests(unittest.TestCase):
    def test_matches_length_prefixed_domain_separated_contract(self) -> None:
        domain = b"claw-control/tencent-secret-pair/v1"
        secret_id = b"id"
        secret_key = b"secret"
        digest = hashlib.sha256()
        for value in (domain, secret_id, secret_key):
            digest.update(str(len(value)).encode("ascii") + b":" + value + b"\n")
        self.assertEqual(
            "sha256:" + digest.hexdigest(),
            fingerprint.canonical(domain, secret_id, secret_key),
        )

    def test_reads_only_direct_named_regular_files_and_trims_entrypoint_newline(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "WORKBENCH_PROVIDER_APP_KEY").write_bytes(b"value\n")
            self.assertEqual(b"value", fingerprint._read(root, "WORKBENCH_PROVIDER_APP_KEY"))
            with self.assertRaises(fingerprint.FingerprintError):
                fingerprint._read(root, "../secret")
            (root / "WORKBENCH_PROVIDER_BAD").write_bytes(b"bad\x00value")
            with self.assertRaises(fingerprint.FingerprintError):
                fingerprint._read(root, "WORKBENCH_PROVIDER_BAD")


if __name__ == "__main__":
    unittest.main()
