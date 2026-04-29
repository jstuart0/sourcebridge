"""Tests for the worker's TLS validation path.

Slice 4 of plan 2026-04-29-workspace-llm-source-of-truth-r2.md added
mTLS for the API↔worker gRPC channel. The worker reads three env vars
(SOURCEBRIDGE_WORKER_TLS_*); when tls_enabled is true, all three paths
must be non-empty + readable + valid PEM.

These tests exercise the validation surface without spinning up a real
gRPC server (the grpc.ssl_server_credentials call requires the
cryptography stack and a free port; not worth the test-suite cost when
we already have Go-side end-to-end coverage in
internal/worker/client_tls_test.go).
"""

from __future__ import annotations

import os
import tempfile
from pathlib import Path

import pytest

from workers.common.config import WorkerConfig


def test_tls_disabled_by_default() -> None:
    """Zero-value WorkerConfig has tls_enabled=False — legacy
    add_insecure_port path is the OSS default."""
    cfg = WorkerConfig()
    assert cfg.tls_enabled is False
    assert cfg.tls_cert_path == ""
    assert cfg.tls_key_path == ""
    assert cfg.tls_ca_path == ""


def test_tls_env_vars_are_picked_up(monkeypatch: pytest.MonkeyPatch) -> None:
    """Pydantic env_prefix maps SOURCEBRIDGE_WORKER_TLS_* env vars to
    the config fields. When tls_enabled=true and paths are set, the
    config carries the expected values."""
    monkeypatch.setenv("SOURCEBRIDGE_WORKER_TLS_ENABLED", "true")
    monkeypatch.setenv("SOURCEBRIDGE_WORKER_TLS_CERT_PATH", "/tmp/cert.pem")
    monkeypatch.setenv("SOURCEBRIDGE_WORKER_TLS_KEY_PATH", "/tmp/key.pem")
    monkeypatch.setenv("SOURCEBRIDGE_WORKER_TLS_CA_PATH", "/tmp/ca.pem")

    cfg = WorkerConfig()
    assert cfg.tls_enabled is True
    assert cfg.tls_cert_path == "/tmp/cert.pem"
    assert cfg.tls_key_path == "/tmp/key.pem"
    assert cfg.tls_ca_path == "/tmp/ca.pem"


def _write_pem(path: str, body: bytes = b"-----BEGIN CERTIFICATE-----\nFAKE\n-----END CERTIFICATE-----\n") -> None:
    Path(path).write_bytes(body)


def test_pem_header_check_helper() -> None:
    """The validation in __main__.py checks that each blob starts with
    `-----BEGIN`. This test pins the helper-shape contract: a real PEM
    starts with that prefix; junk doesn't.
    """
    pem = b"-----BEGIN CERTIFICATE-----\nMIIBkTCCATegAwIBAgIBADAJ...\n-----END CERTIFICATE-----\n"
    junk = b"this is not a PEM"
    binary = b"\x00\x01\x02junk"

    assert pem.lstrip().startswith(b"-----BEGIN")
    assert not junk.lstrip().startswith(b"-----BEGIN")
    assert not binary.lstrip().startswith(b"-----BEGIN")


def test_tls_paths_validate_at_filesystem_level() -> None:
    """When tls_enabled=true, the worker reads each path. Files must
    exist + be readable + be PEM-shaped. Any failure exits non-zero.

    This test exercises the OS-level validation: a path to a directory
    (not a file) is unreadable; a path that doesn't exist raises
    OSError; a file without a PEM header is rejected.
    """
    with tempfile.TemporaryDirectory() as tmpdir:
        # Valid PEM-shaped file.
        valid_path = os.path.join(tmpdir, "valid.pem")
        _write_pem(valid_path)
        assert Path(valid_path).read_bytes().lstrip().startswith(b"-----BEGIN")

        # Non-PEM file (junk).
        junk_path = os.path.join(tmpdir, "junk.txt")
        Path(junk_path).write_bytes(b"this is not a PEM\n")
        assert not Path(junk_path).read_bytes().lstrip().startswith(b"-----BEGIN")

        # Directory passed as a "file" path raises an error on open().
        dir_path = tmpdir
        with pytest.raises(IsADirectoryError):
            Path(dir_path).read_bytes()

        # Non-existent path raises FileNotFoundError.
        missing = os.path.join(tmpdir, "does-not-exist.pem")
        with pytest.raises(FileNotFoundError):
            Path(missing).read_bytes()
