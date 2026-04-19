"""LLM configuration and provider factory."""

from __future__ import annotations

import os

from workers.common.config import WorkerConfig
from workers.common.llm.anthropic import AnthropicProvider
from workers.common.llm.fake import FakeLLMProvider
from workers.common.llm.openai_compat import OpenAICompatProvider
from workers.common.llm.provider import LLMProvider


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


def create_llm_provider(config: WorkerConfig) -> LLMProvider:
    """Create an LLM provider from configuration."""
    if config.test_mode:
        return FakeLLMProvider()

    if config.llm_provider == "anthropic":
        return AnthropicProvider(api_key=config.llm_api_key, model=config.llm_model)
    elif config.llm_provider == "lmstudio":
        lmstudio_url = config.llm_base_url or "http://localhost:1234/v1"
        return OpenAICompatProvider(
            api_key=config.llm_api_key,
            model=config.llm_model,
            base_url=lmstudio_url,
            draft_model=config.llm_draft_model or None,
            provider_name="lmstudio",
        )
    elif config.llm_provider in ("openai", "ollama", "vllm", "llama-cpp", "sglang", "gemini", "openrouter"):
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

        # Disable thinking mode for local models by default. Thinking
        # models (Qwen 3.5) generate long <think> chains that waste
        # tokens on summarization tasks. Operators can re-enable via
        # SOURCEBRIDGE_LLM_ENABLE_THINKING=true.
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
    else:
        raise ValueError(f"Unknown LLM provider: {config.llm_provider}")


def create_llm_provider_for_request(
    config: WorkerConfig,
    *,
    provider: str = "",
    base_url: str = "",
    api_key: str = "",
    model: str = "",
    draft_model: str = "",
    timeout_seconds: int = 0,
) -> tuple[LLMProvider, str]:
    """Create a per-request provider from effective runtime settings.

    Empty override fields fall back to the worker's bootstrap config.
    ``timeout_seconds`` > 0 overrides the worker's bootstrap
    ``llm_timeout``; this is how the admin UI's TimeoutSecs reaches the
    HTTP client on a per-call basis.
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
    return create_llm_provider(effective), effective.llm_model


def create_report_provider(config: WorkerConfig) -> LLMProvider | None:
    """Create a separate LLM provider for report generation, if configured.

    Returns None if no report-specific provider is configured, meaning
    the caller should fall back to the main provider.
    """
    if not config.llm_report_provider and not config.llm_report_model:
        return None

    provider_name = config.llm_report_provider or config.llm_provider
    model = config.llm_report_model or config.llm_model
    api_key = config.llm_report_api_key or config.llm_api_key
    base_url = config.llm_report_base_url or config.llm_base_url

    if provider_name == "anthropic":
        return AnthropicProvider(api_key=api_key, model=model)

    # All other providers use OpenAI-compatible interface
    default_urls = {
        "ollama": "http://localhost:11434/v1",
        "vllm": "http://localhost:8000/v1",
        "lmstudio": "http://localhost:1234/v1",
    }
    if not base_url:
        base_url = default_urls.get(provider_name, "")

    disable_thinking = _resolve_disable_thinking(report=True)

    return OpenAICompatProvider(
        api_key=api_key,
        model=model,
        base_url=base_url,
        provider_name=provider_name,
        disable_thinking=disable_thinking,
    )
