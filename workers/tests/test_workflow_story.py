# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Tests for workflow story generation."""

from __future__ import annotations

import json

import pytest

from workers.common.llm.provider import LLMResponse
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
