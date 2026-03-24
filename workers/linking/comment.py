"""Comment-based linker.

Extracts requirement references from code comments, docstrings, and JSDoc.
Patterns matched:
  - // REQ-xxx: description
  - # REQ-xxx: description
  - * REQ-xxx: description  (JSDoc)
  - /// REQ-xxx: description  (Rust doc)
  - //! REQ-xxx: description  (Rust module doc)
  - # Implements: REQ-xxx
  - // Implements REQ-xxx
"""

from __future__ import annotations

import re

from workers.linking.types import CodeEntity, Link, LinkResult, LinkSource, LinkType

# Matches REQ-xxx, ENG-xxx, etc. in comments and docstrings
_REQ_PATTERN = re.compile(r"(?:^|\s|[/*#!\"'])([A-Z]+-\d+)(?:\s*:|(?=\s|$))", re.MULTILINE)


def extract_comment_links(entities: list[CodeEntity]) -> LinkResult:
    """Extract requirement links from code comments and docstrings.

    Scans each entity's content and doc_comment for requirement ID patterns.

    Args:
        entities: Code entities with content and doc_comment fields populated.

    Returns:
        LinkResult with discovered links.
    """
    result = LinkResult()

    for entity in entities:
        # Search in doc_comment
        req_ids: set[str] = set()
        if entity.doc_comment:
            for match in _REQ_PATTERN.finditer(entity.doc_comment):
                req_ids.add(match.group(1))

        # Search in content (for inline comments within the function body)
        if entity.content:
            for line in entity.content.split("\n"):
                stripped = line.strip()
                # Only scan comment lines, not code
                if _is_comment_line(stripped, entity.language):
                    for match in _REQ_PATTERN.finditer(stripped):
                        req_ids.add(match.group(1))

        for req_id in req_ids:
            result.links.append(
                Link(
                    requirement_id=req_id,
                    entity=entity,
                    source=LinkSource.COMMENT,
                    link_type=LinkType.IMPLEMENTS,
                    confidence=0.95,
                    rationale=f"Comment reference to {req_id} in {entity.file_path}:{entity.name}",
                )
            )

    return result


def _is_comment_line(line: str, language: str) -> bool:
    """Check if a line is a comment."""
    if not line:
        return False
    if line.startswith("//") or line.startswith("#") or line.startswith("*") or line.startswith("/*"):
        return True
    if line.startswith("///") or line.startswith("//!"):
        return True
    return bool(language == "python" and (line.startswith('"""') or line.startswith("'''")))
