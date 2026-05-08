# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Tests for workflow story generation."""

from __future__ import annotations

import json
from unittest.mock import patch

import pytest

from workers.common.llm.fake import FakeLLMProvider
from workers.common.llm.provider import LLMResponse, SnapshotTooLargeError
from workers.knowledge.prompts.workflow_story import REQUIRED_WORKFLOW_STORY_SECTIONS
from workers.knowledge.workflow_story import generate_workflow_story

SAMPLE_SNAPSHOT = json.dumps(
    {
        "repository_id": "repo-1",
        "repository_name": "test-repo",
        "file_count": 2,
        "symbol_count": 4,
        "modules": [{"name": "web", "path": "web", "file_count": 2}],
        "entry_points": [
            {
                "id": "sym-1",
                "name": "handleLogin",
                "kind": "function",
                "file_path": "internal/api/rest/auth.go",
                "start_line": 10,
                "end_line": 48,
            }
        ],
        "public_api": [],
        "complex_symbols": [],
        "high_fan_out": [],
        "high_fan_in": [],
        "test_symbols": [],
        "requirements": [],
        "links": [],
        "docs": [],
        "source_revision": {
            "commit_sha": "abc1234",
            "branch": "main",
            "content_fingerprint": "abc123",
            "docs_fingerprint": "",
        },
    }
)


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
async def test_workflow_story_returns_all_required_sections() -> None:
    provider = StaticLLMProvider(
        json.dumps(
            [
                {
                    "title": "Goal",
                    "content": "A developer signs in and opens a repository workspace.",
                    "summary": "Understand and use the workspace.",
                    "confidence": "high",
                    "inferred": False,
                    "evidence": [],
                },
                {
                    "title": "Likely Actor",
                    "content": "A new engineer exploring the codebase.",
                    "summary": "New engineer.",
                    "confidence": "medium",
                    "inferred": False,
                    "evidence": [],
                },
            ]
        )
    )

    result, usage = await generate_workflow_story(
        provider=provider,
        repository_name="test-repo",
        audience="beginner",
        depth="medium",
        snapshot_json=SAMPLE_SNAPSHOT,
        scope_type="repository",
    )

    titles = [section.title for section in result.sections]
    for required in REQUIRED_WORKFLOW_STORY_SECTIONS:
        assert required in titles
    assert usage.operation == "workflow_story"


@pytest.mark.asyncio
async def test_workflow_story_coerces_unstructured_sections() -> None:
    provider = StaticLLMProvider(
        json.dumps(
            [
                "A user opens the repository workspace to understand auth.",
                "They trace the login flow and inspect the handler.",
            ]
        )
    )

    result, _ = await generate_workflow_story(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="medium",
        snapshot_json=SAMPLE_SNAPSHOT,
        scope_type="symbol",
        scope_path="internal/api/rest/auth.go#handleLogin",
    )

    assert result.sections[0].title == "Goal"
    assert result.sections[0].content != ""
    assert result.sections[0].summary != ""


@pytest.mark.asyncio
async def test_workflow_story_flattens_nested_json_content() -> None:
    provider = StaticLLMProvider(
        json.dumps(
            [
                {
                    "title": "Goal",
                    "content": {
                        "title": "Goal",
                        "content": "A developer signs in and lands in the repository workspace.",
                        "summary": "Sign in and reach the workspace.",
                        "confidence": "high",
                        "inferred": False,
                        "evidence": [],
                    },
                    "summary": "",
                    "confidence": "medium",
                    "inferred": False,
                    "evidence": [],
                },
            ]
        )
    )

    result, _ = await generate_workflow_story(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="medium",
        snapshot_json=SAMPLE_SNAPSHOT,
        scope_type="repository",
        anchor_label="How someone signs in",
    )

    assert result.sections[0].title == "Goal"
    assert result.sections[0].content == "A developer signs in and lands in the repository workspace."
    assert not result.sections[0].content.startswith("{")


