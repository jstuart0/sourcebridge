"""LLM model router with fallback support."""

from __future__ import annotations

from collections.abc import AsyncIterator

import structlog
from tenacity import RetryError

from workers.common.llm.provider import LLMProvider, LLMResponse

log = structlog.get_logger()


def _unwrap_retry_error(exc: BaseException) -> BaseException:
    """Unwrap tenacity.RetryError to expose the original causing exception.

    Phase 3 defensive fix (plan bob H3): ``ConcurrencyGatedProvider`` uses
    tenacity with ``reraise=True``, which re-raises the original exception
    directly.  However, if tenacity ever wraps the exception in a ``RetryError``
    (e.g. when ``reraise=False`` is accidentally used or in future tenacity
    versions), callers see an opaque ``RetryError`` instead of the underlying
    SDK exception — breaking error classification, logging, and the router's
    fallback logic.

    This unwrap is defensive dead-code today (``reraise=True`` is set
    everywhere), but protects against accidental regressions.
    """
    if isinstance(exc, RetryError) and exc.__cause__ is not None:
        return exc.__cause__
    return exc


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
                return await provider.complete(prompt, system=system, max_tokens=max_tokens, temperature=temperature)
            except Exception as e:
                # Unwrap RetryError so the router and its callers always see
                # the original SDK exception — not an opaque tenacity wrapper.
                unwrapped = _unwrap_retry_error(e)
                last_error = unwrapped if isinstance(unwrapped, Exception) else e
                log.warning("provider_failed", provider_index=i, error=str(last_error))
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
                unwrapped = _unwrap_retry_error(e)
                last_error = unwrapped if isinstance(unwrapped, Exception) else e
                log.warning("stream_provider_failed", provider_index=i, error=str(last_error))
                continue
        raise RuntimeError(f"All LLM providers failed for streaming. Last error: {last_error}")
