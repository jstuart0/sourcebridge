# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Learning path generation using LLM."""

from __future__ import annotations

import json
from dataclasses import dataclass, field

import structlog

from workers.common.llm.provider import LLMProvider, LLMResponse
from workers.knowledge.cliff_notes import _parse_sections
from workers.knowledge.prompts.learning_path import (
    LEARNING_PATH_SYSTEM,
    build_learning_path_prompt,
)
from workers.reasoning.types import LLMUsageRecord

log = structlog.get_logger()


@dataclass
class LearningStep:
    """A single step in a learning path."""

    order: int
    title: str
    objective: str
    content: str  # markdown
    file_paths: list[str] = field(default_factory=list)
    symbol_ids: list[str] = field(default_factory=list)
    estimated_time: str = ""


@dataclass
class LearningPathResult:
    """The full learning path generation result."""

    steps: list[LearningStep] = field(default_factory=list)


def _parse_steps(raw: str) -> list[dict[str, object]]:
    """Parse JSON array from LLM response using the shared robust parser."""
    return _parse_sections(raw)


async def generate_learning_path(
    provider: LLMProvider,
    repository_name: str,
    audience: str,
    depth: str,
    snapshot_json: str,
    focus_area: str = "",
    model_override: str | None = None,
) -> tuple[LearningPathResult, LLMUsageRecord]:
    """Generate a learning path from a repository snapshot."""
    prompt = build_learning_path_prompt(repository_name, audience, depth, snapshot_json, focus_area)

    response: LLMResponse = await provider.complete(
        prompt, system=LEARNING_PATH_SYSTEM, temperature=0.0, model=model_override,
    )

    try:
        raw_steps = _parse_steps(response.content)
    except (json.JSONDecodeError, ValueError, TypeError) as exc:
        log.warning("learning_path_parse_fallback", error=str(exc))
        raw_steps = [
            {
                "order": 1,
                "title": "Getting Started",
                "objective": "Understand the repository structure.",
                "content": response.content,
                "file_paths": [],
                "symbol_ids": [],
                "estimated_time": "15 minutes",
            }
        ]

    steps: list[LearningStep] = []
    for raw in raw_steps:
        if not isinstance(raw, dict):
            raw = {"title": str(raw)[:160], "content": str(raw)}
        steps.append(
            LearningStep(
                order=raw.get("order", len(steps) + 1),
                title=raw.get("title", "Untitled"),
                objective=raw.get("objective", ""),
                content=raw.get("content", ""),
                file_paths=raw.get("file_paths", []),
                symbol_ids=raw.get("symbol_ids", []),
                estimated_time=raw.get("estimated_time", ""),
            )
        )

    usage = LLMUsageRecord(
        provider="llm",
        model=response.model,
        input_tokens=response.input_tokens,
        output_tokens=response.output_tokens,
        operation="learning_path",
        entity_name=repository_name,
    )

    return LearningPathResult(steps=steps), usage
