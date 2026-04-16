# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Tests for code tour and explain system generation."""

from __future__ import annotations

import json

import pytest

from workers.common.llm.fake import FakeLLMProvider
from workers.knowledge.code_tour import generate_code_tour
from workers.knowledge.explain_system import explain_system

SAMPLE_SNAPSHOT = json.dumps(
    {
        "repository_id": "repo-1",
        "repository_name": "test-repo",
        "file_count": 2,
        "symbol_count": 3,
        "languages": [{"language": "go", "file_count": 2, "line_count": 150}],
        "entry_points": [
            {
                "id": "sym-1",
                "name": "main",
                "kind": "function",
                "file_path": "main.go",
                "start_line": 1,
                "end_line": 20,
            }
        ],
        "public_api": [],
        "complex_symbols": [],
        "docs": [],
        "source_revision": {"commit_sha": "", "branch": "", "content_fingerprint": "abc", "docs_fingerprint": ""},
    }
)


@pytest.mark.asyncio
async def test_code_tour_returns_ordered_stops() -> None:
    """Code tour must return stops in order."""
    provider = FakeLLMProvider()
    result, _ = await generate_code_tour(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="medium",
        snapshot_json=SAMPLE_SNAPSHOT,
    )

    assert len(result.stops) >= 2
    for i, stop in enumerate(result.stops):
        assert stop.order == i + 1


@pytest.mark.asyncio
async def test_code_tour_stops_have_file_paths() -> None:
    """Each stop must reference a file."""
    provider = FakeLLMProvider()
    result, _ = await generate_code_tour(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="medium",
        snapshot_json=SAMPLE_SNAPSHOT,
    )

    for stop in result.stops:
        assert stop.file_path, f"Stop {stop.order} missing file_path"
        assert stop.title, f"Stop {stop.order} missing title"


@pytest.mark.asyncio
async def test_code_tour_usage_tracking() -> None:
    """LLM usage must be tracked."""
    provider = FakeLLMProvider()
    _, usage = await generate_code_tour(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="summary",
        snapshot_json=SAMPLE_SNAPSHOT,
    )

    assert usage.operation == "code_tour"
    assert usage.input_tokens > 0


@pytest.mark.asyncio
async def test_explain_system_returns_explanation() -> None:
    """ExplainSystem must return a non-empty explanation."""
    provider = FakeLLMProvider()
    result, usage = await explain_system(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        question="What does this system do?",
        snapshot_json=SAMPLE_SNAPSHOT,
    )

    assert len(result.explanation) > 0
    assert usage.operation == "explain_system"


@pytest.mark.asyncio
async def test_explain_system_without_question() -> None:
    """ExplainSystem should work without a specific question."""
    provider = FakeLLMProvider()
    result, _ = await explain_system(
        provider=provider,
        repository_name="test-repo",
        audience="beginner",
        question="",
        snapshot_json=SAMPLE_SNAPSHOT,
    )

    assert len(result.explanation) > 0
