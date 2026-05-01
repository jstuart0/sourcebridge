import pytest

from workers.common.config import SUPPORTED_LLM_PROVIDERS, WorkerConfig
from workers.common.grpc_metadata import RuntimeLLMOverride, resolve_llm_override
from workers.common.llm.config import (
    _resolve_disable_thinking,
    create_llm_provider,
    create_llm_provider_for_request,
)


def test_resolve_disable_thinking_prefers_worker_disable(monkeypatch):
    monkeypatch.setenv("SOURCEBRIDGE_WORKER_LLM_DISABLE_THINKING", "true")
    monkeypatch.setenv("SOURCEBRIDGE_LLM_ENABLE_THINKING", "true")

    assert _resolve_disable_thinking() is True


def test_resolve_disable_thinking_report_scope_can_override(monkeypatch):
    monkeypatch.delenv("SOURCEBRIDGE_WORKER_LLM_DISABLE_THINKING", raising=False)
    monkeypatch.delenv("SOURCEBRIDGE_WORKER_LLM_ENABLE_THINKING", raising=False)
    monkeypatch.setenv("SOURCEBRIDGE_WORKER_LLM_REPORT_ENABLE_THINKING", "false")

    assert _resolve_disable_thinking(report=True) is True


def test_resolve_disable_thinking_global_enable_disables_flag(monkeypatch):
    monkeypatch.delenv("SOURCEBRIDGE_WORKER_LLM_DISABLE_THINKING", raising=False)
    monkeypatch.delenv("SOURCEBRIDGE_WORKER_LLM_ENABLE_THINKING", raising=False)
    monkeypatch.delenv("SOURCEBRIDGE_WORKER_LLM_REPORT_ENABLE_THINKING", raising=False)
    monkeypatch.setenv("SOURCEBRIDGE_LLM_ENABLE_THINKING", "true")

    assert _resolve_disable_thinking() is False


class _FakeMetadataContext:
    def __init__(self, pairs: list[tuple[str, str]]) -> None:
        self._pairs = pairs

    def invocation_metadata(self):
        return self._pairs


def test_resolve_llm_override_parses_timeout_seconds():
    """The API sets x-sb-llm-timeout-seconds from LLMConfig.TimeoutSecs so
    admins can tune the HTTP timeout without a worker restart."""
    ctx = _FakeMetadataContext(
        [
            ("x-sb-llm-provider", "openrouter"),
            ("x-sb-llm-timeout-seconds", "1200"),
        ]
    )
    override = resolve_llm_override(ctx)
    assert override is not None
    assert override.provider == "openrouter"
    assert override.timeout_seconds == 1200


def test_resolve_llm_override_ignores_invalid_timeout():
    ctx = _FakeMetadataContext(
        [
            ("x-sb-llm-provider", "openrouter"),
            ("x-sb-llm-timeout-seconds", "not-a-number"),
        ]
    )
    override = resolve_llm_override(ctx)
    assert override is not None
    assert override.timeout_seconds == 0


def test_create_llm_provider_for_request_passes_timeout_to_client():
    """End-to-end check: a request-scoped timeout reaches the HTTP client."""
    cfg = WorkerConfig(
        llm_provider="openai",
        llm_api_key="test",
        llm_model="gpt-4o",
        llm_timeout=900,
    )
    provider, model = create_llm_provider_for_request(
        cfg,
        provider="openai",
        model="gpt-4o",
        api_key="test",
        timeout_seconds=1800,
    )
    # OpenAICompatProvider stores the effective timeout on the instance
    # for downstream visibility.
    assert getattr(provider, "timeout", None) == 1800.0
    assert model == "gpt-4o"


def test_create_llm_provider_for_request_falls_back_to_bootstrap_timeout():
    """No per-request override → worker's bootstrap llm_timeout wins."""
    cfg = WorkerConfig(
        llm_provider="openai",
        llm_api_key="test",
        llm_model="gpt-4o",
        llm_timeout=900,
    )
    provider, _ = create_llm_provider_for_request(
        cfg,
        provider="openai",
        model="gpt-4o",
        api_key="test",
        timeout_seconds=0,
    )
    assert getattr(provider, "timeout", None) == 900.0


def test_runtime_override_is_empty_when_only_default_timeout():
    """Empty override (default=0 timeout) must still be treated as empty."""
    override = RuntimeLLMOverride()
    assert override.is_empty() is True


# ─── Factory defense in depth (CA-125) ───────────────────────────────


def test_create_llm_provider_rejects_unknown_provider_with_actionable_message():
    """create_llm_provider catches unknown providers reaching it via
    paths that bypass the WorkerConfig validator (notably
    config.model_copy(update={'llm_provider': '...'}) used by
    create_llm_provider_for_request — pydantic v2 model_copy does NOT
    re-run validators by default).

    Tester report 2026-04-30 (Pazaryna) R2 / CA-125: pre-fix this raised
    a bare ValueError reading 'Unknown LLM provider: x' with no list
    of valid alternatives.
    """
    cfg = WorkerConfig(llm_provider="anthropic", llm_api_key="test", llm_model="claude")
    # Bypass the validator the same way per-request overrides do.
    bypassed = cfg.model_copy(update={"llm_provider": "totally-fake"})
    with pytest.raises(ValueError) as exc_info:
        create_llm_provider(bypassed)
    msg = str(exc_info.value)
    assert "totally-fake" in msg
    # Every supported provider must be named so the user knows what to
    # switch to.
    for provider in SUPPORTED_LLM_PROVIDERS:
        assert repr(provider) in msg, f"supported provider {provider} not surfaced in error: {msg}"
