# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Workflow story generation using LLM."""

from __future__ import annotations

import json
from typing import Any

import structlog

from workers.common.llm.provider import (
    LLMProvider,
    LLMResponse,
    SnapshotTooLargeError,
    check_prompt_budget,
    complete_with_optional_model,
    require_nonempty,
)
from workers.knowledge.evidence import evaluate_evidence_gate, extract_section_evidence_refs
from workers.knowledge.parse_utils import (
    coerce_int,
    coerce_section,
    load_json_dict,
    meets_confidence_floor,
    normalize_text,
    parse_evidence,
    parse_json_sections,
)
from workers.knowledge.prompts.workflow_story import (
    REQUIRED_WORKFLOW_STORY_SECTIONS,
    REQUIRED_WORKFLOW_STORY_SECTIONS_DEEP,
    WORKFLOW_STORY_SYSTEM,
    build_workflow_story_prompt,
)
from workers.knowledge.thresholds import MIN_FILES_CLIFF_NOTES, MIN_IDENTIFIERS_DEFAULT, TITLE_SUMMARY_MAX_CHARS
from workers.knowledge.types import CliffNotesResult, CliffNotesSection, EvidenceRef
from workers.reasoning.types import LLMUsageRecord

log = structlog.get_logger()


def _load_json(raw: str) -> dict[str, Any]:
    return load_json_dict(raw)


def _is_placeholder_content(content: str) -> bool:
    text = content.strip().lower()
    return (
        not text
        or text.startswith("{")
        or text.startswith("[")
        or "insufficient data" in text
        or "insufficient structured content" in text
        or "could not be fully structured" in text
        or "not enough grounded evidence" in text
        or "to be determined" in text
        or "placeholder" in text
        or text == "n/a"
        or text == "tbd"
        or "use the focused scope" in text
        or "the story is grounded in the focused scope" in text
        or "start with the focused scope" in text
        or "check the anchored step first" in text
    )


def _gather_execution_evidence(execution_path: dict[str, Any], limit: int = 4) -> list[EvidenceRef]:
    evidence: list[EvidenceRef] = []
    for step in (execution_path.get("steps") or [])[:limit]:
        if not isinstance(step, dict):
            continue
        file_path = normalize_text(step.get("filePath"))
        label = normalize_text(step.get("label"))
        reason = normalize_text(step.get("reason"))
        evidence.append(
            EvidenceRef(
                source_type="symbol" if step.get("symbolId") else "file",
                source_id=normalize_text(step.get("symbolId")),
                file_path=file_path,
                line_start=coerce_int(step.get("lineStart"), 0),
                line_end=coerce_int(step.get("lineEnd"), 0),
                rationale=reason or f"Execution path step: {label}",
            )
        )
    return evidence


def _gather_scope_evidence(snapshot: dict[str, Any], limit: int = 4) -> list[EvidenceRef]:
    scope = snapshot.get("scope_context") or {}
    evidence: list[EvidenceRef] = []
    target_symbol = scope.get("target_symbol") or {}
    if isinstance(target_symbol, dict) and target_symbol.get("file_path"):
        evidence.append(
            EvidenceRef(
                source_type="symbol",
                source_id=normalize_text(target_symbol.get("id")),
                file_path=normalize_text(target_symbol.get("file_path")),
                line_start=coerce_int(target_symbol.get("start_line"), 0),
                line_end=coerce_int(target_symbol.get("end_line"), 0),
                rationale="Focused symbol for this workflow story.",
            )
        )
    target_file = scope.get("target_file") or {}
    if isinstance(target_file, dict) and target_file.get("path"):
        evidence.append(
            EvidenceRef(
                source_type="file",
                file_path=normalize_text(target_file.get("path")),
                rationale="Focused file for this workflow story.",
            )
        )
    for group_name in ("entry_points", "public_api"):
        for item in (snapshot.get(group_name) or [])[:limit]:
            if not isinstance(item, dict):
                continue
            evidence.append(
                EvidenceRef(
                    source_type="symbol",
                    source_id=normalize_text(item.get("id")),
                    file_path=normalize_text(item.get("file_path")),
                    line_start=coerce_int(item.get("start_line"), 0),
                    line_end=coerce_int(item.get("end_line"), 0),
                    rationale=f"Snapshot {group_name.replace('_', ' ')} reference.",
                )
            )
            if len(evidence) >= limit:
                return evidence
    return evidence[:limit]


