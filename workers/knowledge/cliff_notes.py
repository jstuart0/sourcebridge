# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Cliff notes generation using LLM."""

from __future__ import annotations

import json

import structlog

from workers.common.llm.provider import LLMProvider, LLMResponse
from workers.knowledge.prompts.cliff_notes import (
    CLIFF_NOTES_SYSTEM,
    REQUIRED_SECTIONS,
    REQUIRED_SECTIONS_BY_SCOPE,
    build_cliff_notes_prompt,
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
    content = raw.get("content")
    if isinstance(content, dict):
        nested = content
        merged = dict(nested)
        merged.setdefault("title", raw.get("title"))
        merged.setdefault("summary", raw.get("summary", ""))
        merged.setdefault("confidence", raw.get("confidence", "medium"))
        merged.setdefault("inferred", raw.get("inferred", False))
        merged.setdefault("evidence", raw.get("evidence", nested.get("evidence", [])))
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


def _parse_sections(raw: str) -> list[dict[str, object]]:
    """Parse JSON array from LLM response, tolerating markdown fences."""
    text = raw.strip()

    # Strip markdown code fences if present
    if text.startswith("```"):
        first_newline = text.index("\n")
        text = text[first_newline + 1 :]
        if text.endswith("```"):
            text = text[: -3].rstrip()

    parsed = json.loads(text)
    if not isinstance(parsed, list):
        raise ValueError("expected a JSON array of sections")
    return parsed  # type: ignore[no-any-return]


def _parse_evidence(raw_evidence: list[dict]) -> list[EvidenceRef]:
    """Parse evidence entries from the LLM response."""
    result = []
    for ev in raw_evidence:
        if not isinstance(ev, dict):
            continue
        result.append(
            EvidenceRef(
                source_type=ev.get("source_type", "file"),
                source_id=ev.get("source_id", ""),
                file_path=ev.get("file_path", ""),
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
) -> tuple[CliffNotesResult, LLMUsageRecord]:
    """Generate cliff notes from a repository snapshot.

    Returns a CliffNotesResult with all required sections and an LLMUsageRecord.
    """
    effective_scope = scope_type or "repository"
    required_sections = REQUIRED_SECTIONS_BY_SCOPE.get(effective_scope, REQUIRED_SECTIONS)
    prompt = build_cliff_notes_prompt(
        repository_name, audience, depth, snapshot_json, effective_scope, scope_path
    )

    response: LLMResponse = await provider.complete(
        prompt, system=CLIFF_NOTES_SYSTEM, temperature=0.0, max_tokens=8192
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
        fallback_title = (
            required_sections[index] if index < len(required_sections) else f"Section {index + 1}"
        )
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

    usage = LLMUsageRecord(
        provider="llm",
        model=response.model,
        input_tokens=response.input_tokens,
        output_tokens=response.output_tokens,
        operation="cliff_notes",
        entity_name=repository_name,
    )

    return CliffNotesResult(sections=sections), usage
