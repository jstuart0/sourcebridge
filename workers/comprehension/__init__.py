# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Corpus-agnostic comprehension engine.

This package implements the Phase 3+ comprehension engine that produces
whole-corpus artifacts (cliff notes, learning paths, code tours, workflow
stories, and any future text-corpus artifact) from source material that
may be much larger than the model's context window.

Design principles — see thoughts/shared/plans/2026-04-09-comprehension-engine-and-llm-orchestration.md:

  1. Global sensemaking, not selective retrieval. Whole-corpus artifacts
     cannot be produced by stuffing top-k relevant chunks into one call;
     every piece of source must contribute to the final output.

  2. Corpus-agnostic from day one. CorpusSource is the abstraction; the
     CodeCorpus adapter wraps the existing knowledge snapshot today, and
     future adapters (RequirementsCorpus, DocumentCorpus) plug into the
     same engine without touching the strategy implementations.

  3. Strategies are peers, not primary + followups. HierarchicalStrategy
     ships first because it works on every model (including small local
     Ollama), but the package is structured so RAPTORStrategy / GraphRAG /
     LongContextDirect land as sibling implementations.

The public surface of this package is deliberately small:

    - CorpusSource / CorpusUnit                (corpus.py)
    - SummaryTree / SummaryNode                (tree.py)
    - ComprehensionStrategy                    (strategy.py)
    - HierarchicalStrategy                     (hierarchical.py)
    - CodeCorpus                               (adapters/code.py)
    - CliffNotesRenderer                       (renderers.py)

Everything else is implementation detail.
"""

from workers.comprehension.corpus import CorpusSource, CorpusUnit, UnitKind
from workers.comprehension.strategy import ComprehensionStrategy
from workers.comprehension.tree import SummaryNode, SummaryTree

__all__ = [
    "CorpusSource",
    "CorpusUnit",
    "UnitKind",
    "ComprehensionStrategy",
    "SummaryNode",
    "SummaryTree",
]