def _title_lookup(sections: list[CliffNotesSection], required_sections: list[str]) -> dict[str, CliffNotesSection]:
    """Build a lookup mapping required section titles to LLM sections.

    Uses exact match first, then falls back to case-insensitive prefix matching
    so that LLM-generated titles like "Key Branches" still map to the required
    "Key Branches or Failure Points".
    """
    exact = {section.title: section for section in sections}
    result: dict[str, CliffNotesSection] = {}
    for required in required_sections:
        if required in exact:
            result[required] = exact[required]
            continue
        req_lower = required.lower()
        for section in sections:
            title_lower = section.title.strip().rstrip(":").lower()
            if title_lower == req_lower or req_lower.startswith(title_lower) or title_lower.startswith(req_lower):
                result[required] = section
                break
    return result


def _build_workflow_fallbacks(
    repository_name: str,
    scope_type: str,
    scope_path: str,
    anchor_label: str,
    snapshot: dict[str, Any],
    execution_path: dict[str, Any],
) -> dict[str, CliffNotesSection]:
    scope = snapshot.get("scope_context") or {}
    target_symbol = scope.get("target_symbol") or {}
    target_file = scope.get("target_file") or {}
    entry_label = normalize_text(execution_path.get("entryLabel")) or anchor_label or scope_path or repository_name
    path_steps = [step for step in (execution_path.get("steps") or []) if isinstance(step, dict)]
    path_files = []
    for step in path_steps:
        file_path = normalize_text(step.get("filePath"))
        if file_path and file_path not in path_files:
            path_files.append(file_path)

    actor = "A developer trying to understand or safely change this part of the system."
    if scope_type == "repository":
        actor = "A developer new to this repository who wants to understand how this feature works end to end."
    elif scope_type == "symbol":
        actor = "A developer investigating or changing one concrete code symbol without breaking nearby behavior."
    elif scope_type == "file":
        actor = "A developer working in one file who needs to understand what it owns before editing it."

    goal = anchor_label or f"Understand how {entry_label} works and where to inspect it in the codebase."
    trigger = f"The workflow starts when someone reaches `{entry_label}`."
    if scope_type == "symbol" and target_symbol:
        symbol_name = normalize_text(target_symbol.get("name"))
        symbol_file = normalize_text(target_symbol.get("file_path"))
        trigger = f"The workflow starts at the focused symbol `{symbol_name}` in `{symbol_file}`."
    elif scope_type == "file" and target_file:
        trigger = f"The workflow starts in the focused file `{normalize_text(target_file.get('path'))}`."

    if path_steps:
        step_lines = []
        for index, step in enumerate(path_steps[:6], start=1):
            label = normalize_text(step.get("label")) or f"Step {index}"
            explanation = normalize_text(step.get("explanation"))
            step_lines.append(f"{index}. `{label}`. {explanation}")
        main_steps = "\n".join(step_lines)
    else:
        focus_summary = normalize_text(scope.get("focus_summary"))
        if focus_summary:
            main_steps = focus_summary
        else:
            # Build steps from snapshot entry points and public API
            entry_points = snapshot.get("entry_points") or []
            public_api = snapshot.get("public_api") or []
            step_sources = entry_points[:4] or public_api[:4]
            if step_sources:
                step_lines = []
                for idx, sym in enumerate(step_sources, start=1):
                    if not isinstance(sym, dict):
                        continue
                    name = normalize_text(sym.get("qualified_name") or sym.get("name"))
                    fp = normalize_text(sym.get("file_path"))
                    kind = normalize_text(sym.get("kind"))
                    label = f"`{name}`" if name else f"Step {idx}"
                    detail = f" in `{fp}`" if fp else ""
                    kind_hint = f" ({kind})" if kind else ""
                    step_lines.append(f"{idx}. Start at {label}{kind_hint}{detail}.")
                main_steps = (
                    "\n".join(step_lines)
                    if step_lines
                    else (
                        f"Explore the entry points and public API of {repository_name} to trace the main execution flow."  # noqa: E501
                    )
                )
            else:
                main_steps = (
                    f"Explore the entry points and public API of {repository_name} to trace the main execution flow."
                )

    behind_parts = []
    if path_steps:
        observed_count = execution_path.get("observedStepCount", 0)
        inferred_count = execution_path.get("inferredStepCount", 0)
        behind_parts.append(
            f"The traced path currently includes {len(path_steps)} step(s), "
            f"with {observed_count} observed and {inferred_count} inferred."
        )
    if target_symbol:
        symbol_name = normalize_text(target_symbol.get("name"))
        symbol_file = normalize_text(target_symbol.get("file_path"))
        behind_parts.append(f"The workflow is anchored on `{symbol_name}` in `{symbol_file}`.")
    elif target_file:
        behind_parts.append(f"The focused code lives in `{normalize_text(target_file.get('path'))}`.")
    if path_files:
        behind_parts.append("Key files on this path: " + ", ".join(f"`{path}`" for path in path_files[:4]) + ".")
    if not behind_parts:
        # Build from snapshot metadata when no execution path or scope targets
        languages = snapshot.get("languages") or []
        modules = snapshot.get("modules") or []
        if languages or modules:
            lang_names = [normalize_text(lang.get("name")) for lang in languages[:3] if isinstance(lang, dict)]
            mod_names = [normalize_text(mod.get("path")) for mod in modules[:4] if isinstance(mod, dict)]
            if lang_names:
                behind_parts.append(f"The codebase uses {', '.join(lang_names)}.")
            if mod_names:
                behind_parts.append("Key modules: " + ", ".join(f"`{m}`" for m in mod_names) + ".")
    behind_the_scenes = " ".join(part for part in behind_parts if part) or (
        f"The internals of {repository_name} are best understood by tracing its entry points through the module structure."  # noqa: E501
    )

    if execution_path.get("message"):
        branches = normalize_text(execution_path.get("message"))
    elif path_steps:
        branches = (
            "Follow the traced execution path and inspect any downstream helpers or handoffs if the behavior diverges. "
            "If the traced path is short, treat missing steps as unknown rather than assumed."
        )
    else:
        # Build from snapshot complexity signals
        complex_symbols = snapshot.get("complex_symbols") or []
        if complex_symbols:
            complex_names = [
                normalize_text(sym.get("qualified_name") or sym.get("name"))
                for sym in complex_symbols[:3]
                if isinstance(sym, dict)
            ]
            branches = (
                (
                    "Potential complexity hotspots: " + ", ".join(f"`{n}`" for n in complex_names if n) + ". "
                    "These symbols have high cyclomatic complexity and may contain branching logic or error handling."
                )
                if complex_names
                else f"No execution path is available. Inspect entry points of {repository_name} for branching logic."
            )
        else:
            branches = f"No execution path is available. Inspect entry points of {repository_name} for branching logic and error handling."  # noqa: E501

    inspect_targets = []
    if target_file:
        inspect_targets.append(f"`{normalize_text(target_file.get('path'))}`")
    for path in path_files:
        formatted = f"`{path}`"
        if formatted not in inspect_targets:
            inspect_targets.append(formatted)
    if target_symbol:
        inspect_targets.append(f"`{normalize_text(target_symbol.get('name'))}`")
    if not inspect_targets:
        # Populate from snapshot entry points when no scope targets
        entry_points = snapshot.get("entry_points") or []
        for ep in entry_points[:4]:
            if isinstance(ep, dict) and ep.get("file_path"):
                fp = normalize_text(ep["file_path"])
                formatted = f"`{fp}`"
                if formatted not in inspect_targets:
                    inspect_targets.append(formatted)
    inspect = (
        "Start with " + ", ".join(inspect_targets[:5]) + "."
        if inspect_targets
        else f"Start with the entry points and main modules of {repository_name}."
    )

    shared_evidence = _gather_execution_evidence(execution_path) or _gather_scope_evidence(snapshot)

    return {
        "Goal": CliffNotesSection(
            title="Goal",
            content=goal,
            summary="What this workflow is trying to accomplish.",
            confidence="high" if shared_evidence else "medium",
            inferred=False,
            evidence=shared_evidence[:2],
        ),
        "Likely Actor": CliffNotesSection(
            title="Likely Actor",
            content=actor,
            summary="Who this workflow is for in practice.",
            confidence="medium",
            inferred=True,
            evidence=shared_evidence[:2],
        ),
        "Trigger": CliffNotesSection(
            title="Trigger",
            content=trigger,
            summary="What starts the workflow.",
            confidence="high" if shared_evidence else "medium",
            inferred=False,
            evidence=shared_evidence[:2],
        ),
        "Main Steps": CliffNotesSection(
            title="Main Steps",
            content=main_steps,
            summary="The likely happy-path sequence.",
            confidence="high" if path_steps else "medium",
            inferred=not bool(path_steps),
            evidence=shared_evidence,
        ),
        "Behind the Scenes": CliffNotesSection(
            title="Behind the Scenes",
            content=behind_the_scenes,
            summary="What the app and backend do internally.",
            confidence="medium",
            inferred=not bool(path_steps),
            evidence=shared_evidence,
        ),
        "Key Branches or Failure Points": CliffNotesSection(
            title="Key Branches or Failure Points",
            content=branches,
            summary="Where the workflow can diverge or become uncertain.",
            confidence="medium",
            inferred=True,
            evidence=shared_evidence[:3],
        ),
        "Error Recovery": CliffNotesSection(
            title="Error Recovery",
            content=(
                "Recovery behavior is not explicitly traced in the snapshot, so start by checking the branch "
                "handlers and nearby tests for error-return paths, retries, and user-facing messages."
            ),
            summary="Inspect nearby branch handlers and tests for recovery behavior.",
            confidence="low",
            inferred=True,
            evidence=shared_evidence[:2],
        ),
        "Observability": CliffNotesSection(
            title="Observability",
            content=(
                "The snapshot does not surface explicit logs or metrics for this workflow. Verify nearby handlers, "
                "worker entrypoints, and tests for logging calls, metrics increments, or trace spans."
            ),
            summary="Observability is thin or not surfaced in the snapshot.",
            confidence="low",
            inferred=True,
            evidence=shared_evidence[:1],
        ),
        "Where to Inspect or Modify": CliffNotesSection(
            title="Where to Inspect or Modify",
            content=inspect,
            summary="The most relevant places to read or change.",
            confidence="high" if inspect_targets else "medium",
            inferred=False,
            evidence=shared_evidence[:4],
        ),
    }


