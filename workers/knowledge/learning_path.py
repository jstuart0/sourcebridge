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
    SnapshotTooLargeError,
    check_prompt_budget,
    complete_with_optional_model,
    require_nonempty,
)
from workers.knowledge.evidence import evaluate_evidence_gate, extract_step_file_symbol_evidence
from workers.knowledge.parse_utils import (
    coerce_int,
    collect_snapshot_file_paths,
    collect_snapshot_path_signals,
    meets_confidence_floor,
    parse_json_sections,
    parse_with_fallback,
    path_looks_grounded,
)
from workers.knowledge.prompts.learning_path import (
    LEARNING_PATH_STEP_REPAIR_TEMPLATE,
    LEARNING_PATH_SYSTEM,
    build_learning_path_prompt,
)
from workers.knowledge.thresholds import MIN_FILES_LEARNING_PATH, MIN_IDENTIFIERS_DEFAULT, TITLE_SUMMARY_MAX_CHARS
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
    return parse_with_fallback(
        raw,
        fallback_item_fn=lambda text: {
            "order": 1,
            "title": "Getting Started",
            "objective": "Understand the repository structure.",
            "content": text,
            "file_paths": [],
            "symbol_ids": [],
            "estimated_time": "15 minutes",
        },
    )


def _collect_snapshot_file_paths(snapshot_json: str) -> set[str]:
    """Back-compat alias for :func:`collect_snapshot_file_paths`.

    The logic moved into ``parse_utils`` so code_tour can reuse the same
    ground-truth set. The existing learning_path tests import this name
    directly, so keep the alias until those tests follow the move.
    """

    return collect_snapshot_file_paths(snapshot_json)


def _should_accept_repaired_step(current: LearningStep, repaired: LearningStep) -> bool:
    """Decide whether to swap in a repaired step.

    Mirrors cliff_notes' ``_should_accept_repaired_section`` with ``file_paths``
    substituted for evidence refs.  The simplified confidence check is valid
    because the original is always LOW at repair time.
    """
    current_files = len([p for p in (current.file_paths or []) if p])
    repaired_files = len([p for p in (repaired.file_paths or []) if p])
    if current_files > 0 and repaired_files == 0:
        return False
    # original is always LOW here; reject if repair stays LOW (collapsed
    # cliff_notes' `if repaired_low and not current_low` since current_low is True)
    if (repaired.confidence or "").lower() == "low":
        return False
    if repaired_files < current_files and len(repaired.content) <= len(current.content):
        return False
    return True


