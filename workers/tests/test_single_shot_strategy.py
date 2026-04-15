# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Tests for SingleShotStrategy and LongContextDirectStrategy."""

from __future__ import annotations

import json
from collections.abc import AsyncIterator
from dataclasses import dataclass, field

import pytest

from workers.common.llm.provider import LLMResponse, SnapshotTooLargeError
from workers.comprehension.adapters.code import CodeCorpus
from workers.comprehension.long_context import (
    LongContextConfig,
    LongContextDirectStrategy,
)
from workers.comprehension.single_shot import SingleShotConfig, SingleShotStrategy
from workers.knowledge.prompts.cliff_notes import REQUIRED_SECTIONS


@dataclass
class _StubProvider:
    """Provider that returns a valid cliff notes JSON payload."""

    calls: int = 0
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
        self.calls += 1
        self.prompts.append(prompt)
        payload = json.dumps(
            [
                {
                    "title": title,
                    "content": f"Body for {title}",
                    "summary": f"Summary for {title}",
                    "confidence": "medium",
                    "inferred": False,
                    "evidence": [],
                }
                for title in REQUIRED_SECTIONS
            ]
        )
        return LLMResponse(
            content=payload,
            model=model or "stub",
            input_tokens=len(prompt) // 4,
            output_tokens=len(payload) // 4,
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


def _snapshot_json(size_multiplier: int = 1) -> str:
    snap: dict = {
        "repository_id": "repo-ss",
        "repository_name": "SSRepo",
        "file_count": 1,
        "symbol_count": 1,
        "entry_points": [
            {
                "id": "sym-1",
                "name": "Main",
                "kind": "function",
                "file_path": "main.go",
                "signature": "func Main()",
                # Pad the doc comment so tests can simulate large
                # snapshots without needing thousands of symbols.
                "doc_comment": "Main entry point. " * 10 * size_multiplier,
            }
        ],
    }
    return json.dumps(snap)


def _corpus_for_snapshot(snapshot_json: str) -> CodeCorpus:
    return CodeCorpus(snapshot=json.loads(snapshot_json))


@pytest.mark.asyncio
async def test_single_shot_strategy_runs_one_llm_call() -> None:
    provider = _StubProvider()
    snapshot = _snapshot_json()
    cfg = SingleShotConfig(
        repository_name="SSRepo",
        audience="developer",
        depth="medium",
        scope_type="repository",
        snapshot_json=snapshot,
    )
    strategy = SingleShotStrategy(provider, cfg)
    corpus = _corpus_for_snapshot(snapshot)

    tree = await strategy.build_tree(corpus)
    assert provider.calls == 1
    # Exactly one node — a root wrapper around the single-shot result.
    assert len(tree.nodes) == 1
    root = tree.root()
    assert root is not None
    assert root.strategy == "single_shot"
    # last_result carries the parsed sections.
    assert strategy.last_result is not None
    titles = [s.title for s in strategy.last_result.sections]
    for required in REQUIRED_SECTIONS:
        assert required in titles


@pytest.mark.asyncio
async def test_single_shot_capability_requirements_are_modest() -> None:
    provider = _StubProvider()
    strategy = SingleShotStrategy(provider, SingleShotConfig())
    reqs = strategy.capability_requirements()
    # Should accept moderate-context models.
    assert reqs.min_context_tokens <= 4096
    assert reqs.min_instruction_following == "low"


@pytest.mark.asyncio
async def test_long_context_direct_runs_when_snapshot_fits() -> None:
    provider = _StubProvider()
    snapshot = _snapshot_json()
    cfg = LongContextConfig(
        repository_name="SSRepo",
        snapshot_json=snapshot,
        max_prompt_tokens=50_000,
    )
    strategy = LongContextDirectStrategy(provider, cfg)
    corpus = _corpus_for_snapshot(snapshot)

    tree = await strategy.build_tree(corpus)
    assert provider.calls == 1
    assert len(tree.nodes) == 1
    assert strategy.last_result is not None
    assert strategy.last_usage is not None


@pytest.mark.asyncio
async def test_long_context_direct_raises_when_snapshot_exceeds_budget() -> None:
    provider = _StubProvider()
    # Inflate the snapshot to exceed a tiny budget.
    snapshot = _snapshot_json(size_multiplier=50)
    cfg = LongContextConfig(
        snapshot_json=snapshot,
        max_prompt_tokens=10,  # absurdly small
    )
    strategy = LongContextDirectStrategy(provider, cfg)
    corpus = _corpus_for_snapshot(snapshot)

    with pytest.raises(SnapshotTooLargeError):
        await strategy.build_tree(corpus)
    # No LLM call should have happened — the guard runs first.
    assert provider.calls == 0


@pytest.mark.asyncio
async def test_long_context_direct_capability_requirements_scale_with_budget() -> None:
    provider = _StubProvider()
    # Small budget -> at least 32K requirement baseline.
    small = LongContextDirectStrategy(
        provider,
        LongContextConfig(max_prompt_tokens=5000),
    )
    small_reqs = small.capability_requirements()
    assert small_reqs.min_context_tokens >= 32_000

    # Large budget -> requirement matches the budget.
    large = LongContextDirectStrategy(
        provider,
        LongContextConfig(max_prompt_tokens=100_000),
    )
    large_reqs = large.capability_requirements()
    assert large_reqs.min_context_tokens >= 100_000
    assert large_reqs.min_instruction_following == "medium"


def test_long_context_config_from_env_respects_override(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("SOURCEBRIDGE_LONG_CONTEXT_MAX_TOKENS", "90000")
    cfg = LongContextConfig.from_env(repository_name="r", snapshot_json="{}")
    assert cfg.max_prompt_tokens == 90_000


def test_long_context_config_from_env_falls_back_on_bogus_value(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setenv("SOURCEBRIDGE_LONG_CONTEXT_MAX_TOKENS", "not-a-number")
    cfg = LongContextConfig.from_env()
    assert cfg.max_prompt_tokens > 0