@pytest.mark.asyncio
async def test_workflow_story_fills_missing_sections_from_execution_path() -> None:
    provider = StaticLLMProvider(
        json.dumps(
            [
                {
                    "title": "Goal",
                    "content": "A developer signs in to start using the repository workspace.",
                    "summary": "Sign in.",
                    "confidence": "high",
                    "inferred": False,
                    "evidence": [],
                },
            ]
        )
    )

    execution_path = json.dumps(
        {
            "entryLabel": "Post /auth/login",
            "observedStepCount": 3,
            "inferredStepCount": 0,
            "steps": [
                {
                    "label": "Post /auth/login",
                    "explanation": "This HTTP route is the visible entry point.",
                    "filePath": "internal/api/rest/router.go",
                    "lineStart": 10,
                    "lineEnd": 10,
                },
                {
                    "label": "handleLogin",
                    "explanation": "This handler validates credentials and issues a session token.",
                    "filePath": "internal/api/rest/auth.go",
                    "lineStart": 24,
                    "lineEnd": 80,
                    "symbolId": "sym-1",
                },
            ],
        }
    )

    result, _ = await generate_workflow_story(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="medium",
        snapshot_json=SAMPLE_SNAPSHOT,
        scope_type="repository",
        anchor_label="How someone logs in",
        execution_path_json=execution_path,
    )

    main_steps = next(section for section in result.sections if section.title == "Main Steps")
    inspect = next(section for section in result.sections if section.title == "Where to Inspect or Modify")
    assert "Post /auth/login" in main_steps.content
    assert "internal/api/rest/auth.go" in inspect.content
    assert len(main_steps.evidence) > 0


@pytest.mark.asyncio
async def test_workflow_story_handles_empty_snapshot_and_execution_path() -> None:
    """Regression: empty snapshot + empty execution path must not raise NoneType.

    Live thor logs surfaced a repeating 'NoneType' object is not subscriptable
    crash when the workflow story helpers were called with empty inputs. The
    fallback builders were hardened to tolerate missing/None values in nested
    snapshot sections; this test locks in that behavior.
    """
    provider = StaticLLMProvider(
        json.dumps(
            [
                {
                    "title": "Goal",
                    "content": "A minimal goal.",
                    "summary": "Minimal.",
                    "confidence": "low",
                    "inferred": True,
                    "evidence": [],
                },
            ]
        )
    )

    # Empty snapshot and empty execution_path_json — the original failing shape.
    result, usage = await generate_workflow_story(
        provider=provider,
        repository_name="empty-repo",
        audience="developer",
        depth="medium",
        snapshot_json="{}",
        scope_type="repository",
        execution_path_json="",
    )

    assert result.sections, "should still produce sections when inputs are empty"
    titles = [section.title for section in result.sections]
    for required in REQUIRED_WORKFLOW_STORY_SECTIONS:
        assert required in titles
    assert usage.operation == "workflow_story"


# ---------------------------------------------------------------------------
# Repair-pass tests (Phase 2 / CA-178)
# ---------------------------------------------------------------------------

# Snapshot with real file paths so the grounding filter has signal.
_REPAIR_SNAPSHOT = json.dumps(
    {
        "repository_id": "repo-repair",
        "repository_name": "test-repo",
        "file_count": 4,
        "symbol_count": 6,
        "test_count": 2,
        "languages": [{"language": "go", "file_count": 4, "line_count": 300}],
        "modules": [{"name": "main", "path": ".", "file_count": 4}],
        "entry_points": [
            {
                "id": "s1",
                "name": "handleLogin",
                "kind": "function",
                "file_path": "internal/api/auth.go",
                "start_line": 10,
                "end_line": 50,
            },
            {
                "id": "s2",
                "name": "validateToken",
                "kind": "function",
                "file_path": "internal/auth/token.go",
                "start_line": 5,
                "end_line": 30,
            },
        ],
        "public_api": [
            {
                "id": "s3",
                "name": "NewRouter",
                "kind": "function",
                "file_path": "internal/api/router.go",
                "start_line": 1,
                "end_line": 20,
            },
        ],
        "complex_symbols": [
            {
                "id": "s4",
                "name": "processRequest",
                "kind": "function",
                "file_path": "internal/api/handler.go",
                "start_line": 30,
                "end_line": 120,
            },
        ],
        "high_fan_out": [],
        "high_fan_in": [],
        "test_symbols": [],
        "requirements": [],
        "links": [],
        "docs": [],
        "source_revision": {"commit_sha": "", "branch": "", "content_fingerprint": "xyz", "docs_fingerprint": ""},
    }
)

