"""OpenAI-compatible embedding provider.

Works with any service that implements the /v1/embeddings endpoint,
including Ollama's OpenAI-compatible API layer.
"""

from __future__ import annotations

import httpx


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
        timeout: float = 30.0,
    ) -> None:
        self._base_url = base_url.rstrip("/")
        self._model = model
        self._dimension = dimension
        headers = {"Content-Type": "application/json"}
        if api_key:
            headers["Authorization"] = f"Bearer {api_key}"
        self._client = httpx.AsyncClient(timeout=timeout, headers=headers)

    async def embed(self, texts: list[str]) -> list[list[float]]:
        """Generate embeddings via the OpenAI-compatible /v1/embeddings endpoint."""
        response = await self._client.post(
            f"{self._base_url}/v1/embeddings",
            json={"model": self._model, "input": texts},
        )
        response.raise_for_status()
        data = response.json()

        # OpenAI format: {"data": [{"embedding": [...], "index": 0}, ...]}
        items = sorted(data["data"], key=lambda x: x["index"])
        return [item["embedding"] for item in items]

    @property
    def dimension(self) -> int:
        return self._dimension

    async def close(self) -> None:
        """Close the underlying HTTP client."""
        await self._client.aclose()
