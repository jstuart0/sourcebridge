# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Learning path generation using LLM."""

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
from workers.knowledge.evidence import evaluate_evidence_gate, extract_step_file_symbol_evidence
from workers.knowledge.parse_utils import (
    coerce_int,
    collect_snapshot_file_paths,
    meets_confidence_floor,
)
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
    prerequisite_steps: list[int] = field(default_factory=list)
    difficulty: str = "intermediate"
    exercises: list[str] = field(default_factory=list)
    checkpoint: str = ""
    confidence: str = "medium"
    refinement_status: str = ""


@dataclass
class LearningPathResult:
    """The full learning path generation result."""

    steps: list[LearningStep] = field(default_factory=list)


def _parse_steps(raw: str) -> list[dict[str, object]]:
    """Parse JSON array from LLM response using the shared robust parser."""
    return _parse_sections(raw)


def _collect_snapshot_file_paths(snapshot_json: str) -> set[str]:
    """Back-compat alias for :func:`collect_snapshot_file_paths`.

    The logic moved into ``parse_utils`` so code_tour can reuse the same
    ground-truth set. The existing learning_path tests import this name
    directly, so keep the alias until those tests follow the move.
    """

    return collect_snapshot_file_paths(snapshot_json)


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
    depth = (depth or "").strip().lower()
    prompt = build_learning_path_prompt(repository_name, audience, depth, snapshot_json, focus_area)

    check_prompt_budget(
        prompt,
        system=LEARNING_PATH_SYSTEM,
        context="learning_path:repository",
    )

    response: LLMResponse = require_nonempty(
        await complete_with_optional_model(
            provider,
            prompt,
            system=LEARNING_PATH_SYSTEM,
            temperature=0.0,
            # A DEEP learning path targets 10-15 steps; at ~120 words per
            # step the rendered JSON comfortably exceeds the default 4096
            # cap. 16384 matches the cliff-notes renderer ceiling and
            # gives every cloud + local model room to emit a complete
            # array instead of being truncated mid-section.
            max_tokens=16384,
            model=model_override,
        ),
        context="learning_path:repository",
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
                order=coerce_int(raw.get("order"), len(steps) + 1),
                title=raw.get("title", "Untitled"),
                objective=raw.get("objective", ""),
                content=raw.get("content", ""),
                file_paths=raw.get("file_paths", []),
                symbol_ids=raw.get("symbol_ids", []),
                estimated_time=raw.get("estimated_time", ""),
                prerequisite_steps=[coerce_int(x, 0) for x in (raw.get("prerequisite_steps") or [])],
                difficulty=raw.get("difficulty", "intermediate") or "intermediate",
                exercises=raw.get("exercises", []),
                checkpoint=raw.get("checkpoint", ""),
            )
        )

    if depth == "deep":
        known_paths = _collect_snapshot_file_paths(snapshot_json)
        if known_paths:
            for step in steps:
                raw_paths = list(step.file_paths or [])
                filtered = [p for p in raw_paths if p in known_paths]
                dropped = [p for p in raw_paths if p not in known_paths]
                if dropped:
                    step.file_paths = filtered
                    log.info(
                        "learning_path_dropped_hallucinated_paths",
                        step_title=step.title,
                        dropped=dropped[:5],
                        dropped_count=len(dropped),
                        kept_count=len(filtered),
                    )

        for step in steps:
            gate = evaluate_evidence_gate(
                text=f"{step.objective}\n{step.content}\n" + "\n".join(step.exercises),
                evidence=extract_step_file_symbol_evidence(step.content, step.file_paths),
                minimum=2,
            )
            if gate.below_threshold or gate.forbidden_phrases or not step.exercises:
                step.confidence = "low"
                step.refinement_status = "needs_evidence"
            else:
                # Learning-path steps carry their grounding in
                # ``file_paths`` (the files the learner should read)
                # and their content body. If the step names at least
                # two real files and two specific identifiers, it
                # meets the "you can follow this on your own" bar and
                # gets promoted to high confidence.
                step_text = f"{step.objective}\n{step.content}\n" + "\n".join(step.exercises)
                if meets_confidence_floor(
                    current_confidence=step.confidence,
                    unique_file_paths=set(step.file_paths or []),
                    content=step_text,
                    min_files=2,
                    min_identifiers=2,
                ):
                    step.confidence = "high"
                    step.refinement_status = ""

    usage = LLMUsageRecord(
        provider="llm",
        model=response.model,
        input_tokens=response.input_tokens,
        output_tokens=response.output_tokens,
        operation="learning_path",
        entity_name=repository_name,
    )

    return LearningPathResult(steps=steps), usage
