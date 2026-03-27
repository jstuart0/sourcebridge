"""Anthropic Claude LLM adapter."""

from __future__ import annotations

from collections.abc import AsyncIterator

import anthropic

from workers.common.llm.provider import LLMResponse


class AnthropicProvider:
    """Anthropic Claude LLM provider."""

    def __init__(self, api_key: str, model: str = "claude-sonnet-4-20250514") -> None:
        self.client = anthropic.AsyncAnthropic(api_key=api_key)
        self.model = model

    @property
    def default_model(self) -> str:
        """Return the default model ID."""
        return self.model

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
            system=system if system else anthropic.NOT_GIVEN,
            messages=[{"role": "user", "content": prompt}],
        )
        return LLMResponse(
            content=message.content[0].text if message.content else "",
            model=use_model,
            input_tokens=message.usage.input_tokens,
            output_tokens=message.usage.output_tokens,
            stop_reason=message.stop_reason or "",
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
            system=system if system else anthropic.NOT_GIVEN,
            messages=[{"role": "user", "content": prompt}],
        ) as stream:
            async for text in stream.text_stream:
                yield text
