"""Embedding configuration and provider factory."""

from __future__ import annotations

from workers.common.config import WorkerConfig
from workers.common.embedding.fake import FakeEmbeddingProvider
from workers.common.embedding.provider import EmbeddingProvider


def create_embedding_provider(config: WorkerConfig) -> EmbeddingProvider:
    """Create an embedding provider from configuration."""
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

    raise NotImplementedError(f"Embedding provider '{config.embedding_provider}' not yet implemented")
