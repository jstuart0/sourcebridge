# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Corpus abstractions.

``CorpusSource`` is the interface every comprehension strategy sees.
Adapters wrap concrete inputs (a knowledge snapshot, a requirements
document, a markdown collection) as a hierarchy of :class:`CorpusUnit`
values and return leaf text on demand.

Every strategy in this package is written against this interface, not
against a specific corpus type. Adding a new corpus means adding a new
adapter; the strategies stay unchanged.
"""

from __future__ import annotations

import hashlib
from collections.abc import Iterable, Iterator
from dataclasses import dataclass, field
from enum import StrEnum
from typing import Protocol, runtime_checkable


class UnitKind(StrEnum):
    """Canonical labels for the levels of a corpus hierarchy.

    Code adapters use ``repo → package → file → segment``; document
    adapters use ``collection → document → section → paragraph``. Mapping
    both onto a small shared vocabulary lets a renderer say "give me the
    root summary and up to N level-2 summaries" without knowing whether
    level 2 is a package or a section.
    """

    ROOT = "root"
    GROUP = "group"           # package / document / collection
    LEAF_CONTAINER = "leaf_container"  # file / section
    LEAF = "leaf"             # segment / paragraph

    def is_leaf(self) -> bool:
        return self is UnitKind.LEAF


@dataclass(frozen=True)
class CorpusUnit:
    """A single node in a corpus hierarchy.

    ``id`` is a stable identifier used as the dedupe/cache key. ``kind``
    tells the strategy whether this is a leaf (summarize its content) or
    an interior node (summarize its children). ``level`` is a 0-indexed
    depth from the leaves — leaves are level 0, their parent
    ``LEAF_CONTAINER`` is level 1, ``GROUP`` is level 2, the ``ROOT`` is
    level 3. This matches the plan's "4-level tree".
    """

    id: str
    kind: UnitKind
    level: int
    label: str
    parent_id: str | None = None
    # size_tokens is a hint used by the strategy to decide whether a leaf
    # needs further splitting before it fits the model's effective
    # context. Adapters should populate it when possible; 0 means unknown.
    size_tokens: int = 0
    # content_hash is a Merkle fingerprint that changes when the leaf
    # content changes. Used by the incremental reindex to skip nodes
    # whose content hasn't changed since the last build. Empty string
    # means "always rebuild" (safe default for adapters that don't
    # compute hashes yet).
    content_hash: str = ""
    # metadata carries adapter-specific fields (file path, module name,
    # commit sha, etc.) that renderers may surface in evidence links.
    metadata: dict[str, object] = field(default_factory=dict)


@runtime_checkable
class CorpusSource(Protocol):
    """The read-only interface every strategy consumes.

    Implementations return ``CorpusUnit`` references and materialize
    leaf text only on demand. This keeps memory bounded for very large
    corpora — a strategy can stream leaves instead of loading them all
    at once.
    """

    corpus_id: str
    corpus_type: str

    def root(self) -> CorpusUnit:
        """Return the single root unit."""

    def children(self, unit: CorpusUnit) -> Iterable[CorpusUnit]:
        """Return the direct children of ``unit`` in iteration order.

        For a leaf, this is an empty iterable.
        """

    def leaf_content(self, unit: CorpusUnit) -> str:
        """Return the raw text of a leaf unit.

        Implementations may raise ``ValueError`` when called on a
        non-leaf unit; strategies should only call this after confirming
        ``unit.kind.is_leaf()``.
        """

    def revision_fingerprint(self) -> str:
        """Return a fingerprint that changes whenever the corpus content
        or hierarchy changes. Used for cache invalidation in later phases.
        An empty string means "always rebuild" (safe default for
        adapters that don't track revisions yet).
        """


def walk_leaves(corpus: CorpusSource) -> Iterator[CorpusUnit]:
    """Depth-first iteration over every leaf unit in the corpus.

    This is the canonical way for a strategy to enumerate its inputs
    when it needs to run an LLM call per leaf — use it instead of
    recursing by hand to keep strategy code straightforward.
    """
    stack: list[CorpusUnit] = [corpus.root()]
    while stack:
        unit = stack.pop()
        children = list(corpus.children(unit))
        if not children:
            if unit.kind.is_leaf():
                yield unit
            continue
        # Push children in reverse so the iteration order matches the
        # adapter's natural child order.
        stack.extend(reversed(children))


def walk_by_level(corpus: CorpusSource) -> dict[int, list[CorpusUnit]]:
    """Collect every unit in the corpus grouped by its level.

    Returns a dict keyed by level (0=leaves, 1=leaf_container, 2=group,
    3=root) with the corresponding units in insertion order. Strategies
    that build bottom-up need this to process each level in one pass.
    """
    by_level: dict[int, list[CorpusUnit]] = {}
    stack: list[CorpusUnit] = [corpus.root()]
    while stack:
        unit = stack.pop()
        by_level.setdefault(unit.level, []).append(unit)
        stack.extend(reversed(list(corpus.children(unit))))
    return by_level


def content_hash(text: str) -> str:
    """Compute a SHA-256 content hash for Merkle fingerprinting.

    Used to determine whether a leaf's content has changed since the
    last build. Interior nodes derive their hash from their children's
    hashes — the HierarchicalStrategy handles that combination.
    """
    return hashlib.sha256(text.encode("utf-8", errors="replace")).hexdigest()[:16]
