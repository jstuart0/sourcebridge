"""Tests for the fake embedding provider."""

import pytest

from workers.common.embedding.fake import FakeEmbeddingProvider


@pytest.mark.asyncio
async def test_deterministic_embeddings() -> None:
    """Test that same input produces same embedding."""
    provider = FakeEmbeddingProvider(dimension=128)
    vecs1 = await provider.embed(["hello world"])
    vecs2 = await provider.embed(["hello world"])
    assert vecs1 == vecs2


@pytest.mark.asyncio
async def test_different_inputs_different_vectors() -> None:
    """Test that different inputs produce different embeddings."""
    provider = FakeEmbeddingProvider(dimension=128)
    vecs = await provider.embed(["hello", "goodbye"])
    assert vecs[0] != vecs[1]


@pytest.mark.asyncio
async def test_embedding_dimension() -> None:
    """Test embedding dimension matches config."""
    provider = FakeEmbeddingProvider(dimension=256)
    vecs = await provider.embed(["test"])
    assert len(vecs[0]) == 256
    assert provider.dimension == 256


@pytest.mark.asyncio
async def test_unit_vector() -> None:
    """Test embeddings are approximately unit vectors."""
    provider = FakeEmbeddingProvider(dimension=128)
    vecs = await provider.embed(["test input"])
    magnitude = sum(v * v for v in vecs[0]) ** 0.5
    assert abs(magnitude - 1.0) < 0.01