# A HIGH section with 3+ files and 2+ backticked identifiers in evidence.
def _make_high_section(title: str) -> dict:
    return {
        "title": title,
        "content": (
            "`handleLogin` in `internal/api/auth.go` calls `validateToken` from "
            "`internal/auth/token.go`. The router is set up via `NewRouter` in "
            "`internal/api/router.go`. Request dispatch goes through `processRequest` "
            "in `internal/api/handler.go`."
        ),
        "summary": f"{title} — well grounded section.",
        "confidence": "high",
        "inferred": False,
        "evidence": [
            {
                "source_type": "symbol",
                "source_id": "s1",
                "file_path": "internal/api/auth.go",
                "line_start": 10,
                "line_end": 50,
                "rationale": "Entry point for auth workflow.",
            },
            {
                "source_type": "symbol",
                "source_id": "s2",
                "file_path": "internal/auth/token.go",
                "line_start": 5,
                "line_end": 30,
                "rationale": "Token validation.",
            },
            {
                "source_type": "symbol",
                "source_id": "s3",
                "file_path": "internal/api/router.go",
                "line_start": 1,
                "line_end": 20,
                "rationale": "Router wiring.",
            },
        ],
    }


# A LOW section: vague content, no backtick identifiers, no grounded evidence.
_LOW_SECTION: dict = {
    "title": "Error Recovery",
    "content": "The system handles errors in various ways. Check the codebase for details.",
    "summary": "Error handling exists somewhere.",
    "confidence": "low",
    "inferred": True,
    "evidence": [],
}

# Repaired version of the LOW section — passes gate + floor.
_REPAIRED_HIGH_SECTION: dict = {
    "title": "Error Recovery",
    "content": (
        "`handleLogin` in `internal/api/auth.go` returns structured errors when "
        "`validateToken` fails, propagating through `processRequest` in "
        "`internal/api/handler.go`. The `NewRouter` in `internal/api/router.go` "
        "maps error codes to HTTP responses."
    ),
    "summary": "Errors from `validateToken` propagate through `handleLogin` to the router.",
    "confidence": "high",
    "inferred": False,
    "evidence": [
        {
            "source_type": "symbol",
            "source_id": "s1",
            "file_path": "internal/api/auth.go",
            "line_start": 10,
            "line_end": 50,
            "rationale": "Error return site.",
        },
        {
            "source_type": "symbol",
            "source_id": "s2",
            "file_path": "internal/auth/token.go",
            "line_start": 5,
            "line_end": 30,
            "rationale": "Token validation errors.",
        },
        {
            "source_type": "symbol",
            "source_id": "s3",
            "file_path": "internal/api/router.go",
            "line_start": 1,
            "line_end": 20,
            "rationale": "HTTP error mapping.",
        },
    ],
}

# Required DEEP sections payload — all HIGH except "Error Recovery".
def _make_deep_sections_payload(override_section: dict | None = None) -> str:
    """Return a JSON payload for all 9 deep sections."""
    titles = [
        "Goal",
        "Likely Actor",
        "Trigger",
        "Main Steps",
        "Behind the Scenes",
        "Key Branches or Failure Points",
        "Error Recovery",
        "Observability",
        "Where to Inspect or Modify",
    ]
    sections = []
    for title in titles:
        if override_section and override_section["title"] == title:
            sections.append(override_section)
        else:
            sections.append(_make_high_section(title))
    return json.dumps(sections)


