# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""End-to-end tests for the hierarchical cliff notes path in the servicer."""

from __future__ import annotations

import json
from collections.abc import AsyncIterator
from dataclasses import dataclass, field

import pytest
from knowledge.v1 import knowledge_pb2

from workers.common.llm.provider import LLMResponse
from workers.knowledge.prompts.cliff_notes import REQUIRED_SECTIONS
from workers.knowledge.servicer import (
    CLIFF_NOTES_STRATEGY_ENV,
    KnowledgeServicer,
    _selected_cliff_notes_strategy,
)


@dataclass
class _StubProvider:
    """Provider that returns prompt-aware synthetic responses.

    - Hierarchical leaf / file / package / root summary calls: free-text.
    - Final render call: the REQUIRED_SECTIONS JSON payload.

    Detection is based on substrings unique to each prompt template.
    """

    counter: int = 0
    prompts: list[str] = field(default_factory=list)

    async def complete(
        self,
        prompt: str,
        *,
        system: str = "",
        max_tokens: int = 4096,
        temperature: float = 0.0,
        model: str | None = None,
    ) -> LLMResponse:
        self.counter += 1
        self.prompts.append(prompt)

        # The final render prompt carries the "=== Task ===" banner
        # and asks for a JSON array of sections. Everything else is a
        # hierarchical summary step.
        if "=== Task ===" in prompt or "Return a JSON array of sections" in prompt:
            payload = json.dumps([
                {
                    "title": title,
                    "content": f"Hierarchical body for {title}",
                    "summary": f"Summary for {title}",
                    "confidence": "medium",
                    "inferred": False,
                    "evidence": [],
                }
                for title in REQUIRED_SECTIONS
            ])
            return LLMResponse(
                content=payload,
                model=model or "stub",
                input_tokens=len(prompt) // 4,
                output_tokens=len(payload) // 4,
                stop_reason="end_turn",
            )

        # Hierarchical summary stub.
        body = (
            f"Headline for call {self.counter}\n"
            f"\n"
            f"Synthetic summary produced on call {self.counter}."
        )
        return LLMResponse(
            content=body,
            model=model or "stub",
            input_tokens=len(prompt) // 4,
            output_tokens=len(body) // 4,
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


def _snapshot_json() -> str:
    """A minimal snapshot with two modules, three files, four symbols."""
    snap = {
        "repository_id": "repo-1",
        "repository_name": "MACU Sample",
        "source_revision": {
            "commit_sha": "abc123",
            "content_fingerprint": "fp-1",
        },
        "file_count": 3,
        "symbol_count": 4,
        "modules": [
            {"name": "api", "path": "internal/api", "file_count": 2},
            {"name": "store", "path": "internal/store", "file_count": 1},
        ],
        "entry_points": [
            {
                "id": "sym-login",
                "name": "HandleLogin",
                "qualified_name": "api.HandleLogin",
                "kind": "function",
                "signature": "func HandleLogin(ctx context.Context) error",
                "file_path": "internal/api/auth.go",
                "start_line": 10,
                "end_line": 40,
                "doc_comment": "HandleLogin processes a login request.",
            },
            {
                "id": "sym-logout",
                "name": "HandleLogout",
                "qualified_name": "api.HandleLogout",
                "kind": "function",
                "signature": "func HandleLogout()",
                "file_path": "internal/api/auth.go",
                "start_line": 50,
                "end_line": 80,
            },
        ],
        "public_api": [
            {
                "id": "sym-newapi",
                "name": "NewAPI",
                "kind": "function",
                "file_path": "internal/api/api.go",
            }
        ],
        "complex_symbols": [
            {
                "id": "sym-repo",
                "name": "Repository",
                "kind": "struct",
                "file_path": "internal/store/repo.go",
            }
        ],
        "test_symbols": [],
        "high_fan_out_symbols": [],
        "high_fan_in_symbols": [],
    }
    return json.dumps(snap)


def test_selected_strategy_default_chain_starts_with_hierarchical(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Phase 4 default chain: hierarchical first, single_shot as fallback."""
    from workers.knowledge.servicer import (
        DEFAULT_CLIFF_NOTES_CHAIN,
        _cliff_notes_preference_chain,
    )
    monkeypatch.delenv(CLIFF_NOTES_STRATEGY_ENV, raising=False)
    assert _cliff_notes_preference_chain() == DEFAULT_CLIFF_NOTES_CHAIN
    assert _selected_cliff_notes_strategy() == "hierarchical"


def test_selected_strategy_reads_env(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv(CLIFF_NOTES_STRATEGY_ENV, "hierarchical")
    assert _selected_cliff_notes_strategy() == "hierarchical"
    monkeypatch.setenv(CLIFF_NOTES_STRATEGY_ENV, "  HIERARCHICAL  ")
    assert _selected_cliff_notes_strategy() == "hierarchical"


def test_preference_chain_parses_comma_separated(monkeypatch: pytest.MonkeyPatch) -> None:
    """Chain format: comma-separated, whitespace-tolerant, case-insensitive."""
    from workers.knowledge.servicer import _cliff_notes_preference_chain
    monkeypatch.setenv(
        CLIFF_NOTES_STRATEGY_ENV,
        "long_context_direct, HIERARCHICAL ,single_shot",
    )
    assert _cliff_notes_preference_chain() == [
        "long_context_direct",
        "hierarchical",
        "single_shot",
    ]


def test_preference_chain_falls_back_on_empty(monkeypatch: pytest.MonkeyPatch) -> None:
    from workers.knowledge.servicer import (
        DEFAULT_CLIFF_NOTES_CHAIN,
        _cliff_notes_preference_chain,
    )
    monkeypatch.setenv(CLIFF_NOTES_STRATEGY_ENV, "")
    assert _cliff_notes_preference_chain() == DEFAULT_CLIFF_NOTES_CHAIN
    monkeypatch.setenv(CLIFF_NOTES_STRATEGY_ENV, "   ,   ")
    assert _cliff_notes_preference_chain() == DEFAULT_CLIFF_NOTES_CHAIN


@pytest.mark.asyncio
async def test_hierarchical_path_returns_required_sections(monkeypatch: pytest.MonkeyPatch) -> None:
    """End-to-end: request arrives, servicer builds tree, renders, returns sections."""
    monkeypatch.setenv(CLIFF_NOTES_STRATEGY_ENV, "hierarchical")

    provider = _StubProvider()
    servicer = KnowledgeServicer(llm_provider=provider)

    request = knowledge_pb2.GenerateCliffNotesRequest(
        repository_id="repo-1",
        repository_name="MACU Sample",
        audience="developer",
        depth="medium",
        scope_type="repository",
        snapshot_json=_snapshot_json(),
    )

    # The servicer uses context.abort on error; in a unit test we
    # just call the internal helper directly to bypass the ServicerContext
    # shim. This mirrors how existing test_cliff_notes.py invokes the
    # generation functions directly.
    result, usage = await servicer._generate_cliff_notes_hierarchical(
        request=request,
        audience="developer",
        depth="medium",
        scope_type="repository",
        model_override=None,
    )

    titles = [s.title for s in result.sections]
    for required in REQUIRED_SECTIONS:
        assert required in titles
    assert usage.operation == "cliff_notes_render"

    # Sanity: there should be many hierarchical summary calls + exactly
    # one final render call. The snapshot has 4 symbol leaves + 3 files
    # + 2 packages + 1 root = 10 hierarchical calls, plus 1 render = 11.
    # Exact counts depend on file dedupe; we assert >= 8 as a floor.
    assert provider.counter >= 8
    # The final call should be the render (it's the one that includes
    # the Output format banner).
    assert any("=== Task ===" in p for p in provider.prompts)


@pytest.mark.asyncio
async def test_chain_walker_falls_through_on_snapshot_too_large(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """When long_context_direct trips its budget guard, the chain walker
    should fall through to hierarchical and produce a valid result."""
    monkeypatch.setenv("SOURCEBRIDGE_LONG_CONTEXT_MAX_TOKENS", "1")
    monkeypatch.setenv(
        CLIFF_NOTES_STRATEGY_ENV,
        "long_context_direct,hierarchical",
    )

    provider = _StubProvider()
    servicer = KnowledgeServicer(
        llm_provider=provider,
        default_model_id="claude-sonnet-4-6",
    )

    request = knowledge_pb2.GenerateCliffNotesRequest(
        repository_id="repo-fallback",
        repository_name="Fallback Sample",
        audience="developer",
        depth="medium",
        scope_type="repository",
        snapshot_json=_snapshot_json(),
    )

    result, usage, selection = await servicer._run_cliff_notes_strategy_chain(
        request=request,
        audience="developer",
        depth="medium",
        scope_type="repository",
        model_override=None,
    )

    # Final selected strategy is hierarchical (the fallback) because
    # long_context_direct tripped the budget guard at runtime.
    assert selection.strategy_name == "hierarchical"
    assert usage.operation == "cliff_notes_render"
    titles = [s.title for s in result.sections]
    for required in REQUIRED_SECTIONS:
        assert required in titles


@pytest.mark.asyncio
async def test_chain_walker_selects_long_context_on_capable_model(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """A large-context budget + a capable model should pick long_context."""
    monkeypatch.setenv("SOURCEBRIDGE_LONG_CONTEXT_MAX_TOKENS", "100000")
    monkeypatch.setenv(
        CLIFF_NOTES_STRATEGY_ENV,
        "long_context_direct,hierarchical,single_shot",
    )

    provider = _StubProvider()
    servicer = KnowledgeServicer(
        llm_provider=provider,
        default_model_id="claude-sonnet-4-6",
    )

    request = knowledge_pb2.GenerateCliffNotesRequest(
        repository_id="repo-lc",
        repository_name="LC Sample",
        audience="developer",
        depth="medium",
        scope_type="repository",
        snapshot_json=_snapshot_json(),
    )

    _, _, selection = await servicer._run_cliff_notes_strategy_chain(
        request=request,
        audience="developer",
        depth="medium",
        scope_type="repository",
        model_override=None,
    )
    assert selection.strategy_name == "long_context_direct"


@pytest.mark.asyncio
async def test_chain_walker_selects_hierarchical_on_small_model(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Small-context model should skip long_context via capability gating."""
    monkeypatch.setenv("SOURCEBRIDGE_LONG_CONTEXT_MAX_TOKENS", "60000")
    monkeypatch.setenv(
        CLIFF_NOTES_STRATEGY_ENV,
        "long_context_direct,hierarchical",
    )

    provider = _StubProvider()
    servicer = KnowledgeServicer(
        llm_provider=provider,
        default_model_id="llama3:latest",  # 4K effective context
    )

    request = knowledge_pb2.GenerateCliffNotesRequest(
        repository_id="repo-small",
        repository_name="Small Model",
        snapshot_json=_snapshot_json(),
    )

    _, _, selection = await servicer._run_cliff_notes_strategy_chain(
        request=request,
        audience="developer",
        depth="medium",
        scope_type="repository",
        model_override=None,
    )
    assert selection.strategy_name == "hierarchical"
    skipped = [s for s in selection.trace.steps if s.status == "skipped_capability"]
    assert any("long_context" in s.strategy for s in skipped)


@pytest.mark.asyncio
async def test_hierarchical_path_handles_scoped_request(monkeypatch: pytest.MonkeyPatch) -> None:
    """A file-scoped request should still build a (small) tree and render."""
    monkeypatch.setenv(CLIFF_NOTES_STRATEGY_ENV, "hierarchical")

    provider = _StubProvider()
    servicer = KnowledgeServicer(llm_provider=provider)

    snap = {
        "repository_id": "repo-2",
        "repository_name": "Scoped",
        "scope_context": {
            "scope_type": "file",
            "scope_path": "README.md",
            "target_file": {"path": "README.md"},
        },
    }

    request = knowledge_pb2.GenerateCliffNotesRequest(
        repository_id="repo-2",
        repository_name="Scoped",
        audience="developer",
        depth="summary",
        scope_type="file",
        scope_path="README.md",
        snapshot_json=json.dumps(snap),
    )

    result, _ = await servicer._generate_cliff_notes_hierarchical(
        request=request,
        audience="developer",
        depth="summary",
        scope_type="file",
        model_override=None,
    )

    # Should produce cliff notes with every required file-scope section.
    from workers.knowledge.prompts.cliff_notes import REQUIRED_SECTIONS_BY_SCOPE
    expected = REQUIRED_SECTIONS_BY_SCOPE["file"]
    titles = [s.title for s in result.sections]
    for required in expected:
        assert required in titles
