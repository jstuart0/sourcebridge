"""Test name correlation linker.

Matches test functions to requirements by naming conventions:
  - test_req_xxx_* or test_REQ_xxx_*
  - TestReqXxx* or TestREQXxx*
  - test_*_req_xxx
"""

from __future__ import annotations

import re

from workers.linking.types import CodeEntity, Link, LinkResult, LinkSource, LinkType

_TEST_REQ_PATTERN = re.compile(
    r"(?:test_?)?(?:req|REQ)[_-]?(\d+)|"  # test_req_001, test_REQ_001
    r"(?:Test)?(?:Req|REQ)[_-]?(\d+)",     # TestReq001, TestREQ001
    re.IGNORECASE,
)

_REQ_IN_NAME = re.compile(r"\b([A-Z]+-\d+)\b")


def extract_test_links(test_entities: list[CodeEntity]) -> LinkResult:
    """Extract requirement links from test function names.

    Args:
        test_entities: Code entities that are test functions/methods.

    Returns:
        LinkResult with discovered links.
    """
    result = LinkResult()

    for entity in test_entities:
        req_ids: set[str] = set()

        # Check for REQ-xxx pattern directly in name
        for match in _REQ_IN_NAME.finditer(entity.name):
            req_ids.add(match.group(1))

        # Check for test_req_NNN naming pattern
        for match in _TEST_REQ_PATTERN.finditer(entity.name):
            num = match.group(1) or match.group(2)
            if num:
                req_ids.add(f"REQ-{num.zfill(3)}")

        # Also check doc_comment for REQ references
        if entity.doc_comment:
            for match in _REQ_IN_NAME.finditer(entity.doc_comment):
                req_ids.add(match.group(1))

        for req_id in req_ids:
            result.links.append(
                Link(
                    requirement_id=req_id,
                    entity=entity,
                    source=LinkSource.TEST,
                    link_type=LinkType.TESTS,
                    confidence=0.85,
                    rationale=f"Test name '{entity.name}' references {req_id}",
                )
            )

    return result
