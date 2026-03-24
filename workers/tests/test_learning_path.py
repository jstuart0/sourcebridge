# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Tests for learning path generation."""

from __future__ import annotations

import json

import pytest

from workers.common.llm.fake import FakeLLMProvider
from workers.knowledge.learning_path import generate_learning_path

SAMPLE_SNAPSHOT = json.dumps({
    "repository_id": "repo-1",
    "repository_name": "test-repo",
    "file_count": 2,
    "symbol_count": 3,
    "test_count": 0,
    "languages": [{"language": "go", "file_count": 2, "line_count": 100}],
    "modules": [{"name": "main", "path": ".", "file_count": 2}],
    "entry_points": [{
        "id": "sym-1", "name": "main", "kind": "function",
        "file_path": "main.go", "start_line": 1, "end_line": 20,
    }],
    "public_api": [],
    "complex_symbols": [],
    "high_fan_out": [],
    "high_fan_in": [],
    "test_symbols": [],
    "requirements": [],
    "links": [],
    "docs": [],
    "source_revision": {"commit_sha": "", "branch": "", "content_fingerprint": "abc", "docs_fingerprint": ""},
})


@pytest.mark.asyncio
async def test_learning_path_returns_ordered_steps() -> None:
    """Learning path must return steps in order."""
    provider = FakeLLMProvider()
    result, _ = await generate_learning_path(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="medium",
        snapshot_json=SAMPLE_SNAPSHOT,
    )

    assert len(result.steps) >= 2
    for i, step in enumerate(result.steps):
        assert step.order == i + 1


@pytest.mark.asyncio
async def test_learning_path_steps_have_content() -> None:
    """Each step must have title, objective, and content."""
    provider = FakeLLMProvider()
    result, _ = await generate_learning_path(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="medium",
        snapshot_json=SAMPLE_SNAPSHOT,
    )

    for step in result.steps:
        assert step.title, f"Step {step.order} missing title"
        assert step.objective, f"Step {step.order} missing objective"
        assert step.content, f"Step {step.order} missing content"


@pytest.mark.asyncio
async def test_learning_path_usage_tracking() -> None:
    """LLM usage must be tracked."""
    provider = FakeLLMProvider()
    _, usage = await generate_learning_path(
        provider=provider,
        repository_name="test-repo",
        audience="beginner",
        depth="summary",
        snapshot_json=SAMPLE_SNAPSHOT,
    )

    assert usage.operation == "learning_path"
    assert usage.model == "fake-test-model"
    assert usage.input_tokens > 0


@pytest.mark.asyncio
async def test_learning_path_with_focus_area() -> None:
    """Focus area should be accepted without errors."""
    provider = FakeLLMProvider()
    result, _ = await generate_learning_path(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="deep",
        snapshot_json=SAMPLE_SNAPSHOT,
        focus_area="authentication",
    )

    assert len(result.steps) >= 1
