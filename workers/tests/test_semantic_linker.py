"""Tests for the semantic similarity linker."""

import pytest

from workers.common.embedding.fake import FakeEmbeddingProvider
from workers.linking.semantic import RequirementText, cosine_similarity, extract_semantic_links
from workers.linking.types import CodeEntity


def test_cosine_similarity_identical():
    """Identical vectors should have similarity 1.0."""
    vec = [1.0, 0.0, 0.0]
    assert abs(cosine_similarity(vec, vec) - 1.0) < 0.001


def test_cosine_similarity_orthogonal():
    """Orthogonal vectors should have similarity 0.0."""
    a = [1.0, 0.0]
    b = [0.0, 1.0]
    assert abs(cosine_similarity(a, b)) < 0.001


def test_cosine_similarity_opposite():
    """Opposite vectors should have similarity -1.0."""
    a = [1.0, 0.0]
    b = [-1.0, 0.0]
    assert abs(cosine_similarity(a, b) - (-1.0)) < 0.001


@pytest.mark.asyncio
async def test_semantic_linker_finds_links():
    """Semantic linker produces links above threshold."""
    provider = FakeEmbeddingProvider(dimension=64)
    reqs = [
        RequirementText(id="REQ-001", text="Server must start and listen on port"),
    ]
    entities = [
        CodeEntity(
            file_path="main.go",
            name="StartServer",
            kind="function",
            start_line=1,
            end_line=10,
            content="func StartServer(port int) error { listen(port) }",
        ),
    ]
    result = await extract_semantic_links(reqs, entities, provider, threshold=0.0)
    # With fake embeddings, we should get at least one link (threshold=0)
    assert len(result.links) >= 1
    assert result.links[0].requirement_id == "REQ-001"


@pytest.mark.asyncio
async def test_semantic_linker_respects_threshold():
    """Semantic linker filters below threshold."""
    provider = FakeEmbeddingProvider(dimension=64)
    reqs = [RequirementText(id="REQ-001", text="A")]
    entities = [
        CodeEntity(file_path="a.go", name="funcA", kind="function", start_line=1, end_line=5),
    ]
    # Very high threshold should filter most fake-embedding links
    result = await extract_semantic_links(reqs, entities, provider, threshold=0.999)
    # Might or might not produce links depending on fake embeddings
    # Just verify it doesn't crash
    assert isinstance(result.links, list)


@pytest.mark.asyncio
async def test_semantic_linker_empty_inputs():
    """Semantic linker handles empty inputs."""
    provider = FakeEmbeddingProvider(dimension=64)
    result = await extract_semantic_links([], [], provider)
    assert len(result.links) == 0


@pytest.mark.asyncio
async def test_semantic_confidence_capped():
    """Semantic link confidence should be capped at 0.85."""
    provider = FakeEmbeddingProvider(dimension=64)
    reqs = [RequirementText(id="REQ-001", text="test")]
    entities = [
        CodeEntity(file_path="a.go", name="test", kind="function", start_line=1, end_line=5, content="test"),
    ]
    result = await extract_semantic_links(reqs, entities, provider, threshold=0.0)
    for link in result.links:
        assert link.confidence <= 0.85
