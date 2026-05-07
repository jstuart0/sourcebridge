"""Tests for ReasoningServicer.GetLLMGateSnapshot (Phase 7).

Verifies:
  - A host-gated registry with active LLM + embedding kind counters returns
    two rows sharing max_concurrent and tokens_per_second.
  - A fresh registry with no gates returns an empty list.
  - No gate_registry wired → empty response (not an error).

Refs: CA-169 / plan v4 Phase 7 Verification list.
"""

from __future__ import annotations

import pytest
import pytest_asyncio  # noqa: F401
from reasoning.v1 import reasoning_pb2

from workers.common.embedding.fake import FakeEmbeddingProvider
from workers.common.llm.concurrency import ConcurrencyConfig, ProviderGateRegistry
from workers.reasoning.servicer import ReasoningServicer

# ──────────────────────────────────────────────────────────────────────────────
# Fixtures


@pytest.fixture
def embedding():
    return FakeEmbeddingProvider(dimension=1024)


@pytest_asyncio.fixture
async def gate_registry():
    """Fresh ProviderGateRegistry with a very long aggregator interval so it
    never fires unexpectedly during tests."""
    cfg = ConcurrencyConfig(metrics_interval_seconds=9999.0)
    reg = ProviderGateRegistry(cfg)
    yield reg
    await reg.close()


@pytest.fixture
def servicer_with_registry(llm, embedding, gate_registry):
    return ReasoningServicer(llm, embedding, gate_registry=gate_registry)


@pytest.fixture
def servicer_no_registry(llm, embedding):
    """Servicer without a gate_registry (kill-switch-off scenario)."""
    return ReasoningServicer(llm, embedding)


# ──────────────────────────────────────────────────────────────────────────────
# Tests


@pytest.mark.asyncio
async def test_get_llm_gate_snapshot_returns_all_active_gates(
    servicer_with_registry, gate_registry, context
):
    """Ollama host gate with LLM + embedding kind counters → two rows sharing
    max_concurrent and tokens_per_second.

    Plan Phase 7 Verification: "an Ollama daemon with active LLM and embedding
    kinds shows two rows sharing one max_concurrent and one tokens_per_second."
    """
    # Register both LLM and embedding kind counters for the same Ollama host.
    await gate_registry.lookup("ollama", "http://localhost:11434/v1", "llm")
    await gate_registry.lookup("ollama", "http://localhost:11434", "embedding")

    request = reasoning_pb2.GetLLMGateSnapshotRequest()
    response = await servicer_with_registry.GetLLMGateSnapshot(request, context)

    assert isinstance(response, reasoning_pb2.GetLLMGateSnapshotResponse)
    entries = list(response.gates)
    assert len(entries) == 2, f"expected 2 gate entries, got {len(entries)}: {entries}"

    # Both rows should share the same provider and normalized origin.
    assert all(e.provider == "ollama" for e in entries)
    assert all(e.base_url_normalized == "http://localhost:11434" for e in entries)

    # Kinds must be distinct (one llm, one embedding).
    kinds = {e.kind for e in entries}
    assert kinds == {"llm", "embedding"}, f"expected {{llm, embedding}}, got {kinds}"

    # Both rows share the same max_concurrent (Decision 5b: host gate is one semaphore).
    max_concurrent_values = {e.max_concurrent for e in entries}
    assert len(max_concurrent_values) == 1, (
        f"host-gated rows must share max_concurrent, got {max_concurrent_values}"
    )

    # Both rows share the same tokens_per_second (from the shared ring buffer).
    tps_values = {e.tokens_per_second for e in entries}
    assert len(tps_values) == 1, (
        f"host-gated rows must share tokens_per_second, got {tps_values}"
    )

    # All numeric fields are non-negative.
    for e in entries:
        assert e.in_flight >= 0
        assert e.queued >= 0
        assert e.retries_since_start >= 0
        assert e.recent_429_count >= 0
        assert e.tokens_per_second >= 0.0


@pytest.mark.asyncio
async def test_get_llm_gate_snapshot_empty_when_no_gates(
    servicer_with_registry, context
):
    """A fresh registry with no lookups yet → empty gate list."""
    request = reasoning_pb2.GetLLMGateSnapshotRequest()
    response = await servicer_with_registry.GetLLMGateSnapshot(request, context)

    assert isinstance(response, reasoning_pb2.GetLLMGateSnapshotResponse)
    assert len(response.gates) == 0, (
        f"expected empty gates for fresh registry, got {len(response.gates)}"
    )


@pytest.mark.asyncio
async def test_get_llm_gate_snapshot_no_registry_returns_empty(
    servicer_no_registry, context
):
    """No gate_registry wired (kill-switch-off) → empty response without error."""
    request = reasoning_pb2.GetLLMGateSnapshotRequest()
    response = await servicer_no_registry.GetLLMGateSnapshot(request, context)

    assert isinstance(response, reasoning_pb2.GetLLMGateSnapshotResponse)
    assert len(response.gates) == 0, (
        f"expected empty gates when no registry, got {len(response.gates)}"
    )
