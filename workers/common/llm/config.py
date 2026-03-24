"""LLM configuration and provider factory."""

from __future__ import annotations

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
    elif config.llm_provider in ("openai", "ollama", "vllm"):
        if config.llm_base_url:
            base_url: str | None = config.llm_base_url
        elif config.llm_provider == "ollama":
            base_url = "http://localhost:11434/v1"
        elif config.llm_provider == "vllm":
            base_url = "http://localhost:8000/v1"
        else:
            base_url = None
        return OpenAICompatProvider(
            api_key=config.llm_api_key,
            model=config.llm_model,
            base_url=base_url,
        )
    else:
        raise ValueError(f"Unknown LLM provider: {config.llm_provider}")
