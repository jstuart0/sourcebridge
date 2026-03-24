"""Fake embedding provider for deterministic testing."""

from __future__ import annotations

import hashlib


class FakeEmbeddingProvider:
    """Deterministic embedding provider for testing."""

    def __init__(self, dimension: int = 1024) -> None:
        self._dimension = dimension

    async def embed(self, texts: list[str]) -> list[list[float]]:
        """Generate deterministic embeddings from text hashes."""
        return [self._deterministic_vector(text) for text in texts]

    @property
    def dimension(self) -> int:
        return self._dimension

    def _deterministic_vector(self, text: str) -> list[float]:
        """Generate a deterministic unit vector from text content."""
        h = hashlib.sha256(text.encode()).digest()
        # Use hash bytes to seed float values
        values: list[float] = []
        for i in range(self._dimension):
            byte_idx = i % len(h)
            values.append((h[byte_idx] / 255.0) * 2 - 1)  # Normalize to [-1, 1]
        # Normalize to unit vector
        magnitude = sum(v * v for v in values) ** 0.5
        if magnitude > 0:
            values = [v / magnitude for v in values]
        return values
