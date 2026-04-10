# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Summary tree data structures.

A :class:`SummaryTree` is a persistent-at-the-type-level representation
of the hierarchical summaries produced by a comprehension strategy. In
Phase 3 the tree lives only in memory — the plan's ``ca_summary_node``
table and Merkle-tree incremental reindex are deferred to a follow-up
phase so the hot-fix Ollama workaround can ship first.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import UTC, datetime


@dataclass
class SummaryNode:
    """One node in a summary tree.

    The fields are a strict superset of what the ``ca_summary_node``
    table will store so persistence can be bolted on without reshaping
    the in-memory model.
    """

    id: str
    corpus_id: str
    unit_id: str
    level: int
    parent_id: str | None
    child_ids: list[str] = field(default_factory=list)
    summary_text: str = ""
    # Optional short blurb used by renderers that need a one-liner
    # (e.g. a per-file summary on a tree-view).
    headline: str = ""
    summary_tokens: int = 0
    source_tokens: int = 0
    content_hash: str = ""
    model_used: str = ""
    strategy: str = ""
    revision_fp: str = ""
    # Metadata is copied from the originating CorpusUnit so renderers
    # can surface file paths / module names without re-walking the
    # corpus source.
    metadata: dict[str, object] = field(default_factory=dict)
    generated_at: datetime = field(default_factory=lambda: datetime.now(UTC))


@dataclass
class SummaryTree:
    """In-memory summary tree indexed by unit id.

    Strategies populate the tree bottom-up, adding one node per unit
    they summarize. Renderers then pull nodes by level / parent to
    construct the final artifact prompt.
    """

    corpus_id: str
    corpus_type: str
    strategy: str
    revision_fp: str = ""
    nodes: dict[str, SummaryNode] = field(default_factory=dict)

    def add(self, node: SummaryNode) -> None:
        """Insert or replace a node keyed by its ``unit_id``.

        Callers typically pass ``unit_id == id`` since there's one
        summary per corpus unit, but keeping them separate preserves
        the option to have multiple summaries per unit later (different
        audiences, different depths).
        """
        self.nodes[node.unit_id] = node

    def get(self, unit_id: str) -> SummaryNode | None:
        return self.nodes.get(unit_id)

    def root(self) -> SummaryNode | None:
        """Return the highest-level node in the tree, or ``None`` when empty.

        Multiple nodes may share the max level (e.g. during a partial
        build); this method returns the one whose ``parent_id`` is None
        to disambiguate.
        """
        best: SummaryNode | None = None
        for node in self.nodes.values():
            if node.parent_id is not None:
                continue
            if best is None or node.level > best.level:
                best = node
        return best

    def at_level(self, level: int) -> list[SummaryNode]:
        """Return every node at the supplied level in insertion order."""
        return [n for n in self.nodes.values() if n.level == level]

    def children_of(self, unit_id: str) -> list[SummaryNode]:
        """Return direct children of the supplied unit id in tree order."""
        node = self.nodes.get(unit_id)
        if node is None:
            return []
        return [self.nodes[child_id] for child_id in node.child_ids if child_id in self.nodes]

    def top_children(self, parent_id: str, n: int) -> list[SummaryNode]:
        """Return up to ``n`` direct children of ``parent_id``.

        Today this returns the first ``n`` in insertion order. A future
        refinement can rank children by summary_tokens or source_tokens
        to prefer the most substantial modules/files. Renderers call
        this when they want "the biggest N chunks under the root" for
        their final prompt.
        """
        return self.children_of(parent_id)[:n]

    def total_source_tokens(self) -> int:
        """Return the sum of source_tokens across all leaf nodes.

        Useful for sizing the final render prompt: renderers can back
        off to shallower trees when the total is very large.
        """
        return sum(n.source_tokens for n in self.nodes.values() if n.level == 0)

    def stats(self) -> dict[str, int]:
        """Return a short summary of the tree for logging.

        Used by the servicer to emit ``hierarchical_tree_built`` events
        that the Monitor page can surface on the job detail drawer.
        """
        levels: dict[int, int] = {}
        for node in self.nodes.values():
            levels[node.level] = levels.get(node.level, 0) + 1
        return {
            "nodes": len(self.nodes),
            "levels": len(levels),
            **{f"level_{lvl}": count for lvl, count in sorted(levels.items())},
        }
