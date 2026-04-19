# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Tests for snapshot retrieval utilities."""

from __future__ import annotations

from workers.knowledge.retrieval import build_overview_query


def test_build_overview_query_repository_scope() -> None:
    """Repository scope produces a broad overview query."""
    query = build_overview_query("my-repo", "cliff_notes")
    assert "Overview" in query
    assert "my-repo" in query


def test_build_overview_query_symbol_scope() -> None:
    """Symbol scope produces a targeted query with the symbol name."""
    query = build_overview_query(
        "my-repo",
        "cliff_notes",
        scope_type="symbol",
        scope_path="internal/auth.go#handleLogin",
    )
    assert "handleLogin" in query
    assert "internal/auth.go" in query
    assert "callers" in query
    assert "Overview" not in query


def test_build_overview_query_file_scope() -> None:
    """File scope produces a targeted query with the file path."""
    query = build_overview_query(
        "my-repo",
        "cliff_notes",
        scope_type="file",
        scope_path="internal/auth.go",
    )
    assert "internal/auth.go" in query
    assert "symbols" in query
    assert "Overview" not in query


def test_build_overview_query_module_scope() -> None:
    """Module scope produces a targeted query with the module path."""
    query = build_overview_query(
        "my-repo",
        "cliff_notes",
        scope_type="module",
        scope_path="internal/api",
    )
    assert "internal/api" in query
    assert "components" in query


class _CountingEmbeddingProvider:
    """Test double: records how many texts hit the backend."""

    def __init__(self) -> None:
        self._model = "test-embed-model"
        self._base_url = "http://test/"
        self._dimension = 4
        self.embed_calls: list[list[str]] = []

    async def embed(self, texts: list[str]) -> list[list[float]]:
        self.embed_calls.append(list(texts))
        return [[float(len(t)), 0.0, 0.0, 0.0] for t in texts]

    def dimension(self) -> int:
        return self._dimension


async def _run_batch_embed(provider: object, texts: list[str]) -> list[list[float]]:
    from workers.knowledge.retrieval import _batch_embed

    return await _batch_embed(provider, texts)


def test_batch_embed_caches_repeat_texts() -> None:
    """Repeated text inputs must not re-hit the embedding backend."""
    import asyncio

    from workers.knowledge.retrieval import _EMBEDDING_CACHE

    _EMBEDDING_CACHE.clear()
    provider = _CountingEmbeddingProvider()

    # First call: everything is a miss.
    first = asyncio.run(_run_batch_embed(provider, ["alpha", "beta", "gamma"]))
    assert len(first) == 3
    assert len(provider.embed_calls) == 1
    assert provider.embed_calls[0] == ["alpha", "beta", "gamma"]

    # Second call: two repeats + one new input. Only the new one hits the backend.
    second = asyncio.run(_run_batch_embed(provider, ["alpha", "delta", "beta"]))
    assert len(second) == 3
    assert len(provider.embed_calls) == 2
    assert provider.embed_calls[1] == ["delta"]

    # Returned vectors preserve input order.
    first_map = {text: vec for text, vec in zip(["alpha", "beta", "gamma"], first, strict=True)}
    assert second[0] == first_map["alpha"]
    assert second[2] == first_map["beta"]


def test_batch_embed_cache_isolates_providers() -> None:
    """Two providers with different model/base_url must not share cache entries."""
    import asyncio

    from workers.knowledge.retrieval import _EMBEDDING_CACHE

    _EMBEDDING_CACHE.clear()
    provider_a = _CountingEmbeddingProvider()
    provider_b = _CountingEmbeddingProvider()
    provider_b._model = "other-model"

    asyncio.run(_run_batch_embed(provider_a, ["alpha"]))
    # provider_b has a different model — cache key differs — backend must be hit.
    asyncio.run(_run_batch_embed(provider_b, ["alpha"]))

    assert len(provider_a.embed_calls) == 1
    assert len(provider_b.embed_calls) == 1
