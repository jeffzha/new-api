"""Detached OpenSSH signatures dedicated to external load evidence.

This profile is intentionally distinct from the main E2E report signer.  The
observer owns the private key; ``load_sse.py`` receives only a pinned public
allowed-signers file.
"""

from __future__ import annotations

import os
import subprocess
import tempfile
from pathlib import Path

from e2e_lib import (
    ConfigError,
    _read_file_reference,
    _ssh_keygen_path,
    _validate_ed25519_private_key,
    _write_private_temp,
    load_evidence_file,
)


OBSERVER_SIGNATURE_IDENTITY = "claw-load-observer"
OBSERVER_SIGNATURE_NAMESPACE = "claw-load-external-evidence-v1"
MAIN_SIGNATURE_IDENTITY = "claw-workbench-e2e"
MAX_SSH_KEY_BYTES = 64 * 1024
MAX_SSH_SIGNATURE_BYTES = 64 * 1024
MAX_EVIDENCE_BYTES = 1024 * 1024


def _allowed_signer_blob(ref: str, identity: str) -> tuple[str, str]:
    _, raw = _read_file_reference(ref, maximum=MAX_SSH_KEY_BYTES, restricted=False)
    try:
        lines = [
            line.strip()
            for line in raw.decode("utf-8").splitlines()
            if line.strip() and not line.lstrip().startswith("#")
        ]
    except UnicodeDecodeError as error:
        raise ConfigError("load observer allowed_signers is not valid UTF-8") from error
    if len(lines) != 1:
        raise ConfigError("load observer allowed_signers must contain exactly one signer")
    fields = lines[0].split()
    if not fields or fields[0] != identity or "," in fields[0]:
        raise ConfigError(f"allowed_signers must pin the exact identity {identity}")
    indices = [index for index, field in enumerate(fields) if field == "ssh-ed25519"]
    if len(indices) != 1 or indices[0] + 1 >= len(fields):
        raise ConfigError("load observer allowed_signers must pin one ssh-ed25519 key")
    blob = fields[indices[0] + 1]
    if not blob or any(character.isspace() for character in blob):
        raise ConfigError("load observer allowed_signers has an invalid public key")
    return "ssh-ed25519", blob


def _observer_private_public_blob(signing_key_ref: str) -> tuple[Path, bytes, str]:
    source_path, key_bytes = _read_file_reference(
        signing_key_ref,
        maximum=MAX_SSH_KEY_BYTES,
        restricted=os.name != "nt",
    )
    ssh_keygen = _ssh_keygen_path()
    with tempfile.TemporaryDirectory(prefix="claw-load-observer-key-") as directory:
        key_path = _validate_ed25519_private_key(
            source_path, key_bytes, ssh_keygen, Path(directory),
        )
        try:
            result = subprocess.run(
                [ssh_keygen, "-y", "-f", str(key_path)],
                stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=15,
                check=False,
            )
        except (OSError, subprocess.TimeoutExpired) as error:
            raise ConfigError("load observer private key validation failed") from error
        fields = result.stdout.decode("ascii", errors="replace").strip().split()
        if result.returncode != 0 or len(fields) < 2 or fields[0] != "ssh-ed25519":
            raise ConfigError("load observer signing key must be an OpenSSH Ed25519 private key")
        return source_path, key_bytes, fields[1]


def validate_observer_signing_key(
    signing_key_ref: str,
    observer_allowed_signers_ref: str,
    main_allowed_signers_ref: str,
) -> None:
    """Prove the observer private key matches its pin and differs from main E2E."""

    _, _, private_blob = _observer_private_public_blob(signing_key_ref)
    _, observer_blob = _allowed_signer_blob(
        observer_allowed_signers_ref, OBSERVER_SIGNATURE_IDENTITY,
    )
    _, main_blob = _allowed_signer_blob(main_allowed_signers_ref, MAIN_SIGNATURE_IDENTITY)
    if private_blob != observer_blob:
        raise ConfigError("load observer private key does not match its pinned public signer")
    if observer_blob == main_blob:
        raise ConfigError("load observer and main E2E must use different Ed25519 keys")


def validate_observer_verifier(
    observer_allowed_signers_ref: str,
    main_allowed_signers_ref: str,
) -> None:
    """Validate the load-runner public verifier without reading any private key."""

    _, observer_blob = _allowed_signer_blob(
        observer_allowed_signers_ref, OBSERVER_SIGNATURE_IDENTITY,
    )
    _, main_blob = _allowed_signer_blob(main_allowed_signers_ref, MAIN_SIGNATURE_IDENTITY)
    if observer_blob == main_blob:
        raise ConfigError("load observer and main E2E must use different Ed25519 keys")


