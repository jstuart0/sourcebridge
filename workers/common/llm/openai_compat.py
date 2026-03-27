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
        extra_headers: dict[str, str] | None = None,
        draft_model: str | None = None,
        provider_name: str | None = None,
    ) -> None:
        self.client = openai.AsyncOpenAI(
            api_key=api_key or "not-needed",
            base_url=base_url,
            timeout=600.0,
            default_headers=extra_headers or {},
        )
        self.model = model
        self.draft_model = draft_model
        self.provider_name = provider_name

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
        """Generate a completion."""
        use_model = model or self.model
        messages: list[dict[str, str]] = []
        if system:
            messages.append({"role": "system", "content": system})
        messages.append({"role": "user", "content": prompt})

        extra_body: dict[str, str] | None = None
        if self.draft_model:
            extra_body = {"draft_model": self.draft_model}

        response = await self.client.chat.completions.create(
            model=use_model,
            messages=messages,  # type: ignore[arg-type]
            max_tokens=max_tokens,
            temperature=temperature,
            extra_body=extra_body,
        )
        choice = response.choices[0]

        # Extract performance metrics from server-specific response extensions
        tokens_per_second: float | None = None
        generation_time_ms: float | None = None
        acceptance_rate: float | None = None

        # llama-server includes 'timings' in the response
        # vLLM/SGLang may include timing in usage extensions
        # LM Studio includes stats in the response
        raw = response.model_extra or {}
        if "timings" in raw:
            timings = raw["timings"]
            tokens_per_second = timings.get("predicted_per_second")
            if "predicted_ms" in timings:
                generation_time_ms = timings["predicted_ms"]
            acceptance_rate = timings.get("acceptance_rate")
        elif "usage" in raw and isinstance(raw["usage"], dict):
            usage_ext = raw["usage"]
            tokens_per_second = usage_ext.get("tokens_per_second")
            generation_time_ms = usage_ext.get("total_time_ms")

        # LM Studio: compute acceptance_rate from draft token counts
        if acceptance_rate is None and "stats" in raw:
            stats = raw["stats"]
            tokens_per_second = tokens_per_second or stats.get("tokens_per_second")
            accepted = stats.get("accepted_draft_tokens_count")
            total = stats.get("total_draft_tokens_count")
            if accepted is not None and total and total > 0:
                acceptance_rate = accepted / total

        return LLMResponse(
            content=choice.message.content or "",
            model=use_model,
            input_tokens=response.usage.prompt_tokens if response.usage else 0,
            output_tokens=response.usage.completion_tokens if response.usage else 0,
            stop_reason=choice.finish_reason or "",
            tokens_per_second=tokens_per_second,
            generation_time_ms=generation_time_ms,
            acceptance_rate=acceptance_rate,
            provider_name=self.provider_name,
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
        """Stream a completion."""
        use_model = model or self.model
        messages: list[dict[str, str]] = []
        if system:
            messages.append({"role": "system", "content": system})
        messages.append({"role": "user", "content": prompt})

        extra_body: dict[str, str] | None = None
        if self.draft_model:
            extra_body = {"draft_model": self.draft_model}

        stream = await self.client.chat.completions.create(
            model=use_model,
            messages=messages,  # type: ignore[arg-type]
            max_tokens=max_tokens,
            temperature=temperature,
            stream=True,
            extra_body=extra_body,
        )
        async for chunk in stream:
            if chunk.choices and chunk.choices[0].delta.content:
                yield chunk.choices[0].delta.content
