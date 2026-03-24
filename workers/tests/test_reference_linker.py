"""Tests for the PR/commit reference linker."""

from workers.linking.reference import extract_reference_links
from workers.linking.types import CodeEntity


def test_extract_from_commit_message():
    """Reference linker finds REQ-xxx in commit messages."""
    result = extract_reference_links(
        commit_messages=["Fix REQ-042: payment validation", "Update docs"],
    )
    assert len(result.links) == 1
    assert result.links[0].requirement_id == "REQ-042"


def test_extract_from_branch_name():
    """Reference linker finds REQ-xxx in branch names."""
    result = extract_reference_links(
        commit_messages=[],
        branch_name="feature/REQ-001-server-startup",
    )
    assert len(result.links) == 1
    assert result.links[0].requirement_id == "REQ-001"


def test_extract_with_entities():
    """Reference linker links to changed entities."""
    entity = CodeEntity(
        file_path="auth.py", name="login", kind="function",
        start_line=1, end_line=10,
    )
    result = extract_reference_links(
        commit_messages=["Implement REQ-011 session management"],
        changed_entities=[entity],
    )
    assert len(result.links) == 1
    assert result.links[0].entity.name == "login"
    assert result.links[0].requirement_id == "REQ-011"


def test_no_references():
    """Reference linker returns empty when no REQ patterns found."""
    result = extract_reference_links(
        commit_messages=["Fix typo in readme", "Update CI config"],
        branch_name="main",
    )
    assert len(result.links) == 0


def test_multiple_reqs_in_commit():
    """Reference linker finds multiple REQ IDs in a single commit."""
    result = extract_reference_links(
        commit_messages=["Implements REQ-001 and REQ-003"],
    )
    req_ids = {lnk.requirement_id for lnk in result.links}
    assert req_ids == {"REQ-001", "REQ-003"}
