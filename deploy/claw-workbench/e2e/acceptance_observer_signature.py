"""Detached OpenSSH signature profile for non-HTTP acceptance evidence."""

from __future__ import annotations

import tempfile
import subprocess
from pathlib import Path

from e2e_lib import (
    ConfigError,
    _read_file_reference,
    _ssh_keygen_path,
    _write_private_temp,
    load_evidence_file,
)


OBSERVER_IDENTITY = "claw-acceptance-observer"
OBSERVER_NAMESPACE = "claw-acceptance-observer-v1"
MAX_EVIDENCE_BYTES = 1024 * 1024
MAX_SIGNATURE_BYTES = 64 * 1024


def _validated_allowed_signers(ref: str) -> bytes:
    _, raw = _read_file_reference(ref, maximum=64 * 1024, restricted=False)
    try:
        lines = [line.strip() for line in raw.decode("utf-8").splitlines()
                 if line.strip() and not line.lstrip().startswith("#")]
    except UnicodeDecodeError as error:
        raise ConfigError("acceptance observer allowed_signers is not UTF-8") from error
    if len(lines) != 1:
        raise ConfigError("acceptance observer allowed_signers must contain exactly one signer")
    fields = lines[0].split()
    if (len(fields) < 3 or fields[0] != OBSERVER_IDENTITY or "," in fields[0]
            or fields[1] != "ssh-ed25519"):
        raise ConfigError("acceptance observer must pin one claw-acceptance-observer Ed25519 signer")
    return raw


def verify_acceptance_observer_evidence(path: str, allowed_signers_ref: str) -> bytes:
    evidence = load_evidence_file(path, maximum=MAX_EVIDENCE_BYTES)
    signature = load_evidence_file(path + ".sig", maximum=MAX_SIGNATURE_BYTES)
    allowed = _validated_allowed_signers(allowed_signers_ref)
    with tempfile.TemporaryDirectory(prefix="claw-acceptance-observer-verify-") as directory:
        root = Path(directory)
        allowed_path = root / "allowed_signers"
        signature_path = root / "evidence.sig"
        _write_private_temp(allowed_path, allowed)
        _write_private_temp(signature_path, signature)
        try:
            result = subprocess.run(
                [
                    _ssh_keygen_path(), "-Y", "verify", "-f", str(allowed_path),
                    "-I", OBSERVER_IDENTITY, "-n", OBSERVER_NAMESPACE,
                    "-s", str(signature_path),
                ],
                input=evidence,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=15,
                check=False,
            )
        except (OSError, subprocess.TimeoutExpired) as error:
            raise ConfigError("acceptance observer signature verification failed") from error
    if result.returncode != 0:
        raise ConfigError("acceptance observer signature verification failed")
    return evidence
