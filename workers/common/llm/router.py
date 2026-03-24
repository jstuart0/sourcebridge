"""LLM model router with fallback support."""

from __future__ import annotations

from collections.abc import AsyncIterator

import structlog

from workers.common.llm.provider import LLMProvider, LLMResponse

log = structlog.get_logger()


class LLMRouter:
    """Routes LLM requests to providers with fallback support."""

    def __init__(self, providers: list[LLMProvider]) -> None:
        if not providers:
            raise ValueError("At least one LLM provider is required")
        self.providers = providers

    async def complete(
        self,
        prompt: str,
        *,
        system: str = "",
        max_tokens: int = 4096,
        temperature: float = 0.0,
    ) -> LLMResponse:
        """Try each provider in order until one succeeds."""
        last_error: Exception | None = None
        for i, provider in enumerate(self.providers):
            try:
                return await provider.complete(
                    prompt, system=system, max_tokens=max_tokens, temperature=temperature
                )
            except Exception as e:
                last_error = e
                log.warning("provider_failed", provider_index=i, error=str(e))
                continue
        raise RuntimeError(f"All LLM providers failed. Last error: {last_error}")

    async def stream(
        self,
        prompt: str,
        *,
        system: str = "",
        max_tokens: int = 4096,
        temperature: float = 0.0,
    ) -> AsyncIterator[str]:
        """Try each provider in order for streaming."""
        last_error: Exception | None = None
        for i, provider in enumerate(self.providers):
            try:
                async for token in provider.stream(
                    prompt, system=system, max_tokens=max_tokens, temperature=temperature
                ):
                    yield token
                return
            except Exception as e:
                last_error = e
                log.warning("stream_provider_failed", provider_index=i, error=str(e))
                continue
        raise RuntimeError(f"All LLM providers failed for streaming. Last error: {last_error}")
