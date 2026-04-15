"""Data models for spec extraction pipeline."""

from __future__ import annotations

from dataclasses import dataclass, field


@dataclass
class CandidateSpec:
    """A raw specification candidate extracted from source code."""

    source: str  # "test", "schema", "comment"
    source_file: str  # Repository-relative path (POSIX)
    source_line: int  # Starting line number
    raw_text: str  # The extracted text (test name, schema excerpt, comment body)
    group_key: str  # Grouping key (file-under-test, endpoint path, symbol name)
    language: str  # Source language (go, python, typescript, etc.)
    metadata: dict = field(default_factory=dict)


@dataclass
class RefinedSpec:
    """A spec after LLM refinement and deduplication."""

    source: str
    source_file: str
    source_line: int
    source_files: list[str] = field(default_factory=list)
    text: str = ""  # Refined requirement text
    raw_text: str = ""  # Original extraction
    group_key: str = ""
    language: str = ""
    keywords: list[str] = field(default_factory=list)
    confidence: str = "medium"  # "high", "medium", "low"
    llm_refined: bool = False


@dataclass
class LLMUsageRecord:
    """Tracks LLM token usage for a spec extraction run."""

    model: str
    input_tokens: int = 0
    output_tokens: int = 0


@dataclass
class ExtractionResult:
    """Output of the full extraction pipeline."""

    specs: list[RefinedSpec]
    total_candidates: int
    usage: LLMUsageRecord | None
    warnings: list[str] = field(default_factory=list)
