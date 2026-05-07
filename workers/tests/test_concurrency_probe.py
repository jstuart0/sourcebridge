"""Unit tests for workers.common.llm.concurrency_probe."""

from __future__ import annotations

import asyncio

import pytest

from workers.common.llm.concurrency_probe import (
    probe_concurrency,
    run_startup_probe,
)


class _SingleSlotBackend:
    """Simulates a serial (num_parallel=1) backend.

    Each call takes ~0.5s; concurrent calls are serialized so pair_time ≈ 2x.
    Uses asyncio.sleep so the event loop runs; real timing is controlled by
    a lock that forces serialization.
    """

    def __init__(self, call_time: float = 0.05) -> None:
        self._call_time = call_time
        self._lock = asyncio.Lock()
        self.call_count = 0

    async def call(self) -> float:
        async with self._lock:
            self.call_count += 1
            await asyncio.sleep(self._call_time)
        return self._call_time


class _UnboundedBackend:
    """Simulates an unbounded backend (num_parallel>=2).

    Concurrent calls all proceed in parallel so pair_time ≈ single_time.
    """

    def __init__(self, call_time: float = 0.05) -> None:
        self._call_time = call_time
        self.call_count = 0

    async def call(self) -> float:
        self.call_count += 1
        await asyncio.sleep(self._call_time)
        return self._call_time


class _ErrorBackend:
    """Always raises an exception."""

    async def call(self) -> float:
        raise RuntimeError("probe backend unavailable")


@pytest.mark.asyncio
async def test_probe_single_slot_detected() -> None:
    """Serial backend produces ratio >=1.6 → observed=1."""
    backend = _SingleSlotBackend(call_time=0.04)
    observed, confidence = await probe_concurrency(backend, declared=8)
    assert observed == 1, f"Expected 1 for serial backend, got {observed}"


@pytest.mark.asyncio
async def test_probe_unbounded_detected() -> None:
    """Parallel backend produces ratio <1.6 → observed=2."""
    backend = _UnboundedBackend(call_time=0.04)
    observed, confidence = await probe_concurrency(backend, declared=8)
    assert observed == 2, f"Expected 2 for parallel backend, got {observed}"


@pytest.mark.asyncio
async def test_probe_exception_returns_zero_low() -> None:
    """On exception, probe returns (0, 'low') — not a hard failure."""
    backend = _ErrorBackend()
    observed, confidence = await probe_concurrency(backend, declared=1)
    assert observed == 0
    assert confidence == "low"


@pytest.mark.asyncio
async def test_probe_call_count_warm_up() -> None:
    """Probe issues 4 total calls: 1 warm-up + 1 baseline + 2 parallel."""
    backend = _UnboundedBackend(call_time=0.01)
    await probe_concurrency(backend, declared=0)
    assert backend.call_count == 4


@pytest.mark.asyncio
async def test_run_startup_probe_logs_completion(capfd) -> None:
    """run_startup_probe completes without error for a working backend."""
    backend = _UnboundedBackend(call_time=0.01)
    # Should not raise.
    await run_startup_probe(backend, declared=8, provider_name="ollama", model="qwen3.5:9b")


@pytest.mark.asyncio
async def test_run_startup_probe_handles_error_backend() -> None:
    """run_startup_probe swallows exceptions from the probe."""
    backend = _ErrorBackend()
    # Should not raise even when backend always errors.
    await run_startup_probe(backend, declared=1, provider_name="ollama", model="test-model")
