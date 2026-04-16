"""Tests for the confidence scoring module."""

from workers.linking.confidence import score_links
from workers.linking.types import CodeEntity, Link, LinkSource, LinkType


def _make_entity(name: str = "func") -> CodeEntity:
    return CodeEntity(file_path="test.go", name=name, kind="function", start_line=1, end_line=5)


def test_single_signal_no_boost():
    """Single-signal link should not receive a boost."""
    links = [
        Link(
            requirement_id="REQ-001",
            entity=_make_entity(),
            source=LinkSource.COMMENT,
            link_type=LinkType.IMPLEMENTS,
            confidence=0.95,
        ),
    ]
    scored = score_links(links)
    assert len(scored) == 1
    assert scored[0].confidence == 0.95


def test_multi_signal_boost():
    """Multi-signal link (comment + semantic) should receive a boost."""
    entity = _make_entity()
    links = [
        Link(
            requirement_id="REQ-001",
            entity=entity,
            source=LinkSource.COMMENT,
            link_type=LinkType.IMPLEMENTS,
            confidence=0.95,
        ),
        Link(
            requirement_id="REQ-001",
            entity=entity,
            source=LinkSource.SEMANTIC,
            link_type=LinkType.IMPLEMENTS,
            confidence=0.75,
        ),
    ]
    scored = score_links(links)
    assert len(scored) == 1
    # Should be higher than either individual confidence
    assert scored[0].confidence > 0.95


def test_confidence_between_zero_and_one():
    """Confidence score should always be between 0 and 1."""
    entity = _make_entity()
    links = [
        Link(
            requirement_id="REQ-001",
            entity=entity,
            source=LinkSource.COMMENT,
            link_type=LinkType.IMPLEMENTS,
            confidence=0.99,
        ),
        Link(
            requirement_id="REQ-001",
            entity=entity,
            source=LinkSource.SEMANTIC,
            link_type=LinkType.IMPLEMENTS,
            confidence=0.99,
        ),
        Link(
            requirement_id="REQ-001", entity=entity, source=LinkSource.TEST, link_type=LinkType.TESTS, confidence=0.99
        ),
    ]
    scored = score_links(links)
    assert 0.0 <= scored[0].confidence <= 1.0


def test_different_entities_not_merged():
    """Links to different entities should not be merged."""
    links = [
        Link(
            requirement_id="REQ-001",
            entity=_make_entity("funcA"),
            source=LinkSource.COMMENT,
            link_type=LinkType.IMPLEMENTS,
            confidence=0.9,
        ),
        Link(
            requirement_id="REQ-001",
            entity=_make_entity("funcB"),
            source=LinkSource.COMMENT,
            link_type=LinkType.IMPLEMENTS,
            confidence=0.8,
        ),
    ]
    scored = score_links(links)
    assert len(scored) == 2


def test_different_reqs_not_merged():
    """Links to different requirements should not be merged."""
    entity = _make_entity()
    links = [
        Link(
            requirement_id="REQ-001",
            entity=entity,
            source=LinkSource.COMMENT,
            link_type=LinkType.IMPLEMENTS,
            confidence=0.9,
        ),
        Link(
            requirement_id="REQ-002",
            entity=entity,
            source=LinkSource.COMMENT,
            link_type=LinkType.IMPLEMENTS,
            confidence=0.8,
        ),
    ]
    scored = score_links(links)
    assert len(scored) == 2


def test_comment_test_boost():
    """Comment + test signal should receive a smaller boost than comment + semantic."""
    entity = _make_entity()
    links_ct = [
        Link(
            requirement_id="REQ-001",
            entity=entity,
            source=LinkSource.COMMENT,
            link_type=LinkType.IMPLEMENTS,
            confidence=0.90,
        ),
        Link(
            requirement_id="REQ-001", entity=entity, source=LinkSource.TEST, link_type=LinkType.TESTS, confidence=0.85
        ),
    ]
    links_cs = [
        Link(
            requirement_id="REQ-002",
            entity=_make_entity("funcB"),
            source=LinkSource.COMMENT,
            link_type=LinkType.IMPLEMENTS,
            confidence=0.90,
        ),
        Link(
            requirement_id="REQ-002",
            entity=_make_entity("funcB"),
            source=LinkSource.SEMANTIC,
            link_type=LinkType.IMPLEMENTS,
            confidence=0.75,
        ),
    ]
    scored_ct = score_links(links_ct)
    scored_cs = score_links(links_cs)
    # Comment + semantic boost should be >= comment + test boost
    assert scored_cs[0].confidence >= scored_ct[0].confidence