@pytest.mark.asyncio
async def test_workflow_story_repair_fires_and_accepts() -> None:
    """LOW section repaired and upgraded to HIGH; operation == 'workflow_story_repaired'."""
    initial = _make_deep_sections_payload(override_section=_LOW_SECTION)
    repaired = json.dumps([_REPAIRED_HIGH_SECTION])

    provider = FakeLLMProvider(responses=[initial, repaired])
    result, usage = await generate_workflow_story(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="deep",
        snapshot_json=_REPAIR_SNAPSHOT,
    )

    error_recovery = next(s for s in result.sections if s.title == "Error Recovery")
    assert error_recovery.confidence == "high"
    assert usage.operation == "workflow_story_repaired"
    assert usage.input_tokens > 0
    assert provider._call_count == 2


@pytest.mark.asyncio
async def test_workflow_story_repair_merge_identity_multi_section() -> None:
    """3 distinct sections (HIGH, LOW, HIGH): only LOW repaired; HIGH sections untouched."""
    # Use a minimal 3-section payload to keep the test focused.
    goal_high = _make_high_section("Goal")
    error_low = _LOW_SECTION  # "Error Recovery" is LOW
    observability_high = _make_high_section("Observability")
    initial_3 = json.dumps([goal_high, error_low, observability_high])
    repaired_1 = json.dumps([_REPAIRED_HIGH_SECTION])

    provider = FakeLLMProvider(responses=[initial_3, repaired_1])
    result, usage = await generate_workflow_story(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="deep",
        snapshot_json=_REPAIR_SNAPSHOT,
    )

    # Verify the title order from our 3-section payload is preserved.
    # (merge_with_fallbacks adds missing deep sections, so we just check these 3.)
    by_title = {s.title: s for s in result.sections}
    assert by_title["Goal"].confidence == "high"
    assert by_title["Goal"].content == goal_high["content"]
    assert by_title["Observability"].confidence == "high"
    assert by_title["Observability"].content == observability_high["content"]
    assert by_title["Error Recovery"].confidence == "high"
    assert usage.operation == "workflow_story_repaired"
    assert provider._call_count == 2


@pytest.mark.asyncio
async def test_workflow_story_repair_rejects_when_repaired_still_low() -> None:
    """Repair LLM returns confidence=low → original LOW section ships unchanged."""
    initial = _make_deep_sections_payload(override_section=_LOW_SECTION)
    # Repair output is also vague → gate/floor keeps it LOW.
    still_low = dict(_LOW_SECTION)
    still_low["content"] = "Even more vague error handling content."
    repaired = json.dumps([still_low])

    provider = FakeLLMProvider(responses=[initial, repaired])
    result, usage = await generate_workflow_story(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="deep",
        snapshot_json=_REPAIR_SNAPSHOT,
    )

    error_recovery = next(s for s in result.sections if s.title == "Error Recovery")
    assert error_recovery.confidence == "low"
    assert usage.operation == "workflow_story_repair_attempted"
    assert provider._call_count == 2


