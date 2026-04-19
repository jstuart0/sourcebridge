# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Tests for learning path generation."""

from __future__ import annotations

import json

import pytest

from workers.common.llm.fake import FakeLLMProvider
from workers.knowledge.learning_path import _collect_snapshot_file_paths, generate_learning_path

SAMPLE_SNAPSHOT = json.dumps(
    {
        "repository_id": "repo-1",
        "repository_name": "test-repo",
        "file_count": 2,
        "symbol_count": 3,
        "test_count": 0,
        "languages": [{"language": "go", "file_count": 2, "line_count": 100}],
        "modules": [{"name": "main", "path": ".", "file_count": 2}],
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
        "high_fan_out": [],
        "high_fan_in": [],
        "test_symbols": [],
        "requirements": [],
        "links": [],
        "docs": [],
        "source_revision": {"commit_sha": "", "branch": "", "content_fingerprint": "abc", "docs_fingerprint": ""},
    }
)


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


def test_collect_snapshot_file_paths_extracts_from_every_symbol_list():
    """The path extractor must walk every symbol array + modules + files so the
    hallucination filter has a complete ground-truth set to check against."""

    snapshot = json.dumps(
        {
            "entry_points": [{"file_path": "cmd/main.go"}],
            "public_api": [{"file_path": "internal/api/router.go"}],
            "test_symbols": [{"file_path": "internal/api/router_test.go"}],
            "complex_symbols": [{"file_path": "internal/llm/orchestrator.go"}],
            "high_fan_in_symbols": [{"file_path": "internal/db/store.go"}],
            "high_fan_out_symbols": [{"file_path": "internal/graph/index.go"}],
            "modules": [
                {
                    "path": "internal/api",
                    "files": [
                        {"path": "internal/api/handler.go"},
                        "internal/api/middleware.go",
                    ],
                }
            ],
            "files": [{"path": "README.md"}, "go.mod"],
        }
    )
    paths = _collect_snapshot_file_paths(snapshot)
    assert "cmd/main.go" in paths
    assert "internal/api/router.go" in paths
    assert "internal/llm/orchestrator.go" in paths
    assert "internal/graph/index.go" in paths
    assert "internal/api/handler.go" in paths
    assert "internal/api/middleware.go" in paths
    assert "README.md" in paths
    assert "go.mod" in paths


def test_collect_snapshot_file_paths_handles_malformed_snapshot():
    assert _collect_snapshot_file_paths("") == set()
    assert _collect_snapshot_file_paths("not-json") == set()
    assert _collect_snapshot_file_paths("[]") == set()


@pytest.mark.asyncio
async def test_learning_path_deep_filters_hallucinated_file_paths():
    """DEEP-depth generation must drop any file_paths that don't appear in
    the snapshot, silently correcting the LLM when it invents paths."""

    # FakeLLMProvider returns a fixed payload — patch it to emit a step
    # citing one real path (main.go, in SAMPLE_SNAPSHOT) and one invented
    # path (internal/fake/service.go).
    class _FakeLLMWithInventedPath(FakeLLMProvider):
        async def complete(self, prompt, *, system="", max_tokens=4096, temperature=0.0, model=None, frequency_penalty=0.0, extra_body=None):  # noqa: D401
            payload = json.dumps(
                [
                    {
                        "order": 1,
                        "title": "Step 1",
                        "objective": "Read main",
                        "content": "Inspect `main.go` and `internal/fake/service.go`.",
                        "file_paths": ["main.go", "internal/fake/service.go"],
                        "symbol_ids": [],
                        "estimated_time": "10 minutes",
                        "prerequisite_steps": [],
                        "difficulty": "beginner",
                        "exercises": ["Read main.go and trace control flow"],
                        "checkpoint": "You can identify the entry point",
                    }
                ]
            )
            from workers.common.llm.provider import LLMResponse

            return LLMResponse(
                content=payload,
                model=model or "fake-test-model",
                input_tokens=len(prompt) // 4,
                output_tokens=len(payload) // 4,
                stop_reason="end_turn",
            )

    provider = _FakeLLMWithInventedPath()
    result, _ = await generate_learning_path(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="deep",
        snapshot_json=SAMPLE_SNAPSHOT,
    )
    assert len(result.steps) == 1
    step = result.steps[0]
    assert "main.go" in step.file_paths
    # The invented path should be dropped from file_paths entirely.
    assert "internal/fake/service.go" not in step.file_paths
