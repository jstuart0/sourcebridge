# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Tests for cliff notes generation."""

from __future__ import annotations

import json

import pytest

from workers.common.llm.fake import FakeLLMProvider
from workers.common.llm.provider import LLMResponse
from workers.knowledge.cliff_notes import generate_cliff_notes
from workers.knowledge.prompts.cliff_notes import REQUIRED_SECTIONS

SAMPLE_SNAPSHOT = json.dumps({
    "repository_id": "repo-1",
    "repository_name": "test-repo",
    "file_count": 2,
    "symbol_count": 4,
    "test_count": 1,
    "languages": [{"language": "go", "file_count": 2, "line_count": 150}],
    "modules": [{"name": "main", "path": ".", "file_count": 2}],
    "entry_points": [{
        "id": "sym-1", "name": "main", "kind": "function",
        "file_path": "main.go", "start_line": 1, "end_line": 20,
    }],
    "public_api": [],
    "complex_symbols": [],
    "high_fan_out": [],
    "high_fan_in": [],
    "test_symbols": [{
        "id": "sym-t", "name": "TestHelper", "kind": "function",
        "file_path": "util.go", "start_line": 12, "end_line": 20,
    }],
    "requirements": [],
    "links": [],
    "docs": [],
    "source_revision": {"commit_sha": "", "branch": "", "content_fingerprint": "abc123", "docs_fingerprint": ""},
})


class StaticLLMProvider:
    def __init__(self, content: str) -> None:
        self._content = content

    async def complete(
        self,
        prompt: str,
        *,
        system: str = "",
        max_tokens: int = 4096,
        temperature: float = 0.0,
    ) -> LLMResponse:
        return LLMResponse(
            content=self._content,
            model="static-test-model",
            input_tokens=len(prompt.split()),
            output_tokens=len(self._content.split()),
            stop_reason="end_turn",
        )


@pytest.mark.asyncio
async def test_cliff_notes_returns_all_required_sections() -> None:
    """Generated cliff notes must include all 7 required sections."""
    provider = FakeLLMProvider()
    result, usage = await generate_cliff_notes(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="medium",
        snapshot_json=SAMPLE_SNAPSHOT,
    )

    titles = [s.title for s in result.sections]
    for required in REQUIRED_SECTIONS:
        assert required in titles, f"Missing required section: {required}"


@pytest.mark.asyncio
async def test_cliff_notes_sections_have_confidence() -> None:
    """Every section must carry a confidence level."""
    provider = FakeLLMProvider()
    result, _ = await generate_cliff_notes(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="medium",
        snapshot_json=SAMPLE_SNAPSHOT,
    )

    for sec in result.sections:
        assert sec.confidence in ("high", "medium", "low"), (
            f"Section {sec.title!r} has invalid confidence: {sec.confidence}"
        )


@pytest.mark.asyncio
async def test_cliff_notes_sections_have_evidence() -> None:
    """At least some sections should have evidence references."""
    provider = FakeLLMProvider()
    result, _ = await generate_cliff_notes(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="medium",
        snapshot_json=SAMPLE_SNAPSHOT,
    )

    sections_with_evidence = [s for s in result.sections if len(s.evidence) > 0]
    assert len(sections_with_evidence) > 0, "Expected at least one section with evidence"


@pytest.mark.asyncio
async def test_cliff_notes_usage_tracking() -> None:
    """LLM usage must be tracked."""
    provider = FakeLLMProvider()
    _, usage = await generate_cliff_notes(
        provider=provider,
        repository_name="test-repo",
        audience="beginner",
        depth="summary",
        snapshot_json=SAMPLE_SNAPSHOT,
    )

    assert usage.operation == "cliff_notes"
    assert usage.model == "fake-test-model"
    assert usage.input_tokens > 0
    assert usage.output_tokens > 0


@pytest.mark.asyncio
async def test_cliff_notes_beginner_audience() -> None:
    """Beginner audience should work without errors."""
    provider = FakeLLMProvider()
    result, _ = await generate_cliff_notes(
        provider=provider,
        repository_name="test-repo",
        audience="beginner",
        depth="summary",
        snapshot_json=SAMPLE_SNAPSHOT,
    )

    assert len(result.sections) >= len(REQUIRED_SECTIONS)