@pytest.mark.asyncio
async def test_workflow_story_repair_rejects_when_evidence_emptied() -> None:
    """Original LOW had 1 evidence ref; repair returns evidence=[] → reject."""
    low_with_evidence = dict(_LOW_SECTION)
    low_with_evidence["evidence"] = [
        {
            "source_type": "symbol",
            "source_id": "s1",
            "file_path": "internal/api/auth.go",
            "line_start": 10,
            "line_end": 50,
            "rationale": "Auth handler.",
        }
    ]
    initial = _make_deep_sections_payload(override_section=low_with_evidence)

    # Repair claims high confidence but drops all evidence.
    repair_no_evidence = {
        "title": "Error Recovery",
        "content": (
            "`handleLogin` returns errors, `validateToken` validates credentials, "
            "and `NewRouter` maps error codes to HTTP status responses."
        ),
        "summary": "Errors propagated through the auth stack.",
        "confidence": "high",
        "inferred": False,
        "evidence": [],
    }
    repaired = json.dumps([repair_no_evidence])

    provider = FakeLLMProvider(responses=[initial, repaired])
    result, usage = await generate_workflow_story(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="deep",
        snapshot_json=_REPAIR_SNAPSHOT,
    )

    error_recovery = next(s for s in result.sections if s.title == "Error Recovery")
    # Original LOW preserved — evidence emptied → rejected.
    assert len(error_recovery.evidence) == 1
    assert usage.operation == "workflow_story_repair_attempted"
    assert provider._call_count == 2


@pytest.mark.asyncio
async def test_workflow_story_repair_rejects_when_content_shorter_and_fewer_evidence() -> None:
    """Repair returns shorter content AND fewer evidence refs → reject."""
    low_with_evidence = dict(_LOW_SECTION)
    low_with_evidence["content"] = "Detailed but vague error handling across the system. " * 10
    low_with_evidence["evidence"] = [
        {
            "source_type": "symbol",
            "source_id": "s1",
            "file_path": "internal/api/auth.go",
            "line_start": 10,
            "line_end": 50,
            "rationale": "Auth handler.",
        },
        {
            "source_type": "symbol",
            "source_id": "s2",
            "file_path": "internal/auth/token.go",
            "line_start": 5,
            "line_end": 30,
            "rationale": "Token validation.",
        },
    ]
    initial = _make_deep_sections_payload(override_section=low_with_evidence)

    repair_shorter = {
        "title": "Error Recovery",
        "content": "Short.",
        "summary": "Errors handled.",
        "confidence": "high",
        "inferred": False,
        "evidence": [
            {
                "source_type": "symbol",
                "source_id": "s1",
                "file_path": "internal/api/auth.go",
                "line_start": 10,
                "line_end": 50,
                "rationale": "Auth handler.",
            }
        ],
    }
    repaired = json.dumps([repair_shorter])

    provider = FakeLLMProvider(responses=[initial, repaired])
    result, usage = await generate_workflow_story(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="deep",
        snapshot_json=_REPAIR_SNAPSHOT,
    )

    error_recovery = next(s for s in result.sections if s.title == "Error Recovery")
    # Original preserved — shorter content + fewer evidence refs → rejected.
    assert "vague error handling" in error_recovery.content
    assert usage.operation == "workflow_story_repair_attempted"
    assert provider._call_count == 2


@pytest.mark.asyncio
async def test_workflow_story_repair_swallow_exception() -> None:
    """Repair LLM raises TimeoutError → original LOW preserved; warning logged; no exception bubbles."""
    import structlog.testing

    initial = _make_deep_sections_payload(override_section=_LOW_SECTION)

    provider = FakeLLMProvider(responses=[initial, TimeoutError("upstream timeout")])
    with structlog.testing.capture_logs() as captured:
        result, usage = await generate_workflow_story(
            provider=provider,
            repository_name="test-repo",
            audience="developer",
            depth="deep",
            snapshot_json=_REPAIR_SNAPSHOT,
        )

    error_recovery = next(s for s in result.sections if s.title == "Error Recovery")
    assert error_recovery.confidence == "low"
    assert usage.operation == "workflow_story_repair_attempted"
    assert provider._call_count == 2
    assert any(e.get("event") == "workflow_story_repair_skipped" for e in captured)