async def _repair_low_confidence_steps(
    provider: LLMProvider,
    repository_name: str,
    audience: str,
    snapshot_json: str,
    steps: list[LearningStep],
    model_override: str | None,
) -> tuple[list[LearningStep], int, int, bool, bool]:
    """Attempt one repair LLM call per LOW-confidence step (sequential, single-attempt).

    Returns ``(updated_steps, repair_in_toks, repair_out_toks, any_accepted, any_fired)``.
    ``any_fired`` is True when at least one repair attempt was started (including
    attempts that were aborted by budget or LLM exceptions).
    Mirrors the architecture of cliff_notes' ``_repair_deep_sections`` but as a
    module-level free function (no parallelism needed for this artifact — D8).
    """
    known_paths, known_dirs = collect_snapshot_path_signals(snapshot_json)
    by_order = {step.order: step for step in steps}
    repair_in_toks = 0
    repair_out_toks = 0
    any_accepted = False
    any_fired = False

    for step in steps:
        if step.confidence != "low":
            continue

        # H2 guard: skip steps that are LOW solely because exercises are missing.
        # Repair targets content/grounding; it can't synthesise an exercises list
        # that the model omitted from the original render.
        gate = evaluate_evidence_gate(
            text=f"{step.objective}\n{step.content}\n" + "\n".join(step.exercises),
            evidence=extract_step_file_symbol_evidence(step.content, step.file_paths),
            minimum=2,
        )
        if not gate.below_threshold and not gate.forbidden_phrases and not step.exercises:
            continue

        # Mark as fired: a repair attempt is being made for this step.
        any_fired = True

        prompt = LEARNING_PATH_STEP_REPAIR_TEMPLATE.format(
            repository_name=repository_name or "repository",
            audience=audience,
            step_order=step.order,
            step_title=step.title,
            step_objective=step.objective,
            current_content=step.content,
            current_exercises="\n".join(step.exercises) if step.exercises else "(none)",
            current_file_paths="\n".join(step.file_paths) if step.file_paths else "(none)",
            snapshot_excerpt=snapshot_json,
        )

        try:
            check_prompt_budget(
                prompt,
                system=LEARNING_PATH_SYSTEM,
                context=f"learning_path:repair:order_{step.order}",
            )
        except SnapshotTooLargeError as exc:
            log.warning(
                "learning_path_repair_skipped_budget_exceeded",
                step_order=step.order,
                error=str(exc),
            )
            continue

        try:
            response_repair = await complete_with_optional_model(
                provider,
                prompt,
                system=LEARNING_PATH_SYSTEM,
                max_tokens=4096,
                temperature=0.0,
                model=model_override,
            )
            require_nonempty(response_repair, context=f"learning_path:repair:order_{step.order}")
        except Exception as exc:
            log.warning(
                "learning_path_repair_skipped",
                step_order=step.order,
                error=str(exc),
            )
            continue

        try:
            raw_results = parse_json_sections(response_repair.content)
        except (json.JSONDecodeError, ValueError, TypeError) as exc:
            log.warning(
                "learning_path_repair_skipped",
                step_order=step.order,
                error=str(exc),
            )
            repair_in_toks += response_repair.input_tokens
            repair_out_toks += response_repair.output_tokens
            continue

        if not raw_results or not isinstance(raw_results[0], dict):
            log.warning(
                "learning_path_repair_skipped",
                step_order=step.order,
                error="empty_or_non_dict",
            )
            repair_in_toks += response_repair.input_tokens
            repair_out_toks += response_repair.output_tokens
            continue

        raw = raw_results[0]
        candidate = LearningStep(
            order=coerce_int(raw.get("order"), step.order),
            title=raw.get("title", step.title),
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

        # Identity check: reject if the repair LLM changed the step order (D4).
        if candidate.order != step.order:
            log.warning(
                "learning_path_repair_skipped",
                step_order=step.order,
                error=f"order_mismatch: got {candidate.order}",
            )
            repair_in_toks += response_repair.input_tokens
            repair_out_toks += response_repair.output_tokens
            continue

        # Path-grounding filter on repair output (matching main render logic).
        if known_paths or known_dirs:
            raw_paths = list(candidate.file_paths or [])
            candidate.file_paths = [p for p in raw_paths if path_looks_grounded(p, known_paths, known_dirs)]

        # Re-run evidence gate + floor upgrade to set candidate.confidence.
        cand_gate = evaluate_evidence_gate(
            text=f"{candidate.objective}\n{candidate.content}\n" + "\n".join(candidate.exercises),
            evidence=extract_step_file_symbol_evidence(candidate.content, candidate.file_paths),
            minimum=2,
        )
        if cand_gate.below_threshold or cand_gate.forbidden_phrases or not candidate.exercises:
            candidate.confidence = "low"
            candidate.refinement_status = "needs_evidence"
        else:
            cand_text = f"{candidate.objective}\n{candidate.content}\n" + "\n".join(candidate.exercises)
            if meets_confidence_floor(
                current_confidence=candidate.confidence,
                unique_file_paths=set(candidate.file_paths or []),
                content=cand_text,
                min_files=MIN_FILES_LEARNING_PATH,
                min_identifiers=MIN_IDENTIFIERS_DEFAULT,
            ):
                candidate.confidence = "high"
                candidate.refinement_status = ""

        repair_in_toks += response_repair.input_tokens
        repair_out_toks += response_repair.output_tokens

        if _should_accept_repaired_step(step, candidate):
            by_order[step.order] = candidate
            any_accepted = True

    updated_steps = [by_order[s.order] for s in steps]
    return updated_steps, repair_in_toks, repair_out_toks, any_accepted, any_fired


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

    raw_steps = _parse_steps(response.content)

    steps: list[LearningStep] = []
    for raw in raw_steps:
        if not isinstance(raw, dict):
            raw = {"title": str(raw)[:TITLE_SUMMARY_MAX_CHARS], "content": str(raw)}
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

    repair_in_toks = 0
    repair_out_toks = 0
    any_repair_accepted = False
    repair_fired = False

    if depth == "deep":
        known_paths, known_dirs = collect_snapshot_path_signals(snapshot_json)
        if known_paths or known_dirs:
            for step in steps:
                raw_paths = list(step.file_paths or [])
                filtered = [p for p in raw_paths if path_looks_grounded(p, known_paths, known_dirs)]
                dropped = [p for p in raw_paths if not path_looks_grounded(p, known_paths, known_dirs)]
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
                    min_files=MIN_FILES_LEARNING_PATH,
                    min_identifiers=MIN_IDENTIFIERS_DEFAULT,
                ):
                    step.confidence = "high"
                    step.refinement_status = ""

        if any(step.confidence == "low" for step in steps):
            steps, repair_in_toks, repair_out_toks, any_repair_accepted, repair_fired = (
                await _repair_low_confidence_steps(
                    provider=provider,
                    repository_name=repository_name,
                    audience=audience,
                    snapshot_json=snapshot_json,
                    steps=steps,
                    model_override=model_override,
                )
            )

    if repair_fired and any_repair_accepted:
        operation = "learning_path_repaired"
    elif repair_fired:
        operation = "learning_path_repair_attempted"
    else:
        operation = "learning_path"

    usage = LLMUsageRecord(
        provider=response.provider_name or "",
        model=response.model,
        input_tokens=response.input_tokens + repair_in_toks,
        output_tokens=response.output_tokens + repair_out_toks,
        operation=operation,
        entity_name=repository_name,
    )

    return LearningPathResult(steps=steps), usage
