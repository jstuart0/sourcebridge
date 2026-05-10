# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Tests for learning path generation."""

from __future__ import annotations

import json
from unittest.mock import patch

import pytest

from workers.common.llm.fake import FakeLLMProvider
from workers.common.llm.provider import SnapshotTooLargeError
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
            "high_fan_in_symbols": [{"file_path": "internal/db/repository_store.go"}],
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


# ---------------------------------------------------------------------------
# Repair-pass tests (Phase 1 / CA-178)
# ---------------------------------------------------------------------------

# Snapshot with real file paths so the grounding filter is satisfied.
_REPAIR_SNAPSHOT = json.dumps(
    {
        "repository_id": "repo-repair",
        "repository_name": "test-repo",
        "file_count": 3,
        "symbol_count": 5,
        "test_count": 1,
        "languages": [{"language": "go", "file_count": 3, "line_count": 200}],
        "modules": [{"name": "main", "path": ".", "file_count": 3}],
        "entry_points": [
            {"id": "s1", "name": "main", "kind": "function", "file_path": "main.go", "start_line": 1, "end_line": 20},
            {"id": "s2", "name": "handleRequest", "kind": "function", "file_path": "handler.go", "start_line": 1, "end_line": 40},
        ],
        "public_api": [
            {"id": "s3", "name": "NewRouter", "kind": "function", "file_path": "router.go", "start_line": 5, "end_line": 15},
        ],
        "complex_symbols": [],
        "high_fan_out": [],
        "high_fan_in": [],
        "test_symbols": [],
        "requirements": [],
        "links": [],
        "docs": [],
        "source_revision": {"commit_sha": "", "branch": "", "content_fingerprint": "xyz", "docs_fingerprint": ""},
    }
)

# A HIGH step that should sail through gate + floor with no repair needed.
_HIGH_STEP = {
    "order": 1,
    "title": "High Confidence Step",
    "objective": "Understand `NewRouter`, `handleRequest`, and `main` entry points.",
    "content": (
        "Read `main.go` to trace the entry point through `NewRouter` in `router.go` "
        "and then into `handleRequest` in `handler.go`. Pay attention to how `NewRouter` "
        "wires the handler chain and how `handleRequest` processes the incoming request."
    ),
    "file_paths": ["main.go", "router.go", "handler.go"],
    "symbol_ids": [],
    "estimated_time": "20 minutes",
    "prerequisite_steps": [],
    "difficulty": "intermediate",
    "exercises": ["Trace a request from `main` through `handleRequest` and note the middleware chain."],
    "checkpoint": "You can describe the request lifecycle end-to-end.",
}

# A LOW step: no backtick identifiers, no real files, no exercises.
_LOW_STEP = {
    "order": 2,
    "title": "Low Confidence Step",
    "objective": "Understand the codebase structure.",
    "content": "Explore the codebase and familiarize yourself with the layout.",
    "file_paths": [],
    "symbol_ids": [],
    "estimated_time": "10 minutes",
    "prerequisite_steps": [],
    "difficulty": "beginner",
    "exercises": [],
    "checkpoint": "You feel comfortable navigating the code.",
}

# A repaired version of the LOW step that passes gate + floor.
_REPAIRED_HIGH_STEP = {
    "order": 2,
    "title": "Low Confidence Step",
    "objective": "Understand `main.go` startup and `handleRequest` dispatch.",
    "content": (
        "Read `main.go` to see how `NewRouter` is called and sets up the HTTP handler. "
        "Then open `handler.go` and trace `handleRequest` to understand request parsing. "
        "Check `router.go` for how routes are registered via `NewRouter`."
    ),
    "file_paths": ["main.go", "handler.go", "router.go"],
    "symbol_ids": [],
    "estimated_time": "20 minutes",
    "prerequisite_steps": [],
    "difficulty": "intermediate",
    "exercises": ["Open `handler.go` and add a log statement inside `handleRequest` to trace calls."],
    "checkpoint": "You can describe what `handleRequest` does when a request arrives.",
}