def _detached_signature(evidence: bytes, signing_key_ref: str) -> bytes:
    source_path, key_bytes = _read_file_reference(
        signing_key_ref,
        maximum=MAX_SSH_KEY_BYTES,
        restricted=os.name != "nt",
    )
    ssh_keygen = _ssh_keygen_path()
    with tempfile.TemporaryDirectory(prefix="claw-load-observer-sign-") as directory:
        root = Path(directory)
        key_path = _validate_ed25519_private_key(source_path, key_bytes, ssh_keygen, root)
        message_path = root / "external-evidence.json"
        _write_private_temp(message_path, evidence)
        try:
            result = subprocess.run(
                [
                    ssh_keygen, "-Y", "sign", "-f", str(key_path),
                    "-n", OBSERVER_SIGNATURE_NAMESPACE, str(message_path),
                ],
                stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=15,
                check=False,
            )
        except (OSError, subprocess.TimeoutExpired) as error:
            raise ConfigError("load observer evidence signing failed") from error
        if result.returncode != 0:
            raise ConfigError("load observer evidence signing failed")
        signature = load_evidence_file(
            str(message_path) + ".sig", maximum=MAX_SSH_SIGNATURE_BYTES,
        )
    if not signature.startswith(b"-----BEGIN SSH SIGNATURE-----\n"):
        raise ConfigError("load observer did not produce an OpenSSH signature")
    return signature


def write_signed_observer_evidence(
    output: Path,
    content: str,
    signing_key_ref: str,
) -> None:
    """Publish signature first and evidence last so readers never see unsigned bytes."""

    evidence = content.encode("utf-8")
    if len(evidence) > MAX_EVIDENCE_BYTES:
        raise ConfigError("load observer evidence exceeds the signed artifact limit")
    signature = _detached_signature(evidence, signing_key_ref)
    output.parent.mkdir(parents=True, exist_ok=True)
    evidence_fd, evidence_name = tempfile.mkstemp(prefix=f".{output.name}.pending.", dir=output.parent)
    signature_path = output.with_name(output.name + ".sig")
    signature_fd, signature_name = tempfile.mkstemp(
        prefix=f".{signature_path.name}.pending.", dir=output.parent,
    )
    try:
        os.chmod(evidence_name, 0o600)
        os.chmod(signature_name, 0o600)
        with os.fdopen(evidence_fd, "wb") as handle:
            handle.write(evidence)
            handle.flush()
            os.fsync(handle.fileno())
        evidence_fd = -1
        with os.fdopen(signature_fd, "wb") as handle:
            handle.write(signature)
            handle.flush()
            os.fsync(handle.fileno())
        signature_fd = -1
        os.replace(signature_name, signature_path)
        os.replace(evidence_name, output)
        os.chmod(signature_path, 0o600)
        os.chmod(output, 0o600)
    finally:
        if evidence_fd >= 0:
            os.close(evidence_fd)
        if signature_fd >= 0:
            os.close(signature_fd)
        for pending in (evidence_name, signature_name):
            if os.path.exists(pending):
                os.unlink(pending)


def verify_observer_evidence(path_value: str, allowed_signers_ref: str) -> bytes:
    evidence = load_evidence_file(path_value, maximum=MAX_EVIDENCE_BYTES)
    signature = load_evidence_file(
        path_value + ".sig", maximum=MAX_SSH_SIGNATURE_BYTES,
    )
    _allowed_signer_blob(allowed_signers_ref, OBSERVER_SIGNATURE_IDENTITY)
    ssh_keygen = _ssh_keygen_path()
    _, allowed = _read_file_reference(
        allowed_signers_ref, maximum=MAX_SSH_KEY_BYTES, restricted=False,
    )
    with tempfile.TemporaryDirectory(prefix="claw-load-observer-verify-") as directory:
        root = Path(directory)
        allowed_path = root / "allowed_signers"
        signature_path = root / "external-evidence.json.sig"
        _write_private_temp(allowed_path, allowed)
        _write_private_temp(signature_path, signature)
        try:
            result = subprocess.run(
                [
                    ssh_keygen, "-Y", "verify", "-f", str(allowed_path),
                    "-I", OBSERVER_SIGNATURE_IDENTITY,
                    "-n", OBSERVER_SIGNATURE_NAMESPACE,
                    "-s", str(signature_path),
                ],
                input=evidence,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=15,
                check=False,
            )
        except (OSError, subprocess.TimeoutExpired) as error:
            raise ConfigError("load observer evidence signature verification failed") from error
    if result.returncode != 0:
        raise ConfigError("load observer evidence signature verification failed")
    return evidence
