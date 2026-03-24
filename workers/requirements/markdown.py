"""Markdown requirement parser.

Parses structured markdown documents to extract requirements with IDs,
titles, descriptions, priority, and acceptance criteria.

Expected format:
    ## REQ-001: Title
    Description text.
    - **Priority:** High
    - **Acceptance Criteria:**
      - Criterion 1
      - Criterion 2
"""

import re

from workers.requirements.models import Requirement

# Matches ## REQ-001: Title  or  ## ENG-42: Title  etc.
_HEADING_RE = re.compile(
    r"^##\s+(?P<id>[A-Z]+-\d+):\s*(?P<title>.+)$", re.MULTILINE
)
_PRIORITY_RE = re.compile(r"\*\*Priority:\*\*\s*(?P<priority>\w+)")


def parse_markdown(content: str, source: str = "") -> list[Requirement]:
    """Parse a markdown document and extract requirements.

    Args:
        content: The markdown text.
        source: Optional source path for attribution.

    Returns:
        List of parsed Requirement objects.
    """
    requirements: list[Requirement] = []

    # Split into sections by ## headings
    sections = re.split(r"(?=^## )", content, flags=re.MULTILINE)

    for section in sections:
        section = section.strip()
        if not section:
            continue

        match = _HEADING_RE.match(section)
        if not match:
            continue

        req_id = match.group("id")
        title = match.group("title").strip()

        # Extract description (first paragraph after heading)
        lines = section.split("\n")
        desc_lines: list[str] = []
        in_criteria = False
        priority = ""
        criteria: list[str] = []

        for line in lines[1:]:  # Skip heading line
            stripped = line.strip()

            if not stripped:
                if not in_criteria and desc_lines:
                    pass  # End of description paragraph
                continue

            # Check for priority
            pri_match = _PRIORITY_RE.search(stripped)
            if pri_match:
                priority = pri_match.group("priority")
                continue

            # Check for acceptance criteria header
            if "Acceptance Criteria" in stripped:
                in_criteria = True
                continue

            # Collect acceptance criteria
            if in_criteria and stripped.startswith("- "):
                criteria.append(stripped[2:].strip())
                continue

            # Collect description
            if not in_criteria and not stripped.startswith("- **"):
                desc_lines.append(stripped)

        description = " ".join(desc_lines).strip()

        requirements.append(
            Requirement(
                id=req_id,
                title=title,
                description=description,
                priority=priority,
                acceptance_criteria=criteria,
                source=source,
            )
        )

    return requirements