# A third HIGH step to round out multi-step tests.
_HIGH_STEP_3 = {
    "order": 3,
    "title": "Third High Step",
    "objective": "Understand `NewRouter` configuration in depth.",
    "content": (
        "Inspect `router.go` and understand how `NewRouter` registers routes. "
        "Then look at `handler.go` and `main.go` to see how the router is started. "
        "Trace how `handleRequest` is bound to specific paths via `NewRouter`."
    ),
    "file_paths": ["router.go", "handler.go", "main.go"],
    "symbol_ids": [],
    "estimated_time": "15 minutes",
    "prerequisite_steps": [1, 2],
    "difficulty": "intermediate",
    "exercises": ["Add a new route in `router.go` via `NewRouter` and verify `handleRequest` is invoked."],
    "checkpoint": "You can add a route and test it.",
}


@pytest.mark.asyncio
async def test_learning_path_repair_fires_and_accepts() -> None:
    """Single LOW step is repaired and upgraded to HIGH."""
    initial = json.dumps([_LOW_STEP])
    repaired = json.dumps([_REPAIRED_HIGH_STEP])

    provider = FakeLLMProvider(responses=[initial, repaired])
    result, usage = await generate_learning_path(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="deep",
        snapshot_json=_REPAIR_SNAPSHOT,
    )

    assert len(result.steps) == 1
    step = result.steps[0]
    assert step.confidence == "high"
    assert usage.operation == "learning_path_repaired"
    assert provider._call_count == 2


@pytest.mark.asyncio
async def test_learning_path_repair_merge_identity_multi_step() -> None:
    """3 steps (HIGH, LOW, HIGH): only LOW is repaired; others untouched."""
    initial = json.dumps([_HIGH_STEP, _LOW_STEP, _HIGH_STEP_3])
    repaired = json.dumps([_REPAIRED_HIGH_STEP])

    provider = FakeLLMProvider(responses=[initial, repaired])
    result, usage = await generate_learning_path(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="deep",
        snapshot_json=_REPAIR_SNAPSHOT,
    )

    assert len(result.steps) == 3
    assert result.steps[0].order == 1
    assert result.steps[1].order == 2
    assert result.steps[2].order == 3

    # Steps 1 and 3 are unchanged — content must be preserved verbatim.
    assert result.steps[0].confidence == "high"
    assert result.steps[0].content == _HIGH_STEP["content"]
    assert result.steps[2].confidence == "high"
    assert result.steps[2].content == _HIGH_STEP_3["content"]

    # Step 2 was repaired.
    assert result.steps[1].confidence == "high"
    assert usage.operation == "learning_path_repaired"
    assert provider._call_count == 2


@pytest.mark.asyncio
async def test_learning_path_repair_rejects_when_repaired_still_low() -> None:
    """Repair LLM returns confidence=low → original LOW step ships unchanged."""
    still_low = dict(_LOW_STEP)
    still_low["order"] = 1
    initial = json.dumps([still_low])
    # Repair output also has no identifiers or files → gate keeps it LOW.
    repair_output = dict(still_low)
    repair_output["content"] = "Even more vague content with no identifiers."

    provider = FakeLLMProvider(responses=[initial, json.dumps([repair_output])])
    result, usage = await generate_learning_path(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="deep",
        snapshot_json=_REPAIR_SNAPSHOT,
    )

    assert result.steps[0].confidence == "low"
    assert usage.operation == "learning_path_repair_attempted"
    assert provider._call_count == 2


