"""Types for the linking engine."""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import StrEnum


class LinkSource(StrEnum):
    COMMENT = "comment"
    SEMANTIC = "semantic"
    REFERENCE = "reference"
    TEST = "test"
    MANUAL = "manual"


class LinkType(StrEnum):
    IMPLEMENTS = "implements"
    PARTIALLY_IMPLEMENTS = "partially_implements"
    TESTS = "tests"
    REFERENCES = "references"


@dataclass
class CodeEntity:
    """A code entity that can be linked to a requirement."""

    file_path: str
    name: str
    kind: str  # function, class, method, etc.
    start_line: int
    end_line: int
    content: str = ""
    doc_comment: str = ""
    language: str = ""
    id: str = ""  # original symbol UUID from the store


@dataclass
class Link:
    """A link between a requirement and a code entity."""

    requirement_id: str
    entity: CodeEntity
    source: LinkSource
    link_type: LinkType = LinkType.IMPLEMENTS
    confidence: float = 0.0
    rationale: str = ""


@dataclass
class LinkResult:
    """Result from a linker."""

    links: list[Link] = field(default_factory=list)
    warnings: list[str] = field(default_factory=list)
