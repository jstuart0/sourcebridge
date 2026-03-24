"""Tests for the test name correlation linker."""

from workers.linking.test_correlation import extract_test_links
from workers.linking.types import CodeEntity


def test_req_pattern_in_test_name():
    """Test correlation finds REQ-xxx directly in test name."""
    entity = CodeEntity(
        file_path="test_auth.py", name="test_REQ_010_password_hashing",
        kind="function", start_line=1, end_line=10,
    )
    result = extract_test_links([entity])
    assert len(result.links) == 1
    assert result.links[0].requirement_id == "REQ-010"


def test_req_in_docstring():
    """Test correlation finds REQ-xxx in test docstring."""
    entity = CodeEntity(
        file_path="test_api.ts", name="testCreateItem",
        kind="function", start_line=1, end_line=10,
        doc_comment="Tests REQ-003 CRUD operations",
    )
    result = extract_test_links([entity])
    assert len(result.links) == 1
    assert result.links[0].requirement_id == "REQ-003"


def test_no_req_in_test():
    """Test correlation returns empty for tests without requirement refs."""
    entity = CodeEntity(
        file_path="test_utils.ts", name="testHelperFunction",
        kind="function", start_line=1, end_line=5,
    )
    result = extract_test_links([entity])
    assert len(result.links) == 0


def test_link_type_is_tests():
    """Test correlation links should have TESTS link type."""
    entity = CodeEntity(
        file_path="test_auth.py", name="test_REQ_001_startup",
        kind="function", start_line=1, end_line=5,
    )
    result = extract_test_links([entity])
    assert result.links[0].link_type.value == "tests"


def test_multiple_test_entities():
    """Test correlation handles multiple test entities."""
    entities = [
        CodeEntity(file_path="a.py", name="test_REQ_001_start", kind="function", start_line=1, end_line=5),
        CodeEntity(file_path="a.py", name="test_REQ_002_stop", kind="function", start_line=6, end_line=10),
        CodeEntity(file_path="a.py", name="test_no_req", kind="function", start_line=11, end_line=15),
    ]
    result = extract_test_links(entities)
    assert len(result.links) == 2
