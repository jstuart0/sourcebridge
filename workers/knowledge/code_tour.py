# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Code tour generation using LLM."""

from __future__ import annotations

import json
from dataclasses import dataclass, field

import structlog

from workers.common.llm.provider import LLMProvider, LLMResponse
from workers.knowledge.cliff_notes import _parse_sections
from workers.knowledge.prompts.code_tour import (
    CODE_TOUR_SYSTEM,
    build_code_tour_prompt,
)
from workers.reasoning.types import LLMUsageRecord

log = structlog.get_logger()


@dataclass
class TourStop:
    """A single stop in a code tour."""

    order: int
    title: str
    description: str  # markdown
    file_path: str
    line_start: int = 0
    line_end: int = 0


@dataclass
class CodeTourResult:
    """The full code tour generation result."""

    stops: list[TourStop] = field(default_factory=list)


def _parse_stops(raw: str) -> list[dict[str, object]]:
    """Parse JSON array from LLM response using the shared robust parser."""
    return _parse_sections(raw)


async def generate_code_tour(
    provider: LLMProvider,
    repository_name: str,
    audience: str,
    depth: str,
    snapshot_json: str,
    theme: str = "",
    model_override: str | None = None,
) -> tuple[CodeTourResult, LLMUsageRecord]:
    """Generate a code tour from a repository snapshot."""
    prompt = build_code_tour_prompt(repository_name, audience, depth, snapshot_json, theme)

    response: LLMResponse = await provider.complete(
        prompt, system=CODE_TOUR_SYSTEM, temperature=0.0, model=model_override,
    )

    try:
        raw_stops = _parse_stops(response.content)
    except (json.JSONDecodeError, ValueError, TypeError) as exc:
        log.warning("code_tour_parse_fallback", error=str(exc))
        raw_stops = [
            {
                "order": 1,
                "title": "Overview",
                "description": response.content,
                "file_path": "",
                "line_start": 0,
                "line_end": 0,
            }
        ]

    stops: list[TourStop] = []
    for raw in raw_stops:
        if not isinstance(raw, dict):
            raw = {"title": str(raw)[:160], "description": str(raw)}
        stops.append(
            TourStop(
                order=raw.get("order", len(stops) + 1),
                title=raw.get("title", "Untitled"),
                description=raw.get("description", ""),
                file_path=raw.get("file_path", ""),
                line_start=raw.get("line_start", 0),
                line_end=raw.get("line_end", 0),
            )
        )

    usage = LLMUsageRecord(
        provider="llm",
        model=response.model,
        input_tokens=response.input_tokens,
        output_tokens=response.output_tokens,
        operation="code_tour",
        entity_name=repository_name,
    )

    return CodeTourResult(stops=stops), usage
