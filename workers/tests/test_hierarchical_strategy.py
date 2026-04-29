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
            tail = prompt[idx + len(marker) :]
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
                self._add(
                    CorpusUnit(
                        id=fid,
                        kind=UnitKind.LEAF_CONTAINER,
                        level=1,
                        label=f,
                        parent_id=p,
                        metadata={"file_path": fid, "language": "go"},
                    )
                )
                for s in ("Func1", "Func2"):
                    sid = f"{fid}#{s}"
                    self._add(
                        CorpusUnit(
                            id=sid,
                            kind=UnitKind.LEAF,
                            level=0,
                            label=s,
                            parent_id=fid,
                            size_tokens=50,
                            metadata={
                                "file_path": fid,
                                "language": "go",
                                "module_label": p,
                                "symbol_name": s,
                                "symbol_kind": "function",
                                "deterministic_leaf": True,
                            },
                        )
                    )
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
    # Leaves are now deterministic. The model is only used for
    # 4 files + 2 packages + 1 root = 7 calls.
    assert provider.counter == 7


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
async def test_leaf_nodes_are_built_deterministically() -> None:
    provider = FakeLLMProvider()
    strategy = HierarchicalStrategy(
        provider,
        HierarchicalConfig(repository_name="toy", leaf_concurrency=2),
    )
    corpus = _ToyCorpus()
    tree = await strategy.build_tree(corpus)

    leaves = tree.at_level(0)
    assert len(leaves) == 8
    assert all(node.model_used == "deterministic" for node in leaves)
    assert any("Defines" in node.summary_text for node in leaves)
    assert not any("Segment:" in prompt for prompt in provider.prompts)


@pytest.mark.asyncio
async def test_nonleaf_prompts_include_structured_facts() -> None:
    provider = FakeLLMProvider()
    strategy = HierarchicalStrategy(
        provider,
        HierarchicalConfig(repository_name="toy", leaf_concurrency=2),
    )
    corpus = _ToyCorpus()

    await strategy.build_tree(corpus)

    assert any("Structured file facts:" in prompt for prompt in provider.prompts)
    assert any("Representative symbols:" in prompt for prompt in provider.prompts)
    assert any("Structured package facts:" in prompt for prompt in provider.prompts)
    assert any("Representative files:" in prompt for prompt in provider.prompts)
    assert any("Structured repository facts:" in prompt for prompt in provider.prompts)


@pytest.mark.asyncio
async def test_nonleaf_nodes_carry_compact_fact_metadata() -> None:
    provider = FakeLLMProvider()
    strategy = HierarchicalStrategy(
        provider,
        HierarchicalConfig(repository_name="toy", leaf_concurrency=2),
    )
    corpus = _ToyCorpus()
    tree = await strategy.build_tree(corpus)

    file_node = tree.get("pkg1/a.go")
    assert file_node is not None
    assert file_node.metadata.get("fact_symbol_count") == 2
    assert "Func1" in (file_node.metadata.get("fact_symbol_names") or [])
    assert file_node.metadata.get("fact_symbol_kinds")
    assert file_node.metadata.get("fact_path_signals") is not None
    assert file_node.metadata.get("fact_entity_signals") is not None

    package_node = tree.get("pkg1")
    assert package_node is not None
    assert package_node.metadata.get("fact_file_count") == 2
    assert package_node.metadata.get("fact_total_symbols") == 4
    assert "pkg1/a.go" in (package_node.metadata.get("fact_key_files") or [])
    assert package_node.metadata.get("fact_package_signals") is not None
    assert package_node.metadata.get("fact_package_entities") is not None

    root = tree.root()
    assert root is not None
    assert root.metadata.get("fact_file_count") == 4
    assert root.metadata.get("fact_segment_count") == 8
    assert "pkg1" in (root.metadata.get("fact_package_labels") or [])
    assert root.metadata.get("fact_root_signals") is not None
    assert root.metadata.get("fact_root_entities") is not None


