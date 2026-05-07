"""LLM configuration and provider factory."""

from __future__ import annotations

import os
from typing import TYPE_CHECKING

from workers.common.config import (
    SUPPORTED_LLM_PROVIDERS,
    WorkerConfig,
    _format_supported,
)
from workers.common.llm.anthropic import AnthropicProvider
from workers.common.llm.fake import FakeLLMProvider
from workers.common.llm.openai_compat import OpenAICompatProvider
from workers.common.llm.provider import LLMProvider

if TYPE_CHECKING:
    from workers.common.llm.concurrency import ProviderGateRegistry


def _env_truthy(value: str) -> bool:
    return value.strip().lower() in ("true", "1", "yes", "on")


def _resolve_disable_thinking(*, report: bool = False) -> bool:
    """Resolve whether thinking/reasoning mode should be disabled.

    Historical worker deployments used ``SOURCEBRIDGE_WORKER_LLM_DISABLE_THINKING=true``,
    while the provider factory only checked ``SOURCEBRIDGE_LLM_ENABLE_THINKING``.
    That mismatch leaves Qwen-family report models in reasoning mode, which
    produces long internal chains and weak visible output.

    Precedence:
    1. Explicit report-scoped env vars
    2. Worker-scoped env vars
    3. Global env vars
    4. Default to disabled
    """
    if report:
        explicit_disable = os.environ.get("SOURCEBRIDGE_WORKER_LLM_REPORT_DISABLE_THINKING", "").strip()
        if explicit_disable:
            return _env_truthy(explicit_disable)
        explicit_enable = os.environ.get("SOURCEBRIDGE_WORKER_LLM_REPORT_ENABLE_THINKING", "").strip()
        if explicit_enable:
            return not _env_truthy(explicit_enable)

    worker_disable = os.environ.get("SOURCEBRIDGE_WORKER_LLM_DISABLE_THINKING", "").strip()
    if worker_disable:
        return _env_truthy(worker_disable)
    worker_enable = os.environ.get("SOURCEBRIDGE_WORKER_LLM_ENABLE_THINKING", "").strip()
    if worker_enable:
        return not _env_truthy(worker_enable)

    global_disable = os.environ.get("SOURCEBRIDGE_LLM_DISABLE_THINKING", "").strip()
    if global_disable:
        return _env_truthy(global_disable)
    global_enable = os.environ.get("SOURCEBRIDGE_LLM_ENABLE_THINKING", "").strip()
    if global_enable:
        return not _env_truthy(global_enable)

    return True


def _create_llm_provider_sync(
    config: WorkerConfig,
    *,
    provider: str = "",
    base_url: str = "",
    api_key: str = "",
    model: str = "",
    draft_model: str = "",
    timeout_seconds: int = 0,
) -> tuple[LLMProvider, str]:
    """Synchronous per-request provider factory — returns the raw (unwrapped) provider.

    Used by ``servicer_utils.resolve_provider_for_context`` which must remain
    synchronous (called from backward-compat wrappers on gRPC servicers that
    haven't been made async yet).  No gate wrapping — the caller uses the
    returned ``ProviderResolutionKey`` to look up the gate directly.
    """
    effective = config.model_copy(
        update={
            "llm_provider": provider or config.llm_provider,
            "llm_base_url": base_url or config.llm_base_url,
            "llm_api_key": api_key or config.llm_api_key,
            "llm_model": model or config.llm_model,
            "llm_draft_model": draft_model or config.llm_draft_model,
            "llm_timeout": timeout_seconds if timeout_seconds > 0 else config.llm_timeout,
        }
    )
    return _build_raw_llm_provider(effective), effective.llm_model


def _build_raw_llm_provider(config: WorkerConfig) -> LLMProvider:
    """Build a raw (unwrapped) LLM provider from config.  No gate, no async."""
    if config.test_mode:
        return FakeLLMProvider()

    if config.llm_provider == "anthropic":
        return AnthropicProvider(api_key=config.llm_api_key, model=config.llm_model)

    if config.llm_provider == "lmstudio":
        lmstudio_url = config.llm_base_url or "http://localhost:1234/v1"
        return OpenAICompatProvider(
            api_key=config.llm_api_key,
            model=config.llm_model,
            base_url=lmstudio_url,
            draft_model=config.llm_draft_model or None,
            provider_name="lmstudio",
        )

    if config.llm_provider in ("openai", "ollama", "vllm", "llama-cpp", "sglang", "gemini", "openrouter"):
        if config.llm_base_url:
            base_url: str | None = config.llm_base_url
        elif config.llm_provider == "ollama":
            base_url = "http://localhost:11434/v1"
        elif config.llm_provider == "vllm":
            base_url = "http://localhost:8000/v1"
        elif config.llm_provider == "llama-cpp":
            base_url = "http://localhost:8080/v1"
        elif config.llm_provider == "sglang":
            base_url = "http://localhost:30000/v1"
        elif config.llm_provider == "gemini":
            base_url = "https://generativelanguage.googleapis.com/v1beta/openai/"
        elif config.llm_provider == "openrouter":
            base_url = "https://openrouter.ai/api/v1"
        else:
            base_url = None

        extra_headers: dict[str, str] | None = None
        if config.llm_provider == "openrouter":
            extra_headers = {
                "HTTP-Referer": "https://sourcebridge.dev",
                "X-Title": "SourceBridge",
            }

        disable_thinking = _resolve_disable_thinking()
        return OpenAICompatProvider(
            api_key=config.llm_api_key,
            model=config.llm_model,
            base_url=base_url,
            extra_headers=extra_headers,
            provider_name=config.llm_provider,
            disable_thinking=disable_thinking,
            timeout=float(config.llm_timeout) if config.llm_timeout else None,
        )

    raise ValueError(
        f"LLM provider {config.llm_provider!r} is not supported. "
        f"Supported LLM providers: {_format_supported(SUPPORTED_LLM_PROVIDERS)}."
    )


