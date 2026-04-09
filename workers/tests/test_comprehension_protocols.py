# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Tests for the comprehension package protocols and data structures."""

from __future__ import annotations

from dataclasses import dataclass, field

import pytest

from workers.comprehension.corpus import (
    CorpusSource,
    CorpusUnit,
    UnitKind,
    walk_by_level,
    walk_leaves,
)
from workers.comprehension.tree import SummaryNode, SummaryTree


@dataclass
class _SimpleCorpus:
    """Minimal CorpusSource built out of a list of units.

    Used in tests to construct tiny hierarchies without pulling in the
    full CodeCorpus adapter — exercises the protocol without any I/O.
    """

    corpus_id: str
    corpus_type: str
    units: dict[str, CorpusUnit]
    children_map: dict[str, list[str]] = field(default_factory=dict)
    leaf_texts: dict[str, str] = field(default_factory=dict)

    def root(self) -> CorpusUnit:
        # Root is whichever unit has parent_id == None and the highest level.
        roots = [u for u in self.units.values() if u.parent_id is None]
        roots.sort(key=lambda u: -u.level)
        return roots[0]

    def children(self, unit: CorpusUnit):
        for child_id in self.children_map.get(unit.id, []):
            yield self.units[child_id]

    def leaf_content(self, unit: CorpusUnit) -> str:
        if not unit.kind.is_leaf():
            raise ValueError(f"not a leaf: {unit.id}")
        return self.leaf_texts.get(unit.id, "")

    def revision_fingerprint(self) -> str:
        return "test-rev"


def _build_sample_corpus() -> _SimpleCorpus:
    """A 4-level tree with 2 packages, 2 files each, 2 segments per file."""
    units: dict[str, CorpusUnit] = {}
    children_map: dict[str, list[str]] = {}
    leaf_texts: dict[str, str] = {}

    def add(unit: CorpusUnit) -> None:
        units[unit.id] = unit
        if unit.parent_id:
            children_map.setdefault(unit.parent_id, []).append(unit.id)

    add(CorpusUnit(id="repo", kind=UnitKind.ROOT, level=3, label="repo"))
    for p in ("pkg1", "pkg2"):
        add(CorpusUnit(id=p, kind=UnitKind.GROUP, level=2, label=p, parent_id="repo"))
        for f in ("a.go", "b.go"):
            fid = f"{p}/{f}"
            add(CorpusUnit(
                id=fid,
                kind=UnitKind.LEAF_CONTAINER,
                level=1,
                label=f,
                parent_id=p,
                metadata={"file_path": fid},
            ))
            for s in ("Func1", "Func2"):
                sid = f"{fid}#{s}"
                add(CorpusUnit(
                    id=sid,
                    kind=UnitKind.LEAF,
                    level=0,
                    label=s,
                    parent_id=fid,
                    size_tokens=100,
                    metadata={"file_path": fid, "symbol": s},
                ))
                leaf_texts[sid] = f"func {s}() {{ // body for {fid} }}"
    return _SimpleCorpus(
        corpus_id="test-corpus",
        corpus_type="code",
        units=units,
        children_map=children_map,
        leaf_texts=leaf_texts,
    )


def test_simple_corpus_satisfies_protocol() -> None:
    corpus = _build_sample_corpus()
    assert isinstance(corpus, CorpusSource)


def test_walk_leaves_visits_every_leaf_once() -> None:
    corpus = _build_sample_corpus()
    seen = list(walk_leaves(corpus))
    # 2 packages × 2 files × 2 segments = 8 leaves
    assert len(seen) == 8
    assert len({u.id for u in seen}) == 8
    assert all(u.kind is UnitKind.LEAF for u in seen)


def test_walk_leaves_preserves_adapter_order() -> None:
    corpus = _build_sample_corpus()
    ids = [u.id for u in walk_leaves(corpus)]
    assert ids[0] == "pkg1/a.go#Func1"
    assert ids[-1] == "pkg2/b.go#Func2"


def test_walk_by_level_counts() -> None:
    corpus = _build_sample_corpus()
    by_level = walk_by_level(corpus)
    assert len(by_level[3]) == 1  # root
    assert len(by_level[2]) == 2  # packages
    assert len(by_level[1]) == 4  # files
    assert len(by_level[0]) == 8  # segments


def test_corpus_unit_is_frozen() -> None:
    unit = CorpusUnit(id="x", kind=UnitKind.LEAF, level=0, label="x")
    # Frozen dataclasses raise FrozenInstanceError on attribute writes.
    with pytest.raises((AttributeError, TypeError)):
        unit.label = "mutation attempt"  # type: ignore[misc]


def test_leaf_content_raises_on_non_leaf() -> None:
    corpus = _build_sample_corpus()
    with pytest.raises(ValueError):
        corpus.leaf_content(corpus.root())


def test_summary_tree_add_and_retrieve() -> None:
    tree = SummaryTree(corpus_id="c", corpus_type="code", strategy="hierarchical")
    leaf = SummaryNode(
        id="node-leaf",
        corpus_id="c",
        unit_id="pkg1/a.go#Func1",
        level=0,
        parent_id="pkg1/a.go",
        summary_text="does work",
        source_tokens=100,
        summary_tokens=20,
    )
    tree.add(leaf)
    assert tree.get("pkg1/a.go#Func1") is leaf
    assert tree.at_level(0) == [leaf]


def test_summary_tree_root_returns_highest_level_without_parent() -> None:
    tree = SummaryTree(corpus_id="c", corpus_type="code", strategy="hierarchical")
    leaf = SummaryNode(
        id="leaf-id",
        corpus_id="c",
        unit_id="leaf",
        level=0,
        parent_id="file",
    )
    root = SummaryNode(
        id="root-id",
        corpus_id="c",
        unit_id="repo",
        level=3,
        parent_id=None,
    )
    tree.add(leaf)
    tree.add(root)
    assert tree.root() is root


def test_summary_tree_children_of_and_top_children() -> None:
    tree = SummaryTree(corpus_id="c", corpus_type="code", strategy="hierarchical")
    parent = SummaryNode(
        id="pid",
        corpus_id="c",
        unit_id="pkg1",
        level=2,
        parent_id="repo",
        child_ids=["pkg1/a.go", "pkg1/b.go"],
    )
    child_a = SummaryNode(id="a", corpus_id="c", unit_id="pkg1/a.go", level=1, parent_id="pkg1")
    child_b = SummaryNode(id="b", corpus_id="c", unit_id="pkg1/b.go", level=1, parent_id="pkg1")
    tree.add(parent)
    tree.add(child_a)
    tree.add(child_b)

    assert [n.unit_id for n in tree.children_of("pkg1")] == ["pkg1/a.go", "pkg1/b.go"]
    assert len(tree.top_children("pkg1", 1)) == 1


def test_summary_tree_stats_is_serializable() -> None:
    tree = SummaryTree(corpus_id="c", corpus_type="code", strategy="hierarchical")
    tree.add(SummaryNode(id="n0", corpus_id="c", unit_id="u0", level=0, parent_id="u1"))
    tree.add(SummaryNode(id="n1", corpus_id="c", unit_id="u1", level=1, parent_id=None))
    stats = tree.stats()
    assert stats["nodes"] == 2
    assert stats["level_0"] == 1
    assert stats["level_1"] == 1
