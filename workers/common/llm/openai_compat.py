"""OpenAI-compatible LLM adapter (works with OpenAI, Ollama, vLLM)."""

from __future__ import annotations

from collections.abc import AsyncIterator

import openai

from workers.common.llm.provider import LLMResponse


class OpenAICompatProvider:
    """OpenAI-compatible LLM provider."""

    def __init__(
        self,
        api_key: str = "",
        model: str = "gpt-4o",
        base_url: str | None = None,
    ) -> None:
        self.client = openai.AsyncOpenAI(
            api_key=api_key or "not-needed",
            base_url=base_url,
            timeout=600.0,
        )
        self.model = model

    async def complete(
        self,
        prompt: str,
        *,
        system: str = "",
        max_tokens: int = 4096,
        temperature: float = 0.0,
    ) -> LLMResponse:
        """Generate a completion."""
        messages: list[dict[str, str]] = []
        if system:
            messages.append({"role": "system", "content": system})
        messages.append({"role": "user", "content": prompt})

        response = await self.client.chat.completions.create(
            model=self.model,
            messages=messages,  # type: ignore[arg-type]
            max_tokens=max_tokens,
            temperature=temperature,
        )
        choice = response.choices[0]
        return LLMResponse(
            content=choice.message.content or "",
            model=self.model,
            input_tokens=response.usage.prompt_tokens if response.usage else 0,
            output_tokens=response.usage.completion_tokens if response.usage else 0,
            stop_reason=choice.finish_reason or "",
        )

    async def stream(
        self,
        prompt: str,
        *,
        system: str = "",
        max_tokens: int = 4096,
        temperature: float = 0.0,
    ) -> AsyncIterator[str]:
        """Stream a completion."""
        messages: list[dict[str, str]] = []
        if system:
            messages.append({"role": "system", "content": system})
        messages.append({"role": "user", "content": prompt})

        stream = await self.client.chat.completions.create(
            model=self.model,
            messages=messages,  # type: ignore[arg-type]
            max_tokens=max_tokens,
            temperature=temperature,
            stream=True,
        )
        async for chunk in stream:
            if chunk.choices and chunk.choices[0].delta.content:
                yield chunk.choices[0].delta.content
