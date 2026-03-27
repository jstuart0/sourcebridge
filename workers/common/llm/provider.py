"""LLM provider protocol and response types."""

from __future__ import annotations

from collections.abc import AsyncIterator
from dataclasses import dataclass
from typing import Protocol


@dataclass
class LLMResponse:
    """Response from an LLM provider."""

    content: str
    model: str
    input_tokens: int = 0
    output_tokens: int = 0
    stop_reason: str = ""
    tokens_per_second: float | None = None
    generation_time_ms: float | None = None
    acceptance_rate: float | None = None
    provider_name: str | None = None


class LLMProvider(Protocol):
    """Protocol for LLM providers."""

    async def complete(
        self,
        prompt: str,
        *,
        system: str = "",
        max_tokens: int = 4096,
        temperature: float = 0.0,
        model: str | None = None,
    ) -> LLMResponse:
        """Generate a completion. If model is provided, it overrides the default."""
        ...

    async def stream(
        self,
        prompt: str,
        *,
        system: str = "",
        max_tokens: int = 4096,
        temperature: float = 0.0,
        model: str | None = None,
    ) -> AsyncIterator[str]:
        """Stream a completion token by token."""
        ...