@pytest.mark.asyncio
async def test_learning_path_repair_rejects_when_file_paths_emptied() -> None:
    """Repair drops file_paths to [] when original had a file → reject.

    The original step is LOW because of ``below_threshold`` (only 1 file, minimum=2),
    so the H2 exercises-only guard does NOT trigger and repair is attempted.
    The repair output drops file_paths to empty — acceptance must reject it.
    """
    low_with_one_file = {
        "order": 1,
        "title": "Low With One File",
        "objective": "Understand the layout.",
        "content": "Explore the codebase and read main.go for context.",
        # Only 1 file → extracted evidence count = 1 < minimum=2 → below_threshold=True.
        "file_paths": ["main.go"],
        "symbol_ids": [],
        "estimated_time": "10 minutes",
        "prerequisite_steps": [],
        "difficulty": "beginner",
        "exercises": ["Read main.go"],
        "checkpoint": "Done.",
    }
    # Repair clears file_paths entirely.
    repair_clears_files = dict(low_with_one_file)
    repair_clears_files["file_paths"] = []
    repair_clears_files["confidence"] = "high"

    initial = json.dumps([low_with_one_file])
    repair_response = json.dumps([repair_clears_files])

    provider = FakeLLMProvider(responses=[initial, repair_response])
    result, usage = await generate_learning_path(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="deep",
        snapshot_json=_REPAIR_SNAPSHOT,
    )

    # Original preserved (still has one file, still LOW).
    assert result.steps[0].confidence == "low"
    assert result.steps[0].file_paths == ["main.go"]
    assert usage.operation == "learning_path_repair_attempted"
    assert provider._call_count == 2


@pytest.mark.asyncio
async def test_learning_path_repair_rejects_when_content_shorter_and_fewer_files() -> None:
    """Repair returns shorter content + fewer files than original → reject.

    The original step is LOW because of ``below_threshold`` (only 1 file, minimum=2),
    so the H2 exercises-only guard does NOT trigger and repair is attempted.
    The repair output has shorter content AND fewer files — acceptance must reject it.
    """
    low_longer = {
        "order": 1,
        "title": "Low Long",
        "objective": "Understand entry points.",
        # Long content; only 1 file → below_threshold=True → LOW is due to evidence, not exercises.
        "content": "This is a detailed but vague exploration of the codebase. " * 10,
        "file_paths": ["main.go"],
        "symbol_ids": [],
        "estimated_time": "30 minutes",
        "prerequisite_steps": [],
        "difficulty": "intermediate",
        "exercises": ["Read main.go carefully."],
        "checkpoint": "Done.",
    }
    repair_shorter = {
        "order": 1,
        "title": "Low Long",
        "objective": "Understand entry points.",
        # Shorter content + no files → fewer evidence + shorter → reject.
        "content": "Short.",
        "file_paths": [],
        "symbol_ids": [],
        "estimated_time": "10 minutes",
        "prerequisite_steps": [],
        "difficulty": "beginner",
        "exercises": ["Read main.go"],
        "checkpoint": "Done.",
        "confidence": "high",
    }

    initial = json.dumps([low_longer])
    repair_response = json.dumps([repair_shorter])

    provider = FakeLLMProvider(responses=[initial, repair_response])
    result, usage = await generate_learning_path(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="deep",
        snapshot_json=_REPAIR_SNAPSHOT,
    )

    # Original preserved — shorter content + fewer files triggers rejection.
    assert "detailed but vague" in result.steps[0].content
    assert result.steps[0].confidence == "low"
    assert usage.operation == "learning_path_repair_attempted"
    assert provider._call_count == 2


@pytest.mark.asyncio
async def test_learning_path_repair_swallow_exception() -> None:
    """Repair LLM raises TimeoutError → original LOW ships; warning logged; no exception bubbles."""
    import structlog.testing

    low_step = dict(_LOW_STEP, order=1)
    initial = json.dumps([low_step])

    provider = FakeLLMProvider(responses=[initial, TimeoutError("upstream timeout")])
    with structlog.testing.capture_logs() as captured:
        result, usage = await generate_learning_path(
            provider=provider,
            repository_name="test-repo",
            audience="developer",
            depth="deep",
            snapshot_json=_REPAIR_SNAPSHOT,
        )

    assert result.steps[0].confidence == "low"
    assert usage.operation == "learning_path_repair_attempted"
    # Both initial render + repair attempt were called.
    assert provider._call_count == 2
    # The named log signal must fire when the LLM call raises a generic exception.
    assert any(e.get("event") == "learning_path_repair_skipped" for e in captured)


