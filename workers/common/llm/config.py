"""LLM configuration and provider factory."""

from __future__ import annotations

import os

from workers.common.config import WorkerConfig
from workers.common.llm.anthropic import AnthropicProvider
from workers.common.llm.fake import FakeLLMProvider
from workers.common.llm.openai_compat import OpenAICompatProvider
from workers.common.llm.provider import LLMProvider


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
        disable_thinking = os.environ.get("SOURCEBRIDGE_LLM_ENABLE_THINKING", "").lower() not in ("true", "1", "yes")

        return OpenAICompatProvider(
            api_key=config.llm_api_key,
            model=config.llm_model,
            base_url=base_url,
            extra_headers=extra_headers,
            provider_name=config.llm_provider,
            disable_thinking=disable_thinking,
        )
    else:
        raise ValueError(f"Unknown LLM provider: {config.llm_provider}")


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

    return OpenAICompatProvider(
        api_key=api_key,
        model=model,
        base_url=base_url,
        provider_name=provider_name,
    )
