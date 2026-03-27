"""OpenAI-compatible embedding provider.

Works with any service that implements the /v1/embeddings endpoint,
including Ollama's OpenAI-compatible API layer.
"""

from __future__ import annotations

import httpx
import structlog

log = structlog.get_logger()

_BATCH_SIZE = 256


class OpenAICompatEmbeddingProvider:
    """Embedding provider using the OpenAI /v1/embeddings API.

    Compatible with OpenAI, Ollama (via /v1/embeddings), vLLM, LiteLLM, etc.
    """

    def __init__(
        self,
        base_url: str = "http://localhost:11434",
        model: str = "nomic-embed-text",
        dimension: int = 768,
        api_key: str = "",
        timeout: float = 300.0,
    ) -> None:
        self._base_url = base_url.rstrip("/")
        self._model = model
        self._dimension = dimension
        headers = {"Content-Type": "application/json"}
        if api_key:
            headers["Authorization"] = f"Bearer {api_key}"
        self._client = httpx.AsyncClient(timeout=timeout, headers=headers)

    async def _embed_batch(self, texts: list[str]) -> list[list[float]]:
        """Embed a single batch of texts."""
        response = await self._client.post(
            f"{self._base_url}/v1/embeddings",
            json={"model": self._model, "input": texts},
        )
        response.raise_for_status()
        data = response.json()

        items = sorted(data["data"], key=lambda x: x["index"])
        return [item["embedding"] for item in items]

    async def embed(self, texts: list[str]) -> list[list[float]]:
        """Generate embeddings via the OpenAI-compatible /v1/embeddings endpoint.

        Automatically splits large batches into chunks of _BATCH_SIZE.
        """
        if len(texts) <= _BATCH_SIZE:
            return await self._embed_batch(texts)

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
