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
from workers.knowledge.prompts.cliff_notes import REQUIRED_SECTIONS, REQUIRED_SECTIONS_DEEP_REPOSITORY


@dataclass
class _RecordingProvider:
    """Provider that returns a pre-set response and records the prompt."""

    response_text: str
    captured_prompt: str = ""
    captured_prompts: list[str] | None = None
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
        if self.captured_prompts is None:
            self.captured_prompts = []
        self.captured_prompts.append(prompt)
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


@dataclass
class _FlakyProvider:
    response_text: str
    fail_on_calls: set[int] | None = None
    fail_on_prompt_substring: str | None = None
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
        if self.fail_on_prompt_substring and self.fail_on_prompt_substring in prompt:
            raise RuntimeError("Compute error.")
        if self.fail_on_calls and self.calls in self.fail_on_calls:
            raise RuntimeError("Compute error.")
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


def _build_tree() -> SummaryTree:
    """A small 4-level tree with 1 root, 2 packages, 3 files, 4 leaves.
    Sufficient to exercise the renderer's selection + formatting."""
    tree = SummaryTree(corpus_id="repo", corpus_type="code", strategy="hierarchical")
    tree.add(
        SummaryNode(
            id="r",
            corpus_id="repo",
            unit_id="repo",
            level=3,
            parent_id=None,
            child_ids=["package:api", "package:store"],
            summary_text="Headline\n\nRoot summary of the sample repository.",
            headline="Sample repo headline",
            source_tokens=500,
            metadata={
                "repository_name": "Sample",
                "fact_root_signals": ["api", "worker", "store"],
                "fact_root_roles": ["public api surface", "high fan-out behavior"],
                "fact_external_dependencies": ["graphql", "surreal"],
                "fact_key_files": ["internal/api/auth.go", "internal/store/repo.go"],
            },
        )
    )
    tree.add(
        SummaryNode(
            id="pa",
            corpus_id="repo",
            unit_id="package:api",
            level=2,
            parent_id="repo",
            child_ids=["file:api/a.go", "file:api/b.go"],
            summary_text="API headline\n\nExposes the public HTTP API.",
            headline="API package",
            source_tokens=300,
            metadata={"module_label": "api", "fact_package_signals": ["api", "route"]},
        )
    )
    tree.add(
        SummaryNode(
            id="ps",
            corpus_id="repo",
            unit_id="package:store",
            level=2,
            parent_id="repo",
            child_ids=["file:store/repo.go"],
            summary_text="Store headline\n\nPersists domain objects.",
            headline="Store package",
            source_tokens=200,
            metadata={"module_label": "store", "fact_package_signals": ["store"]},
        )
    )
    tree.add(
        SummaryNode(
            id="fa",
            corpus_id="repo",
            unit_id="file:api/a.go",
            level=1,
            parent_id="package:api",
            summary_text="Auth handlers for login/logout.",
            headline="Auth handlers",
            source_tokens=150,
            metadata={"file_path": "internal/api/auth.go", "fact_path_signals": ["api", "auth", "route"]},
        )
    )
    tree.add(
        SummaryNode(
            id="fb",
            corpus_id="repo",
            unit_id="file:api/b.go",
            level=1,
            parent_id="package:api",
            summary_text="Router wiring.",
            headline="Router",
            source_tokens=100,
            metadata={"file_path": "internal/api/router.go", "fact_path_signals": ["api", "route"]},
        )
    )
    tree.add(
        SummaryNode(
            id="fs",
            corpus_id="repo",
            unit_id="file:store/repo.go",
            level=1,
            parent_id="package:store",
            summary_text="Repository pattern over SurrealDB.",
            headline="Repository",
            source_tokens=200,
            metadata={
                "file_path": "internal/store/repo.go",
                "fact_path_signals": ["store"],
                "fact_external_dependencies": ["surreal"],
            },
        )
    )
    return tree


def _valid_response_payload() -> str:
    """Build a JSON payload with every required repository-scope section."""
    return json.dumps(
        [
            {
                "title": title,
                "content": f"Body for {title}",
                "summary": f"Summary for {title}",
                "confidence": "high",
                "inferred": False,
                "evidence": [],
            }
            for title in REQUIRED_SECTIONS
        ]
    )