def _merge_with_fallbacks(
    sections: list[CliffNotesSection],
    fallback_sections: dict[str, CliffNotesSection],
    *,
    required_sections: list[str],
) -> list[CliffNotesSection]:
    by_title = _title_lookup(sections, required_sections)
    merged: list[CliffNotesSection] = []
    for title in required_sections:
        fallback = fallback_sections[title]
        existing = by_title.get(title)
        if existing is None or _is_placeholder_content(existing.content):
            merged.append(fallback)
            continue
        if not existing.evidence and fallback.evidence:
            existing.evidence = fallback.evidence
        if not existing.summary:
            existing.summary = fallback.summary
        merged.append(existing)
    return merged


async def generate_workflow_story(
    provider: LLMProvider,
    repository_name: str,
    audience: str,
    depth: str,
    snapshot_json: str,
    scope_type: str = "repository",
    scope_path: str = "",
    anchor_label: str = "",
    execution_path_json: str = "",
    model_override: str | None = None,
) -> tuple[CliffNotesResult, LLMUsageRecord]:
    """Generate a workflow story from a repository snapshot."""
    snapshot = _load_json(snapshot_json)
    execution_path = _load_json(execution_path_json)

    prompt = build_workflow_story_prompt(
        repository_name=repository_name,
        audience=audience,
        depth=depth,
        snapshot_json=snapshot_json,
        scope_type=scope_type,
        scope_path=scope_path,
        anchor_label=anchor_label,
        execution_path_json=execution_path_json,
    )

    # Budget check runs OUTSIDE the fallback try/except below so that
    # SNAPSHOT_TOO_LARGE propagates to the caller as an actionable error
    # rather than being swallowed into a templated fallback story.
    check_prompt_budget(
        prompt,
        system=WORKFLOW_STORY_SYSTEM,
        context=f"workflow_story:{scope_type or 'repository'}",
    )

    sections: list[CliffNotesSection] = []
    response: LLMResponse | None = None
    try:
        response = require_nonempty(
            await complete_with_optional_model(
                provider,
                prompt,
                system=WORKFLOW_STORY_SYSTEM,
                temperature=0.0,
                # DEEP workflow stories emit 9 structured sections with
                # long-form Main Steps + Behind-the-Scenes bodies. Haiku
                # routinely hits >22k characters of JSON, which at the
                # old 8192 cap was getting truncated mid-Observability
                # and sending the parser into fallback. Match the 16384
                # ceiling we already use for learning_path and code_tour.
                max_tokens=16384,
                model=model_override,
            ),
            context=f"workflow_story:{scope_type or 'repository'}",
        )
    except SnapshotTooLargeError:
        # Re-raise so the servicer can classify and surface the error.
        raise
    except Exception as exc:
        log.warning("workflow_story_llm_failed_using_fallbacks", error=str(exc))
        response = None

    if response is not None:
        try:
            raw_sections = parse_json_sections(response.content)
        except (json.JSONDecodeError, ValueError, TypeError) as exc:
            log.warning(
                "workflow_story_parse_fallback",
                error=str(exc),
                response_preview=response.content[:2000] if response.content else "(empty)",
            )
            raw_sections = [
                {
                    "title": "Goal",
                    "content": response.content,
                    "summary": "The workflow story could not be fully structured.",
                    "confidence": "low",
                    "inferred": True,
                    "evidence": [],
                }
            ]

        seen_titles: set[str] = set()
        required_sections = REQUIRED_WORKFLOW_STORY_SECTIONS_DEEP if depth == "deep" else REQUIRED_WORKFLOW_STORY_SECTIONS
        for index, raw in enumerate(raw_sections):
            fallback_title = (
                required_sections[index]
                if index < len(required_sections)
                else f"Section {index + 1}"
            )
            normalized = coerce_section(raw, fallback_title=fallback_title, title_summary_max_chars=TITLE_SUMMARY_MAX_CHARS)
            title = str(normalized.get("title", fallback_title))
            evidence = normalized.get("evidence", [])
            if not isinstance(evidence, list):
                evidence = []
            seen_titles.add(title)
            sections.append(
                CliffNotesSection(
                    title=title,
                    content=str(normalized.get("content", "")),
                    summary=str(normalized.get("summary", "")),
                    confidence=str(normalized.get("confidence", "medium")),
                    inferred=bool(normalized.get("inferred", False)),
                    evidence=parse_evidence(evidence),
                )
            )
    fallback_sections = _build_workflow_fallbacks(
        repository_name=repository_name,
        scope_type=scope_type or "repository",
        scope_path=scope_path,
        anchor_label=anchor_label,
        snapshot=snapshot,
        execution_path=execution_path,
    )
    required_sections = REQUIRED_WORKFLOW_STORY_SECTIONS_DEEP if depth == "deep" else REQUIRED_WORKFLOW_STORY_SECTIONS
    sections = _merge_with_fallbacks(sections, fallback_sections, required_sections=required_sections)

    if depth == "deep":
        minimums = {"Main Steps": 3, "Behind the Scenes": 3, "Error Recovery": 2, "Observability": 1, "Where to Inspect or Modify": 3}
        for section in sections:
            gate = evaluate_evidence_gate(
                text=f"{section.summary}\n{section.content}",
                evidence=extract_section_evidence_refs(section.evidence),
                minimum=minimums.get(section.title, 2),
            )
            if gate.below_threshold or gate.forbidden_phrases:
                section.confidence = "low"
                section.refinement_status = "needs_evidence"
            else:
                # Workflow-story sections, like cliff-notes sections, cite
                # multiple evidence refs per section. Use the same 3-files
                # / 2-identifiers mechanical floor that drove the cliff-
                # notes HIGH-confidence upgrades in v13.
                cited = {e.file_path for e in (section.evidence or []) if e.file_path}
                if meets_confidence_floor(
                    current_confidence=section.confidence,
                    unique_file_paths=cited,
                    content=f"{section.summary}\n{section.content}",
                    min_files=MIN_FILES_CLIFF_NOTES,
                    min_identifiers=MIN_IDENTIFIERS_DEFAULT,
                ):
                    section.confidence = "high"
                    section.refinement_status = ""

    # --- Baseline quality instrumentation ---
    evidence_by_type: dict[str, int] = {}
    total_evidence = 0
    sections_with_content = 0
    placeholder_sections = 0
    for sec in sections:
        content = sec.content or ""
        if content and content != "*Insufficient data to generate this section.*":
            if any(p in content.lower() for p in ["placeholder", "to be determined", "tbd", "lorem ipsum"]):
                placeholder_sections += 1
            else:
                sections_with_content += 1
        for ev in sec.evidence:
            evidence_by_type[ev.source_type] = evidence_by_type.get(ev.source_type, 0) + 1
            total_evidence += 1

    log.info(
        "workflow_story_quality_metrics",
        scope_type=scope_type or "repository",
        scope_path=scope_path,
        repository=repository_name,
        depth=depth,
        total_sections=len(sections),
        sections_with_content=sections_with_content,
        placeholder_sections=placeholder_sections,
        total_evidence=total_evidence,
        evidence_by_type=evidence_by_type,
        has_execution_path=bool(execution_path_json),
        execution_path_steps=len(execution_path.get("steps") or []) if isinstance(execution_path, dict) else 0,
    )

    usage = LLMUsageRecord(
        provider="llm",
        model=response.model if response else "fallback",
        input_tokens=response.input_tokens if response else 0,
        output_tokens=response.output_tokens if response else 0,
        operation="workflow_story",
        entity_name=repository_name,
    )

    return CliffNotesResult(sections=sections), usage
