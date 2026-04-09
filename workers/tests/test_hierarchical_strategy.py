# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Tests for the hierarchical comprehension strategy."""

from __future__ import annotations

import asyncio
from collections.abc import AsyncIterator
from dataclasses import dataclass, field

import pytest

from workers.common.llm.provider import LLMResponse
from workers.comprehension.corpus import CorpusUnit, UnitKind
from workers.comprehension.hierarchical import HierarchicalConfig, HierarchicalStrategy


@dataclass
class FakeLLMProvider:
    """Records every completion prompt for inspection and returns
    synthesized text so tests can verify the strategy built the
    right tree without depending on a real model."""

    counter: int = 0
    prompts: list[str] = field(default_factory=list)
    # When set, the provider raises this exception on the Nth call
    # (1-indexed) so tests can verify fallback behavior.
    fail_on_call: int | None = None

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
        if self.fail_on_call is not None and self.counter == self.fail_on_call:
            raise RuntimeError("boom from fake provider")
        # Return a prompt-aware synthetic summary so the tree's level
        # parents actually see distinguishable child content.
        label = _label_hint(prompt)
        body = (
            f"Headline for {label}\n"
            f"\n"
            f"This is a synthetic summary of {label}. "
            f"It mentions the call number {self.counter} to aid debugging."
        )
        return LLMResponse(
            content=body,
            model=model or "fake-model",
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
        # Not used by the hierarchical strategy today.
        yield ""


def _label_hint(prompt: str) -> str:
    """Extract a short label from a hierarchical prompt for synthetic summaries."""
    for marker in ("Segment: ", "File: ", "Package: ", "Repository: "):
        idx = prompt.find(marker)
        if idx >= 0:
            tail = prompt[idx + len(marker):]
            line = tail.splitlines()[0].strip()
            if line:
                return line[:60]
    return "unknown"


@dataclass
class _ToyCorpus:
    """A 4-level toy corpus used to exercise the strategy.

    Structure: repo / {pkg1, pkg2} / {a.go, b.go} / {Func1, Func2}
    """

    corpus_id: str = "toy"
    corpus_type: str = "code"
    units: dict[str, CorpusUnit] = field(default_factory=dict)
    children_map: dict[str, list[str]] = field(default_factory=dict)
    leaf_texts: dict[str, str] = field(default_factory=dict)

    def __post_init__(self) -> None:
        if self.units:
            return
        self._add(CorpusUnit(id="repo", kind=UnitKind.ROOT, level=3, label="toy"))
        for p in ("pkg1", "pkg2"):
            self._add(CorpusUnit(id=p, kind=UnitKind.GROUP, level=2, label=p, parent_id="repo"))
            for f in ("a.go", "b.go"):
                fid = f"{p}/{f}"
                self._add(CorpusUnit(
                    id=fid,
                    kind=UnitKind.LEAF_CONTAINER,
                    level=1,
                    label=f,
                    parent_id=p,
                    metadata={"file_path": fid, "language": "go"},
                ))
                for s in ("Func1", "Func2"):
                    sid = f"{fid}#{s}"
                    self._add(CorpusUnit(
                        id=sid,
                        kind=UnitKind.LEAF,
                        level=0,
                        label=s,
                        parent_id=fid,
                        size_tokens=50,
                        metadata={"file_path": fid, "language": "go"},
                    ))
                    self.leaf_texts[sid] = f"func {s}() {{}}"

    def _add(self, unit: CorpusUnit) -> None:
        self.units[unit.id] = unit
        if unit.parent_id:
            self.children_map.setdefault(unit.parent_id, []).append(unit.id)

    def root(self) -> CorpusUnit:
        return self.units["repo"]

    def children(self, unit: CorpusUnit):
        for child_id in self.children_map.get(unit.id, []):
            yield self.units[child_id]

    def leaf_content(self, unit: CorpusUnit) -> str:
        if not unit.kind.is_leaf():
            raise ValueError(f"not a leaf: {unit.id}")
        return self.leaf_texts[unit.id]

    def revision_fingerprint(self) -> str:
        return "rev-1"


@pytest.mark.asyncio
async def test_build_tree_produces_one_node_per_unit() -> None:
    provider = FakeLLMProvider()
    strategy = HierarchicalStrategy(
        provider,
        HierarchicalConfig(repository_name="toy", leaf_concurrency=2),
    )
    corpus = _ToyCorpus()

    tree = await strategy.build_tree(corpus)

    # 1 root + 2 packages + 4 files + 8 segments = 15 nodes.
    assert len(tree.nodes) == 15
    assert len(tree.at_level(0)) == 8
    assert len(tree.at_level(1)) == 4
    assert len(tree.at_level(2)) == 2
    assert len(tree.at_level(3)) == 1


@pytest.mark.asyncio
async def test_build_tree_calls_llm_once_per_node() -> None:
    provider = FakeLLMProvider()
    strategy = HierarchicalStrategy(
        provider,
        HierarchicalConfig(repository_name="toy", leaf_concurrency=2),
    )
    corpus = _ToyCorpus()
    await strategy.build_tree(corpus)
    # One call per node: 8 leaves + 4 files + 2 packages + 1 root = 15
    assert provider.counter == 15


@pytest.mark.asyncio
async def test_root_summary_is_populated_and_child_ids_linked() -> None:
    provider = FakeLLMProvider()
    strategy = HierarchicalStrategy(
        provider,
        HierarchicalConfig(repository_name="toy", leaf_concurrency=2),
    )
    corpus = _ToyCorpus()
    tree = await strategy.build_tree(corpus)

    root = tree.root()
    assert root is not None
    assert root.summary_text.startswith("Headline for toy")
    assert len(root.child_ids) == 2  # pkg1, pkg2
    # Every child id in the root should resolve to a package node.
    for child_id in root.child_ids:
        child = tree.get(child_id)
        assert child is not None
        assert child.level == 2


@pytest.mark.asyncio
async def test_leaf_failure_falls_back_without_aborting_build() -> None:
    # Fail on the 3rd leaf call; expect a stub summary there but every
    # other node should still be present.
    provider = FakeLLMProvider(fail_on_call=3)
    strategy = HierarchicalStrategy(
        provider,
        HierarchicalConfig(repository_name="toy", leaf_concurrency=1),
    )
    corpus = _ToyCorpus()
    tree = await strategy.build_tree(corpus)

    assert len(tree.nodes) == 15
    # At least one leaf node should carry the fallback text.
    fallback_leaves = [
        n for n in tree.at_level(0) if "Could not summarize this segment" in n.summary_text
    ]
    assert len(fallback_leaves) == 1


@pytest.mark.asyncio
async def test_progress_callback_is_invoked_for_each_phase() -> None:
    provider = FakeLLMProvider()
    strategy = HierarchicalStrategy(
        provider,
        HierarchicalConfig(repository_name="toy", leaf_concurrency=2),
    )
    corpus = _ToyCorpus()

    events: list[tuple[str, float, str]] = []

    async def capture(phase: str, progress: float, message: str) -> None:
        events.append((phase, progress, message))

    await strategy.build_tree(corpus, progress=capture)

    phases = [ph for ph, _, _ in events]
    assert "leaves" in phases
    assert "files" in phases
    assert "packages" in phases
    assert "root" in phases
    assert "ready" in phases
    # Progress should be monotonically non-decreasing.
    progresses = [p for _, p, _ in events]
    assert progresses == sorted(progresses)


@pytest.mark.asyncio
async def test_leaf_concurrency_is_bounded() -> None:
    """Fire a moderately large build and make sure the semaphore holds
    the in-flight LLM call count at or below the configured limit."""
    in_flight = 0
    peak = 0
    lock = asyncio.Lock()

    class _BoundedProvider(FakeLLMProvider):
        async def complete(self, prompt, **kwargs):
            nonlocal in_flight, peak
            async with lock:
                in_flight += 1
                peak = max(peak, in_flight)
            try:
                await asyncio.sleep(0.01)
                return await FakeLLMProvider.complete(self, prompt, **kwargs)
            finally:
                async with lock:
                    in_flight -= 1

    provider = _BoundedProvider()
    strategy = HierarchicalStrategy(
        provider,
        HierarchicalConfig(repository_name="toy", leaf_concurrency=3),
    )
    corpus = _ToyCorpus()
    await strategy.build_tree(corpus)

    # The concurrency semaphore gates leaf calls only. File/package/root
    # calls run sequentially, so the peak is bounded by leaf_concurrency.
    assert peak <= 3


@pytest.mark.asyncio
async def test_capability_requirements_allow_small_models() -> None:
    """Hierarchical is the floor strategy — it must accept any model."""
    strategy = HierarchicalStrategy(FakeLLMProvider())
    reqs = strategy.capability_requirements()
    assert reqs.min_context_tokens <= 2048
    assert reqs.min_instruction_following == "low"
    assert reqs.requires_json_mode is False