def _valid_deep_response_payload() -> str:
    return json.dumps(
        [
            {
                "title": title,
                "content": f"Body for {title} referencing internal/api/auth.go and internal/store/repo.go",
                "summary": f"Summary for {title}",
                "confidence": "high",
                "inferred": False,
                "evidence": [
                    {"source_type": "file", "file_path": "internal/api/auth.go", "line_start": 10, "line_end": 20},
                    {"source_type": "file", "file_path": "internal/store/repo.go", "line_start": 1, "line_end": 5},
                    {"source_type": "file", "file_path": "internal/api/router.go", "line_start": 5, "line_end": 8},
                ],
            }
            for title in REQUIRED_SECTIONS_DEEP_REPOSITORY
        ]
    )


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
    payload = json.dumps(
        [
            {
                "title": "System Purpose",
                "content": "Provides sample services.",
                "summary": "Sample service",
                "confidence": "medium",
            }
        ]
    )
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
async def test_deep_render_prompt_includes_section_evidence_plan() -> None:
    provider = _RecordingProvider(response_text=_valid_deep_response_payload())
    renderer = CliffNotesRenderer(provider=provider)
    tree = _build_tree()

    await renderer.render(
        tree,
        repository_name="Sample",
        audience="developer",
        depth="deep",
        scope_type="repository",
    )

    prompts = provider.captured_prompts or []
    assert any("=== Section evidence plan ===" in prompt for prompt in prompts)
    assert any("System Purpose" in prompt for prompt in prompts)
    assert any("Architecture Overview" in prompt for prompt in prompts)
    joined = "\n".join(prompts)
    assert "internal/api/auth.go" in joined
    assert "internal/store/repo.go" in joined


@pytest.mark.asyncio
async def test_deep_repository_render_splits_into_parallel_groups() -> None:
    provider = _RecordingProvider(response_text=_valid_deep_response_payload())
    renderer = CliffNotesRenderer(provider=provider)
    tree = _build_tree()

    result, usage = await renderer.render(
        tree,
        repository_name="Sample",
        audience="developer",
        depth="deep",
        scope_type="repository",
    )

    assert provider.calls == 8
    assert usage.operation == "cliff_notes_render_parallel_repaired"
    assert len(result.sections) == len(REQUIRED_SECTIONS_DEEP_REPOSITORY)
    prompts = provider.captured_prompts or []
    assert len(prompts) == 8
    assert any("- System Purpose" in prompt and "- Architecture Overview" in prompt for prompt in prompts)
    assert any("- Security Model" in prompt and "- Configuration & Feature Flags" in prompt for prompt in prompts)
    assert any("Rewrite ONLY the `Domain Model` section" in prompt for prompt in prompts)


@pytest.mark.asyncio
async def test_deep_repository_render_falls_back_per_group_not_whole_artifact() -> None:
    provider = _FlakyProvider(
        response_text=_valid_deep_response_payload(),
        fail_on_prompt_substring="- Security Model",
    )
    renderer = CliffNotesRenderer(provider=provider)
    tree = _build_tree()

    result, usage = await renderer.render(
        tree,
        repository_name="Sample",
        audience="developer",
        depth="deep",
        scope_type="repository",
    )

    assert len(result.sections) == len(REQUIRED_SECTIONS_DEEP_REPOSITORY)
    assert usage.operation == "cliff_notes_render_parallel_repaired_partial"
    fallback_sections = [section for section in result.sections if section.confidence == "low"]
    assert fallback_sections
    assert any("hierarchical summaries" in section.content for section in fallback_sections)


