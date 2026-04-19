# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Cliff notes generation using LLM."""

from __future__ import annotations

import json

import structlog

from workers.common.llm.provider import (
    LLMProvider,
    LLMResponse,
    check_prompt_budget,
    complete_with_optional_model,
    require_nonempty,
)
from workers.knowledge.prompts.cliff_notes import (
    CLIFF_NOTES_SYSTEM,
    REQUIRED_SECTIONS,
    REQUIRED_SECTIONS_DEEP_REPOSITORY,
    REQUIRED_SECTIONS_BY_SCOPE,
    build_cliff_notes_prompt,
)
from workers.knowledge.parse_utils import coerce_int as _coerce_int
from workers.knowledge.parse_utils import (
    coerce_section,
    normalize_text,
    parse_evidence,
    parse_json_sections,
)
from workers.knowledge.evidence import (
    evaluate_evidence_gate,
    extract_section_evidence_refs,
    find_section_weakness_phrases,
    is_valid_evidence_path,
    strip_forbidden_phrase_sentences,
    strip_unsupported_claim_sentences,
)
from workers.knowledge.thresholds import DEEP_MIN_EVIDENCE, TITLE_SUMMARY_MAX_CHARS
from workers.knowledge.types import CliffNotesResult, CliffNotesSection, EvidenceRef
from workers.reasoning.types import LLMUsageRecord

log = structlog.get_logger()


async def generate_cliff_notes(
    provider: LLMProvider,
    repository_name: str,
    audience: str,
    depth: str,
    snapshot_json: str,
    scope_type: str = "repository",
    scope_path: str = "",
    model_override: str | None = None,
) -> tuple[CliffNotesResult, LLMUsageRecord]:
    """Generate cliff notes from a repository snapshot.

    Returns a CliffNotesResult with all required sections and an LLMUsageRecord.
    """
    effective_scope = scope_type or "repository"
    if depth == "deep" and effective_scope == "repository":
        required_sections = REQUIRED_SECTIONS_DEEP_REPOSITORY
    else:
        required_sections = REQUIRED_SECTIONS_BY_SCOPE.get(effective_scope, REQUIRED_SECTIONS)
    prompt = build_cliff_notes_prompt(repository_name, audience, depth, snapshot_json, effective_scope, scope_path)

    check_prompt_budget(
        prompt,
        system=CLIFF_NOTES_SYSTEM,
        context=f"cliff_notes:{effective_scope}",
    )

    response: LLMResponse = require_nonempty(
        await complete_with_optional_model(
            provider,
            prompt,
            system=CLIFF_NOTES_SYSTEM,
            temperature=0.0,
            max_tokens=8192,
            model=model_override,
        ),
        context=f"cliff_notes:{effective_scope or 'repository'}",
    )

    try:
        raw_sections = parse_json_sections(response.content)
    except (json.JSONDecodeError, ValueError, TypeError) as exc:
        log.warning("cliff_notes_parse_fallback", error=str(exc))
        # Fallback: return the raw content as a single section
        raw_sections = [
            {
                "title": "System Purpose",
                "content": response.content,
                "summary": "LLM output could not be parsed into structured sections.",
                "confidence": "low",
                "inferred": True,
                "evidence": [],
            }
        ]

    sections: list[CliffNotesSection] = []
    seen_titles: set[str] = set()

    for index, raw in enumerate(raw_sections):
        fallback_title = required_sections[index] if index < len(required_sections) else f"Section {index + 1}"
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

    # Ensure all required sections are present (add stubs for missing ones)
    for req_title in required_sections:
        if req_title not in seen_titles:
            sections.append(
                CliffNotesSection(
                    title=req_title,
                    content="*Insufficient data to generate this section.*",
                    summary="Not enough information available.",
                    confidence="low",
                    inferred=True,
                )
            )

    if required_sections == REQUIRED_SECTIONS_DEEP_REPOSITORY:
        evidence_store_text = snapshot_json
        for section in sections:
            gate = evaluate_evidence_gate(
                text=f"{section.summary}\n{section.content}",
                evidence=extract_section_evidence_refs(section.evidence),
                minimum=DEEP_MIN_EVIDENCE.get(section.title, 3),
                evidence_store_text=evidence_store_text,
            )
            if gate.unsupported_claim_terms:
                section.content = strip_unsupported_claim_sentences(section.content, gate.unsupported_claim_terms)
                section.summary = strip_unsupported_claim_sentences(section.summary, gate.unsupported_claim_terms)
                section.confidence = "low"
                section.inferred = True
                section.refinement_status = "unsupported_claims"
                continue
            if gate.below_threshold or gate.forbidden_phrases:
                if gate.forbidden_phrases:
                    section.content = strip_forbidden_phrase_sentences(section.content, gate.forbidden_phrases)
                    section.summary = strip_forbidden_phrase_sentences(section.summary, gate.forbidden_phrases)
                section.confidence = "low"
                section.refinement_status = "needs_evidence"
            weakness_phrases = find_section_weakness_phrases(
                section.title,
                f"{section.summary}\n{section.content}",
            )
            if weakness_phrases:
                section.content = strip_forbidden_phrase_sentences(section.content, weakness_phrases)
                section.summary = strip_forbidden_phrase_sentences(section.summary, weakness_phrases)
                section.confidence = "low"
                section.inferred = True
                section.refinement_status = "needs_evidence"

    # --- Baseline quality instrumentation ---
    evidence_by_type: dict[str, int] = {}
    total_evidence = 0
    sections_with_content = 0
    for sec in sections:
        if sec.content and sec.content != "*Insufficient data to generate this section.*":
            sections_with_content += 1
        for ev in sec.evidence:
            evidence_by_type[ev.source_type] = evidence_by_type.get(ev.source_type, 0) + 1
            total_evidence += 1

    log.info(
        "cliff_notes_quality_metrics",
        scope_type=effective_scope,
        scope_path=scope_path,
        repository=repository_name,
        depth=depth,
        total_sections=len(sections),
        required_sections=len(required_sections),
        sections_with_content=sections_with_content,
        stub_sections=len(sections) - sections_with_content,
        total_evidence=total_evidence,
        evidence_by_type=evidence_by_type,
        high_confidence=sum(1 for s in sections if s.confidence == "high"),
        medium_confidence=sum(1 for s in sections if s.confidence == "medium"),
        low_confidence=sum(1 for s in sections if s.confidence == "low"),
        inferred_sections=sum(1 for s in sections if s.inferred),
        avg_content_length=sum(len(s.content) for s in sections) // max(len(sections), 1),
    )

    usage = LLMUsageRecord(
        provider="llm",
        model=response.model,
        input_tokens=response.input_tokens,
        output_tokens=response.output_tokens,
        operation="cliff_notes",
        entity_name=repository_name,
    )

    return CliffNotesResult(sections=sections), usage