async def create_llm_provider(
    config: WorkerConfig,
    *,
    gate_registry: ProviderGateRegistry | None = None,
) -> LLMProvider:
    """Create an LLM provider from configuration.

    When ``gate_registry`` is supplied (Phase 2+), the returned provider is
    wrapped in a ``ConcurrencyGatedProvider`` so all calls pass through the
    host/kind gate.  When None (kill-switch off or tests that construct
    providers directly), the raw provider is returned unchanged.
    """
    raw = _build_raw_llm_provider(config)

    if gate_registry is not None:
        from workers.common.llm.concurrency import wrap_provider

        base_url_for_gate = _provider_base_url(config)
        return await wrap_provider(
            raw,
            provider_name=config.llm_provider,
            base_url=base_url_for_gate,
            kind="llm",
            registry=gate_registry,
        )
    return raw


def _provider_base_url(config: WorkerConfig) -> str | None:
    """Resolve the effective base URL for a provider config (used for gate key lookup)."""
    if config.llm_base_url:
        return config.llm_base_url
    _defaults = {
        "ollama": "http://localhost:11434/v1",
        "vllm": "http://localhost:8000/v1",
        "llama-cpp": "http://localhost:8080/v1",
        "sglang": "http://localhost:30000/v1",
        "lmstudio": "http://localhost:1234/v1",
        "gemini": "https://generativelanguage.googleapis.com/v1beta/openai/",
        "openrouter": "https://openrouter.ai/api/v1",
    }
    return _defaults.get(config.llm_provider)


async def create_llm_provider_for_request(
    config: WorkerConfig,
    *,
    provider: str = "",
    base_url: str = "",
    api_key: str = "",
    model: str = "",
    draft_model: str = "",
    timeout_seconds: int = 0,
    gate_registry: ProviderGateRegistry | None = None,
) -> tuple[LLMProvider, str]:
    """Create a per-request provider from effective runtime settings.

    Empty override fields fall back to the worker's bootstrap config.
    ``timeout_seconds`` > 0 overrides the worker's bootstrap
    ``llm_timeout``; this is how the admin UI's TimeoutSecs reaches the
    HTTP client on a per-call basis.

    When ``gate_registry`` is supplied it is forwarded to
    ``create_llm_provider`` so the returned provider is wrapped in the
    concurrency gate (plan v4 Phase 2, bob H4).
    """
    effective = config.model_copy(
        update={
            "llm_provider": provider or config.llm_provider,
            "llm_base_url": base_url or config.llm_base_url,
            "llm_api_key": api_key or config.llm_api_key,
            "llm_model": model or config.llm_model,
            "llm_draft_model": draft_model or config.llm_draft_model,
            "llm_timeout": timeout_seconds if timeout_seconds > 0 else config.llm_timeout,
        }
    )
    return await create_llm_provider(effective, gate_registry=gate_registry), effective.llm_model


async def create_report_provider(
    config: WorkerConfig,
    *,
    gate_registry: ProviderGateRegistry | None = None,
) -> LLMProvider | None:
    """Create a separate LLM provider for report generation, if configured.

    Returns None if no report-specific provider is configured, meaning
    the caller should fall back to the main provider.

    When ``gate_registry`` is supplied the returned provider is wrapped in
    the concurrency gate using the report provider's name and base URL
    (plan v4 Phase 2).
    """
    if not config.llm_report_provider and not config.llm_report_model:
        return None

    provider_name = config.llm_report_provider or config.llm_provider
    model = config.llm_report_model or config.llm_model
    api_key = config.llm_report_api_key or config.llm_api_key
    base_url = config.llm_report_base_url or config.llm_base_url

    if provider_name == "anthropic":
        raw: LLMProvider = AnthropicProvider(api_key=api_key, model=model)
        effective_base_url: str | None = base_url or None
    else:
        # All other providers use OpenAI-compatible interface
        default_urls = {
            "ollama": "http://localhost:11434/v1",
            "vllm": "http://localhost:8000/v1",
            "lmstudio": "http://localhost:1234/v1",
        }
        if not base_url:
            base_url = default_urls.get(provider_name, "")

        disable_thinking = _resolve_disable_thinking(report=True)

        raw = OpenAICompatProvider(
            api_key=api_key,
            model=model,
            base_url=base_url,
            provider_name=provider_name,
            disable_thinking=disable_thinking,
        )
        effective_base_url = base_url or None

    if gate_registry is not None:
        from workers.common.llm.concurrency import wrap_provider

        return await wrap_provider(
            raw,
            provider_name=provider_name,
            base_url=effective_base_url,
            kind="llm",
            registry=gate_registry,
        )
    return raw