@pytest.mark.asyncio
async def test_deep_section_evidence_plan_prefers_product_core_over_examples() -> None:
    provider = _RecordingProvider(response_text=_valid_deep_response_payload())
    renderer = CliffNotesRenderer(provider=provider)
    tree = _build_tree()
    tree.add(
        SummaryNode(
            id="fe",
            corpus_id="repo",
            unit_id="file:examples/auth.ts",
            level=1,
            parent_id="package:api",
            summary_text="Example auth middleware for demo flows.",
            headline="Example auth middleware",
            source_tokens=500,
            metadata={"file_path": "examples/acme-api/src/api/middleware/auth.ts"},
        )
    )

    await renderer.render(
        tree,
        repository_name="Sample",
        audience="developer",
        depth="deep",
        scope_type="repository",
    )

    prompts = provider.captured_prompts or []
    security_line = next(
        line
        for prompt in prompts
        for line in prompt.splitlines()
        if line.startswith("- Security Model:")
    )
    assert "internal/api/auth.go" in security_line
    assert "examples/acme-api/src/api/middleware/auth.ts" not in security_line


@pytest.mark.asyncio
async def test_deep_system_slice_diversifies_top_level_areas() -> None:
    provider = _RecordingProvider(response_text=_valid_deep_response_payload())
    renderer = CliffNotesRenderer(provider=provider)
    tree = _build_tree()
    tree.add(
        SummaryNode(
            id="fc",
            corpus_id="repo",
            unit_id="file:cli/index.go",
            level=1,
            parent_id="package:api",
            summary_text="CLI indexing entrypoint.",
            headline="CLI entry",
            source_tokens=170,
            metadata={"file_path": "cli/index.go"},
        )
    )
    tree.add(
        SummaryNode(
            id="fw",
            corpus_id="repo",
            unit_id="file:web/app/page.tsx",
            level=1,
            parent_id="package:api",
            summary_text="Repository landing page.",
            headline="Web entry",
            source_tokens=180,
            metadata={"file_path": "web/src/app/page.tsx"},
        )
    )
    tree.add(
        SummaryNode(
            id="fk",
            corpus_id="repo",
            unit_id="file:workers/knowledge/servicer.py",
            level=1,
            parent_id="package:store",
            summary_text="Knowledge worker entrypoints.",
            headline="Knowledge worker",
            source_tokens=190,
            metadata={"file_path": "workers/knowledge/servicer.py"},
        )
    )

    await renderer.render(
        tree,
        repository_name="Sample",
        audience="developer",
        depth="deep",
        scope_type="repository",
    )

    prompts = provider.captured_prompts or []
    system_prompt = next(
        prompt
        for prompt in prompts
        if "- System Purpose" in prompt and "- Architecture Overview" in prompt
    )
    assert system_prompt.index("workers/knowledge/servicer.py") < system_prompt.index("cli/index.go")
    assert system_prompt.index("internal/api/auth.go") < system_prompt.index("cli/index.go")
    assert "cli/index.go" in system_prompt
    assert "workers/knowledge/servicer.py" in system_prompt
    assert "web/src/app/page.tsx" in system_prompt


@pytest.mark.asyncio
async def test_deep_system_slice_includes_system_shape_guardrail() -> None:
    provider = _RecordingProvider(response_text=_valid_deep_response_payload())
    renderer = CliffNotesRenderer(provider=provider)
    tree = _build_tree()
    tree.add(
        SummaryNode(
            id="fc",
            corpus_id="repo",
            unit_id="file:cli/index.go",
            level=1,
            parent_id="package:api",
            summary_text="CLI indexing entrypoint.",
            headline="CLI entry",
            source_tokens=170,
            metadata={"file_path": "cli/index.go"},
        )
    )
    tree.add(
        SummaryNode(
            id="fw",
            corpus_id="repo",
            unit_id="file:web/app/page.tsx",
            level=1,
            parent_id="package:api",
            summary_text="Repository landing page.",
            headline="Web entry",
            source_tokens=180,
            metadata={"file_path": "web/src/app/page.tsx"},
        )
    )

    await renderer.render(
        tree,
        repository_name="Sample",
        audience="developer",
        depth="deep",
        scope_type="repository",
    )

    prompts = provider.captured_prompts or []
    system_prompt = next(
        prompt
        for prompt in prompts
        if "- System Purpose" in prompt and "- Architecture Overview" in prompt
    )
    assert "=== System shape guardrail ===" in system_prompt
    assert "multi-surface code intelligence system" in system_prompt
    assert "If a web product surface is present, mention it explicitly" in system_prompt
    assert "- API/GraphQL surface: `internal/api/auth.go`" in system_prompt
    assert "- web product surface: `web/src/app/page.tsx`" in system_prompt
    assert "- CLI surface: `cli/index.go`" in system_prompt


