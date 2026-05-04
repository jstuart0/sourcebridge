"""Tests for workers.common.servicer_utils.resolve_provider_for_context."""

from __future__ import annotations

import pytest

from workers.common.llm.fake import FakeLLMProvider
from workers.common.servicer_utils import resolve_provider_for_context


# ---------------------------------------------------------------------------
# Minimal mock of grpc.aio.ServicerContext
# ---------------------------------------------------------------------------


class _MockContext:
    """Minimal gRPC servicer context that carries invocation metadata."""

    def __init__(self, metadata: dict[str, str] | None = None):
        self._metadata = list((metadata or {}).items())

    def invocation_metadata(self):
        return self._metadata


class _MockConfig:
    """Minimal WorkerConfig stand-in."""

    def __init__(
        self,
        *,
        llm_provider: str = "openai",
        llm_base_url: str = "http://localhost:11434",
        llm_api_key: str = "test-key",
        llm_model: str = "gpt-4",
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

    def model_copy(self, *, update: dict) -> "_MockConfig":
        """Pydantic-style model_copy for tests."""
        cfg = _MockConfig(
            llm_provider=update.get("llm_provider", self.llm_provider),
            llm_base_url=update.get("llm_base_url", self.llm_base_url),
            llm_api_key=update.get("llm_api_key", self.llm_api_key),
            llm_model=update.get("llm_model", self.llm_model),
            llm_draft_model=update.get("llm_draft_model", self.llm_draft_model),
            llm_timeout=update.get("llm_timeout", self.llm_timeout),
            llm_report_model=update.get("llm_report_model", self.llm_report_model),
        )
        return cfg


# ---------------------------------------------------------------------------
# Tests: default path (no overrides in metadata)
# ---------------------------------------------------------------------------


def test_default_path_returns_default_llm():
    """No overrides in metadata → default llm returned."""
    llm = FakeLLMProvider()
    context = _MockContext()
    provider, model = resolve_provider_for_context(llm, None, context)
    assert provider is llm
    assert model is None


def test_default_path_with_config():
    """No overrides, config present → default llm still returned."""
    llm = FakeLLMProvider()
    config = _MockConfig()
    context = _MockContext()
    provider, model = resolve_provider_for_context(llm, config, context)
    assert provider is llm
    assert model is None


# ---------------------------------------------------------------------------
# Tests: model-only override
# ---------------------------------------------------------------------------


def test_model_override_no_config():
    """x-sb-model present, no config → default llm returned with override model.

    resolve_llm_override returns a non-None override (model is not empty),
    but with config=None the function cannot build a fresh provider and falls
    back to the supplied llm with override.model.
    """
    llm = FakeLLMProvider()
    context = _MockContext({"x-sb-model": "claude-3-5-sonnet"})
    provider, model = resolve_provider_for_context(llm, None, context)
    assert provider is llm
    assert model == "claude-3-5-sonnet"


# ---------------------------------------------------------------------------
# Tests: fallback_llm path (report provider variant)
# ---------------------------------------------------------------------------


def test_fallback_llm_used_when_no_override():
    """No override in metadata + fallback_llm supplied → fallback returned."""
    default_llm = FakeLLMProvider()
    report_llm = FakeLLMProvider()
    context = _MockContext()
    provider, model = resolve_provider_for_context(default_llm, None, context, fallback_llm=report_llm)
    assert provider is report_llm
    assert model is None


def test_fallback_llm_uses_report_model_from_config():
    """Config has llm_report_model → fallback path returns it as model string."""
    default_llm = FakeLLMProvider()
    report_llm = FakeLLMProvider()
    config = _MockConfig(llm_report_model="gpt-4o")
    context = _MockContext()
    provider, model = resolve_provider_for_context(default_llm, config, context, fallback_llm=report_llm)
    assert provider is report_llm
    assert model == "gpt-4o"


def test_fallback_llm_model_override_wins_over_config_report_model():
    """x-sb-model override wins over config.llm_report_model when no config.

    When x-sb-model is sent without x-sb-llm-provider, resolve_llm_override
    still returns a non-None override (model is included in is_empty check).
    With no config the code returns (fallback_llm, override.model).
    """
    default_llm = FakeLLMProvider()
    report_llm = FakeLLMProvider()
    # No config: the full-override branch with config=None returns fallback_llm + model
    context = _MockContext({"x-sb-model": "claude-3-haiku"})
    provider, model = resolve_provider_for_context(default_llm, None, context, fallback_llm=report_llm)
    assert provider is report_llm
    assert model == "claude-3-haiku"


def test_no_fallback_llm_model_override_no_config():
    """No fallback_llm, no config, x-sb-model set → default llm, override model.

    When x-sb-model is set (override non-None) but config is None, the code
    cannot build a fresh provider and returns the default llm with override.model.
    """
    llm = FakeLLMProvider()
    context = _MockContext({"x-sb-model": "gpt-3.5-turbo"})
    provider, model = resolve_provider_for_context(llm, None, context)
    assert provider is llm
    assert model == "gpt-3.5-turbo"


# ---------------------------------------------------------------------------
# Tests: full LLM override (x-sb-llm-provider present)
# ---------------------------------------------------------------------------


def test_full_override_no_config_returns_default_llm():
    """Full LLM override present but no config → default llm, override model."""
    llm = FakeLLMProvider()
    context = _MockContext({"x-sb-llm-provider": "anthropic", "x-sb-model": "claude-3-5-sonnet"})
    provider, model = resolve_provider_for_context(llm, None, context)
    # No config → cannot build a fresh provider; falls back to llm
    assert provider is llm
    assert model == "claude-3-5-sonnet"


def test_full_override_no_config_fallback_llm_returned():
    """Full override + no config + fallback_llm → fallback returned."""
    default_llm = FakeLLMProvider()
    report_llm = FakeLLMProvider()
    context = _MockContext({"x-sb-llm-provider": "anthropic", "x-sb-model": "claude-3-5-sonnet"})
    provider, model = resolve_provider_for_context(default_llm, None, context, fallback_llm=report_llm)
    assert provider is report_llm
    assert model == "claude-3-5-sonnet"


# ---------------------------------------------------------------------------
# Tests: backward-compat wrappers on each servicer
# ---------------------------------------------------------------------------


def test_requirements_servicer_resolve_provider_wrapper():
    """RequirementsServicer._resolve_provider delegates to resolve_provider_for_context."""
    from workers.requirements.servicer import RequirementsServicer

    llm = FakeLLMProvider()
    svc = RequirementsServicer(llm)
    context = _MockContext({"x-sb-model": "gpt-4o-mini"})
    provider, model = svc._resolve_provider(context)
    assert provider is llm
    assert model == "gpt-4o-mini"


def test_reasoning_servicer_resolve_provider_wrapper():
    """ReasoningServicer._resolve_provider delegates to resolve_provider_for_context."""
    from workers.common.embedding.fake import FakeEmbeddingProvider
    from workers.reasoning.servicer import ReasoningServicer

    llm = FakeLLMProvider()
    emb = FakeEmbeddingProvider(dimension=1024)
    svc = ReasoningServicer(llm, emb)
    context = _MockContext()
    provider, model = svc._resolve_provider(context)
    assert provider is llm
    assert model is None


def test_knowledge_servicer_resolve_request_provider_wrapper():
    """KnowledgeServicer._resolve_request_provider delegates to resolve_provider_for_context."""
    from workers.common.embedding.fake import FakeEmbeddingProvider
    from workers.knowledge.servicer import KnowledgeServicer

    llm = FakeLLMProvider()
    emb = FakeEmbeddingProvider(dimension=1024)
    svc = KnowledgeServicer(llm, emb)
    context = _MockContext()
    provider, model = svc._resolve_request_provider(context)
    assert provider is llm
    assert model is None


def test_knowledge_servicer_resolve_report_provider_no_report_llm():
    """KnowledgeServicer._resolve_report_provider without report_llm → default llm."""
    from workers.common.embedding.fake import FakeEmbeddingProvider
    from workers.knowledge.servicer import KnowledgeServicer

    llm = FakeLLMProvider()
    emb = FakeEmbeddingProvider(dimension=1024)
    svc = KnowledgeServicer(llm, emb)
    context = _MockContext()
    provider, model = svc._resolve_report_provider(context)
    assert provider is llm
    assert model is None


def test_knowledge_servicer_resolve_report_provider_with_report_llm():
    """KnowledgeServicer._resolve_report_provider with report_llm → report_llm returned."""
    from workers.common.embedding.fake import FakeEmbeddingProvider
    from workers.knowledge.servicer import KnowledgeServicer

    llm = FakeLLMProvider()
    report_llm = FakeLLMProvider()
    emb = FakeEmbeddingProvider(dimension=1024)
    svc = KnowledgeServicer(llm, emb, report_llm=report_llm)
    context = _MockContext()
    provider, model = svc._resolve_report_provider(context)
    assert provider is report_llm
