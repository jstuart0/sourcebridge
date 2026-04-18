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
    DEEP_MIN_EVIDENCE,
    REQUIRED_SECTIONS,
    REQUIRED_SECTIONS_DEEP_REPOSITORY,
    REQUIRED_SECTIONS_BY_SCOPE,
    build_cliff_notes_prompt,
)
from workers.knowledge.evidence import (
    evaluate_evidence_gate,
    extract_section_evidence_refs,
    find_section_weakness_phrases,
    is_valid_evidence_path,
    strip_forbidden_phrase_sentences,
    strip_unsupported_claim_sentences,
)
from workers.knowledge.types import CliffNotesResult, CliffNotesSection, EvidenceRef
from workers.reasoning.types import LLMUsageRecord

log = structlog.get_logger()


def _normalize_text(raw: object) -> str:
    """Flatten nested content values into readable text."""
    if raw is None:
        return ""
    if isinstance(raw, str):
        text = raw.strip()
        if text.startswith("{") or text.startswith("["):
            try:
                decoded = json.loads(text)
            except (json.JSONDecodeError, TypeError, ValueError):
                return text
            return _normalize_text(decoded)
        return text
    if isinstance(raw, dict):
        if "content" in raw:
            return _normalize_text(raw.get("content"))
        if "text" in raw:
            return _normalize_text(raw.get("text"))
        if "summary" in raw:
            return _normalize_text(raw.get("summary"))
        return json.dumps(raw, ensure_ascii=False)
    if isinstance(raw, list):
        parts = [_normalize_text(item) for item in raw]
        parts = [part for part in parts if part]
        return "\n".join(parts)
    return str(raw).strip()


def _normalize_section_object(raw: dict[str, object]) -> dict[str, object]:
    """Flatten nested section-shaped objects into the expected top-level shape."""
    try:
        content = raw.get("content")
        if isinstance(content, dict):
            nested = content
            merged = dict(nested)
            merged.setdefault("title", raw.get("title"))
            merged.setdefault("summary", raw.get("summary", ""))
            merged.setdefault("confidence", raw.get("confidence", "medium"))
            merged.setdefault("inferred", raw.get("inferred", False))
            # Safely extract evidence — nested.get("evidence") may be a string
            outer_ev = raw.get("evidence", [])
            inner_ev = nested.get("evidence", [])
            fallback_ev = outer_ev if isinstance(outer_ev, list) else (inner_ev if isinstance(inner_ev, list) else [])
            merged.setdefault("evidence", fallback_ev)
            raw = merged

        evidence = raw.get("evidence", [])
        if not isinstance(evidence, list):
            evidence = []

        content_text = _normalize_text(raw.get("content", ""))
        summary_text = _normalize_text(raw.get("summary", ""))
        if not summary_text and content_text:
            summary_text = content_text.splitlines()[0][:160]

        return {
            "title": _normalize_text(raw.get("title", "")),
            "content": content_text,
            "summary": summary_text,
            "confidence": _normalize_text(raw.get("confidence", "medium")) or "medium",
            "inferred": bool(raw.get("inferred", False)),
            "evidence": evidence,
        }
    except (AttributeError, TypeError, KeyError) as exc:
        log.warning("section_normalize_failed", error=str(exc), raw_type=type(raw).__name__)
        return {
            "title": "",
            "content": _normalize_text(raw) if raw else "",
            "summary": "",
            "confidence": "low",
            "inferred": True,
            "evidence": [],
        }


def _parse_sections(raw: str) -> list[dict[str, object]]:
    """Parse JSON array from LLM response, tolerating common LLM quirks.

    Handles: <think> blocks, markdown fences, object-wrapped arrays,
    and preamble/postamble text around JSON.
    """
    import re

    text = raw.strip()

    # Strip <think>...</think> blocks (Qwen and other reasoning models)
    text = re.sub(r"<think>.*?</think>", "", text, flags=re.DOTALL).strip()

    # Strip markdown code fences if present (handles ```json, ``` json, etc.)
    if text.startswith("```"):
        first_newline = text.find("\n")
        text = text[first_newline + 1 :] if first_newline != -1 else text[3:]
        text = text.rstrip()
        if text.endswith("```"):
            text = text[:-3].rstrip()

    # Try direct parse first
    try:
        parsed = json.loads(text)
    except json.JSONDecodeError:
        # Try to extract a JSON array from the text (LLM added preamble/postamble)
        match = re.search(r"\[.*\]", text, flags=re.DOTALL)
        if match:
            parsed = json.loads(match.group())
        else:
            raise

    # If parsed is a dict, try to extract a list from known keys
    if isinstance(parsed, dict):
        for key in ("sections", "data", "items", "results", "steps", "stops"):
            if key in parsed and isinstance(parsed[key], list):
                return parsed[key]  # type: ignore[no-any-return]
        # Check for single nested key wrapping the real content
        if len(parsed) == 1:
            sole_value = next(iter(parsed.values()))
            if isinstance(sole_value, list):
                return sole_value  # type: ignore[no-any-return]
            if isinstance(sole_value, dict) and all(
                isinstance(v, dict) for v in sole_value.values()
            ):
                # e.g. {"workflow_story": {"Goal": {...}, "Trigger": {...}}}
                return [{"title": k, **v} for k, v in sole_value.items()]
        # If it looks like a single section object (has "title" and "content"), wrap it
        if "title" in parsed and "content" in parsed:
            return [parsed]
        # If all values are dicts (keyed by section title), convert to list
        if all(isinstance(v, dict) for v in parsed.values()):
            return [{"title": k, **v} for k, v in parsed.items()]
        # If all values are strings (keyed by section title), wrap as content
        if all(isinstance(v, str) for v in parsed.values()):
            return [{"title": k, "content": v} for k, v in parsed.items()]
        raise ValueError("expected a JSON array of sections")

    if not isinstance(parsed, list):
        raise ValueError("expected a JSON array of sections")
    return parsed  # type: ignore[no-any-return]


def _parse_evidence(raw_evidence: list[dict]) -> list[EvidenceRef]:
    """Parse evidence entries from the LLM response."""
    result = []
    for ev in raw_evidence:
        if not isinstance(ev, dict):
            continue
        file_path = str(ev.get("file_path", "") or "").strip()
        if not is_valid_evidence_path(file_path):
            continue
        result.append(
            EvidenceRef(
                source_type=ev.get("source_type", "file"),
                source_id=ev.get("source_id", ""),
                file_path=file_path,
                line_start=ev.get("line_start", 0),
                line_end=ev.get("line_end", 0),
                rationale=ev.get("rationale", ""),
            )
        )
    return result


def _coerce_section(
    raw: object,
    *,
    fallback_title: str,
) -> dict[str, object]:
    """Coerce a raw LLM section candidate into the expected dict shape."""
    if isinstance(raw, dict):
        normalized = _normalize_section_object(raw)
        if not normalized.get("title"):
            normalized["title"] = fallback_title
        return normalized

    text = _normalize_text(raw)
    summary = text.splitlines()[0] if text else "LLM output could not be structured for this section."
    return {
        "title": fallback_title,
        "content": text or "*Insufficient structured content returned for this section.*",
        "summary": summary[:160],
        "confidence": "low",
        "inferred": True,
        "evidence": [],
    }


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
        raw_sections = _parse_sections(response.content)
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
        normalized = _coerce_section(raw, fallback_title=fallback_title)
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
                evidence=_parse_evidence(evidence),
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
