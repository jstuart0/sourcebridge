"""PR/commit reference linker.

Extracts requirement references from commit messages, branch names, and PR descriptions.
Patterns matched:
  - REQ-xxx in commit messages
  - REQ-xxx in branch names (e.g., feature/REQ-042-payment)
  - Fixes REQ-xxx, Implements REQ-xxx, Closes REQ-xxx
"""

from __future__ import annotations

import re

from workers.linking.types import CodeEntity, Link, LinkResult, LinkSource, LinkType

_REQ_PATTERN = re.compile(r"\b([A-Z]+-\d+)\b")


def extract_reference_links(
    commit_messages: list[str],
    branch_name: str = "",
    changed_entities: list[CodeEntity] | None = None,
) -> LinkResult:
    """Extract requirement links from VCS metadata.

    Args:
        commit_messages: List of commit messages to scan.
        branch_name: Current branch name.
        changed_entities: Code entities changed in those commits.

    Returns:
        LinkResult with discovered links.
    """
    result = LinkResult()
    req_ids: set[str] = set()

    # Scan commit messages
    for msg in commit_messages:
        for match in _REQ_PATTERN.finditer(msg):
            req_ids.add(match.group(1))

    # Scan branch name
    if branch_name:
        for match in _REQ_PATTERN.finditer(branch_name):
            req_ids.add(match.group(1))

    if not req_ids:
        return result

    # If we have changed entities, link each req to each entity
    entities = changed_entities or []
    for req_id in req_ids:
        if entities:
            for entity in entities:
                result.links.append(
                    Link(
                        requirement_id=req_id,
                        entity=entity,
                        source=LinkSource.REFERENCE,
                        link_type=LinkType.IMPLEMENTS,
                        confidence=0.7,
                        rationale=f"VCS reference to {req_id} in commit/branch",
                    )
                )
        else:
            # No entities to link to — create a link with a placeholder
            result.links.append(
                Link(
                    requirement_id=req_id,
                    entity=CodeEntity(file_path="", name="", kind="", start_line=0, end_line=0),
                    source=LinkSource.REFERENCE,
                    link_type=LinkType.REFERENCES,
                    confidence=0.5,
                    rationale=f"VCS reference to {req_id} without specific code entity",
                )
            )

    return result
