"""Phase 1 unit tests for the concurrency gate foundations.

Tests verify:
  - URL normalization helper (Decision 1, v4)
  - Registry returns the same gate object for the same logical key
  - Host gates share a single semaphore across LLM + embedding for local
    providers (the Ollama OLLAMA_NUM_PARALLEL=1 correctness fix)
  - Per-kind gates are independent for frontier providers
  - openai-compatible defaults to host gating; can be overridden via env var

Real factory defaults used throughout (not toy URLs):
  Ollama LLM:       http://localhost:11434/v1  (workers/common/llm/config.py:81)
  Ollama embedding: http://localhost:11434      (workers/common/embedding/config.py:33)

Refs: CA-169 / plan v4 Phase 1 Verification list.
"""

from __future__ import annotations

import asyncio

import pytest

from workers.common.llm.concurrency import (
    ConcurrencyConfig,
    ConcurrencyGatedProvider,
    ProviderGate,
    ProviderGateRegistry,
    _normalize_host_key,
)

# ──────────────────────────────────────────────────────────────────────────────
# Helpers


def _registry(config: ConcurrencyConfig | None = None) -> ProviderGateRegistry:
    return ProviderGateRegistry(config or ConcurrencyConfig())


# ──────────────────────────────────────────────────────────────────────────────
# 1. _normalize_host_key


def test_normalize_host_key_strips_path_and_trailing_slash() -> None:
    assert _normalize_host_key("ollama", "http://localhost:11434/v1") == (
        "ollama",
        "http://localhost:11434",
    )
    assert _normalize_host_key("ollama", "http://localhost:11434/") == (
        "ollama",
        "http://localhost:11434",
    )
    assert _normalize_host_key("ollama", "http://localhost:11434") == (
        "ollama",
        "http://localhost:11434",
    )


def test_normalize_host_key_strips_query_and_fragment() -> None:
    assert _normalize_host_key("vllm", "http://localhost:8000/v1?foo=bar#x") == (
        "vllm",
        "http://localhost:8000",
    )


def test_normalize_host_key_empty_base_url() -> None:
    result = _normalize_host_key("ollama", None)
    assert result == ("ollama", "")
    result2 = _normalize_host_key("ollama", "")
    assert result2 == ("ollama", "")


def test_normalize_host_key_preserves_port() -> None:
    assert _normalize_host_key("openai-compatible", "http://192.168.1.10:8080/v1") == (
        "openai-compatible",
        "http://192.168.1.10:8080",
    )


# ──────────────────────────────────────────────────────────────────────────────
# 2. Registry returns same gate for same logical key


@pytest.mark.asyncio
async def test_provider_gate_registry_returns_same_gate_for_same_key() -> None:
    reg = _registry()
    gate_a = await reg.lookup("ollama", "http://localhost:11434/v1", "llm")
    gate_b = await reg.lookup("ollama", "http://localhost:11434/v1", "llm")
    # Both façades should share the same underlying _HostGate.
    assert gate_a._binding is gate_b._binding


# ──────────────────────────────────────────────────────────────────────────────
# 3. Host gate normalizes URL to origin (same gate for /v1 and bare host)


@pytest.mark.asyncio
async def test_host_gate_normalizes_url_to_origin() -> None:
    """Ollama LLM default and embedding default share one host gate."""
    reg = _registry()
    # Real factory defaults:
    #   LLM:       http://localhost:11434/v1  (llm/config.py:81)
    #   embedding: http://localhost:11434     (embedding/config.py:33)
    gate_llm = await reg.lookup("ollama", "http://localhost:11434/v1", "llm")
    gate_embed = await reg.lookup("ollama", "http://localhost:11434", "embedding")

    # Different kinds, but same normalized origin → same _HostGate binding.
    assert gate_llm._binding is gate_embed._binding


# ──────────────────────────────────────────────────────────────────────────────
# 4. Combined in-flight capped at 1 for real Ollama defaults with max_concurrent=1


