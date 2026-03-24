"""Embedding provider protocol."""

from __future__ import annotations

from typing import Protocol


class EmbeddingProvider(Protocol):
    """Protocol for embedding providers."""

    async def embed(self, texts: list[str]) -> list[list[float]]:
        """Generate embeddings for a batch of texts."""
        ...

    @property
    def dimension(self) -> int:
        """Return the embedding dimension."""
        ...
