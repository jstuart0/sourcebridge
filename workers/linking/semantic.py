"""Semantic similarity linker.

Uses embeddings to find code entities that are semantically similar to requirements.
"""

from __future__ import annotations

import math
from dataclasses import dataclass

from workers.common.embedding.provider import EmbeddingProvider
from workers.linking.types import CodeEntity, Link, LinkResult, LinkSource, LinkType


@dataclass
class RequirementText:
    """A requirement with its text for embedding."""

    id: str
    text: str  # title + description + acceptance criteria


def cosine_similarity(a: list[float], b: list[float]) -> float:
    """Compute cosine similarity between two vectors."""
    dot = sum(x * y for x, y in zip(a, b, strict=False))
    mag_a = math.sqrt(sum(x * x for x in a))
    mag_b = math.sqrt(sum(x * x for x in b))
    if mag_a == 0 or mag_b == 0:
        return 0.0
    return dot / (mag_a * mag_b)


async def extract_semantic_links(
    requirements: list[RequirementText],
    entities: list[CodeEntity],
    embedding_provider: EmbeddingProvider,
    threshold: float = 0.6,
) -> LinkResult:
    """Find semantic links between requirements and code entities.

    Embeds both requirement text and code entity signatures/content,
    then finds pairs above the similarity threshold.

    Args:
        requirements: Requirements with text.
        entities: Code entities with content.
        embedding_provider: Provider for generating embeddings.
        threshold: Minimum cosine similarity to create a link.

    Returns:
        LinkResult with discovered links.
    """
    result = LinkResult()

    if not requirements or not entities:
        return result

    # Build texts for embedding
    req_texts = [r.text for r in requirements]
    entity_texts = [_entity_text(e) for e in entities]

    # Generate embeddings
    req_embeddings = await embedding_provider.embed(req_texts)
    entity_embeddings = await embedding_provider.embed(entity_texts)

    # Compare all pairs
    for i, req in enumerate(requirements):
        for j, entity in enumerate(entities):
            sim = cosine_similarity(req_embeddings[i], entity_embeddings[j])
            if sim >= threshold:
                result.links.append(
                    Link(
                        requirement_id=req.id,
                        entity=entity,
                        source=LinkSource.SEMANTIC,
                        link_type=LinkType.IMPLEMENTS,
                        confidence=min(sim, 0.85),  # Cap semantic confidence
                        rationale=f"Semantic similarity {sim:.2f} between '{req.id}' and '{entity.name}'",
                    )
                )

    return result


def _entity_text(entity: CodeEntity) -> str:
    """Build text representation of an entity for embedding."""
    parts = [entity.name]
    if entity.doc_comment:
        parts.append(entity.doc_comment)
    if entity.content:
        # Use first few lines of content
        lines = entity.content.split("\n")[:20]
        parts.append("\n".join(lines))
    return " ".join(parts)
