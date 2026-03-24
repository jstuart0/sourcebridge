"""Tests for the CSV requirement parser."""

from pathlib import Path

from workers.requirements.csv_parser import parse_csv

FIXTURE_DIR = Path(__file__).parent.parent.parent / "tests" / "fixtures" / "multi-lang-repo"


def _read_fixture(name: str) -> str:
    return (FIXTURE_DIR / name).read_text()


def test_parse_fixture_csv():
    content = _read_fixture("requirements.csv")
    reqs = parse_csv(content, source="requirements.csv")

    assert len(reqs) == 4, f"Expected 4 requirements, got {len(reqs)}"


def test_parse_csv_ids():
    content = _read_fixture("requirements.csv")
    reqs = parse_csv(content)

    ids = [r.id for r in reqs]
    assert ids == ["REQ-001", "REQ-003", "REQ-004", "REQ-010"]


def test_parse_csv_priorities():
    content = _read_fixture("requirements.csv")
    reqs = parse_csv(content)

    priorities = {r.id: r.priority for r in reqs}
    assert priorities["REQ-001"] == "High"
    assert priorities["REQ-010"] == "Critical"


def test_parse_csv_acceptance_criteria():
    content = _read_fixture("requirements.csv")
    reqs = parse_csv(content)

    req_map = {r.id: r for r in reqs}
    assert len(req_map["REQ-001"].acceptance_criteria) == 3
    assert len(req_map["REQ-003"].acceptance_criteria) == 4
    assert len(req_map["REQ-010"].acceptance_criteria) == 3


def test_parse_csv_source():
    content = _read_fixture("requirements.csv")
    reqs = parse_csv(content, source="test.csv")

    assert all(r.source == "test.csv" for r in reqs)


def test_parse_csv_empty():
    reqs = parse_csv("")
    assert len(reqs) == 0


def test_parse_csv_header_only():
    content = "id,title,description,priority,acceptance_criteria\n"
    reqs = parse_csv(content)
    assert len(reqs) == 0


def test_parse_csv_skip_incomplete_rows():
    content = """id,title,description,priority,acceptance_criteria
REQ-001,Valid Row,Description,High,Criterion 1
,Missing ID,Description,High,Criterion 2
REQ-003,,Description,Medium,Criterion 3
"""
    reqs = parse_csv(content)
    assert len(reqs) == 1
    assert reqs[0].id == "REQ-001"


def test_parse_csv_custom_column_mapping():
    content = """req_num,name,desc,prio,criteria
R-01,Test Req,A test requirement,High,Works correctly;Handles errors
"""
    mapping = {
        "id": "req_num",
        "title": "name",
        "description": "desc",
        "priority": "prio",
        "acceptance_criteria": "criteria",
    }
    reqs = parse_csv(content, column_mapping=mapping)
    assert len(reqs) == 1
    assert reqs[0].id == "R-01"
    assert reqs[0].title == "Test Req"
    assert len(reqs[0].acceptance_criteria) == 2


def test_parse_csv_no_criteria():
    content = """id,title,description,priority,acceptance_criteria
REQ-099,No Criteria,Has no acceptance criteria,Low,
"""
    reqs = parse_csv(content)
    assert len(reqs) == 1
    assert reqs[0].acceptance_criteria == []
