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


class LLMProvider(Protocol):
    """Protocol for LLM providers."""

    async def complete(
        self,
        prompt: str,
        *,
        system: str = "",
        max_tokens: int = 4096,
        temperature: float = 0.0,
    ) -> LLMResponse:
        """Generate a completion."""
        ...

    async def stream(
        self,
        prompt: str,
        *,
        system: str = "",
        max_tokens: int = 4096,
        temperature: float = 0.0,
    ) -> AsyncIterator[str]:
        """Stream a completion token by token."""
        ...
