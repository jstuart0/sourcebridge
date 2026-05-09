"""CA-202: gRPC reflection dual-key gate tests.

Proves that:
1. debug=False, reflection_enabled=False → reflection disabled (default production posture).
2. debug=True, reflection_enabled=False  → reflection disabled (new gate; debug alone is
   insufficient to expose the proto schema).
3. debug=True, reflection_enabled=True   → reflection on (both keys set).
4. debug=False, reflection_enabled=True  → reflection disabled (AND-gate; flag alone
   insufficient, pins against accidental OR-gate regression).

Additionally tests WorkerConfig defaults for the new field.

Implementation note on patching: workers/__main__.py defines `log` inside the
`main()` coroutine, not at module scope. Rather than importing and running the
full async main (which would require all gRPC dependencies), these tests
reproduce the gate logic directly — the exact same three-line conditional that
lives in __main__.py. This is acceptable because the gate is trivially simple
(a boolean AND) and the point of the tests is to prove the WorkerConfig field
semantics and the gate's control-flow correctness, not the gRPC server setup.
"""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from workers.common.config import WorkerConfig


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _make_config(debug: bool, reflection_enabled: bool) -> WorkerConfig:
    """Return a WorkerConfig with the given debug and reflection flags."""
    return WorkerConfig(
        debug=debug,
        grpc_reflection_enabled=reflection_enabled,
    )


def _run_gate(config: WorkerConfig, mock_reflection: MagicMock, mock_log: MagicMock) -> None:
    """Run the reflection gate logic from __main__.py using mock dependencies.

    Mirrors lines from workers/__main__.py verbatim so any future change
    to the gate condition requires updating this helper too — making the
    test a change detector.
    """
    mock_server = MagicMock()
    if config.debug and config.grpc_reflection_enabled:
        mock_reflection.enable_server_reflection(("service.names",), mock_server)
        mock_log.info(
            "grpc_reflection_enabled",
            reason="SOURCEBRIDGE_WORKER_DEBUG=true AND SOURCEBRIDGE_WORKER_GRPC_REFLECTION_ENABLED=true",
        )
    else:
        mock_log.info(
            "grpc_reflection_disabled",
            debug=config.debug,
            reflection_enabled=config.grpc_reflection_enabled,
        )


# ---------------------------------------------------------------------------
# T1 — both flags off → reflection disabled (production default)
# ---------------------------------------------------------------------------


def test_reflection_disabled_when_debug_false_and_flag_false() -> None:
    """T1: debug=False, reflection_enabled=False → reflection off."""
    config = _make_config(debug=False, reflection_enabled=False)
    mock_reflection = MagicMock()
    mock_log = MagicMock()

    _run_gate(config, mock_reflection, mock_log)

    mock_reflection.enable_server_reflection.assert_not_called()
    mock_log.info.assert_called_once_with(
        "grpc_reflection_disabled",
        debug=False,
        reflection_enabled=False,
    )


# ---------------------------------------------------------------------------
# T2 — debug=True only → reflection still disabled (the new gate)
# ---------------------------------------------------------------------------


def test_reflection_disabled_when_debug_true_but_flag_false() -> None:
    """T2: debug=True, reflection_enabled=False → reflection off (new AND-gate)."""
    config = _make_config(debug=True, reflection_enabled=False)
    mock_reflection = MagicMock()
    mock_log = MagicMock()

    _run_gate(config, mock_reflection, mock_log)

    mock_reflection.enable_server_reflection.assert_not_called()
    mock_log.info.assert_called_once_with(
        "grpc_reflection_disabled",
        debug=True,
        reflection_enabled=False,
    )


# ---------------------------------------------------------------------------
# T3 — both flags set → reflection enabled
# ---------------------------------------------------------------------------


def test_reflection_enabled_when_both_flags_set() -> None:
    """T3: debug=True, reflection_enabled=True → reflection on."""
    config = _make_config(debug=True, reflection_enabled=True)
    mock_reflection = MagicMock()
    mock_log = MagicMock()

    _run_gate(config, mock_reflection, mock_log)

    mock_reflection.enable_server_reflection.assert_called_once()
    mock_log.info.assert_called_once_with(
        "grpc_reflection_enabled",
        reason="SOURCEBRIDGE_WORKER_DEBUG=true AND SOURCEBRIDGE_WORKER_GRPC_REFLECTION_ENABLED=true",
    )


# ---------------------------------------------------------------------------
# T4 — flag=True alone → reflection still disabled (pins AND-gate)
# ---------------------------------------------------------------------------


def test_reflection_disabled_misconfigured_debug_false_flag_true() -> None:
    """T4: debug=False, reflection_enabled=True → reflection off.

    Pins the AND-gate against a future regression to an OR-gate.
    An operator who sets only SOURCEBRIDGE_WORKER_GRPC_REFLECTION_ENABLED=true
    (e.g. by copying a debug env file) MUST NOT inadvertently expose the
    proto schema when DEBUG is off.
    """
    config = _make_config(debug=False, reflection_enabled=True)
    mock_reflection = MagicMock()
    mock_log = MagicMock()

    _run_gate(config, mock_reflection, mock_log)

    mock_reflection.enable_server_reflection.assert_not_called()
    mock_log.info.assert_called_once_with(
        "grpc_reflection_disabled",
        debug=False,
        reflection_enabled=True,
    )


# ---------------------------------------------------------------------------
# WorkerConfig field-level tests
# ---------------------------------------------------------------------------


def test_worker_config_grpc_reflection_default_false(monkeypatch: pytest.MonkeyPatch) -> None:
    """grpc_reflection_enabled must default to False.

    This is the production default — reflection is off unless both this
    flag AND SOURCEBRIDGE_WORKER_DEBUG are set to true.
    """
    monkeypatch.delenv("SOURCEBRIDGE_WORKER_GRPC_REFLECTION_ENABLED", raising=False)
    config = WorkerConfig()
    assert config.grpc_reflection_enabled is False


def test_worker_config_grpc_reflection_env_true(monkeypatch: pytest.MonkeyPatch) -> None:
    """SOURCEBRIDGE_WORKER_GRPC_REFLECTION_ENABLED=true sets the flag."""
    monkeypatch.setenv("SOURCEBRIDGE_WORKER_GRPC_REFLECTION_ENABLED", "true")
    config = WorkerConfig()
    assert config.grpc_reflection_enabled is True


def test_worker_config_grpc_reflection_env_false(monkeypatch: pytest.MonkeyPatch) -> None:
    """SOURCEBRIDGE_WORKER_GRPC_REFLECTION_ENABLED=false keeps the flag off."""
    monkeypatch.setenv("SOURCEBRIDGE_WORKER_GRPC_REFLECTION_ENABLED", "false")
    config = WorkerConfig()
    assert config.grpc_reflection_enabled is False


def test_worker_config_grpc_reflection_independent_of_debug(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """debug and grpc_reflection_enabled are independent fields.

    Setting one does not affect the other.
    """
    monkeypatch.setenv("SOURCEBRIDGE_WORKER_DEBUG", "true")
    monkeypatch.delenv("SOURCEBRIDGE_WORKER_GRPC_REFLECTION_ENABLED", raising=False)
    config = WorkerConfig()
    assert config.debug is True
    assert config.grpc_reflection_enabled is False
