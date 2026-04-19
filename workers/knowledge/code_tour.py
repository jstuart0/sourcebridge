# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Code tour generation using LLM."""

from __future__ import annotations

import json
from dataclasses import dataclass, field

import structlog

from workers.common.llm.provider import (
    LLMProvider,
    LLMResponse,
    check_prompt_budget,
    complete_with_optional_model,
    require_nonempty,
)
from workers.knowledge.cliff_notes import _parse_sections
from workers.knowledge.evidence import evaluate_evidence_gate, extract_code_tour_stop_evidence
from workers.knowledge.parse_utils import (
    coerce_int,
    collect_snapshot_file_paths,
    meets_confidence_floor,
)
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
    trail: str = ""
    modification_hints: list[str] = field(default_factory=list)
    confidence: str = "medium"
    refinement_status: str = ""


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
    depth = (depth or "").strip().lower()
    prompt = build_code_tour_prompt(repository_name, audience, depth, snapshot_json, theme)

    check_prompt_budget(
        prompt,
        system=CODE_TOUR_SYSTEM,
        context="code_tour:repository",
    )

    response: LLMResponse = require_nonempty(
        await complete_with_optional_model(
            provider,
            prompt,
            system=CODE_TOUR_SYSTEM,
            temperature=0.0,
            # Same rationale as learning_path: 10+ stops with detailed
            # descriptions can exceed the default 4096-token cap,
            # truncating the JSON and sending the parser into fallback.
            max_tokens=16384,
            model=model_override,
        ),
        context="code_tour:repository",
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
                order=coerce_int(raw.get("order"), len(stops) + 1),
                title=raw.get("title", "Untitled"),
                description=raw.get("description", ""),
                file_path=raw.get("file_path", ""),
                line_start=coerce_int(raw.get("line_start"), 0),
                line_end=coerce_int(raw.get("line_end"), 0),
                trail=raw.get("trail", ""),
                modification_hints=raw.get("modification_hints", []),
            )
        )

    if depth == "deep":
        # Post-parse hallucination filter: drop stops anchored at file
        # paths that aren't in the snapshot. A code-tour stop without a
        # real anchor would open to a 404 in the UI and provides false
        # evidence in the artifact; better to lose the stop than keep a
        # bogus one. Mirrors the learning_path filter that took haiku
        # from 31% → 0% hallucination in iteration 5.
        known_paths = collect_snapshot_file_paths(snapshot_json)
        if known_paths:
            kept: list[TourStop] = []
            dropped_paths: list[str] = []
            for stop in stops:
                fp = (stop.file_path or "").strip()
                if not fp or fp in known_paths:
                    kept.append(stop)
                else:
                    dropped_paths.append(fp)
            if dropped_paths:
                log.info(
                    "code_tour_dropped_hallucinated_stops",
                    dropped=dropped_paths[:10],
                    dropped_count=len(dropped_paths),
                    kept_count=len(kept),
                )
                for index, stop in enumerate(kept, start=1):
                    stop.order = index
                stops = kept

        for stop in stops:
            gate = evaluate_evidence_gate(
                text=f"{stop.description}\n" + "\n".join(stop.modification_hints),
                evidence=extract_code_tour_stop_evidence(stop.file_path, stop.line_start, stop.line_end),
                minimum=1,
            )
            if gate.below_threshold or gate.forbidden_phrases or not stop.trail:
                stop.confidence = "low"
                stop.refinement_status = "needs_evidence"
            else:
                # A code-tour stop only grounds one file path (the one the
                # stop is anchored on), so the floor threshold on files
                # drops to 1. Specific-identifier bar stays at 2 — a
                # stop that can't name at least two concrete types or
                # functions in its description isn't adding much over
                # the file reference alone.
                stop_text = f"{stop.description}\n" + "\n".join(stop.modification_hints)
                if meets_confidence_floor(
                    current_confidence=stop.confidence,
                    unique_file_paths={stop.file_path} if stop.file_path else set(),
                    content=stop_text,
                    min_files=1,
                    min_identifiers=2,
                ):
                    stop.confidence = "high"
                    stop.refinement_status = ""

    usage = LLMUsageRecord(
        provider="llm",
        model=response.model,
        input_tokens=response.input_tokens,
        output_tokens=response.output_tokens,
        operation="code_tour",
        entity_name=repository_name,
    )

    return CodeTourResult(stops=stops), usage
