"""Ollama embedding provider using the native /api/embed endpoint."""

from __future__ import annotations

import httpx
import structlog

log = structlog.get_logger()

# Max texts per batch to avoid Ollama memory issues on large sets
_BATCH_SIZE = 256


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
        timeout: float = 300.0,
    ) -> None:
        self._base_url = base_url.rstrip("/")
        self._model = model
        self._dimension = dimension
        self._client = httpx.AsyncClient(timeout=timeout)

    async def _embed_batch(self, texts: list[str]) -> list[list[float]]:
        """Embed a single batch of texts."""
        response = await self._client.post(
            f"{self._base_url}/api/embed",
            json={"model": self._model, "input": texts},
        )
        response.raise_for_status()
        data = response.json()

        embeddings: list[list[float]] | None = data.get("embeddings")
        if not embeddings:
            raise ValueError(f"Ollama returned empty embeddings for {len(texts)} input(s)")
        for i, vec in enumerate(embeddings):
            if vec is None:
                raise ValueError(f"Ollama returned null embedding at index {i} of {len(embeddings)}")
        return embeddings

    async def embed(self, texts: list[str]) -> list[list[float]]:
        """Generate embeddings for a batch of texts via Ollama /api/embed.

        Automatically splits large batches into chunks of _BATCH_SIZE to
        avoid memory issues on the Ollama side.
        """
        if len(texts) <= _BATCH_SIZE:
            return await self._embed_batch(texts)

        # Split into batches
        all_embeddings: list[list[float]] = []
        for i in range(0, len(texts), _BATCH_SIZE):
            batch = texts[i : i + _BATCH_SIZE]
            log.info("embedding_batch", batch_num=i // _BATCH_SIZE + 1, batch_size=len(batch), total=len(texts))
            batch_embeddings = await self._embed_batch(batch)
            all_embeddings.extend(batch_embeddings)
        return all_embeddings

    @property
    def dimension(self) -> int:
        return self._dimension

    async def close(self) -> None:
        """Close the underlying HTTP client."""
        await self._client.aclose()
