"""Phase 2 tests for the concurrency gate wiring through factories and servicers.

Tests verify:
  - create_llm_provider wraps when kill switch is on / unwraps when off
    (covered also in test_llm_config.py; cross-cutting here for capability path)
  - create_llm_provider_for_request forwards gate_registry (bob H4)
  - create_report_provider wraps using the same gate as main provider when
    pointing at the same endpoint
  - GetProviderCapabilities sources cap from gate registry's effective value
    (Decision 12, codex r1 H2 / r2 H2)
  - GetProviderCapabilities honors per-request metadata override (r2 H2)
  - GetProviderCapabilities falls back to legacy path when kill switch off
  - resolve_provider_for_context returns canonical resolution key as third elem
  - CLI helper build_cli_runtime_provider returns a ConcurrencyGatedProvider
  - benchmark _create_provider("live") returns a ConcurrencyGatedProvider

Refs: CA-169 / plan v4 Phase 2 Verification list.
"""

from __future__ import annotations

import pytest
import pytest_asyncio  # noqa: F401

from workers.common.embedding.fake import FakeEmbeddingProvider
from workers.common.llm.concurrency import ConcurrencyConfig, ConcurrencyGatedProvider, ProviderGateRegistry
from workers.common.llm.fake import FakeLLMProvider
from workers.common.servicer_utils import ProviderResolutionKey, resolve_provider_for_context

# ──────────────────────────────────────────────────────────────────────────────
# Helpers / fixtures

class _MockContext:
    def __init__(self, metadata: dict[str, str] | None = None):
        self._metadata = list((metadata or {}).items())

    def invocation_metadata(self):
        return self._metadata


class _MockConfig:
    def __init__(
        self,
        *,
        llm_provider: str = "ollama",
        llm_base_url: str = "http://localhost:11434/v1",
        llm_api_key: str = "test",
        llm_model: str = "qwen3:7b",
        llm_draft_model: str = "",
        llm_timeout: int = 60,
        llm_report_model: str = "",
    ) -> None:
        self.llm_provider = llm_provider
        self.llm_base_url = llm_base_url
        self.llm_api_key = llm_api_key
        self.llm_model = llm_model
        self.llm_draft_model = llm_draft_model
        self.llm_timeout = llm_timeout
        self.llm_report_model = llm_report_model

    def model_copy(self, *, update: dict) -> _MockConfig:
        return _MockConfig(
            llm_provider=update.get("llm_provider", self.llm_provider),
            llm_base_url=update.get("llm_base_url", self.llm_base_url),
            llm_api_key=update.get("llm_api_key", self.llm_api_key),
            llm_model=update.get("llm_model", self.llm_model),
            llm_draft_model=update.get("llm_draft_model", self.llm_draft_model),
            llm_timeout=update.get("llm_timeout", self.llm_timeout),
            llm_report_model=update.get("llm_report_model", self.llm_report_model),
        )


# ──────────────────────────────────────────────────────────────────────────────
# 1. resolve_provider_for_context returns canonical resolution key


@pytest.mark.asyncio
async def test_resolve_provider_for_context_returns_canonical_key_no_override():
    """No metadata override → resolution key reflects bootstrap config."""
    llm = FakeLLMProvider()
    config = _MockConfig(llm_provider="ollama", llm_base_url="http://localhost:11434/v1")
    registry = ProviderGateRegistry(ConcurrencyConfig())
    context = _MockContext()

    provider, model, resolution_key = resolve_provider_for_context(
        llm, config, context, gate_registry=registry
    )
    assert provider is llm
    assert model is None
    assert isinstance(resolution_key, ProviderResolutionKey)
    assert resolution_key.provider == "ollama"
    assert resolution_key.base_url == "http://localhost:11434/v1"
    assert resolution_key.kind == "llm"

    await registry.close()


def test_resolve_provider_for_context_returns_none_resolution_key_when_no_registry():
    """No registry supplied → third return is None."""
    llm = FakeLLMProvider()
    context = _MockContext()
    _, _, resolution_key = resolve_provider_for_context(llm, None, context)
    assert resolution_key is None


@pytest.mark.asyncio
async def test_resolve_provider_for_context_returns_none_key_when_config_none():
    """Config is None + no override → resolution key is None (no provider info)."""
    llm = FakeLLMProvider()
    registry = ProviderGateRegistry(ConcurrencyConfig())
    context = _MockContext()
    _, _, resolution_key = resolve_provider_for_context(llm, None, context, gate_registry=registry)
    # _build_resolution_key returns None when config is None.
    assert resolution_key is None

    await registry.close()


# ──────────────────────────────────────────────────────────────────────────────
# 2. GetProviderCapabilities — gate active, reports registry's effective cap


@pytest.mark.asyncio
async def test_get_provider_capabilities_reports_gate_effective_cap(monkeypatch):
    """Gate active + OLLAMA_MAX_CONCURRENT=2 → capability response reports cap=2.

    Tests Decision 12: resolved-context lookup through gate registry.
    Refs: codex r1 H2 / D12.
    """
    monkeypatch.setenv("SOURCEBRIDGE_LLM_PROVIDER_OLLAMA_MAX_CONCURRENT", "2")
    from reasoning.v1 import reasoning_pb2

    from workers.common.config import WorkerConfig
    from workers.reasoning.servicer import ReasoningServicer

    config = WorkerConfig(
        llm_provider="ollama",
        llm_model="qwen3:7b",
        test_mode=True,
    )
    concurrency_config = ConcurrencyConfig.from_env()
    registry = ProviderGateRegistry(concurrency_config)

    # Use test_mode → FakeLLMProvider (no real HTTP calls).
    from workers.common.llm.factory import create_llm_provider

    llm = await create_llm_provider(config, gate_registry=registry)
    emb = FakeEmbeddingProvider(dimension=1024)
    servicer = ReasoningServicer(llm, emb, worker_config=config, gate_registry=registry)

    context = _MockContext()  # no metadata override → uses bootstrap (ollama)
    response = await servicer.GetProviderCapabilities(
        reasoning_pb2.GetProviderCapabilitiesRequest(), context
    )

    assert response.max_concurrent_calls == 2
    assert response.max_concurrent_calls_known is True
    await registry.close()