@pytest.mark.asyncio
async def test_deep_prompt_includes_root_fact_orientation() -> None:
    provider = _RecordingProvider(response_text=_valid_deep_response_payload())
    renderer = CliffNotesRenderer(provider=provider)
    tree = _build_tree()

    await renderer.render(
        tree,
        repository_name="Sample",
        audience="developer",
        depth="deep",
        scope_type="repository",
    )

    prompts = provider.captured_prompts or []
    system_prompt = next(
        prompt
        for prompt in prompts
        if "- System Purpose" in prompt and "- Architecture Overview" in prompt
    )
    assert "Structured repository signals: api, worker, store" in system_prompt
    assert "Structured external dependency hints: graphql, surreal" in system_prompt


@pytest.mark.asyncio
async def test_system_purpose_evidence_plan_spans_multiple_surfaces() -> None:
    provider = _RecordingProvider(response_text=_valid_deep_response_payload())
    renderer = CliffNotesRenderer(provider=provider)
    tree = _build_tree()
    tree.add(
        SummaryNode(
            id="fc",
            corpus_id="repo",
            unit_id="file:cli/index.go",
            level=1,
            parent_id="package:api",
            summary_text="CLI indexing entrypoint.",
            headline="CLI entry",
            source_tokens=170,
            metadata={"file_path": "cli/index.go"},
        )
    )
    tree.add(
        SummaryNode(
            id="fw",
            corpus_id="repo",
            unit_id="file:web/app/page.tsx",
            level=1,
            parent_id="package:api",
            summary_text="Repository landing page.",
            headline="Web entry",
            source_tokens=180,
            metadata={"file_path": "web/src/app/page.tsx"},
        )
    )
    tree.add(
        SummaryNode(
            id="fk",
            corpus_id="repo",
            unit_id="file:workers/knowledge/servicer.py",
            level=1,
            parent_id="package:store",
            summary_text="Knowledge worker entrypoints.",
            headline="Knowledge worker",
            source_tokens=190,
            metadata={"file_path": "workers/knowledge/servicer.py"},
        )
    )

    await renderer.render(
        tree,
        repository_name="Sample",
        audience="developer",
        depth="deep",
        scope_type="repository",
    )

    prompts = provider.captured_prompts or []
    system_prompt = next(
        prompt
        for prompt in prompts
        if "- System Purpose" in prompt and "- Architecture Overview" in prompt
    )
    system_line = next(
        line
        for line in system_prompt.splitlines()
        if line.startswith("- System Purpose:")
    )
    assert "internal/api/auth.go" in system_line
    assert "web/src/app/page.tsx" in system_line
    assert "workers/knowledge/servicer.py" in system_line
    assert "cli/index.go" in system_line


@pytest.mark.asyncio
async def test_external_dependencies_evidence_plan_uses_fact_signals() -> None:
    provider = _RecordingProvider(response_text=_valid_deep_response_payload())
    renderer = CliffNotesRenderer(provider=provider)
    tree = _build_tree()
    tree.add(
        SummaryNode(
            id="fd",
            corpus_id="repo",
            unit_id="file:internal/platform/client.go",
            level=1,
            parent_id="package:store",
            summary_text="Generic client helpers.",
            headline="Client helpers",
            source_tokens=80,
            metadata={
                "file_path": "internal/platform/client.go",
                "fact_external_dependencies": ["openrouter", "grpc"],
                "fact_path_signals": ["integration"],
            },
        )
    )

    await renderer.render(
        tree,
        repository_name="Sample",
        audience="developer",
        depth="deep",
        scope_type="repository",
    )

    prompts = provider.captured_prompts or []
    ops_prompt = next(
        prompt
        for prompt in prompts
        if "- External Dependencies" in prompt and "- Security Model" in prompt
    )
    deps_line = next(line for line in ops_prompt.splitlines() if line.startswith("- External Dependencies:"))
    assert "internal/platform/client.go" in deps_line


