"""OpenAI-compatible embedding provider.

Works with any service that implements the /v1/embeddings endpoint,
including Ollama's OpenAI-compatible API layer.
"""

from __future__ import annotations

import asyncio

import httpx
import structlog

from workers.common.llm.rebind_guard import RebindGuardedTransport

log = structlog.get_logger()

_BATCH_SIZE = 256
# Concurrent batch fan-out limit. Four is enough to saturate a frontier
# embedding endpoint without over-queuing; Ollama uses a separate serial
# provider (ollama.py) so this constant applies to OpenAI-compat only.
LOCAL_EMBEDDING_FANOUT_LIMIT = 4


class OpenAICompatEmbeddingProvider:
    """Embedding provider using the OpenAI /v1/embeddings API.

    Compatible with OpenAI, Ollama (via /v1/embeddings), vLLM, LiteLLM, etc.

    Each request uses a fresh ``httpx.AsyncClient`` with a per-call
    ``RebindGuardedTransport`` so that DNS-rebind attacks against the
    operator-configured ``base_url`` are blocked at request time (D-013 /
    X-H1).  httpx's ``async with`` ``__aexit__`` calls ``aclose()`` on the
    transport, which is correct and harmless for per-call instantiation.
    """

    def __init__(
        self,
        base_url: str = "http://localhost:11434",
        model: str = "nomic-embed-text",
        dimension: int = 768,
        api_key: str = "",
        timeout: float = 300.0,
        allow_private: bool = True,
    ) -> None:
        self._base_url = base_url.rstrip("/")
        self._model = model
        self._dimension = dimension
        self._timeout = timeout
        # D-013 / D-014: stored so per-call transport mirrors the operator's
        # llm_allow_private_base_url policy.  Defaults True (same as the LLM
        # provider factory) so local/self-hosted setups work out of the box.
        self._allow_private = allow_private
        self._headers: dict[str, str] = {"Content-Type": "application/json"}
        if api_key:
            self._headers["Authorization"] = f"Bearer {api_key}"

    async def _embed_batch(self, texts: list[str]) -> list[list[float]]:
        """Embed a single batch of texts."""
        async with httpx.AsyncClient(
            transport=RebindGuardedTransport(allow_private=self._allow_private),
            timeout=self._timeout,
            headers=self._headers,
        ) as client:
            response = await client.post(
                f"{self._base_url}/v1/embeddings",
                json={"model": self._model, "input": texts},
            )
            response.raise_for_status()
            data = response.json()

        items = sorted(data["data"], key=lambda x: x["index"])
        return [item["embedding"] for item in items]

    async def embed(self, texts: list[str]) -> list[list[float]]:
        """Generate embeddings via the OpenAI-compatible /v1/embeddings endpoint.

        Automatically splits large batches into chunks of _BATCH_SIZE and
        issues them concurrently (up to LOCAL_EMBEDDING_FANOUT_LIMIT batches
        in-flight). Output order is preserved: output[i] corresponds to
        input[i].
        """
        if len(texts) <= _BATCH_SIZE:
            return await self._embed_batch(texts)

        batches = [texts[i : i + _BATCH_SIZE] for i in range(0, len(texts), _BATCH_SIZE)]
        local_sem = asyncio.Semaphore(LOCAL_EMBEDDING_FANOUT_LIMIT)

        async def _embed_one(batch_num: int, batch: list[str]) -> list[list[float]]:
            async with local_sem:
                log.info(
                    "embedding_batch",
                    batch_num=batch_num + 1,
                    batch_size=len(batch),
                    total=len(texts),
                )
                return await self._embed_batch(batch)

        results = await asyncio.gather(*[_embed_one(i, b) for i, b in enumerate(batches)])

        # Flatten in order: results[i] corresponds to batches[i] which
        # corresponds to texts[i * _BATCH_SIZE : (i + 1) * _BATCH_SIZE].
        all_embeddings: list[list[float]] = []
        for batch_result in results:
            all_embeddings.extend(batch_result)
        return all_embeddings

    @property
    def dimension(self) -> int:
        return self._dimension

    async def close(self) -> None:
        """No-op: per-call clients are closed after each request."""
