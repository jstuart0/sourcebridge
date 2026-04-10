"""Anthropic Claude LLM adapter.

Supports prompt caching via ``cache_control`` on system messages.
When ``enable_cache=True`` (the default), the system message is sent
with ``cache_control: {"type": "ephemeral"}`` so that Anthropic can
reuse the cached prefix across multiple calls with the same system
prompt. This saves ~85% on input tokens for multi-artifact runs
where the repository skeleton prompt is stable across calls.

See: https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching
"""

from __future__ import annotations

import logging
from collections.abc import AsyncIterator

import anthropic

from workers.common.llm.provider import LLMResponse

logger = logging.getLogger(__name__)


class AnthropicProvider:
    """Anthropic Claude LLM provider with prompt caching support."""

    def __init__(
        self,
        api_key: str,
        model: str = "claude-sonnet-4-20250514",
        enable_cache: bool = True,
    ) -> None:
        self.client = anthropic.AsyncAnthropic(api_key=api_key)
        self.model = model
        self.enable_cache = enable_cache

    @property
    def default_model(self) -> str:
        """Return the default model ID."""
        return self.model

    def _build_system(self, system: str) -> object:
        """Build the system parameter, optionally with cache_control.

        When caching is enabled and a system prompt is provided, we send
        it as a structured block with ``cache_control`` so Anthropic can
        cache the prefix. The system prompt for hierarchical summarization
        is stable across all leaf calls, making it an ideal cache target.
        """
        if not system:
            return anthropic.NOT_GIVEN
        if not self.enable_cache:
            return system
        return [
            {
                "type": "text",
                "text": system,
                "cache_control": {"type": "ephemeral"},
            }
        ]

    async def complete(
        self,
        prompt: str,
        *,
        system: str = "",
        max_tokens: int = 4096,
        temperature: float = 0.0,
        model: str | None = None,
    ) -> LLMResponse:
        """Generate a completion via Anthropic API."""
        use_model = model or self.model
        message = await self.client.messages.create(
            model=use_model,
            max_tokens=max_tokens,
            temperature=temperature,
            system=self._build_system(system),
            messages=[{"role": "user", "content": prompt}],
        )

        # Log cache performance when available
        usage = message.usage
        cache_read = getattr(usage, "cache_read_input_tokens", 0) or 0
        cache_create = getattr(usage, "cache_creation_input_tokens", 0) or 0
        if cache_read > 0 or cache_create > 0:
            logger.debug(
                "anthropic_cache: read=%d creation=%d input=%d",
                cache_read,
                cache_create,
                usage.input_tokens,
            )

        return LLMResponse(
            content=message.content[0].text if message.content else "",
            model=use_model,
            input_tokens=usage.input_tokens,
            output_tokens=usage.output_tokens,
            stop_reason=message.stop_reason or "",
            provider_name="anthropic",
        )

    async def stream(
        self,
        prompt: str,
        *,
        system: str = "",
        max_tokens: int = 4096,
        temperature: float = 0.0,
        model: str | None = None,
    ) -> AsyncIterator[str]:
        """Stream a completion via Anthropic API."""
        use_model = model or self.model
        async with self.client.messages.stream(
            model=use_model,
            max_tokens=max_tokens,
            temperature=temperature,
            system=self._build_system(system),
            messages=[{"role": "user", "content": prompt}],
        ) as stream:
            async for text in stream.text_stream:
                yield text
