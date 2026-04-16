"""Types for the reasoning module."""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import StrEnum


class SummaryLevel(StrEnum):
    FUNCTION = "function"
    FILE = "file"
    MODULE = "module"


@dataclass
class Summary:
    """Structured code summary."""

    purpose: str
    inputs: list[str] = field(default_factory=list)
    outputs: list[str] = field(default_factory=list)
    dependencies: list[str] = field(default_factory=list)
    side_effects: list[str] = field(default_factory=list)
    risks: list[str] = field(default_factory=list)
    confidence: float = 0.0
    level: str = "function"
    entity_name: str = ""
    content_hash: str = ""


class ReviewTemplate(StrEnum):
    SECURITY = "security"
    SOLID = "solid"
    PERFORMANCE = "performance"
    RELIABILITY = "reliability"
    MAINTAINABILITY = "maintainability"


class Severity(StrEnum):
    CRITICAL = "critical"
    HIGH = "high"
    MEDIUM = "medium"
    LOW = "low"
    INFO = "info"


@dataclass
class Finding:
    """A single review finding."""

    category: str
    severity: str
    message: str
    file_path: str = ""
    start_line: int = 0
    end_line: int = 0
    suggestion: str = ""


@dataclass
class ReviewResult:
    """Result of a structured code review."""

    template: str
    findings: list[Finding] = field(default_factory=list)
    score: float = 0.0


@dataclass
class DiscussionAnswer:
    """Response to a code question."""

    answer: str
    references: list[str] = field(default_factory=list)
    related_requirements: list[str] = field(default_factory=list)


@dataclass
class LLMUsageRecord:
    """Tracks a single LLM call."""

    provider: str
    model: str
    input_tokens: int
    output_tokens: int
    operation: str  # summary, review, discussion, explain
    entity_name: str = ""
