"""LLM provider protocol and response types."""

from __future__ import annotations

import os
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
        frequency_penalty: float = 0.0,
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


class LLMEmptyResponseError(RuntimeError):
    """Raised when a provider returns an empty or whitespace-only response."""

    def __init__(self, response: LLMResponse, context: str):
        self.response = response
        self.context = context
        super().__init__(
            "LLM returned empty content "
            f"(context={context}, model={response.model}, input_tokens={response.input_tokens}, "
            f"stop_reason={response.stop_reason})"
        )


def require_nonempty(response: LLMResponse, context: str) -> LLMResponse:
    """Reject empty completions so callers fail explicitly instead of fabricating success."""
    if not response.content or not response.content.strip():
        raise LLMEmptyResponseError(response, context)
    return response


# Default prompt budget ceiling. Sized conservatively so that the combined
# prompt + system message fits comfortably inside a ~32K context window
# (the common ceiling for current Ollama deployments and mid-tier cloud models).
# Operators can raise or lower this via SOURCEBRIDGE_MAX_PROMPT_TOKENS.
DEFAULT_MAX_PROMPT_TOKENS = 24000


class SnapshotTooLargeError(RuntimeError):
    """Raised when a prompt exceeds the configured token budget.

    This is the pre-flight guard that prevents oversized prompts from reaching
    providers that silently truncate (notably Ollama). The error message maps
    one-for-one onto the SNAPSHOT_TOO_LARGE classifier in the Go API, so
    the caller can surface an actionable remediation to the user.
    """

    def __init__(self, approx_tokens: int, budget_tokens: int, context: str):
        self.approx_tokens = approx_tokens
        self.budget_tokens = budget_tokens
        self.context = context
        super().__init__(
            f"snapshot too large ({context}): ~{approx_tokens} tokens exceeds budget "
            f"{budget_tokens}. Reduce scope/depth or select a larger-context model."
        )


def estimate_tokens(text: str) -> int:
    """Approximate token count using the len/4 heuristic.

    This is intentionally cheap and dependency-free. It overestimates for
    code-heavy prompts (which skews in the safe direction for a budget guard)
    and underestimates for prompts with many non-ASCII characters. Good enough
    for a pre-flight check; not suitable for billing.
    """
    if not text:
        return 0
    return len(text) // 4


def check_prompt_budget(
    prompt: str,
    *,
    system: str = "",
    context: str,
    budget_tokens: int | None = None,
) -> int:
    """Pre-flight guard: raise SnapshotTooLargeError if prompt+system exceeds budget.

    The default budget is sourced from the ``SOURCEBRIDGE_MAX_PROMPT_TOKENS``
    environment variable (falling back to :data:`DEFAULT_MAX_PROMPT_TOKENS`).
    Callers can override per-call when they know the model's effective context.

    Args:
        prompt: The user prompt that will be sent to the model.
        system: The system prompt (also counts against the budget).
        context: Short label used in the error message (e.g. ``"cliff_notes:repository"``).
        budget_tokens: Optional override; falls back to the env var / default.

    Returns:
        The approximate token count when within budget.

    Raises:
        SnapshotTooLargeError: When the combined prompt exceeds the budget.
    """
    if budget_tokens is None:
        raw = os.environ.get("SOURCEBRIDGE_MAX_PROMPT_TOKENS", "").strip()
        try:
            budget_tokens = int(raw) if raw else DEFAULT_MAX_PROMPT_TOKENS
        except ValueError:
            budget_tokens = DEFAULT_MAX_PROMPT_TOKENS
    if budget_tokens <= 0:
        # Zero/negative disables the guard — useful for tests and benchmarks.
        return estimate_tokens(prompt) + estimate_tokens(system)
    approx = estimate_tokens(prompt) + estimate_tokens(system)
    if approx > budget_tokens:
        raise SnapshotTooLargeError(approx, budget_tokens, context)
    return approx


async def complete_with_optional_model(
    provider: LLMProvider,
    prompt: str,
    *,
    system: str = "",
    max_tokens: int = 4096,
    temperature: float = 0.0,
    frequency_penalty: float = 0.0,
    model: str | None = None,
) -> LLMResponse:
    """Call provider.complete while remaining compatible with legacy test doubles.

    Some local test providers do not accept the optional ``model`` kwarg yet.
    """
    if model is None:
        return await provider.complete(
            prompt,
            system=system,
            max_tokens=max_tokens,
            temperature=temperature,
            frequency_penalty=frequency_penalty,
        )
    return await provider.complete(
        prompt,
        system=system,
        max_tokens=max_tokens,
        temperature=temperature,
        frequency_penalty=frequency_penalty,
        model=model,
    )
