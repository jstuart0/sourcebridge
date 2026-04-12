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
from dataclasses import dataclass

import structlog

from workers.common.llm.provider import (
    LLMProvider,
    LLMResponse,
    SnapshotTooLargeError,
    check_prompt_budget,
    complete_with_optional_model,
    require_nonempty,
)
from workers.comprehension.corpus import CorpusSource, CorpusUnit, walk_by_level
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

log = structlog.get_logger()


DEFAULT_LEAF_CONCURRENCY = 1
DEFAULT_MAX_TOKENS_PER_CALL = 16384  # thinking models (Qwen 3.x) consume thousands of tokens on <think> chains before producing visible summary text


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
    max_tokens_per_call: int = DEFAULT_MAX_TOKENS_PER_CALL
    model_override: str | None = None
    cached_tree: SummaryTree | None = None

    @classmethod
    def from_env(cls, repository_name: str = "") -> HierarchicalConfig:
        """Load tunables from environment variables, falling back to defaults."""
        raw_conc = os.environ.get("SOURCEBRIDGE_HIERARCHICAL_CONCURRENCY", "").strip()
        try:
            conc = int(raw_conc) if raw_conc else DEFAULT_LEAF_CONCURRENCY
        except ValueError:
            conc = DEFAULT_LEAF_CONCURRENCY
        if conc <= 0:
            conc = 1
        return cls(
            repository_name=repository_name,
            leaf_concurrency=conc,
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
        progress: ProgressCallback = _noop_progress,
    ) -> SummaryTree:
        """Build a summary tree for the supplied corpus.

        The tree is built bottom-up: leaves first in parallel, then
        each level's parents sequentially (parents are fast — one call
        per parent — and sequencing avoids a deeper concurrency bloom).
        """
        self._fallback_count = 0
        self._provider_compute_errors = 0
        self._root_fallback = False

        by_level = walk_by_level(corpus)
        leaf_units = by_level.get(0, [])
        file_units = by_level.get(1, [])
        package_units = by_level.get(2, [])
        root_units = by_level.get(3, [])

        total_nodes = sum(len(v) for v in by_level.values())
        log.info(
            "hierarchical_build_started",
            corpus_id=corpus.corpus_id,
            leaves=len(leaf_units),
            files=len(file_units),
            packages=len(package_units),
            total=total_nodes,
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
            await self._summarize_leaves(corpus, leaf_units, tree, progress)

        # Level 1 — file summaries.
        if file_units:
            await _maybe_await(progress("files", 0.55, f"Summarizing {len(file_units)} files"))
            for unit in file_units:
                self._populate_child_ids(unit, corpus, tree)
                await self._summarize_file(corpus, unit, tree)

        # Level 2 — package summaries.
        if package_units:
            await _maybe_await(
                progress("packages", 0.8, f"Summarizing {len(package_units)} packages")
            )
            for unit in package_units:
                self._populate_child_ids(unit, corpus, tree)
                await self._summarize_package(corpus, unit, tree)

        # Level 3 — root summary.
        if root_units:
            await _maybe_await(progress("root", 0.95, "Summarizing repository"))
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
            "hierarchical_build_completed",
            corpus_id=corpus.corpus_id,
            stats=tree.stats(),
        )
        await _maybe_await(progress("ready", 1.0, "Hierarchical summary tree built"))
        return tree

    def diagnostics(self) -> dict[str, int | bool]:
        return {
            "fallback_count": self._fallback_count,
            "provider_compute_errors": self._provider_compute_errors,
            "root_fallback": self._root_fallback,
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
                    await _maybe_await(
                        progress("leaves", pct, f"Summarized {completed}/{total} segments")
                    )

        await asyncio.gather(*(one(u) for u in leaves))

    async def _summarize_leaf(
        self,
        corpus: CorpusSource,
        unit: CorpusUnit,
        tree: SummaryTree,
    ) -> None:
        # Incremental reindex: skip if the cached summary has the same content_hash.
        if unit.content_hash and self._config.cached_tree:
            cached = self._config.cached_tree.get(unit.id)
            if cached and cached.content_hash == unit.content_hash and cached.summary_text:
                log.debug("hierarchical_leaf_cache_hit", unit_id=unit.id)
                tree.add(cached)
                return

        try:
            code = corpus.leaf_content(unit)
        except ValueError as exc:
            log.warning("hierarchical_leaf_missing_content", unit_id=unit.id, error=str(exc))
            tree.add(self._stub_node(unit, tree, "Missing content — could not load segment."))
            return

        file_path = str(unit.metadata.get("file_path", "")) if unit.metadata else ""
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
            context=f"hierarchical:leaf:{file_path}#{unit.label}",
            fallback="Could not summarize this segment.",
        )
        tree.add(
            SummaryNode(
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
        )

    async def _summarize_file(
        self,
        corpus: CorpusSource,
        unit: CorpusUnit,
        tree: SummaryTree,
    ) -> None:
        child_summaries = [
            n.summary_text for n in tree.children_of(unit.id) if n.summary_text
        ]
        if not child_summaries:
            tree.add(self._stub_node(unit, tree, "File contains no summarizable segments."))
            return

        file_path = str(unit.metadata.get("file_path", "")) if unit.metadata else unit.id
        language = str(unit.metadata.get("language", "")) if unit.metadata else ""
        prompt = build_file_prompt(
            repository_name=self._config.repository_name,
            file_path=file_path,
            language=language,
            segment_summaries=child_summaries,
        )
        summary, model, _, tokens_out = await self._call_llm(
            prompt,
            context=f"hierarchical:file:{file_path}",
            fallback=f"Could not summarize file {file_path}.",
        )
        tree.add(
            SummaryNode(
                id=str(uuid.uuid4()),
                corpus_id=tree.corpus_id,
                unit_id=unit.id,
                level=1,
                parent_id=unit.parent_id,
                child_ids=[n.unit_id for n in tree.children_of(unit.id)],
                summary_text=summary,
                headline=_first_line(summary),
                summary_tokens=tokens_out,
                source_tokens=sum(n.source_tokens for n in tree.children_of(unit.id)),
                model_used=model,
                strategy=self.name,
                revision_fp=tree.revision_fp,
                metadata=dict(unit.metadata),
            )
        )

    async def _summarize_package(
        self,
        corpus: CorpusSource,
        unit: CorpusUnit,
        tree: SummaryTree,
    ) -> None:
        child_summaries = [
            n.summary_text for n in tree.children_of(unit.id) if n.summary_text
        ]
        if not child_summaries:
            tree.add(self._stub_node(unit, tree, "Package has no summarized files."))
            return

        prompt = build_package_prompt(
            repository_name=self._config.repository_name,
            package_label=unit.label,
            file_summaries=child_summaries,
        )
        summary, model, _, tokens_out = await self._call_llm(
            prompt,
            context=f"hierarchical:package:{unit.label}",
            fallback=f"Could not summarize package {unit.label}.",
        )
        tree.add(
            SummaryNode(
                id=str(uuid.uuid4()),
                corpus_id=tree.corpus_id,
                unit_id=unit.id,
                level=2,
                parent_id=unit.parent_id,
                child_ids=[n.unit_id for n in tree.children_of(unit.id)],
                summary_text=summary,
                headline=_first_line(summary),
                summary_tokens=tokens_out,
                source_tokens=sum(n.source_tokens for n in tree.children_of(unit.id)),
                model_used=model,
                strategy=self.name,
                revision_fp=tree.revision_fp,
                metadata=dict(unit.metadata),
            )
        )

    async def _summarize_root(
        self,
        corpus: CorpusSource,
        unit: CorpusUnit,
        tree: SummaryTree,
        *,
        file_count: int,
        segment_count: int,
    ) -> None:
        child_summaries = [
            n.summary_text for n in tree.children_of(unit.id) if n.summary_text
        ]
        if not child_summaries:
            tree.add(self._stub_node(unit, tree, "Repository has no summarizable packages."))
            return

        prompt = build_root_prompt(
            repository_name=self._config.repository_name,
            package_summaries=child_summaries,
            file_count=file_count,
            segment_count=segment_count,
        )
        summary, model, _, tokens_out = await self._call_llm(
            prompt,
            context="hierarchical:root",
            fallback=f"Could not summarize repository {self._config.repository_name}.",
        )
        tree.add(
            SummaryNode(
                id=str(uuid.uuid4()),
                corpus_id=tree.corpus_id,
                unit_id=unit.id,
                level=3,
                parent_id=None,
                child_ids=[n.unit_id for n in tree.children_of(unit.id)],
                summary_text=summary,
                headline=_first_line(summary),
                summary_tokens=tokens_out,
                source_tokens=sum(n.source_tokens for n in tree.children_of(unit.id)),
                model_used=model,
                strategy=self.name,
                revision_fp=tree.revision_fp,
                metadata=dict(unit.metadata),
            )
        )

    # ------------------------------------------------------------------
    # Low-level helpers

    async def _call_llm(
        self,
        prompt: str,
        *,
        context: str,
        fallback: str,
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
                        max_tokens=self._config.max_tokens_per_call,
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