@pytest.mark.asyncio
async def test_system_purpose_evidence_plan_avoids_specialized_scanners_when_broader_surfaces_exist() -> None:
    provider = _RecordingProvider(response_text=_valid_deep_response_payload())
    renderer = CliffNotesRenderer(provider=provider)
    tree = _build_tree()
    tree.add(
        SummaryNode(
            id="fc",
            corpus_id="repo",
            unit_id="file:cli/index.go",
            level=1,
            parent_id="package:api",
            summary_text="CLI entrypoint for developer operations.",
            headline="CLI entrypoint",
            source_tokens=170,
            metadata={"file_path": "cli/index.go"},
        )
    )
    tree.add(
        SummaryNode(
            id="fw",
            corpus_id="repo",
            unit_id="file:web/src/app/page.tsx",
            level=1,
            parent_id="package:api",
            summary_text="Web product surface for the repository.",
            headline="Web page",
            source_tokens=175,
            metadata={"file_path": "web/src/app/page.tsx"},
        )
    )
    tree.add(
        SummaryNode(
            id="fk",
            corpus_id="repo",
            unit_id="file:workers/knowledge/servicer.py",
            level=1,
            parent_id="package:api",
            summary_text="Knowledge service worker for artifact generation.",
            headline="Knowledge servicer",
            source_tokens=240,
            metadata={"file_path": "workers/knowledge/servicer.py"},
        )
    )
    tree.add(
        SummaryNode(
            id="fs",
            corpus_id="repo",
            unit_id="file:workers/requirements/scanners/schema_scanner.py",
            level=1,
            parent_id="package:workers",
            summary_text="Schema scanner worker implementation.",
            headline="Schema scanner",
            source_tokens=600,
            metadata={"file_path": "workers/requirements/scanners/schema_scanner.py"},
        )
    )

    await renderer.render(
        tree,
        repository_name="Sample",
        audience="developer",
        depth="deep",
        scope_type="repository",
    )

    prompts = provider.captured_prompts or []
    system_prompt = next(
        prompt
        for prompt in prompts
        if "- System Purpose" in prompt and "- Architecture Overview" in prompt
    )
    system_line = next(line for line in system_prompt.splitlines() if line.startswith("- System Purpose:"))
    assert "workers/requirements/scanners/schema_scanner.py" not in system_line
    assert "workers/knowledge/servicer.py" in system_line
    assert "web/src/app/page.tsx" in system_line
    assert "cli/index.go" in system_line


@pytest.mark.asyncio
async def test_domain_model_evidence_plan_prefers_knowledge_models() -> None:
    provider = _RecordingProvider(response_text=_valid_deep_response_payload())
    renderer = CliffNotesRenderer(provider=provider)
    tree = _build_tree()
    tree.add(
        SummaryNode(
            id="fm",
            corpus_id="repo",
            unit_id="file:internal/knowledge/models.go",
            level=1,
            parent_id="package:store",
            summary_text="Knowledge artifact, understanding, and scope model definitions.",
            headline="Knowledge models",
            source_tokens=230,
            metadata={"file_path": "internal/knowledge/models.go"},
        )
    )
    tree.add(
        SummaryNode(
            id="fj",
            corpus_id="repo",
            unit_id="file:internal/llm/job.go",
            level=1,
            parent_id="package:store",
            summary_text="Job status, job priority, and orchestration model definitions.",
            headline="Job models",
            source_tokens=220,
            metadata={"file_path": "internal/llm/job.go"},
        )
    )

    await renderer.render(
        tree,
        repository_name="Sample",
        audience="developer",
        depth="deep",
        scope_type="repository",
    )

    prompts = provider.captured_prompts or []
    model_prompt = next(
        prompt
        for prompt in prompts
        if "- Domain Model" in prompt and "- Key Abstractions" in prompt
    )
    domain_line = next(
        line
        for line in model_prompt.splitlines()
        if line.startswith("- Domain Model:")
    )
    assert "internal/knowledge/models.go" in domain_line
    assert "internal/llm/job.go" in domain_line
    assert "focus on repositories, knowledge artifacts, understanding revisions, jobs, and scopes" in domain_line