@pytest.mark.asyncio
async def test_workflow_story_repair_swallows_budget_exception() -> None:
    """check_prompt_budget raises SnapshotTooLargeError → original LOW preserved; budget log fires."""
    import structlog.testing

    initial = _make_deep_sections_payload(override_section=_LOW_SECTION)

    provider = FakeLLMProvider(responses=[initial])
    with structlog.testing.capture_logs() as captured, patch(
        "workers.knowledge.workflow_story.check_prompt_budget",
        side_effect=[None, SnapshotTooLargeError(99999, 8000, "workflow_story:repair:Error Recovery")],
    ):
        result, usage = await generate_workflow_story(
            provider=provider,
            repository_name="test-repo",
            audience="developer",
            depth="deep",
            snapshot_json=_REPAIR_SNAPSHOT,
        )

    error_recovery = next(s for s in result.sections if s.title == "Error Recovery")
    assert error_recovery.confidence == "low"
    assert usage.operation == "workflow_story_repair_attempted"
    assert provider._call_count == 1
    assert any(e.get("event") == "workflow_story_repair_skipped_budget_exceeded" for e in captured)


@pytest.mark.asyncio
async def test_workflow_story_repair_skip_non_deep_depth() -> None:
    """depth=medium → no repair invoked even with LOW initial output; _call_count == 1."""
    provider = FakeLLMProvider(responses=[json.dumps([_LOW_SECTION])])
    _, usage = await generate_workflow_story(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="medium",
        snapshot_json=_REPAIR_SNAPSHOT,
    )

    assert usage.operation == "workflow_story"
    assert provider._call_count == 1


@pytest.mark.asyncio
async def test_workflow_story_repair_no_fire_all_high() -> None:
    """All sections HIGH → no repair fired; operation == 'workflow_story'; _call_count == 1."""
    all_high = json.dumps([_make_high_section(t) for t in [
        "Goal", "Likely Actor", "Trigger", "Main Steps",
        "Behind the Scenes", "Key Branches or Failure Points",
        "Error Recovery", "Observability", "Where to Inspect or Modify",
    ]])

    provider = FakeLLMProvider(responses=[all_high])
    _, usage = await generate_workflow_story(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="deep",
        snapshot_json=_REPAIR_SNAPSHOT,
    )

    assert usage.operation == "workflow_story"
    assert provider._call_count == 1


@pytest.mark.asyncio
async def test_workflow_story_repair_skip_when_response_is_none() -> None:
    """Initial LLM call fails → fallback sections ship; no repair; operation == 'workflow_story'."""
    # Make the initial render raise so response becomes None (fallback path).
    provider = FakeLLMProvider(responses=[RuntimeError("initial render fail")])
    _, usage = await generate_workflow_story(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="deep",
        snapshot_json=_REPAIR_SNAPSHOT,
    )

    assert usage.operation == "workflow_story"
    # Only 1 call attempt was made (the initial one that failed).
    assert provider._call_count == 1


@pytest.mark.asyncio
async def test_workflow_story_replaces_placeholder_sections() -> None:
    """Sections with placeholder content should be replaced by grounded fallbacks."""
    provider = StaticLLMProvider(
        json.dumps(
            [
                {
                    "title": "Goal",
                    "content": "Understand the login flow.",
                    "summary": "Login flow.",
                    "confidence": "high",
                    "inferred": False,
                    "evidence": [],
                },
                {
                    "title": "Likely Actor",
                    "content": "To be determined based on further analysis.",
                    "summary": "",
                    "confidence": "low",
                    "inferred": True,
                    "evidence": [],
                },
                {
                    "title": "Trigger",
                    "content": '{"nested": "json that was not parsed"}',
                    "summary": "",
                    "confidence": "low",
                    "inferred": True,
                    "evidence": [],
                },
            ]
        )
    )

    result, _ = await generate_workflow_story(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="medium",
        snapshot_json=SAMPLE_SNAPSHOT,
        scope_type="repository",
    )

    actor = next(s for s in result.sections if s.title == "Likely Actor")
    trigger = next(s for s in result.sections if s.title == "Trigger")
    # Both should have been replaced with grounded fallback content
    assert "to be determined" not in actor.content.lower()
    assert not trigger.content.startswith("{")
    assert trigger.content != ""