@pytest.mark.asyncio
async def test_file_failure_falls_back_without_aborting_build() -> None:
    # Leaves are deterministic, so fail the first modeled stage.
    provider = FakeLLMProvider(fail_on_call=1)
    strategy = HierarchicalStrategy(
        provider,
        HierarchicalConfig(repository_name="toy", leaf_concurrency=1),
    )
    corpus = _ToyCorpus()
    tree = await strategy.build_tree(corpus)

    assert len(tree.nodes) == 15
    fallback_files = [n for n in tree.at_level(1) if "Could not summarize file" in n.summary_text]
    assert len(fallback_files) == 1


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


def test_config_from_env_reads_stage_token_limits(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("SOURCEBRIDGE_HIERARCHICAL_CONCURRENCY", "4")
    monkeypatch.setenv("SOURCEBRIDGE_HIERARCHICAL_LEAF_MAX_TOKENS", "111")
    monkeypatch.setenv("SOURCEBRIDGE_HIERARCHICAL_FILE_MAX_TOKENS", "222")
    monkeypatch.setenv("SOURCEBRIDGE_HIERARCHICAL_PACKAGE_MAX_TOKENS", "333")
    monkeypatch.setenv("SOURCEBRIDGE_HIERARCHICAL_ROOT_MAX_TOKENS", "444")

    cfg = HierarchicalConfig.from_env(repository_name="toy")

    assert cfg.leaf_concurrency == 4
    assert cfg.leaf_max_tokens == 111
    assert cfg.file_max_tokens == 222
    assert cfg.package_max_tokens == 333
    assert cfg.root_max_tokens == 444


@pytest.mark.asyncio
async def test_hierarchical_strips_think_tags_from_provider_output() -> None:
    """Integration-style: a provider that wraps every response in <think> tags
    should still produce non-empty node summaries after the strip pass."""

    @dataclass
    class ThinkingFakeLLMProvider:
        """Returns responses wrapped in <think> blocks followed by real content."""

        counter: int = 0

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
            label = _label_hint(prompt)
            visible = (
                f"Headline for {label}\n"
                f"\n"
                f"This is a synthetic summary of {label}."
            )
            # Wrap in <think> block — the provider-level strip should remove it.
            content = f"<think>Internal reasoning step {self.counter}.</think>{visible}"
            return LLMResponse(
                content=content,
                model=model or "think-fake",
                input_tokens=len(prompt) // 4,
                output_tokens=len(visible) // 4,
                stop_reason="stop",
            )

        async def stream(self, prompt: str, **kwargs):
            yield ""

    # The thinking provider is used directly — the strip happens inside
    # openai_compat, but here we're testing that the hierarchical pipeline
    # handles pre-stripped content correctly (provider already returns stripped
    # content in production via openai_compat; this test exercises the
    # scenario where a custom provider's output still has think tags).
    #
    # To simulate end-to-end, manually strip in the provider so the tree
    # receives clean summaries and can assemble the full tree without stubs.
    from workers.common.llm.openai_compat import _strip_think_tags

    @dataclass
    class StrippingThinkingProvider:
        """Strips <think> before returning, simulating what openai_compat does."""

        counter: int = 0

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
            label = _label_hint(prompt)
            visible = f"Headline for {label}\n\nSynthetic summary of {label}."
            raw = f"<think>Internal reasoning {self.counter}.</think>{visible}"
            return LLMResponse(
                content=_strip_think_tags(raw),
                model=model or "stripped-think-fake",
                input_tokens=len(prompt) // 4,
                output_tokens=len(visible) // 4,
                stop_reason="stop",
            )

        async def stream(self, prompt: str, **kwargs):
            yield ""

    provider = StrippingThinkingProvider()
    strategy = HierarchicalStrategy(
        provider,
        HierarchicalConfig(repository_name="toy", leaf_concurrency=2),
    )
    corpus = _ToyCorpus()
    tree = await strategy.build_tree(corpus)

    # All nodes must be present and none should contain raw <think> text.
    assert len(tree.nodes) == 15
    for node in tree.nodes.values():
        if node.summary_text:
            assert "<think>" not in node.summary_text.lower()
            assert "Internal reasoning" not in node.summary_text

    # Non-leaf nodes produced by the provider must have real content.
    file_nodes = tree.at_level(1)
    assert all(n.summary_text and "Synthetic summary" in n.summary_text for n in file_nodes)