@pytest.mark.asyncio
async def test_learning_path_repair_skip_non_deep_depth() -> None:
    """depth=medium → no repair invoked even with LOW steps; _call_count == 1."""
    # Use default FakeLLMProvider (returns LEARNING_PATH_RESPONSE).
    provider = FakeLLMProvider()
    _, usage = await generate_learning_path(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="medium",
        snapshot_json=_REPAIR_SNAPSHOT,
    )

    assert usage.operation == "learning_path"
    assert provider._call_count == 1


@pytest.mark.asyncio
async def test_learning_path_repair_no_fire_all_high() -> None:
    """All steps HIGH → no repair fired; operation == 'learning_path'; _call_count == 1."""
    initial = json.dumps([_HIGH_STEP, _HIGH_STEP_3])

    provider = FakeLLMProvider(responses=[initial])
    _, usage = await generate_learning_path(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="deep",
        snapshot_json=_REPAIR_SNAPSHOT,
    )

    assert usage.operation == "learning_path"
    assert provider._call_count == 1


@pytest.mark.asyncio
async def test_learning_path_repair_skip_exercises_only_low() -> None:
    """Step is LOW solely because exercises=[] (good content + files + identifiers) → skipped."""
    exercises_only_low = {
        "order": 1,
        "title": "Missing Exercises",
        "objective": "Understand `NewRouter`, `handleRequest`, and `main` bootstrapping.",
        "content": (
            "Read `main.go` for the entry point, then `router.go` for `NewRouter`, "
            "and `handler.go` for `handleRequest`. The flow goes: `main` → `NewRouter` "
            "→ `handleRequest`."
        ),
        "file_paths": ["main.go", "router.go", "handler.go"],
        "symbol_ids": [],
        "estimated_time": "20 minutes",
        "prerequisite_steps": [],
        "difficulty": "intermediate",
        # exercises intentionally empty — this is the only reason it would be LOW
        "exercises": [],
        "checkpoint": "You can trace a request from `main` through `handleRequest`.",
    }
    initial = json.dumps([exercises_only_low])

    provider = FakeLLMProvider(responses=[initial])
    result, usage = await generate_learning_path(
        provider=provider,
        repository_name="test-repo",
        audience="developer",
        depth="deep",
        snapshot_json=_REPAIR_SNAPSHOT,
    )

    # Step ships LOW (exercises gate) but no repair LLM call was made.
    assert result.steps[0].confidence == "low"
    assert usage.operation == "learning_path"
    assert provider._call_count == 1


@pytest.mark.asyncio
async def test_learning_path_repair_swallows_budget_exception() -> None:
    """check_prompt_budget raises SnapshotTooLargeError → original LOW preserved; _call_count == 1."""
    import structlog.testing

    low_step = dict(_LOW_STEP, order=1)
    initial = json.dumps([low_step])

    provider = FakeLLMProvider(responses=[initial])
    with structlog.testing.capture_logs() as captured, patch(
        "workers.knowledge.learning_path.check_prompt_budget",
        side_effect=[None, SnapshotTooLargeError(99999, 8000, "learning_path:repair:order_1")],
    ):
            result, usage = await generate_learning_path(
                provider=provider,
                repository_name="test-repo",
                audience="developer",
                depth="deep",
                snapshot_json=_REPAIR_SNAPSHOT,
            )

    # Repair skipped; original LOW preserved; LLM never called for repair.
    assert result.steps[0].confidence == "low"
    assert usage.operation == "learning_path_repair_attempted"
    assert provider._call_count == 1
    # The budget-exceeded log key must fire (distinct from the generic skipped key).
    assert any(e.get("event") == "learning_path_repair_skipped_budget_exceeded" for e in captured)