@pytest.mark.asyncio
async def test_domain_model_hint_seed_beats_larger_store_file() -> None:
    provider = _RecordingProvider(response_text=_valid_deep_response_payload())
    renderer = CliffNotesRenderer(provider=provider)
    tree = _build_tree()
    tree.add(
        SummaryNode(
            id="fm",
            corpus_id="repo",
            unit_id="file:internal/knowledge/models.go",
            level=1,
            parent_id="package:store",
            summary_text="Generic summary.",
            headline="Knowledge models",
            source_tokens=80,
            metadata={
                "file_path": "internal/knowledge/models.go",
                "fact_entity_signals": ["knowledge_artifact", "understanding", "repository"],
            },
        )
    )
    tree.add(
        SummaryNode(
            id="fg",
            corpus_id="repo",
            unit_id="file:internal/graph/store.go",
            level=1,
            parent_id="package:store",
            summary_text="Large graph store implementation.",
            headline="Graph store",
            source_tokens=500,
            metadata={"file_path": "internal/graph/store.go", "fact_entity_signals": ["graph"]},
        )
    )
    tree.add(
        SummaryNode(
            id="fj",
            corpus_id="repo",
            unit_id="file:internal/llm/job.go",
            level=1,
            parent_id="package:store",
            summary_text="Job models.",
            headline="Job models",
            source_tokens=90,
            metadata={"file_path": "internal/llm/job.go", "fact_entity_signals": ["job"]},
        )
    )

    await renderer.render(
        tree,
        repository_name="Sample",
        audience="developer",
        depth="deep",
        scope_type="repository",
    )

    prompts = provider.captured_prompts or []
    model_prompt = next(
        prompt
        for prompt in prompts
        if "- Domain Model" in prompt and "- Key Abstractions" in prompt
    )
    domain_line = next(
        line
        for line in model_prompt.splitlines()
        if line.startswith("- Domain Model:")
    )
    assert "internal/knowledge/models.go" in domain_line
    assert "internal/llm/job.go" in domain_line
    assert "=== Domain-model guardrail ===" in model_prompt
    assert "- knowledge_artifact: `internal/knowledge/models.go`" in model_prompt
    assert "- job: `internal/llm/job.go`" in model_prompt


@pytest.mark.asyncio
async def test_deep_system_slice_includes_contrastive_examples() -> None:
    provider = _RecordingProvider(response_text=_valid_deep_response_payload())
    renderer = CliffNotesRenderer(provider=provider)
    tree = _build_tree()

    await renderer.render(
        tree,
        repository_name="Sample",
        audience="developer",
        depth="deep",
        scope_type="repository",
    )

    prompts = provider.captured_prompts or []
    system_prompt = next(
        prompt
        for prompt in prompts
        if "- System Purpose" in prompt and "- Architecture Overview" in prompt
    )
    assert "Good System Purpose example:" in system_prompt
    assert "Bad System Purpose example:" in system_prompt
    assert "Good Architecture Overview example:" in system_prompt


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
    tree.add(
        SummaryNode(
            id="root",
            corpus_id="r",
            unit_id="repo",
            level=3,
            parent_id=None,
            child_ids=[f"pkg{i}" for i in range(20)],
            summary_text="Root",
            metadata={},
        )
    )
    for i in range(20):
        tree.add(
            SummaryNode(
                id=f"p{i}",
                corpus_id="r",
                unit_id=f"pkg{i}",
                level=2,
                parent_id="repo",
                summary_text=f"Package {i} content",
                headline=f"pkg{i}",
                source_tokens=100 - i,  # decreasing so ordering matters
                metadata={"module_label": f"pkg{i}"},
            )
        )

    provider = _RecordingProvider(response_text=_valid_response_payload())
    renderer = CliffNotesRenderer(provider=provider, max_group_summaries=5)
    await renderer.render(tree, repository_name="X")

    prompt = provider.captured_prompt
    # Only the top 5 subsystems (biggest source_tokens first) should appear
    # under the "Notable subsystems" banner.
    assert "pkg0" in prompt
    assert "pkg4" in prompt
    assert "pkg10" not in prompt  # capped out
