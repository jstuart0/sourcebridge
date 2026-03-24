"""Shared requirement data model for parsers."""

from __future__ import annotations

from dataclasses import dataclass, field


@dataclass
class Requirement:
    """A parsed requirement."""

    id: str
    title: str
    description: str
    priority: str = ""
    acceptance_criteria: list[str] = field(default_factory=list)
    tags: list[str] = field(default_factory=list)
    source: str = ""