@pytest.mark.asyncio
async def test_host_gate_caps_combined_in_flight_at_one_for_real_ollama_defaults() -> None:
    """Under max_concurrent=1 both LLM and embedding calls share the one slot."""
    config = ConcurrencyConfig(llm_max_concurrent={"ollama": 1})
    reg = _registry(config)

    gate_llm = await reg.lookup("ollama", "http://localhost:11434/v1", "llm")
    gate_embed = await reg.lookup("ollama", "http://localhost:11434", "embedding")

    peak_in_flight: list[int] = []
    barrier = asyncio.Event()

    async def hold_slot(gate: ProviderGate) -> None:
        async with gate.slot():
            peak_in_flight.append(gate_llm._binding._in_flight)
            barrier.set()
            # Hold the slot briefly so the other coroutine is forced to queue.
            await asyncio.sleep(0.02)

    # Start both concurrently; only one should run at a time.
    results = await asyncio.gather(
        hold_slot(gate_llm),
        hold_slot(gate_embed),
        return_exceptions=True,
    )
    assert all(r is None for r in results), results
    assert max(peak_in_flight) == 1, f"Peak in-flight was {max(peak_in_flight)}, expected 1"


# ──────────────────────────────────────────────────────────────────────────────
# 5. Host gate shared across all local provider kinds


@pytest.mark.asyncio
async def test_host_gate_shared_across_kinds_for_local_providers() -> None:
    """All five host-gated providers share within-provider host semaphore."""
    host_gated = [
        ("ollama", "http://localhost:11434/v1", "http://localhost:11434"),
        ("vllm", "http://localhost:8000/v1", "http://localhost:8000"),
        ("llama-cpp", "http://localhost:8080/v1", "http://localhost:8080"),
        ("sglang", "http://localhost:30000/v1", "http://localhost:30000"),
        ("lmstudio", "http://localhost:1234/v1", "http://localhost:1234"),
    ]
    reg = _registry()
    for provider, llm_url, embed_url in host_gated:
        gate_llm = await reg.lookup(provider, llm_url, "llm")
        gate_embed = await reg.lookup(provider, embed_url, "embedding")
        assert gate_llm._binding is gate_embed._binding, (
            f"{provider}: expected LLM and embedding to share a host gate"
        )


# ──────────────────────────────────────────────────────────────────────────────
# 6. Per-kind gates are independent for frontier providers


@pytest.mark.asyncio
async def test_per_kind_gates_independent_for_frontier_providers() -> None:
    """openai LLM and openai embedding use separate semaphores."""
    reg = _registry()
    gate_llm = await reg.lookup("openai", "https://api.openai.com/v1", "llm")
    gate_embed = await reg.lookup("openai", "https://api.openai.com/v1", "embedding")
    # Different bindings → independent semaphores → independent quotas.
    assert gate_llm._binding is not gate_embed._binding


# ──────────────────────────────────────────────────────────────────────────────
# 7. openai-compatible defaults to host gating


@pytest.mark.asyncio
async def test_openai_compatible_gating_default_is_host() -> None:
    """openai-compatible at the same host shares a gate across kinds."""
    reg = _registry()
    gate_llm = await reg.lookup("openai-compatible", "http://localhost:8000/v1", "llm")
    gate_embed = await reg.lookup("openai-compatible", "http://localhost:8000", "embedding")
    assert gate_llm._binding is gate_embed._binding


# ──────────────────────────────────────────────────────────────────────────────
# 8. openai-compatible flips to per-kind when overridden


