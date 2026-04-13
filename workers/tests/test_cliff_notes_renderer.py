# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Tests for the CliffNotesRenderer."""

from __future__ import annotations

import json
from collections.abc import AsyncIterator
from dataclasses import dataclass

import pytest

from workers.common.llm.provider import LLMResponse
from workers.comprehension.renderers import CliffNotesRenderer
from workers.comprehension.tree import SummaryNode, SummaryTree
from workers.knowledge.prompts.cliff_notes import REQUIRED_SECTIONS


@dataclass
class _RecordingProvider:
    """Provider that returns a pre-set response and records the prompt."""

    response_text: str
    captured_prompt: str = ""
    calls: int = 0

    async def complete(
        self,
        prompt: str,
        *,
        system: str = "",
        max_tokens: int = 4096,
        temperature: float = 0.0,
        model: str | None = None,
    ) -> LLMResponse:
        self.calls += 1
        self.captured_prompt = prompt
        return LLMResponse(
            content=self.response_text,
            model=model or "fake-model",
            input_tokens=len(prompt) // 4,
            output_tokens=len(self.response_text) // 4,
            stop_reason="end_turn",
        )

    async def stream(
        self,
        prompt: str,
        *,
        system: str = "",
        max_tokens: int = 4096,
        temperature: float = 0.0,
        model: str | None = None,
    ) -> AsyncIterator[str]:
        yield ""


@dataclass
class _FailingProvider:
    async def complete(
        self,
        prompt: str,
        *,
        system: str = "",
        max_tokens: int = 4096,
        temperature: float = 0.0,
        model: str | None = None,
    ) -> LLMResponse:
        raise RuntimeError("Compute error.")

    async def stream(
        self,
        prompt: str,
        *,
        system: str = "",
        max_tokens: int = 4096,
        temperature: float = 0.0,
        model: str | None = None,
    ) -> AsyncIterator[str]:
        yield ""


def _build_tree() -> SummaryTree:
    """A small 4-level tree with 1 root, 2 packages, 3 files, 4 leaves.
    Sufficient to exercise the renderer's selection + formatting."""
    tree = SummaryTree(corpus_id="repo", corpus_type="code", strategy="hierarchical")
    tree.add(SummaryNode(
        id="r",
        corpus_id="repo",
        unit_id="repo",
        level=3,
        parent_id=None,
        child_ids=["package:api", "package:store"],
        summary_text="Headline\n\nRoot summary of the sample repository.",
        headline="Sample repo headline",
        source_tokens=500,
        metadata={"repository_name": "Sample"},
    ))
    tree.add(SummaryNode(
        id="pa",
        corpus_id="repo",
        unit_id="package:api",
        level=2,
        parent_id="repo",
        child_ids=["file:api/a.go", "file:api/b.go"],
        summary_text="API headline\n\nExposes the public HTTP API.",
        headline="API package",
        source_tokens=300,
        metadata={"module_label": "api"},
    ))
    tree.add(SummaryNode(
        id="ps",
        corpus_id="repo",
        unit_id="package:store",
        level=2,
        parent_id="repo",
        child_ids=["file:store/repo.go"],
        summary_text="Store headline\n\nPersists domain objects.",
        headline="Store package",
        source_tokens=200,
        metadata={"module_label": "store"},
    ))
    tree.add(SummaryNode(
        id="fa",
        corpus_id="repo",
        unit_id="file:api/a.go",
        level=1,
        parent_id="package:api",
        summary_text="Auth handlers for login/logout.",
        headline="Auth handlers",
        source_tokens=150,
        metadata={"file_path": "internal/api/auth.go"},
    ))
    tree.add(SummaryNode(
        id="fb",
        corpus_id="repo",
        unit_id="file:api/b.go",
        level=1,
        parent_id="package:api",
        summary_text="Router wiring.",
        headline="Router",
        source_tokens=100,
        metadata={"file_path": "internal/api/router.go"},
    ))
    tree.add(SummaryNode(
        id="fs",
        corpus_id="repo",
        unit_id="file:store/repo.go",
        level=1,
        parent_id="package:store",
        summary_text="Repository pattern over SurrealDB.",
        headline="Repository",
        source_tokens=200,
        metadata={"file_path": "internal/store/repo.go"},
    ))
    return tree


def _valid_response_payload() -> str:
    """Build a JSON payload with every required repository-scope section."""
    return json.dumps([
        {
            "title": title,
            "content": f"Body for {title}",
            "summary": f"Summary for {title}",
            "confidence": "high",
            "inferred": False,
            "evidence": [],
        }
        for title in REQUIRED_SECTIONS
    ])