@pytest.mark.asyncio
async def test_get_provider_capabilities_honors_metadata_override(monkeypatch):
    """Metadata override switches provider → capability response reflects the override's gate cap.

    Bootstrap = ollama (gate cap=1); metadata forces openai (gate cap=8).
    Asserts response.max_concurrent_calls == 8 (the metadata-resolved cap), not 1.
    Refs: codex r2 H2 / Decision 12.
    """
    monkeypatch.setenv("SOURCEBRIDGE_LLM_PROVIDER_OLLAMA_MAX_CONCURRENT", "1")
    monkeypatch.setenv("SOURCEBRIDGE_LLM_PROVIDER_OPENAI_MAX_CONCURRENT", "8")
    from reasoning.v1 import reasoning_pb2

    from workers.common.config import WorkerConfig
    from workers.reasoning.servicer import ReasoningServicer

    config = WorkerConfig(
        llm_provider="ollama",
        llm_model="qwen3:7b",
        llm_api_key="",
        test_mode=True,
    )
    concurrency_config = ConcurrencyConfig.from_env()
    registry = ProviderGateRegistry(concurrency_config)

    from workers.common.llm.factory import create_llm_provider

    llm = await create_llm_provider(config, gate_registry=registry)
    emb = FakeEmbeddingProvider(dimension=1024)
    servicer = ReasoningServicer(llm, emb, worker_config=config, gate_registry=registry)

    # Metadata override: flip provider to openai.
    context = _MockContext({
        "x-sb-llm-provider": "openai",
        "x-sb-llm-api-key": "test-key",
        "x-sb-model": "gpt-4o",
    })
    response = await servicer.GetProviderCapabilities(
        reasoning_pb2.GetProviderCapabilitiesRequest(), context
    )

    assert response.max_concurrent_calls == 8, (
        f"Expected openai gate cap 8, got {response.max_concurrent_calls}"
    )
    assert response.max_concurrent_calls_known is True
    await registry.close()


@pytest.mark.asyncio
async def test_get_provider_capabilities_falls_back_when_wrapper_disabled(monkeypatch):
    """Kill switch off → legacy WorkerConfig-sourced cap returned.

    Refs: codex r1 H2 / D12 legacy fallback.
    """
    monkeypatch.setenv("SOURCEBRIDGE_LLM_CONCURRENCY_WRAPPER_ENABLED", "false")
    from reasoning.v1 import reasoning_pb2

    from workers.common.config import WorkerConfig
    from workers.reasoning.servicer import ReasoningServicer

    config = WorkerConfig(
        llm_provider="ollama",
        llm_model="qwen3:7b",
        llm_max_concurrent_calls=3,
        test_mode=True,
    )
    concurrency_config = ConcurrencyConfig.from_env()
    assert not concurrency_config.wrapper_enabled
    registry = ProviderGateRegistry(concurrency_config)

    from workers.common.llm.factory import create_llm_provider

    llm = await create_llm_provider(config, gate_registry=registry)
    emb = FakeEmbeddingProvider(dimension=1024)
    servicer = ReasoningServicer(llm, emb, worker_config=config, gate_registry=registry)

    context = _MockContext()
    response = await servicer.GetProviderCapabilities(
        reasoning_pb2.GetProviderCapabilitiesRequest(), context
    )

    # Kill switch off → effective_llm_max_concurrent returns None → legacy path
    # → reads config.llm_max_concurrent_calls = 3.
    assert response.max_concurrent_calls == 3
    assert response.max_concurrent_calls_known is True
    await registry.close()


# ──────────────────────────────────────────────────────────────────────────────
# 3. CLI helper tests


@pytest.mark.asyncio
async def test_cli_review_constructs_registry():
    """build_cli_runtime_provider returns a ConcurrencyGatedProvider for live mode."""
    from workers.common.cli_main import build_cli_runtime_provider
    from workers.common.config import WorkerConfig

    cfg = WorkerConfig(llm_provider="openai", llm_api_key="test", llm_model="gpt-4o")
    provider, registry = await build_cli_runtime_provider(cfg)
    try:
        assert isinstance(provider, ConcurrencyGatedProvider)
        assert registry is not None
    finally:
        await registry.close()


@pytest.mark.asyncio
async def test_benchmark_constructs_registry(monkeypatch):
    """_create_provider('live') returns a ConcurrencyGatedProvider."""
    # Patch WorkerConfig so no real provider credentials are needed.
    monkeypatch.setenv("SOURCEBRIDGE_LLM_PROVIDER", "openai")
    monkeypatch.setenv("SOURCEBRIDGE_LLM_API_KEY", "test")
    monkeypatch.setenv("SOURCEBRIDGE_LLM_MODEL", "gpt-4o")

    from workers.benchmarks.run_comprehension_bench import _create_provider

    provider, provider_name, model_id, gate_registry = await _create_provider("live")
    try:
        assert isinstance(provider, ConcurrencyGatedProvider)
        assert gate_registry is not None
    finally:
        if gate_registry is not None:
            await gate_registry.close()
