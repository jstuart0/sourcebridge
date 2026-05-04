"""SEC-11: WorkerConfig.debug field — gRPC reflection gate.

Proves that:
  1. The default value of WorkerConfig.debug is False (reflection off in prod).
  2. SOURCEBRIDGE_WORKER_DEBUG=true sets debug=True (reflection on in dev).
  3. SOURCEBRIDGE_WORKER_DEBUG=false keeps debug=False.
  4. The field is not accidentally tied to test_mode.
"""

import os

import pytest

from workers.common.config import WorkerConfig


def test_debug_default_is_false(monkeypatch: pytest.MonkeyPatch) -> None:
    """WorkerConfig.debug must default to False.

    This is the production default — reflection is off unless explicitly
    enabled via SOURCEBRIDGE_WORKER_DEBUG=true.
    """
    monkeypatch.delenv("SOURCEBRIDGE_WORKER_DEBUG", raising=False)
    config = WorkerConfig()
    assert config.debug is False


def test_debug_env_true(monkeypatch: pytest.MonkeyPatch) -> None:
    """SOURCEBRIDGE_WORKER_DEBUG=true enables debug mode."""
    monkeypatch.setenv("SOURCEBRIDGE_WORKER_DEBUG", "true")
    config = WorkerConfig()
    assert config.debug is True


def test_debug_env_false_explicit(monkeypatch: pytest.MonkeyPatch) -> None:
    """SOURCEBRIDGE_WORKER_DEBUG=false keeps debug mode off."""
    monkeypatch.setenv("SOURCEBRIDGE_WORKER_DEBUG", "false")
    config = WorkerConfig()
    assert config.debug is False


def test_debug_env_1(monkeypatch: pytest.MonkeyPatch) -> None:
    """SOURCEBRIDGE_WORKER_DEBUG=1 also enables debug mode (pydantic bool)."""
    monkeypatch.setenv("SOURCEBRIDGE_WORKER_DEBUG", "1")
    config = WorkerConfig()
    assert config.debug is True


def test_debug_independent_of_test_mode(monkeypatch: pytest.MonkeyPatch) -> None:
    """debug and test_mode are independent fields; setting one does not
    affect the other."""
    monkeypatch.delenv("SOURCEBRIDGE_WORKER_DEBUG", raising=False)
    monkeypatch.setenv("SOURCEBRIDGE_WORKER_TEST_MODE", "true")
    config = WorkerConfig()
    assert config.test_mode is True
    assert config.debug is False


def test_debug_does_not_affect_test_mode(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("SOURCEBRIDGE_WORKER_DEBUG", "true")
    monkeypatch.delenv("SOURCEBRIDGE_WORKER_TEST_MODE", raising=False)
    monkeypatch.delenv("SOURCEBRIDGE_TEST_MODE", raising=False)
    config = WorkerConfig()
    assert config.debug is True
    assert config.test_mode is False