@pytest.mark.asyncio
async def test_render_returns_all_required_sections_from_valid_payload() -> None:
    provider = _RecordingProvider(response_text=_valid_response_payload())
    renderer = CliffNotesRenderer(provider=provider)
    tree = _build_tree()

    result, usage = await renderer.render(
        tree,
        repository_name="Sample",
        audience="developer",
        depth="medium",
        scope_type="repository",
    )

    titles = [s.title for s in result.sections]
    for required in REQUIRED_SECTIONS:
        assert required in titles
    assert usage.operation == "cliff_notes_render"
    assert provider.calls == 1


@pytest.mark.asyncio
async def test_render_backfills_missing_sections_as_stubs() -> None:
    # Provider returns only one of the required sections — the renderer
    # should backfill the rest with low-confidence stubs.
    payload = json.dumps([
        {
            "title": "System Purpose",
            "content": "Provides sample services.",
            "summary": "Sample service",
            "confidence": "medium",
        }
    ])
    provider = _RecordingProvider(response_text=payload)
    renderer = CliffNotesRenderer(provider=provider)
    tree = _build_tree()

    result, _ = await renderer.render(tree, repository_name="Sample")
    titles = [s.title for s in result.sections]
    for required in REQUIRED_SECTIONS:
        assert required in titles
    # Most sections should be stubs (low confidence + inferred).
    stubbed = [s for s in result.sections if s.title != "System Purpose"]
    assert all(s.confidence == "low" for s in stubbed)
    assert all(s.inferred for s in stubbed)


@pytest.mark.asyncio
async def test_render_prompt_includes_root_summary_and_subsystems() -> None:
    provider = _RecordingProvider(response_text=_valid_response_payload())
    renderer = CliffNotesRenderer(provider=provider)
    tree = _build_tree()

    await renderer.render(tree, repository_name="Sample")

    prompt = provider.captured_prompt
    assert "Root summary of the sample repository" in prompt
    assert "API package" in prompt or "Exposes the public HTTP API" in prompt
    assert "Store package" in prompt or "Persists domain objects" in prompt
    assert "External Dependencies" in prompt


@pytest.mark.asyncio
async def test_render_raises_on_empty_tree() -> None:
    provider = _RecordingProvider(response_text=_valid_response_payload())
    renderer = CliffNotesRenderer(provider=provider)
    empty_tree = SummaryTree(corpus_id="r", corpus_type="code", strategy="hierarchical")

    with pytest.raises(ValueError):
        await renderer.render(empty_tree, repository_name="Sample")


@pytest.mark.asyncio
async def test_render_falls_back_when_final_render_call_fails() -> None:
    provider = _FailingProvider()
    renderer = CliffNotesRenderer(provider=provider)
    tree = _build_tree()

    result, usage = await renderer.render(tree, repository_name="Sample")

    titles = [s.title for s in result.sections]
    for required in REQUIRED_SECTIONS:
        assert required in titles
    assert all(s.confidence == "low" for s in result.sections)
    assert all(s.inferred for s in result.sections)
    assert usage.operation == "cliff_notes_render_fallback"


@pytest.mark.asyncio
async def test_render_limits_group_summaries() -> None:
    """The renderer caps how many level-2 summaries it feeds into the prompt."""
    tree = SummaryTree(corpus_id="r", corpus_type="code", strategy="hierarchical")
    tree.add(SummaryNode(
        id="root",
        corpus_id="r",
        unit_id="repo",
        level=3,
        parent_id=None,
        child_ids=[f"pkg{i}" for i in range(20)],
        summary_text="Root",
        metadata={},
    ))
    for i in range(20):
        tree.add(SummaryNode(
            id=f"p{i}",
            corpus_id="r",
            unit_id=f"pkg{i}",
            level=2,
            parent_id="repo",
            summary_text=f"Package {i} content",
            headline=f"pkg{i}",
            source_tokens=100 - i,  # decreasing so ordering matters
            metadata={"module_label": f"pkg{i}"},
        ))

    provider = _RecordingProvider(response_text=_valid_response_payload())
    renderer = CliffNotesRenderer(provider=provider, max_group_summaries=5)
    await renderer.render(tree, repository_name="X")

    prompt = provider.captured_prompt
    # Only the top 5 subsystems (biggest source_tokens first) should appear
    # under the "Notable subsystems" banner.
    assert "pkg0" in prompt
    assert "pkg4" in prompt
    assert "pkg10" not in prompt  # capped out