@pytest.mark.asyncio
async def test_cliff_notes_coerces_string_sections_without_crashing() -> None:
    provider = StaticLLMProvider(json.dumps([
        "Purpose of this scope",
        "Main behavior of this scope",
    ]))

    result, _ = await generate_cliff_notes(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="medium",
        snapshot_json=SAMPLE_SNAPSHOT,
        scope_type="symbol",
        scope_path="auth.go#handleLogin",
    )

    assert len(result.sections) >= 2
    assert result.sections[0].title == "Purpose"
    assert result.sections[0].inferred is True
    assert result.sections[0].confidence == "low"


@pytest.mark.asyncio
async def test_cliff_notes_ignores_non_object_evidence_entries() -> None:
    provider = StaticLLMProvider(json.dumps([
        {
            "title": "Purpose",
            "content": "Handles login submissions.",
            "summary": "Login handler.",
            "confidence": "high",
            "inferred": False,
            "evidence": [
                "not-an-object",
                {
                    "source_type": "symbol",
                    "source_id": "sym-1",
                    "file_path": "auth.go",
                    "line_start": 10,
                    "line_end": 40,
                    "rationale": "Primary handler implementation.",
                },
            ],
        },
    ]))

    result, _ = await generate_cliff_notes(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="medium",
        snapshot_json=SAMPLE_SNAPSHOT,
        scope_type="symbol",
        scope_path="auth.go#handleLogin",
    )

    assert len(result.sections[0].evidence) == 1
    assert result.sections[0].evidence[0].source_id == "sym-1"


@pytest.mark.asyncio
async def test_cliff_notes_flattens_nested_content_objects() -> None:
    provider = StaticLLMProvider(json.dumps([
        {
            "title": "Purpose",
            "content": {
                "title": "Purpose",
                "content": "Handles login submissions.",
                "summary": "Login handler.",
                "confidence": "high",
                "inferred": False,
                "evidence": [],
            },
            "summary": "",
            "confidence": "medium",
            "inferred": False,
            "evidence": [],
        },
    ]))

    result, _ = await generate_cliff_notes(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="medium",
        snapshot_json=SAMPLE_SNAPSHOT,
        scope_type="symbol",
        scope_path="auth.go#handleLogin",
    )

    assert result.sections[0].title == "Purpose"
    assert result.sections[0].content == "Handles login submissions."


@pytest.mark.asyncio
async def test_cliff_notes_handles_nested_content_with_string_evidence() -> None:
    """Nested content dict where evidence is a string must not crash."""
    provider = StaticLLMProvider(json.dumps([
        {
            "title": "Purpose",
            "content": {
                "content": "Handles authentication.",
                "evidence": "see handlers/auth.go",
            },
            "summary": "",
            "confidence": "medium",
            "inferred": False,
            "evidence": [],
        },
    ]))

    result, _ = await generate_cliff_notes(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="medium",
        snapshot_json=SAMPLE_SNAPSHOT,
        scope_type="symbol",
        scope_path="auth.go#handleLogin",
    )

    assert result.sections[0].content == "Handles authentication."
    assert result.sections[0].evidence == []


@pytest.mark.asyncio
async def test_cliff_notes_handles_evidence_as_non_list() -> None:
    """Evidence field as a string instead of list must not crash."""
    provider = StaticLLMProvider(json.dumps([
        {
            "title": "Purpose",
            "content": "Handles auth.",
            "summary": "Auth handler.",
            "confidence": "high",
            "inferred": False,
            "evidence": "auth.go handles login",
        },
    ]))

    result, _ = await generate_cliff_notes(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="medium",
        snapshot_json=SAMPLE_SNAPSHOT,
        scope_type="symbol",
        scope_path="auth.go#handleLogin",
    )

    assert result.sections[0].evidence == []


def test_parse_sections_handles_think_tags() -> None:
    """_parse_sections must strip <think> blocks from Qwen-style models."""
    from workers.knowledge.cliff_notes import _parse_sections

    raw = '<think>Let me analyze this...</think>\n[{"title":"Purpose","content":"Handles auth."}]'
    result = _parse_sections(raw)
    assert len(result) == 1
    assert result[0]["title"] == "Purpose"


