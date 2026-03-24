"""Tests for the markdown requirement parser."""

from pathlib import Path

from workers.requirements.markdown import parse_markdown

FIXTURE_DIR = Path(__file__).parent.parent.parent / "tests" / "fixtures" / "multi-lang-repo"


def _read_fixture(name: str) -> str:
    return (FIXTURE_DIR / name).read_text()


def test_parse_fixture_requirements():
    content = _read_fixture("requirements.md")
    reqs = parse_markdown(content, source="requirements.md")

    assert len(reqs) == 14, f"Expected 14 requirements, got {len(reqs)}"

    # Check first requirement
    first = reqs[0]
    assert first.id == "REQ-001"
    assert first.title == "System Startup"
    assert "listen on the configured port" in first.description
    assert first.priority == "High"
    assert len(first.acceptance_criteria) == 3


def test_parse_all_ids():
    content = _read_fixture("requirements.md")
    reqs = parse_markdown(content)

    ids = [r.id for r in reqs]
    expected = [
        "REQ-001", "REQ-003", "REQ-004", "REQ-005", "REQ-006",
        "REQ-007", "REQ-008", "REQ-009", "REQ-010", "REQ-011",
        "REQ-012", "REQ-013", "REQ-014", "REQ-015",
    ]
    assert ids == expected


def test_parse_priorities():
    content = _read_fixture("requirements.md")
    reqs = parse_markdown(content)

    priorities = {r.id: r.priority for r in reqs}
    assert priorities["REQ-010"] == "Critical"
    assert priorities["REQ-006"] == "Medium"
    assert priorities["REQ-001"] == "High"


def test_parse_acceptance_criteria():
    content = _read_fixture("requirements.md")
    reqs = parse_markdown(content)

    req_map = {r.id: r for r in reqs}
    # REQ-003 should have 4 acceptance criteria
    assert len(req_map["REQ-003"].acceptance_criteria) == 4
    # REQ-007 should have 2
    assert len(req_map["REQ-007"].acceptance_criteria) == 2


def test_parse_empty_content():
    reqs = parse_markdown("")
    assert len(reqs) == 0


def test_parse_no_requirements():
    content = "# Just a title\n\nSome text without requirements."
    reqs = parse_markdown(content)
    assert len(reqs) == 0


def test_parse_missing_priority():
    content = """## REQ-099: No Priority
This requirement has no priority field.
- **Acceptance Criteria:**
  - Something works
"""
    reqs = parse_markdown(content)
    assert len(reqs) == 1
    assert reqs[0].priority == ""
    assert len(reqs[0].acceptance_criteria) == 1


def test_parse_missing_acceptance_criteria():
    content = """## REQ-098: No Criteria
This requirement has no acceptance criteria.
- **Priority:** Low
"""
    reqs = parse_markdown(content)
    assert len(reqs) == 1
    assert reqs[0].acceptance_criteria == []


def test_parse_source_attribution():
    content = """## REQ-100: Test Source
Description.
- **Priority:** High
"""
    reqs = parse_markdown(content, source="test.md")
    assert reqs[0].source == "test.md"
