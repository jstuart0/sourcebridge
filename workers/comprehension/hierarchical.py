# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Hierarchical bottom-up summarization strategy.

This is the strategy that solves the "repository too big for one LLM
call" problem that originally broke cliff notes generation on thor.
Every LLM call is small (one segment, one file's children, one
package's children, or one repo's children), so the total prompt never
exceeds the model's context window regardless of repository size.

Design notes:

- Leaf calls run under a local asyncio.Semaphore so a large repo does
  not fire thousands of concurrent LLM calls into a single worker. The
  default concurrency is intentionally conservative; operators can tune
  this via env var when the backend can actually sustain more load.

- Each call passes through ``check_prompt_budget`` so an overflowing
  leaf (a single giant function body) surfaces as SNAPSHOT_TOO_LARGE
  instead of silently truncating.

- If an individual leaf call fails, the strategy records a stub
  summary ("Failed to summarize this segment") and keeps going. The
  alternative — aborting the whole tree build on one bad leaf — is
  user-hostile because a single malformed file could take down a
  100 KLOC repo's cliff notes.

- The tree is assembled in memory only. Persistent caching against
  ca_summary_node is a follow-up: see the superseding plan for the
  Merkle-tree incremental reindex.
"""

from __future__ import annotations

import asyncio
import os
import uuid
from collections import Counter
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from time import monotonic
from typing import Any

import structlog

from workers.common.llm.provider import (
    LLMProvider,
    LLMResponse,
    SnapshotTooLargeError,
    check_prompt_budget,
    complete_with_optional_model,
    require_nonempty,
)
from workers.comprehension.corpus import (
    CorpusSource,
    CorpusUnit,
    walk_by_level,
)
from workers.comprehension.corpus import (
    content_hash as corpus_content_hash,
)
from workers.comprehension.prompts.hierarchical import (
    HIERARCHICAL_SYSTEM,
    build_file_prompt,
    build_leaf_prompt,
    build_package_prompt,
    build_root_prompt,
)
from workers.comprehension.strategy import (
    CapabilityRequirements,
    ProgressCallback,
    _noop_progress,
)
from workers.comprehension.tree import SummaryNode, SummaryTree
from workers.knowledge.parse_utils import coerce_int

log = structlog.get_logger()


DEFAULT_LEAF_CONCURRENCY = 1
DEFAULT_FILE_CONCURRENCY = 2
DEFAULT_PACKAGE_CONCURRENCY = 2
DEFAULT_LEAF_MAX_TOKENS = 384
DEFAULT_FILE_MAX_TOKENS = 640
DEFAULT_PACKAGE_MAX_TOKENS = 896
DEFAULT_ROOT_MAX_TOKENS = 1280

TREE_TOKEN_BUDGETS = {
    "summary": {"leaf": 256, "file": 384, "package": 512, "root": 768},
    "medium": {"leaf": 384, "file": 640, "package": 896, "root": 1280},
    "deep": {"leaf": 512, "file": 1024, "package": 1536, "root": 2048},
}

ROLE_LABELS = {
    "entry_points": "entry-point code",
    "public_api": "public API surface",
    "complex_symbols": "complex logic",
    "test_symbols": "test coverage",
    "high_fan_out_symbols": "high fan-out behavior",
    "high_fan_in_symbols": "high fan-in behavior",
}


@dataclass
class HierarchicalConfig:
    """Tunables for the hierarchical strategy.

    ``leaf_concurrency`` bounds how many leaf-level LLM calls run at
    once within a single build_tree invocation. This is a *local*
    semaphore — it does not interact with the Go-side orchestrator
    queue, which counts whole comprehension jobs rather than individual
    LLM calls.

    ``repository_name`` is a label used in the prompts so the model
    knows what it's summarizing.

    ``model_override`` lets the caller force a specific model for every
    call in the tree — useful for smoke-testing a particular local
    model without changing the provider default.

    ``cached_tree`` is an optional previously-built tree loaded from
    the summary node store. When provided, the strategy skips leaf
    summaries whose content_hash matches the cached node, avoiding
    redundant LLM calls for unchanged code.
    """

    repository_name: str = ""
    leaf_concurrency: int = DEFAULT_LEAF_CONCURRENCY
    file_concurrency: int = DEFAULT_FILE_CONCURRENCY
    package_concurrency: int = DEFAULT_PACKAGE_CONCURRENCY
    leaf_max_tokens: int = DEFAULT_LEAF_MAX_TOKENS
    file_max_tokens: int = DEFAULT_FILE_MAX_TOKENS
    package_max_tokens: int = DEFAULT_PACKAGE_MAX_TOKENS
    root_max_tokens: int = DEFAULT_ROOT_MAX_TOKENS
    model_override: str | None = None
    cached_tree: SummaryTree | None = None
    on_stage_completed: Callable[[str, SummaryTree], Awaitable[None]] | None = None
    on_node_completed: Callable[[str, SummaryTree, SummaryNode], Awaitable[None]] | None = None
    on_log: Callable[[str, str, str, dict[str, Any] | None], Awaitable[None]] | None = None
    depth: str = "medium"

    @classmethod
    def from_env(cls, repository_name: str = "", depth: str = "medium") -> HierarchicalConfig:
        """Load tunables from environment variables, falling back to defaults."""
        conc = _env_int("SOURCEBRIDGE_HIERARCHICAL_CONCURRENCY", DEFAULT_LEAF_CONCURRENCY)
        if conc <= 0:
            conc = DEFAULT_LEAF_CONCURRENCY
        budgets = TREE_TOKEN_BUDGETS.get(depth, TREE_TOKEN_BUDGETS["medium"])
        return cls(
            repository_name=repository_name,
            leaf_concurrency=conc,
            file_concurrency=_env_int(
                "SOURCEBRIDGE_HIERARCHICAL_FILE_CONCURRENCY",
                DEFAULT_FILE_CONCURRENCY,
            ),
            package_concurrency=_env_int(
                "SOURCEBRIDGE_HIERARCHICAL_PACKAGE_CONCURRENCY",
                DEFAULT_PACKAGE_CONCURRENCY,
            ),
            leaf_max_tokens=_env_int(
                "SOURCEBRIDGE_HIERARCHICAL_LEAF_MAX_TOKENS",
                budgets["leaf"],
            ),
            file_max_tokens=_env_int(
                "SOURCEBRIDGE_HIERARCHICAL_FILE_MAX_TOKENS",
                budgets["file"],
            ),
            package_max_tokens=_env_int(
                "SOURCEBRIDGE_HIERARCHICAL_PACKAGE_MAX_TOKENS",
                budgets["package"],
            ),
            root_max_tokens=_env_int(
                "SOURCEBRIDGE_HIERARCHICAL_ROOT_MAX_TOKENS",
                budgets["root"],
            ),
            depth=depth,
        )


class HierarchicalStrategy:
    """Bottom-up 4-level hierarchical summarization."""

    name: str = "hierarchical"

    def __init__(self, provider: LLMProvider, config: HierarchicalConfig | None = None) -> None:
        self._provider = provider
        self._config = config or HierarchicalConfig()
        self._fallback_count = 0
        self._provider_compute_errors = 0
        self._root_fallback = False
        self._cache_hits: dict[str, int] = {
            "leaves": 0,
            "files": 0,
            "packages": 0,
            "root": 0,
        }

    def capability_requirements(self) -> CapabilityRequirements:
        # Hierarchical is the floor strategy — every LLM call is small
        # enough that even a 2K-context model can handle it.
        return CapabilityRequirements(
            min_context_tokens=2048,
            min_instruction_following="low",
        )

    async def build_tree(
        self,
        corpus: CorpusSource,
        *,
        depth: str | None = None,
        progress: ProgressCallback = _noop_progress,
    ) -> SummaryTree:
        """Build a summary tree for the supplied corpus.

        The tree is built bottom-up: leaves first in parallel, then
        each level's parents sequentially (parents are fast — one call
        per parent — and sequencing avoids a deeper concurrency bloom).
        """
        if depth:
            self._config.depth = depth
        self._fallback_count = 0
        self._provider_compute_errors = 0
        self._root_fallback = False
        self._cache_hits = {
            "leaves": 0,
            "files": 0,
            "packages": 0,
            "root": 0,
        }

        by_level = walk_by_level(corpus)
        leaf_units = by_level.get(0, [])
        file_units = by_level.get(1, [])
        package_units = by_level.get(2, [])
        root_units = by_level.get(3, [])

        total_nodes = sum(len(v) for v in by_level.values())
        build_started = monotonic()
        log.info(
            "hierarchical_build_started",
            corpus_id=corpus.corpus_id,
            leaves=len(leaf_units),
            files=len(file_units),
            packages=len(package_units),
            total=total_nodes,
            depth=self._config.depth,
        )
        await self._emit_log(
            "leaves",
            "hierarchical_build_started",
            "Hierarchical summary build started",
            {
                "corpus_id": corpus.corpus_id,
                "leaves": len(leaf_units),
                "files": len(file_units),
                "packages": len(package_units),
                "total_nodes": total_nodes,
            },
        )
        await _maybe_await(progress("leaves", 0.05, f"Summarizing {len(leaf_units)} segments"))

        tree = SummaryTree(
            corpus_id=corpus.corpus_id,
            corpus_type=corpus.corpus_type,
            strategy=self.name,
            revision_fp=corpus.revision_fingerprint(),
        )

        # Level 0 — leaf summaries, run in parallel under a semaphore.
        if leaf_units:
            stage_started = monotonic()
            log.info(
                "hierarchical_stage_started",
                corpus_id=corpus.corpus_id,
                stage="leaves",
                total=len(leaf_units),
            )
            await self._emit_log(
                "leaves", "hierarchical_stage_started", "Leaf summarization started", {"total": len(leaf_units)}
            )
            await self._summarize_leaves(corpus, leaf_units, tree, progress)
            log.info(
                "hierarchical_stage_completed",
                corpus_id=corpus.corpus_id,
                stage="leaves",
                total=len(leaf_units),
                cache_hits=self._cache_hits["leaves"],
                elapsed_ms=int((monotonic() - stage_started) * 1000),
            )
            await self._emit_log(
                "leaves",
                "hierarchical_stage_completed",
                "Leaf summarization completed",
                {
                    "total": len(leaf_units),
                    "cache_hits": self._cache_hits["leaves"],
                    "elapsed_ms": int((monotonic() - stage_started) * 1000),
                },
            )
            await self._emit_stage_checkpoint("leaves", tree)

        # Level 1 — file summaries.
        if file_units:
            await _maybe_await(progress("files", 0.55, f"Summarizing {len(file_units)} files"))
            stage_started = monotonic()
            log.info(
                "hierarchical_stage_started",
                corpus_id=corpus.corpus_id,
                stage="files",
                total=len(file_units),
            )
            await self._emit_log(
                "files", "hierarchical_stage_started", "File summarization started", {"total": len(file_units)}
            )
            await self._summarize_nonleaf_stage(
                corpus=corpus,
                units=file_units,
                tree=tree,
                stage="files",
                concurrency=self._config.file_concurrency,
                summarize=self._summarize_file,
            )
            log.info(
                "hierarchical_stage_completed",
                corpus_id=corpus.corpus_id,
                stage="files",
                total=len(file_units),
                cache_hits=self._cache_hits["files"],
                elapsed_ms=int((monotonic() - stage_started) * 1000),
            )
            await self._emit_log(
                "files",
                "hierarchical_stage_completed",
                "File summarization completed",
                {
                    "total": len(file_units),
                    "cache_hits": self._cache_hits["files"],
                    "elapsed_ms": int((monotonic() - stage_started) * 1000),
                },
            )
            await self._emit_stage_checkpoint("files", tree)

        # Level 2 — package summaries.
        if package_units:
            await _maybe_await(progress("packages", 0.8, f"Summarizing {len(package_units)} packages"))
            stage_started = monotonic()
            log.info(
                "hierarchical_stage_started",
                corpus_id=corpus.corpus_id,
                stage="packages",
                total=len(package_units),
            )
            await self._emit_log(
                "packages", "hierarchical_stage_started", "Package summarization started", {"total": len(package_units)}
            )
            await self._summarize_nonleaf_stage(
                corpus=corpus,
                units=package_units,
                tree=tree,
                stage="packages",
                concurrency=self._config.package_concurrency,
                summarize=self._summarize_package,
            )
            log.info(
                "hierarchical_stage_completed",
                corpus_id=corpus.corpus_id,
                stage="packages",
                total=len(package_units),
                cache_hits=self._cache_hits["packages"],
                elapsed_ms=int((monotonic() - stage_started) * 1000),
            )
            await self._emit_log(
                "packages",
                "hierarchical_stage_completed",
                "Package summarization completed",
                {
                    "total": len(package_units),
                    "cache_hits": self._cache_hits["packages"],
                    "elapsed_ms": int((monotonic() - stage_started) * 1000),
                },
            )
            await self._emit_stage_checkpoint("packages", tree)

        # Level 3 — root summary.
        if root_units:
            await _maybe_await(progress("root", 0.95, "Summarizing repository"))
            stage_started = monotonic()
            log.info(
                "hierarchical_stage_started",
                corpus_id=corpus.corpus_id,
                stage="root",
                total=1,
            )
            await self._emit_log("root", "hierarchical_stage_started", "Root summarization started", {"total": 1})
            root = root_units[0]
            self._populate_child_ids(root, corpus, tree)
            await self._summarize_root(
                corpus,
                root,
                tree,
                file_count=len(file_units),
                segment_count=len(leaf_units),
            )
            log.info(
                "hierarchical_stage_completed",
                corpus_id=corpus.corpus_id,
                stage="root",
                total=1,
                cache_hits=self._cache_hits["root"],
                elapsed_ms=int((monotonic() - stage_started) * 1000),
            )
            await self._emit_log(
                "root",
                "hierarchical_stage_completed",
                "Root summarization completed",
                {
                    "cache_hits": self._cache_hits["root"],
                    "elapsed_ms": int((monotonic() - stage_started) * 1000),
                },
            )
            await self._emit_stage_checkpoint("root", tree)

        log.info(
            "hierarchical_build_completed",
            corpus_id=corpus.corpus_id,
            stats=tree.stats(),
            elapsed_ms=int((monotonic() - build_started) * 1000),
        )
        await self._emit_log(
            "ready",
            "hierarchical_build_completed",
            "Hierarchical summary tree built",
            {
                "stats": tree.stats(),
                "elapsed_ms": int((monotonic() - build_started) * 1000),
            },
        )
        await _maybe_await(progress("ready", 1.0, "Hierarchical summary tree built"))
        return tree

    async def _emit_stage_checkpoint(self, stage: str, tree: SummaryTree) -> None:
        if self._config.on_stage_completed is None:
            return
        await self._config.on_stage_completed(stage, tree)

    async def _emit_node_checkpoint(self, stage: str, tree: SummaryTree, node: SummaryNode) -> None:
        if self._config.on_node_completed is None:
            return
        await self._config.on_node_completed(stage, tree, node)

    async def _emit_log(
        self,
        phase: str,
        event: str,
        message: str,
        payload: dict[str, Any] | None = None,
    ) -> None:
        if self._config.on_log is None:
            return
        await self._config.on_log(phase, event, message, payload)

    def diagnostics(self) -> dict[str, int | bool]:
        return {
            "fallback_count": self._fallback_count,
            "provider_compute_errors": self._provider_compute_errors,
            "root_fallback": self._root_fallback,
            "leaf_cache_hits": self._cache_hits["leaves"],
            "file_cache_hits": self._cache_hits["files"],
            "package_cache_hits": self._cache_hits["packages"],
            "root_cache_hits": self._cache_hits["root"],
        }

    # ------------------------------------------------------------------
    # Level-specific summarization helpers

    async def _summarize_leaves(
        self,
        corpus: CorpusSource,
        leaves: list[CorpusUnit],
        tree: SummaryTree,
        progress: ProgressCallback,
    ) -> None:
        sem = asyncio.Semaphore(self._config.leaf_concurrency)
        total = len(leaves)
        completed = 0
        completed_lock = asyncio.Lock()

        async def one(unit: CorpusUnit) -> None:
            nonlocal completed
            async with sem:
                await self._summarize_leaf(corpus, unit, tree)
            async with completed_lock:
                completed += 1
                if completed % max(1, total // 10) == 0 or completed == total:
                    # Progress range 0.05 → 0.55 for leaves.
                    pct = 0.05 + 0.5 * (completed / total)
                    log.info(
                        "hierarchical_stage_progress",
                        corpus_id=corpus.corpus_id,
                        stage="leaves",
                        completed=completed,
                        total=total,
                    )
                    await self._emit_log(
                        "leaves",
                        "hierarchical_stage_progress",
                        f"Leaf summarization progress {completed}/{total}",
                        {"completed": completed, "total": total},
                    )
                    await _maybe_await(progress("leaves", pct, f"Summarized {completed}/{total} segments"))

        await asyncio.gather(*(one(u) for u in leaves))

    async def _summarize_nonleaf_stage(
        self,
        *,
        corpus: CorpusSource,
        units: list[CorpusUnit],
        tree: SummaryTree,
        stage: str,
        concurrency: int,
        summarize: Callable[[CorpusSource, CorpusUnit, SummaryTree], Awaitable[None]],
    ) -> None:
        sem = asyncio.Semaphore(max(1, concurrency))
        total = len(units)
        completed = 0
        completed_lock = asyncio.Lock()

        async def one(unit: CorpusUnit) -> None:
            nonlocal completed
            self._populate_child_ids(unit, corpus, tree)
            async with sem:
                await summarize(corpus, unit, tree)
            async with completed_lock:
                completed += 1
                if completed == total or completed % max(1, total // 5) == 0:
                    log.info(
                        "hierarchical_stage_progress",
                        corpus_id=corpus.corpus_id,
                        stage=stage,
                        completed=completed,
                        total=total,
                    )
                    await self._emit_log(
                        stage,
                        "hierarchical_stage_progress",
                        f"{stage.capitalize()} progress {completed}/{total}",
                        {"completed": completed, "total": total},
                    )

        await asyncio.gather(*(one(unit) for unit in units))

    async def _summarize_leaf(
        self,
        corpus: CorpusSource,
        unit: CorpusUnit,
        tree: SummaryTree,
    ) -> None:
        leaf_started = monotonic()
        file_path = str(unit.metadata.get("file_path", "")) if unit.metadata else ""
        context = f"hierarchical:leaf:{file_path}#{unit.label}"
        log.info(
            "hierarchical_leaf_started",
            corpus_id=corpus.corpus_id,
            unit_id=unit.id,
            file_path=file_path,
            segment_label=unit.label,
        )
        await self._emit_log(
            "leaves",
            "hierarchical_leaf_started",
            f"Summarizing leaf {unit.label}",
            {"unit_id": unit.id, "file_path": file_path, "segment_label": unit.label},
        )
        # Incremental reindex: skip if the cached summary has the same content_hash.
        if unit.content_hash and self._config.cached_tree:
            cached = self._config.cached_tree.get(unit.id)
            if cached and cached.content_hash == unit.content_hash and cached.summary_text:
                log.info(
                    "hierarchical_leaf_cache_hit",
                    corpus_id=corpus.corpus_id,
                    unit_id=unit.id,
                    file_path=file_path,
                    segment_label=unit.label,
                    elapsed_ms=int((monotonic() - leaf_started) * 1000),
                )
                await self._emit_log(
                    "leaves",
                    "hierarchical_leaf_cache_hit",
                    f"Reused cached leaf {unit.label}",
                    {"unit_id": unit.id, "file_path": file_path, "segment_label": unit.label},
                )
                self._cache_hits["leaves"] += 1
                tree.add(cached)
                return

        deterministic_summary = _deterministic_leaf_summary(unit)
        if deterministic_summary is not None:
            elapsed_ms = int((monotonic() - leaf_started) * 1000)
            node = SummaryNode(
                id=str(uuid.uuid4()),
                corpus_id=tree.corpus_id,
                unit_id=unit.id,
                level=0,
                parent_id=unit.parent_id,
                summary_text=deterministic_summary,
                headline=_first_line(deterministic_summary),
                summary_tokens=len(deterministic_summary.split()),
                source_tokens=unit.size_tokens or 1,
                content_hash=unit.content_hash,
                model_used="deterministic",
                strategy=self.name,
                revision_fp=tree.revision_fp,
                metadata=dict(unit.metadata),
            )
            log.info(
                "hierarchical_leaf_completed_deterministic",
                corpus_id=corpus.corpus_id,
                unit_id=unit.id,
                file_path=file_path,
                segment_label=unit.label,
                elapsed_ms=elapsed_ms,
            )
            await self._emit_log(
                "leaves",
                "hierarchical_leaf_completed_deterministic",
                f"Completed deterministic leaf {unit.label}",
                {
                    "unit_id": unit.id,
                    "file_path": file_path,
                    "segment_label": unit.label,
                    "elapsed_ms": elapsed_ms,
                },
            )
            tree.add(node)
            await self._emit_node_checkpoint("leaves", tree, node)
            return

        try:
            code = corpus.leaf_content(unit)
        except ValueError as exc:
            log.warning("hierarchical_leaf_missing_content", unit_id=unit.id, error=str(exc))
            await self._emit_log(
                "leaves",
                "hierarchical_leaf_missing_content",
                f"Leaf content missing for {unit.label}",
                {"unit_id": unit.id, "error": str(exc)},
            )
            tree.add(self._stub_node(unit, tree, "Missing content — could not load segment."))
            return

        language = str(unit.metadata.get("language", "")) if unit.metadata else ""
        prompt = build_leaf_prompt(
            repository_name=self._config.repository_name,
            file_path=file_path,
            segment_label=unit.label,
            language=language,
            code=code,
        )

        summary, model, tokens_in, tokens_out = await self._call_llm(
            prompt,
            context=context,
            fallback="Could not summarize this segment.",
            max_tokens=self._config.leaf_max_tokens,
        )
        elapsed_ms = int((monotonic() - leaf_started) * 1000)
        if elapsed_ms >= 30000:
            log.warning(
                "hierarchical_leaf_slow",
                corpus_id=corpus.corpus_id,
                unit_id=unit.id,
                file_path=file_path,
                segment_label=unit.label,
                elapsed_ms=elapsed_ms,
                model=model,
            )
            await self._emit_log(
                "leaves",
                "hierarchical_leaf_slow",
                f"Slow leaf {unit.label}",
                {"unit_id": unit.id, "elapsed_ms": elapsed_ms, "model": model},
            )
        log.info(
            "hierarchical_leaf_completed",
            corpus_id=corpus.corpus_id,
            unit_id=unit.id,
            file_path=file_path,
            segment_label=unit.label,
            elapsed_ms=elapsed_ms,
            model=model,
            input_tokens=tokens_in,
            output_tokens=tokens_out,
        )
        await self._emit_log(
            "leaves",
            "hierarchical_leaf_completed",
            f"Completed leaf {unit.label}",
            {
                "unit_id": unit.id,
                "file_path": file_path,
                "segment_label": unit.label,
                "elapsed_ms": elapsed_ms,
                "model": model,
                "input_tokens": tokens_in,
                "output_tokens": tokens_out,
            },
        )
        node = SummaryNode(
            id=str(uuid.uuid4()),
            corpus_id=tree.corpus_id,
            unit_id=unit.id,
            level=0,
            parent_id=unit.parent_id,
            summary_text=summary,
            headline=_first_line(summary),
            summary_tokens=tokens_out,
            source_tokens=unit.size_tokens or max(len(code) // 4, 1),
            content_hash=unit.content_hash,
            model_used=model,
            strategy=self.name,
            revision_fp=tree.revision_fp,
            metadata=dict(unit.metadata),
        )
        tree.add(node)
        await self._emit_node_checkpoint("leaves", tree, node)

    async def _summarize_file(
        self,
        corpus: CorpusSource,
        unit: CorpusUnit,
        tree: SummaryTree,
    ) -> None:
        children = tree.children_of(unit.id)
        node_hash = self._derived_content_hash(unit, children)
        cached = self._cached_node(unit.id, node_hash, stage="files")
        if cached is not None:
            self._cache_hits["files"] += 1
            tree.add(cached)
            return
        child_summaries = [n.summary_text for n in children if n.summary_text]
        if not child_summaries:
            tree.add(self._stub_node(unit, tree, "File contains no summarizable segments."))
            return

        file_path = str(unit.metadata.get("file_path", "")) if unit.metadata else unit.id
        language = str(unit.metadata.get("language", "")) if unit.metadata else ""
        file_facts = _collect_file_facts(unit, children)
        prompt = build_file_prompt(
            repository_name=self._config.repository_name,
            file_path=file_path,
            language=language,
            segment_summaries=child_summaries,
            file_facts=_format_file_fact_lines(file_facts),
        )
        summary, model, _, tokens_out = await self._call_llm(
            prompt,
            context=f"hierarchical:file:{file_path}",
            fallback=f"Could not summarize file {file_path}.",
            max_tokens=self._config.file_max_tokens,
        )
        node = SummaryNode(
            id=str(uuid.uuid4()),
            corpus_id=tree.corpus_id,
            unit_id=unit.id,
            level=1,
            parent_id=unit.parent_id,
            child_ids=[n.unit_id for n in tree.children_of(unit.id)],
            summary_text=summary,
            headline=_first_line(summary),
            summary_tokens=tokens_out,
            source_tokens=sum(n.source_tokens for n in children),
            content_hash=node_hash,
            model_used=model,
            strategy=self.name,
            revision_fp=tree.revision_fp,
            metadata={**dict(unit.metadata), **file_facts},
        )
        tree.add(node)
        await self._emit_node_checkpoint("files", tree, node)

    async def _summarize_package(
        self,
        corpus: CorpusSource,
        unit: CorpusUnit,
        tree: SummaryTree,
    ) -> None:
        children = tree.children_of(unit.id)
        node_hash = self._derived_content_hash(unit, children)
        cached = self._cached_node(unit.id, node_hash, stage="packages")
        if cached is not None:
            self._cache_hits["packages"] += 1
            tree.add(cached)
            return
        child_summaries = [n.summary_text for n in children if n.summary_text]
        if not child_summaries:
            tree.add(self._stub_node(unit, tree, "Package has no summarized files."))
            return

        package_facts = _collect_package_facts(unit, children)
        prompt = build_package_prompt(
            repository_name=self._config.repository_name,
            package_label=unit.label,
            file_summaries=child_summaries,
            package_facts=_format_package_fact_lines(package_facts),
        )
        summary, model, _, tokens_out = await self._call_llm(
            prompt,
            context=f"hierarchical:package:{unit.label}",
            fallback=f"Could not summarize package {unit.label}.",
            max_tokens=self._config.package_max_tokens,
        )
        node = SummaryNode(
            id=str(uuid.uuid4()),
            corpus_id=tree.corpus_id,
            unit_id=unit.id,
            level=2,
            parent_id=unit.parent_id,
            child_ids=[n.unit_id for n in tree.children_of(unit.id)],
            summary_text=summary,
            headline=_first_line(summary),
            summary_tokens=tokens_out,
            source_tokens=sum(n.source_tokens for n in children),
            content_hash=node_hash,
            model_used=model,
            strategy=self.name,
            revision_fp=tree.revision_fp,
            metadata={**dict(unit.metadata), **package_facts},
        )
        tree.add(node)
        await self._emit_node_checkpoint("packages", tree, node)

    async def _summarize_root(
        self,
        corpus: CorpusSource,
        unit: CorpusUnit,
        tree: SummaryTree,
        *,
        file_count: int,
        segment_count: int,
    ) -> None:
        children = tree.children_of(unit.id)
        node_hash = self._derived_content_hash(unit, children)
        cached = self._cached_node(unit.id, node_hash, stage="root")
        if cached is not None:
            self._cache_hits["root"] += 1
            tree.add(cached)
            return
        child_summaries = [n.summary_text for n in children if n.summary_text]
        if not child_summaries:
            tree.add(self._stub_node(unit, tree, "Repository has no summarizable packages."))
            return

        root_facts = _collect_root_facts(children, file_count=file_count, segment_count=segment_count)
        prompt = build_root_prompt(
            repository_name=self._config.repository_name,
            package_summaries=child_summaries,
            file_count=file_count,
            segment_count=segment_count,
            root_facts=_format_root_fact_lines(root_facts),
        )
        summary, model, _, tokens_out = await self._call_llm(
            prompt,
            context="hierarchical:root",
            fallback=f"Could not summarize repository {self._config.repository_name}.",
            max_tokens=self._config.root_max_tokens,
        )
        node = SummaryNode(
            id=str(uuid.uuid4()),
            corpus_id=tree.corpus_id,
            unit_id=unit.id,
            level=3,
            parent_id=None,
            child_ids=[n.unit_id for n in tree.children_of(unit.id)],
            summary_text=summary,
            headline=_first_line(summary),
            summary_tokens=tokens_out,
            source_tokens=sum(n.source_tokens for n in children),
            content_hash=node_hash,
            model_used=model,
            strategy=self.name,
            revision_fp=tree.revision_fp,
            metadata={**dict(unit.metadata), **root_facts},
        )
        tree.add(node)
        await self._emit_node_checkpoint("root", tree, node)

    # ------------------------------------------------------------------
    # Low-level helpers

    async def _call_llm(
        self,
        prompt: str,
        *,
        context: str,
        fallback: str,
        max_tokens: int,
    ) -> tuple[str, str, int, int]:
        """Call the provider with budget + empty-response guards.

        Returns ``(summary_text, model, tokens_in, tokens_out)``.

        On errors other than SnapshotTooLargeError, returns the fallback
        text so a single bad leaf/file does not abort the whole tree
        build. SnapshotTooLargeError propagates — that's a structural
        issue the caller needs to see (the leaf is genuinely too large
        for this model's budget and splitting would be required).
        """
        try:
            check_prompt_budget(prompt, system=HIERARCHICAL_SYSTEM, context=context)
        except SnapshotTooLargeError:
            # Leaf-level overflow is caller-visible — a single function
            # body exceeding the budget is a real problem.
            raise

        max_attempts = 3
        last_exc: Exception | None = None
        for attempt in range(1, max_attempts + 1):
            try:
                response: LLMResponse = require_nonempty(
                    await complete_with_optional_model(
                        self._provider,
                        prompt,
                        system=HIERARCHICAL_SYSTEM,
                        temperature=0.0,
                        max_tokens=max_tokens,
                        model=self._config.model_override,
                    ),
                    context=context,
                )
                return (
                    response.content.strip(),
                    response.model,
                    response.input_tokens,
                    response.output_tokens,
                )
            except Exception as exc:
                last_exc = exc
                if _is_provider_compute_error(exc):
                    self._provider_compute_errors += 1
                    if attempt < max_attempts:
                        delay = 0.35 * (2 ** (attempt - 1))
                        log.warning(
                            "hierarchical_node_retry",
                            context=context,
                            attempt=attempt,
                            delay_s=delay,
                            error=str(exc),
                        )
                        await asyncio.sleep(delay)
                        continue
                break

        self._fallback_count += 1
        if ":root" in context:
            self._root_fallback = True
        log.warning(
            "hierarchical_node_fallback",
            context=context,
            error=str(last_exc) if last_exc else "unknown error",
        )
        return (fallback, self._config.model_override or "unknown", 0, 0)

    def _populate_child_ids(
        self,
        unit: CorpusUnit,
        corpus: CorpusSource,
        tree: SummaryTree,
    ) -> None:
        """Pre-populate the unit's child list in the tree before summarizing.

        Called once per interior node so ``tree.children_of(unit.id)``
        works even before the parent node itself is added. We add a
        placeholder node with the child ids set; it will be overwritten
        once the actual summary is produced.
        """
        child_ids = [c.id for c in corpus.children(unit)]
        existing = tree.get(unit.id)
        if existing is not None:
            existing.child_ids = child_ids
            return
        tree.add(
            SummaryNode(
                id=str(uuid.uuid4()),
                corpus_id=tree.corpus_id,
                unit_id=unit.id,
                level=unit.level,
                parent_id=unit.parent_id,
                child_ids=child_ids,
                strategy=self.name,
                revision_fp=tree.revision_fp,
                metadata=dict(unit.metadata),
            )
        )

    def _stub_node(
        self,
        unit: CorpusUnit,
        tree: SummaryTree,
        message: str,
    ) -> SummaryNode:
        return SummaryNode(
            id=str(uuid.uuid4()),
            corpus_id=tree.corpus_id,
            unit_id=unit.id,
            level=unit.level,
            parent_id=unit.parent_id,
            summary_text=message,
            headline=message,
            model_used="fallback",
            strategy=self.name,
            revision_fp=tree.revision_fp,
            metadata=dict(unit.metadata),
        )

    def _cached_node(self, unit_id: str, content_hash: str, *, stage: str) -> SummaryNode | None:
        if not content_hash or self._config.cached_tree is None:
            return None
        cached = self._config.cached_tree.get(unit_id)
        if cached and cached.content_hash == content_hash and cached.summary_text:
            log.info(
                "hierarchical_node_cache_hit",
                stage=stage,
                unit_id=unit_id,
                level=cached.level,
            )
            return cached
        return None

    def _derived_content_hash(self, unit: CorpusUnit, children: list[SummaryNode]) -> str:
        child_fingerprints = [child.content_hash or child.unit_id for child in children]
        if not child_fingerprints:
            return ""
        return corpus_content_hash("|".join([unit.label, *child_fingerprints]))


def _deterministic_leaf_summary(unit: CorpusUnit) -> str | None:
    metadata = unit.metadata or {}
    if not metadata.get("deterministic_leaf"):
        return None

    file_path = str(metadata.get("file_path") or "")
    language = str(metadata.get("language") or "unknown")
    module_label = str(metadata.get("module_label") or "")

    if metadata.get("chunked"):
        symbol_count = coerce_int(metadata.get("symbol_count"))
        symbol_names = [str(name) for name in (metadata.get("symbol_names") or []) if str(name)]
        roles = [ROLE_LABELS.get(str(role), str(role).replace("_", " ")) for role in (metadata.get("symbol_roles") or [])]
        headline = f"Groups {symbol_count or 'multiple'} related symbols in {file_path or unit.label}"
        body_parts = [
            f"This deterministic leaf groups {symbol_count or 'multiple'} symbols from `{file_path or unit.label}` so later stages can summarize the file without restating each signature."
        ]
        if symbol_names:
            preview = ", ".join(symbol_names[:4])
            if len(symbol_names) > 4:
                preview += ", and others"
            body_parts.append(f"Included symbols: {preview}.")
        if roles:
            body_parts.append(f"The grouped symbols contribute repository roles such as {', '.join(roles[:3])}.")
        if module_label:
            body_parts.append(f"The file sits under the `{module_label}` module.")
        return f"{headline}\n\n{' '.join(body_parts)}"

    symbol_name = str(metadata.get("symbol_name") or unit.label or "symbol")
    symbol_kind = str(metadata.get("symbol_kind") or "symbol")
    roles = [ROLE_LABELS.get(str(role), str(role).replace("_", " ")) for role in (metadata.get("symbol_roles") or [])]
    fan_in = coerce_int(metadata.get("fan_in"))
    fan_out = coerce_int(metadata.get("fan_out"))
    has_doc_comment = bool(metadata.get("has_doc_comment"))
    start_line = coerce_int(metadata.get("start_line"))
    end_line = coerce_int(metadata.get("end_line"))

    headline = f"Defines {symbol_kind} `{symbol_name}`"
    body_parts = [
        f"This deterministic leaf records `{symbol_name}` from `{file_path or unit.label}` as a {symbol_kind} in {language}."
    ]
    if start_line and end_line:
        body_parts.append(f"It is indexed at lines {start_line}-{end_line}.")
    if module_label:
        body_parts.append(f"It belongs to the `{module_label}` module.")
    if roles:
        body_parts.append(f"Its repository role is visible through {', '.join(roles[:3])}.")
    if fan_in or fan_out:
        flow_parts: list[str] = []
        if fan_in:
            flow_parts.append(f"fan-in {fan_in}")
        if fan_out:
            flow_parts.append(f"fan-out {fan_out}")
        body_parts.append(f"Indexed graph signals show {' and '.join(flow_parts)}.")
    if has_doc_comment:
        body_parts.append("A doc comment is available for later summarization stages.")
    return f"{headline}\n\n{' '.join(body_parts)}"


def _collect_file_facts(unit: CorpusUnit, children: list[SummaryNode]) -> dict[str, Any]:
    kind_counts: Counter[str] = Counter()
    role_counts: Counter[str] = Counter()
    signal_counts: Counter[str] = Counter()
    entity_counts: Counter[str] = Counter()
    external_counts: Counter[str] = Counter()
    symbol_names: list[str] = []
    doc_comments = 0
    max_fan_in = 0
    max_fan_out = 0
    module_label = str((unit.metadata or {}).get("module_label") or "")

    for child in children:
        metadata = child.metadata or {}
        symbol_kind = str(metadata.get("symbol_kind") or "")
        if symbol_kind:
            kind_counts[symbol_kind] += 1
        name = str(metadata.get("symbol_name") or "")
        if name and len(symbol_names) < 5:
            symbol_names.append(name)
        if bool(metadata.get("has_doc_comment")):
            doc_comments += 1
        max_fan_in = max(max_fan_in, coerce_int(metadata.get("fan_in")))
        max_fan_out = max(max_fan_out, coerce_int(metadata.get("fan_out")))
        for role in metadata.get("symbol_roles") or []:
            role_counts[str(role)] += 1
        for signal in metadata.get("path_signals") or []:
            signal_counts[str(signal)] += 1
        for entity in metadata.get("entity_signals") or []:
            entity_counts[str(entity)] += 1
        for dependency in metadata.get("external_dependency_signals") or []:
            external_counts[str(dependency)] += 1
        if not module_label:
            module_label = str(metadata.get("module_label") or "")

    return {
        "module_label": module_label,
        "fact_symbol_count": len(children),
        "fact_symbol_kinds": [f"{kind} ({count})" for kind, count in kind_counts.most_common(3)],
        "fact_symbol_names": symbol_names,
        "fact_roles": [ROLE_LABELS.get(role, role.replace("_", " ")) for role, _ in role_counts.most_common(3)],
        "fact_path_signals": [signal for signal, _ in signal_counts.most_common(4)],
        "fact_entity_signals": [entity for entity, _ in entity_counts.most_common(5)],
        "fact_external_dependencies": [dep for dep, _ in external_counts.most_common(4)],
        "fact_doc_comment_count": doc_comments,
        "fact_max_fan_in": max_fan_in,
        "fact_max_fan_out": max_fan_out,
    }


def _collect_package_facts(unit: CorpusUnit, children: list[SummaryNode]) -> dict[str, Any]:
    language_counts: Counter[str] = Counter()
    role_counts: Counter[str] = Counter()
    kind_counts: Counter[str] = Counter()
    signal_counts: Counter[str] = Counter()
    entity_counts: Counter[str] = Counter()
    external_counts: Counter[str] = Counter()
    file_paths: list[str] = []
    module_labels: list[str] = []
    total_symbols = 0

    for child in children:
        metadata = child.metadata or {}
        language = str(metadata.get("language") or "")
        if language:
            language_counts[language] += 1
        file_path = str(metadata.get("file_path") or "")
        if file_path:
            file_paths.append(file_path)
        module_label = str(metadata.get("module_label") or "")
        if module_label:
            module_labels.append(module_label)
        total_symbols += coerce_int(metadata.get("fact_symbol_count"))
        for label in metadata.get("fact_symbol_kinds") or []:
            kind = str(label).split(" (", 1)[0].strip()
            if kind:
                kind_counts[kind] += 1
        for role in metadata.get("fact_roles") or []:
            role_counts[str(role)] += 1
        for signal in metadata.get("fact_path_signals") or []:
            signal_counts[str(signal)] += 1
        for entity in metadata.get("fact_entity_signals") or []:
            entity_counts[str(entity)] += 1
        for dependency in metadata.get("fact_external_dependencies") or []:
            external_counts[str(dependency)] += 1

    return {
        "fact_file_count": len(children),
        "fact_total_symbols": total_symbols,
        "fact_languages": [f"{language} ({count})" for language, count in language_counts.most_common(3)],
        "fact_package_roles": [role for role, _ in role_counts.most_common(3)],
        "fact_package_symbol_kinds": [kind for kind, _ in kind_counts.most_common(3)],
        "fact_package_signals": [signal for signal, _ in signal_counts.most_common(4)],
        "fact_package_entities": [entity for entity, _ in entity_counts.most_common(5)],
        "fact_external_dependencies": [dep for dep, _ in external_counts.most_common(5)],
        "fact_key_files": file_paths[:4],
        "fact_module_labels": _dedupe_preserve_order(module_labels)[:3] or [unit.label],
    }


def _collect_root_facts(
    children: list[SummaryNode],
    *,
    file_count: int,
    segment_count: int,
) -> dict[str, Any]:
    language_counts: Counter[str] = Counter()
    role_counts: Counter[str] = Counter()
    signal_counts: Counter[str] = Counter()
    entity_counts: Counter[str] = Counter()
    external_counts: Counter[str] = Counter()
    package_labels: list[str] = []
    key_files: list[str] = []
    total_symbols = 0

    for child in children:
        metadata = child.metadata or {}
        package_labels.append(child.unit_id)
        total_symbols += coerce_int(metadata.get("fact_total_symbols"))
        key_files.extend([str(path) for path in (metadata.get("fact_key_files") or []) if str(path)])
        for label in metadata.get("fact_languages") or []:
            language = str(label).split(" (", 1)[0].strip()
            if language:
                language_counts[language] += 1
        for role in metadata.get("fact_package_roles") or []:
            role_counts[str(role)] += 1
        for signal in metadata.get("fact_package_signals") or []:
            signal_counts[str(signal)] += 1
        for entity in metadata.get("fact_package_entities") or []:
            entity_counts[str(entity)] += 1
        for dependency in metadata.get("fact_external_dependencies") or []:
            external_counts[str(dependency)] += 1

    return {
        "fact_file_count": file_count,
        "fact_segment_count": segment_count,
        "fact_total_symbols": total_symbols,
        "fact_package_labels": package_labels[:6],
        "fact_languages": [language for language, _ in language_counts.most_common(4)],
        "fact_root_roles": [role for role, _ in role_counts.most_common(4)],
        "fact_root_signals": [signal for signal, _ in signal_counts.most_common(5)],
        "fact_root_entities": [entity for entity, _ in entity_counts.most_common(6)],
        "fact_external_dependencies": [dep for dep, _ in external_counts.most_common(6)],
        "fact_key_files": _dedupe_preserve_order(key_files)[:6],
    }


def _format_file_fact_lines(facts: dict[str, Any]) -> list[str]:
    lines = [f"Symbols indexed: {int(facts.get('fact_symbol_count') or 0)}"]
    if facts.get("module_label"):
        lines.append(f"Module: {facts['module_label']}")
    if facts.get("fact_symbol_kinds"):
        lines.append(f"Symbol mix: {', '.join(str(v) for v in facts['fact_symbol_kinds'])}")
    if facts.get("fact_roles"):
        lines.append(f"Roles: {', '.join(str(v) for v in facts['fact_roles'])}")
    if facts.get("fact_path_signals"):
        lines.append(f"Execution signals: {', '.join(str(v) for v in facts['fact_path_signals'])}")
    if facts.get("fact_entity_signals"):
        lines.append(f"Domain entities: {', '.join(str(v) for v in facts['fact_entity_signals'])}")
    if facts.get("fact_symbol_names"):
        lines.append(f"Representative symbols: {', '.join(str(v) for v in facts['fact_symbol_names'])}")
    if facts.get("fact_external_dependencies"):
        lines.append(f"External dependency hints: {', '.join(str(v) for v in facts['fact_external_dependencies'])}")
    if facts.get("fact_doc_comment_count"):
        lines.append(f"Doc-commented symbols: {facts['fact_doc_comment_count']}")
    fan_notes: list[str] = []
    if facts.get("fact_max_fan_in"):
        fan_notes.append(f"max fan-in {facts['fact_max_fan_in']}")
    if facts.get("fact_max_fan_out"):
        fan_notes.append(f"max fan-out {facts['fact_max_fan_out']}")
    if fan_notes:
        lines.append(f"Graph signals: {', '.join(fan_notes)}")
    return lines


def _format_package_fact_lines(facts: dict[str, Any]) -> list[str]:
    lines = [f"Files indexed: {int(facts.get('fact_file_count') or 0)}"]
    if facts.get("fact_total_symbols"):
        lines.append(f"Total symbols represented: {facts['fact_total_symbols']}")
    if facts.get("fact_languages"):
        lines.append(f"Languages: {', '.join(str(v) for v in facts['fact_languages'])}")
    if facts.get("fact_package_roles"):
        lines.append(f"Dominant roles: {', '.join(str(v) for v in facts['fact_package_roles'])}")
    if facts.get("fact_package_signals"):
        lines.append(f"Execution signals: {', '.join(str(v) for v in facts['fact_package_signals'])}")
    if facts.get("fact_package_entities"):
        lines.append(f"Domain entities: {', '.join(str(v) for v in facts['fact_package_entities'])}")
    if facts.get("fact_package_symbol_kinds"):
        lines.append(f"Common symbol kinds: {', '.join(str(v) for v in facts['fact_package_symbol_kinds'])}")
    if facts.get("fact_key_files"):
        lines.append(f"Representative files: {', '.join(str(v) for v in facts['fact_key_files'])}")
    if facts.get("fact_external_dependencies"):
        lines.append(f"External dependency hints: {', '.join(str(v) for v in facts['fact_external_dependencies'])}")
    return lines


def _format_root_fact_lines(facts: dict[str, Any]) -> list[str]:
    lines = [
        f"Repository files indexed: {int(facts.get('fact_file_count') or 0)}",
        f"Repository segments indexed: {int(facts.get('fact_segment_count') or 0)}",
    ]
    if facts.get("fact_total_symbols"):
        lines.append(f"Total symbols represented: {facts['fact_total_symbols']}")
    if facts.get("fact_package_labels"):
        lines.append(f"Packages represented: {', '.join(str(v) for v in facts['fact_package_labels'])}")
    if facts.get("fact_languages"):
        lines.append(f"Languages: {', '.join(str(v) for v in facts['fact_languages'])}")
    if facts.get("fact_root_roles"):
        lines.append(f"Repository roles: {', '.join(str(v) for v in facts['fact_root_roles'])}")
    if facts.get("fact_root_signals"):
        lines.append(f"Execution signals: {', '.join(str(v) for v in facts['fact_root_signals'])}")
    if facts.get("fact_root_entities"):
        lines.append(f"Repository entities: {', '.join(str(v) for v in facts['fact_root_entities'])}")
    if facts.get("fact_key_files"):
        lines.append(f"Representative files: {', '.join(str(v) for v in facts['fact_key_files'])}")
    if facts.get("fact_external_dependencies"):
        lines.append(f"External dependency hints: {', '.join(str(v) for v in facts['fact_external_dependencies'])}")
    return lines


def _dedupe_preserve_order(values: list[str]) -> list[str]:
    seen: set[str] = set()
    ordered: list[str] = []
    for value in values:
        if not value or value in seen:
            continue
        seen.add(value)
        ordered.append(value)
    return ordered


def _first_line(text: str) -> str:
    """Return the first non-empty line, truncated to 140 chars."""
    if not text:
        return ""
    for line in text.splitlines():
        line = line.strip()
        if line:
            return line[:140]
    return ""


def _is_provider_compute_error(exc: Exception) -> bool:
    text = str(exc).lower()
    return "compute error" in text or "server_error" in text


async def _maybe_await(value: object) -> None:
    """Await ``value`` if it is an awaitable, otherwise no-op.

    Lets ProgressCallback be either sync or async without forcing
    callers to decide up front.
    """
    if asyncio.iscoroutine(value):
        await value


def _env_int(name: str, default: int) -> int:
    raw = os.environ.get(name, "").strip()
    if not raw:
        return default
    try:
        value = int(raw)
    except ValueError:
        return default
    return value if value > 0 else default