def test_parse_sections_handles_object_wrapper() -> None:
    """_parse_sections must extract array from object-wrapped responses."""
    from workers.knowledge.cliff_notes import _parse_sections

    raw = '{"sections": [{"title":"Purpose","content":"Auth handler."}]}'
    result = _parse_sections(raw)
    assert len(result) == 1
    assert result[0]["title"] == "Purpose"


def test_parse_sections_handles_preamble_text() -> None:
    """_parse_sections must extract JSON array from text with preamble."""
    from workers.knowledge.cliff_notes import _parse_sections

    raw = 'Here is the JSON array:\n[{"title":"Purpose","content":"Auth handler."}]'
    result = _parse_sections(raw)
    assert len(result) == 1
    assert result[0]["title"] == "Purpose"


def test_parse_sections_handles_keyed_dict() -> None:
    """_parse_sections must convert title-keyed dicts to array."""
    from workers.knowledge.cliff_notes import _parse_sections

    raw = '{"Purpose": {"content": "Auth handler."}, "Architecture": {"content": "Layered."}}'
    result = _parse_sections(raw)
    assert len(result) == 2
    titles = {r["title"] for r in result}
    assert "Purpose" in titles
    assert "Architecture" in titles


def test_parse_sections_handles_string_valued_dict() -> None:
    """_parse_sections must handle dict where values are content strings."""
    from workers.knowledge.cliff_notes import _parse_sections

    raw = '{"Goal": "Understand the system.", "Trigger": "User opens the page."}'
    result = _parse_sections(raw)
    assert len(result) == 2
    assert result[0]["title"] == "Goal"
    assert result[0]["content"] == "Understand the system."


def test_parse_sections_handles_nested_wrapper() -> None:
    """_parse_sections must unwrap single-key nested wrappers."""
    from workers.knowledge.cliff_notes import _parse_sections

    raw = '{"workflow_story": {"Goal": {"content": "Understand it."}, "Trigger": {"content": "Click."}}}'
    result = _parse_sections(raw)
    assert len(result) == 2
    titles = {r["title"] for r in result}
    assert "Goal" in titles
    assert "Trigger" in titles


def test_parse_sections_handles_single_section_object() -> None:
    """_parse_sections must wrap a bare section object in a list."""
    from workers.knowledge.cliff_notes import _parse_sections

    raw = '{"title": "Goal", "content": "Understand the system.", "confidence": "high"}'
    result = _parse_sections(raw)
    assert len(result) == 1
    assert result[0]["title"] == "Goal"
    assert result[0]["content"] == "Understand the system."


@pytest.mark.asyncio
async def test_symbol_cliff_notes_require_impact_analysis_section() -> None:
    provider = StaticLLMProvider(json.dumps([
        {
            "title": "Purpose",
            "content": "Handles login submissions.",
            "summary": "Login handler.",
            "confidence": "high",
            "inferred": False,
            "evidence": [],
        },
    ]))

    result, _ = await generate_cliff_notes(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="medium",
        snapshot_json=SAMPLE_SNAPSHOT,
        scope_type="symbol",
        scope_path="auth.go#handleLogin",
    )

    titles = [section.title for section in result.sections]
    assert "Impact Analysis" in titles


def test_parse_sections_handles_fenced_json_with_language_hint() -> None:
    """_parse_sections must strip ```json fences correctly."""
    from workers.knowledge.cliff_notes import _parse_sections

    raw = '```json\n[{"title":"Purpose","content":"Auth handler."}]\n```'
    result = _parse_sections(raw)
    assert len(result) == 1
    assert result[0]["title"] == "Purpose"


def test_parse_sections_handles_fenced_json_with_trailing_whitespace() -> None:
    """_parse_sections must strip fences even with trailing whitespace."""
    from workers.knowledge.cliff_notes import _parse_sections

    raw = '```json\n[{"title":"Purpose","content":"Auth handler."}]\n```  \n'
    result = _parse_sections(raw)
    assert len(result) == 1
    assert result[0]["title"] == "Purpose"
