# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Types for the knowledge generation module."""

from __future__ import annotations

from dataclasses import dataclass, field


@dataclass
class EvidenceRef:
    """A reference to a source artifact backing a knowledge section."""

    source_type: str  # "file", "symbol", "requirement", "doc"
    source_id: str = ""
    file_path: str = ""
    line_start: int = 0
    line_end: int = 0
    rationale: str = ""


@dataclass
class CliffNotesSection:
    """A single section of a cliff-notes report."""

    title: str
    content: str  # markdown
    summary: str  # one-line summary
    confidence: str = "medium"  # "high", "medium", "low"
    inferred: bool = False
    evidence: list[EvidenceRef] = field(default_factory=list)


@dataclass
class CliffNotesResult:
    """The full cliff-notes generation result."""

    sections: list[CliffNotesSection] = field(default_factory=list)
