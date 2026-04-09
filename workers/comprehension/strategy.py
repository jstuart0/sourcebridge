# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Comprehension strategy protocol.

Every comprehension implementation in this package satisfies the
:class:`ComprehensionStrategy` protocol. The protocol is deliberately
narrow so a new strategy (RAPTOR, GraphRAG, LongContextDirect) can be
added without touching any renderer or servicer code.
"""

from __future__ import annotations

from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from typing import Protocol, runtime_checkable

from workers.comprehension.corpus import CorpusSource
from workers.comprehension.tree import SummaryTree

# ProgressCallback is the signature the servicer passes into
# build_tree. The values line up with llm.Runtime on the Go side —
# phase name, 0.0-1.0 progress, and a human-readable message — so
# forwarding events through the orchestrator is a straight passthrough
# when we wire streaming progress in Phase 5.
ProgressCallback = Callable[[str, float, str], Awaitable[None] | None]


async def _noop_progress(phase: str, progress: float, message: str) -> None:
    """Default progress callback that discards every event.

    Passed through when callers don't care about progress (e.g. tests).
    """
    return None


@dataclass
class CapabilityRequirements:
    """Minimum model capability to run a strategy.

    Phase 4 will wire this against a model capability registry so the
    settings UI can warn when a selected model is too weak. In Phase 3
    we only need the shape so the hierarchical strategy can declare
    ``min_context_tokens=2048`` — the floor that lets it run on any
    supported model including small Ollama variants.
    """

    min_context_tokens: int = 2048
    min_instruction_following: str = "low"  # "low" | "medium" | "high"
    requires_json_mode: bool = False
    requires_tool_use: bool = False


@runtime_checkable
class ComprehensionStrategy(Protocol):
    """Produces a :class:`SummaryTree` from a :class:`CorpusSource`.

    Phase 3 strategies only implement ``build_tree``. Renderers are
    separate objects that consume a tree — this keeps the rendering
    concern (what prompt to build for a cliff note vs. a learning path)
    isolated from the comprehension concern (how to summarize a corpus
    that's too big for one LLM call).
    """

    name: str

    def capability_requirements(self) -> CapabilityRequirements:
        """Return the minimum model capability for this strategy."""

    async def build_tree(
        self,
        corpus: CorpusSource,
        *,
        progress: ProgressCallback = _noop_progress,
    ) -> SummaryTree:
        """Build a summary tree for the corpus.

        Implementations must be idempotent — calling ``build_tree``
        twice on the same corpus with the same revision fingerprint
        should produce equivalent trees. This lets later phases add
        DB-backed caching without changing strategy code.
        """
