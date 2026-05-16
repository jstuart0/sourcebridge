"""Embedding configuration and provider factory."""

from __future__ import annotations

from typing import TYPE_CHECKING

from workers.common.config import (
    SUPPORTED_EMBEDDING_PROVIDERS,
    WorkerConfig,
    _format_supported,
)
from workers.common.embedding.fake import FakeEmbeddingProvider
from workers.common.embedding.provider import EmbeddingProvider

if TYPE_CHECKING:
    from workers.common.llm.concurrency import ProviderGateRegistry


async def create_embedding_provider(
    config: WorkerConfig,
    *,
    gate_registry: ProviderGateRegistry | None = None,
) -> EmbeddingProvider:
    """Create an embedding provider from configuration.

    Defense in depth: ``WorkerConfig._validate_embedding_provider``
    rejects unknown providers at config-load time, but per-request
    overrides via ``config.model_copy(update=...)`` skip validators in
    pydantic v2 by default. The ``ValueError`` raised below catches
    that path with the same actionable message so a metadata-driven
    override carrying a typo doesn't crash the worker mid-request
    with a confusing stack trace.

    When ``gate_registry`` is supplied the returned provider is wrapped
    in a ``ConcurrencyGatedEmbeddingProvider`` (plan v4 Phase 2).

    Tester report 2026-04-30 (Pazaryna) Issue 3 / CA-125.
    """
    if config.test_mode:
        return FakeEmbeddingProvider(dimension=config.embedding_dimension)

    if config.embedding_provider == "ollama":
        from workers.common.embedding.ollama import OllamaEmbeddingProvider

        base_url = config.embedding_base_url or "http://localhost:11434"
        raw: EmbeddingProvider = OllamaEmbeddingProvider(
            base_url=base_url,
            model=config.embedding_model,
            dimension=config.embedding_dimension,
            # D-013: pass allow_private so the DNS-rebind guard mirrors the
            # same policy as the LLM provider (llm_allow_private_base_url).
            allow_private=config.llm_allow_private_base_url,
        )
        embed_base_url: str | None = base_url
        embed_provider_name = "ollama"
    elif config.embedding_provider in ("openai", "openai-compatible"):
        from workers.common.embedding.openai_compat import OpenAICompatEmbeddingProvider

        base_url = config.embedding_base_url or "http://localhost:11434"
        raw = OpenAICompatEmbeddingProvider(
            base_url=base_url,
            model=config.embedding_model,
            dimension=config.embedding_dimension,
            api_key=config.embedding_api_key,
            # D-013: same allow_private policy as the LLM provider.
            allow_private=config.llm_allow_private_base_url,
        )
        embed_base_url = base_url
        embed_provider_name = config.embedding_provider
    else:
        msg = (
            f"embedding provider {config.embedding_provider!r} is not supported. "
            f"Supported embedding providers: {_format_supported(SUPPORTED_EMBEDDING_PROVIDERS)}."
        )
        if config.embedding_provider == "anthropic":
            msg += (
                " Anthropic does not offer an embeddings API as of 2026; "
                "use 'ollama' (the default), 'openai', or 'openai-compatible' "
                "for a self-hosted endpoint."
            )
        raise ValueError(msg)

    if gate_registry is not None:
        from workers.common.embedding.concurrency import wrap_embedding_provider

        return await wrap_embedding_provider(
            raw,
            provider_name=embed_provider_name,
            base_url=embed_base_url,
            kind="embedding",
            registry=gate_registry,
        )
    return raw
