"""Embedding configuration and provider factory."""

from __future__ import annotations

from workers.common.config import (
    SUPPORTED_EMBEDDING_PROVIDERS,
    WorkerConfig,
    _format_supported,
)
from workers.common.embedding.fake import FakeEmbeddingProvider
from workers.common.embedding.provider import EmbeddingProvider


def create_embedding_provider(config: WorkerConfig) -> EmbeddingProvider:
    """Create an embedding provider from configuration.

    Defense in depth: ``WorkerConfig._validate_embedding_provider``
    rejects unknown providers at config-load time, but per-request
    overrides via ``config.model_copy(update=...)`` skip validators in
    pydantic v2 by default. The ``ValueError`` raised below catches
    that path with the same actionable message so a metadata-driven
    override carrying a typo doesn't crash the worker mid-request
    with a confusing stack trace.

    Tester report 2026-04-30 (Pazaryna) Issue 3 / CA-125.
    """
    if config.test_mode:
        return FakeEmbeddingProvider(dimension=config.embedding_dimension)

    if config.embedding_provider == "ollama":
        from workers.common.embedding.ollama import OllamaEmbeddingProvider

        base_url = config.embedding_base_url or "http://localhost:11434"
        return OllamaEmbeddingProvider(
            base_url=base_url,
            model=config.embedding_model,
            dimension=config.embedding_dimension,
        )

    if config.embedding_provider in ("openai", "openai-compatible"):
        from workers.common.embedding.openai_compat import OpenAICompatEmbeddingProvider

        base_url = config.embedding_base_url or "http://localhost:11434"
        return OpenAICompatEmbeddingProvider(
            base_url=base_url,
            model=config.embedding_model,
            dimension=config.embedding_dimension,
            api_key=config.embedding_api_key,
        )

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
