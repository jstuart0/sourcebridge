"""Confidence scoring for links.

Combines signals from multiple linkers and produces a final confidence score.
Multi-signal links (e.g., comment + semantic) receive higher confidence.
"""

from __future__ import annotations

from collections import defaultdict

from workers.linking.types import Link, LinkSource


def score_links(links: list[Link]) -> list[Link]:
    """Compute final confidence scores for links.

    Groups links by (requirement_id, entity) and boosts confidence
    when multiple signals agree.

    Args:
        links: Raw links from all linkers.

    Returns:
        Deduplicated links with final confidence scores.
    """
    # Group by (req_id, entity key)
    groups: dict[tuple[str, str], list[Link]] = defaultdict(list)
    for link in links:
        key = (link.requirement_id, _entity_key(link))
        groups[key].append(link)

    scored: list[Link] = []
    for (_req_id, _ek), group in groups.items():
        # Pick the highest-confidence link as the base
        best = max(group, key=lambda lnk: lnk.confidence)

        # Boost for multiple signals
        sources = {lnk.source for lnk in group}
        boost = _multi_signal_boost(sources)
        final_confidence = min(best.confidence + boost, 1.0)

        # Build rationale
        source_names = sorted(s.value for s in sources)
        if len(sources) > 1:
            rationale = f"Multi-signal ({', '.join(source_names)}): {best.rationale}"
        else:
            rationale = best.rationale

        scored.append(
            Link(
                requirement_id=best.requirement_id,
                entity=best.entity,
                source=best.source,
                link_type=best.link_type,
                confidence=final_confidence,
                rationale=rationale,
            )
        )

    return scored


def _entity_key(link: Link) -> str:
    """Create a unique key for the entity side of a link."""
    return f"{link.entity.file_path}:{link.entity.name}"


def _multi_signal_boost(sources: set[LinkSource]) -> float:
    """Calculate confidence boost for multi-signal agreement.

    Comment + semantic = +0.05
    Comment + test = +0.03
    Three or more signals = +0.08
    """
    if len(sources) >= 3:
        return 0.08
    if len(sources) == 2:
        if LinkSource.COMMENT in sources and LinkSource.SEMANTIC in sources:
            return 0.05
        if LinkSource.COMMENT in sources and LinkSource.TEST in sources:
            return 0.03
        return 0.03
    return 0.0
