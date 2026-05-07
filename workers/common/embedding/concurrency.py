"""Thin concurrency gate wrapper for embedding providers.

Shares the same ``ProviderGateRegistry`` instance as the LLM gate, using
``kind="embedding"`` so that host-gated providers (Ollama) count embedding
calls against the same semaphore as LLM calls.

No retry in Phase 1 — embedding calls are generally idempotent and the
error surface is narrower than LLM calls.  Phase 6 can add retry if needed.

See plan: thoughts/shared/plans/active-2026-05-06-deliver-worker-llm-concurrency.md
"""

from __future__ import annotations

from workers.common.embedding.provider import EmbeddingProvider
from workers.common.llm.concurrency import (
    ConcurrencyConfig,
    ProviderGate,
    ProviderGateRegistry,
)


class ConcurrencyGatedEmbeddingProvider:
    """``EmbeddingProvider`` decorator that routes calls through a gate semaphore.

    Thinner than the LLM wrapper: no retry, no RPM limiter, no tok/s recording.
    The slot is acquired per ``embed()`` call; parallel chunk fan-out within
    one ``embed()`` call is handled by the provider itself (see Phase 5 for
    the ``OpenAICompatEmbeddingProvider`` fan-out).
    """

    def __init__(
        self,
        raw: EmbeddingProvider,
        gate: ProviderGate,
        config: ConcurrencyConfig | None = None,
    ) -> None:
        self._raw = raw
        self._gate = gate
        self._config = config or ConcurrencyConfig()

    async def embed(self, texts: list[str]) -> list[list[float]]:
        async with self._gate.slot():
            return await self._raw.embed(texts)

    @property
    def dimension(self) -> int:
        return self._raw.dimension


async def wrap_embedding_provider(
    raw: EmbeddingProvider,
    provider_name: str,
    base_url: str | None,
    kind: str = "embedding",
    registry: ProviderGateRegistry | None = None,
    config: ConcurrencyConfig | None = None,
) -> EmbeddingProvider:
    """Wrap ``raw`` in a gate if the kill switch is on and registry is provided.

    Returns ``raw`` unchanged when no registry is given or when
    ``config.wrapper_enabled`` is False.
    """
    if registry is None:
        return raw
    cfg = config or registry._config
    if not cfg.wrapper_enabled:
        return raw
    gate = await registry.lookup(provider_name, base_url, kind)
    return ConcurrencyGatedEmbeddingProvider(raw, gate, cfg)
