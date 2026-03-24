"""Ollama embedding provider using the native /api/embed endpoint."""

from __future__ import annotations

import httpx


class OllamaEmbeddingProvider:
    """Embedding provider backed by an Ollama instance.

    Uses the Ollama-native ``/api/embed`` endpoint (NOT the OpenAI-compat
    endpoint) which accepts ``model`` and ``input`` fields.
    """

    def __init__(
        self,
        base_url: str = "http://localhost:11434",
        model: str = "nomic-embed-text",
        dimension: int = 768,
        timeout: float = 30.0,
    ) -> None:
        self._base_url = base_url.rstrip("/")
        self._model = model
        self._dimension = dimension
        self._client = httpx.AsyncClient(timeout=timeout)

    async def embed(self, texts: list[str]) -> list[list[float]]:
        """Generate embeddings for a batch of texts via Ollama /api/embed."""
        response = await self._client.post(
            f"{self._base_url}/api/embed",
            json={"model": self._model, "input": texts},
        )
        response.raise_for_status()
        data = response.json()

        # Ollama returns {"embeddings": [[...], [...]]}
        embeddings: list[list[float]] | None = data.get("embeddings")
        if not embeddings:
            raise ValueError(f"Ollama returned empty embeddings for {len(texts)} input(s)")
        # Guard against individual null vectors within the list
        for i, vec in enumerate(embeddings):
            if vec is None:
                raise ValueError(f"Ollama returned null embedding at index {i} of {len(embeddings)}")
        return embeddings

    @property
    def dimension(self) -> int:
        return self._dimension

    async def close(self) -> None:
        """Close the underlying HTTP client."""
        await self._client.aclose()