@pytest.mark.asyncio
async def test_openai_compatible_gating_per_kind_when_overridden(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """SOURCEBRIDGE_LLM_PROVIDER_OPENAI_COMPATIBLE_GATING=per_kind flips behavior."""
    monkeypatch.setenv("SOURCEBRIDGE_LLM_PROVIDER_OPENAI_COMPATIBLE_GATING", "per_kind")
    config = ConcurrencyConfig.from_env()
    assert config.openai_compatible_gating == "per_kind"

    reg = _registry(config)
    gate_llm = await reg.lookup("openai-compatible", "https://api.some-service.com/v1", "llm")
    gate_embed = await reg.lookup("openai-compatible", "https://api.some-service.com/v1", "embedding")
    # per_kind → different bindings.
    assert gate_llm._binding is not gate_embed._binding


# ──────────────────────────────────────────────────────────────────────────────
# 9. Registry rejects lookup after close


@pytest.mark.asyncio
async def test_registry_rejects_lookup_after_close() -> None:
    reg = _registry()
    await reg.close()
    with pytest.raises(RuntimeError, match="closed"):
        await reg.lookup("ollama", "http://localhost:11434/v1", "llm")


# ──────────────────────────────────────────────────────────────────────────────
# 10. Registry close is idempotent


@pytest.mark.asyncio
async def test_registry_close_idempotent() -> None:
    reg = _registry()
    await reg.close()
    await reg.close()  # should not raise


# ──────────────────────────────────────────────────────────────────────────────
# 11. _GateBase raises on max_concurrent < 1


def test_gate_rejects_zero_max_concurrent() -> None:
    from workers.common.llm.concurrency import _HostGate

    with pytest.raises(ValueError, match="max_concurrent"):
        _HostGate(max_concurrent=0)


# ──────────────────────────────────────────────────────────────────────────────
# 12. ConcurrencyConfig.from_env rejects invalid values


def test_concurrency_config_from_env_rejects_invalid_rpm(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setenv("SOURCEBRIDGE_LLM_PROVIDER_OPENAI_RPM", "0")
    config = ConcurrencyConfig.from_env()
    # 0 is invalid; should be ignored (no entry added).
    assert "openai" not in config.rpm


def test_concurrency_config_from_env_rejects_zero_max_concurrent(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setenv("SOURCEBRIDGE_LLM_PROVIDER_OPENAI_MAX_CONCURRENT", "0")
    config = ConcurrencyConfig.from_env()
    assert "openai" not in config.llm_max_concurrent


# ──────────────────────────────────────────────────────────────────────────────
# 13. FakeLLMProvider fail-mode kwargs (Phase 1 smoke; full tests in Phase 3)


@pytest.mark.asyncio
async def test_fake_provider_raise_on_attempts() -> None:
    from workers.common.llm.fake import FakeLLMProvider

    exc = ValueError("injected")
    provider = FakeLLMProvider(raise_on_attempts=2, exc=exc)

    with pytest.raises(ValueError, match="injected"):
        await provider.complete("test")
    with pytest.raises(ValueError, match="injected"):
        await provider.complete("test")
    # Third call should succeed.
    response = await provider.complete("test")
    assert response.content


@pytest.mark.asyncio
async def test_fake_provider_responses_queue() -> None:
    from workers.common.llm.fake import FakeLLMProvider

    provider = FakeLLMProvider(responses=["hello world", RuntimeError("boom")])

    r1 = await provider.complete("x")
    assert r1.content == "hello world"

    with pytest.raises(RuntimeError, match="boom"):
        await provider.complete("x")

    # Queue exhausted; falls back to fixture dispatch.
    r3 = await provider.complete("x")
    assert r3.content  # non-empty fixture response


# ──────────────────────────────────────────────────────────────────────────────
# 14. ConcurrencyGatedProvider is pass-through in Phase 1 (kill-switch on,
#     sentinel-uncapped, tenacity no-op predicate)


@pytest.mark.asyncio
async def test_concurrency_gated_provider_passthrough_phase1() -> None:
    """With retry_max_attempts=1 and sentinel cap, the wrapper is transparent."""
    from workers.common.llm.fake import FakeLLMProvider

    config = ConcurrencyConfig(retry_max_attempts=1)
    reg = _registry(config)
    raw = FakeLLMProvider()

    gate = await reg.lookup("ollama", "http://localhost:11434/v1", "llm")
    wrapped = ConcurrencyGatedProvider(raw, gate, config)

    response = await wrapped.complete("Summarize this function")
    # Should return the fixture summary content.
    import json

    data = json.loads(response.content)
    assert "purpose" in data


@pytest.mark.asyncio
async def test_concurrency_gated_provider_stream_passthrough_phase1() -> None:
    from workers.common.llm.fake import FakeLLMProvider

    config = ConcurrencyConfig(retry_max_attempts=1)
    reg = _registry(config)
    raw = FakeLLMProvider()

    gate = await reg.lookup("ollama", "http://localhost:11434/v1", "llm")
    wrapped = ConcurrencyGatedProvider(raw, gate, config)

    chunks: list[str] = []
    async for chunk in wrapped.stream("Summarize this function"):
        chunks.append(chunk)
    assert len(chunks) > 0
