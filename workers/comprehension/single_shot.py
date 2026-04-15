# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""SingleShotStrategy — the legacy "one prompt, one call" path.

Wraps the existing ``workers.knowledge.cliff_notes.generate_cliff_notes``
function as a first-class :class:`ComprehensionStrategy`. This keeps the
pre-Phase 3 behavior available for rollback and for small corpora where
hierarchical overhead is wasteful.

SingleShotStrategy does NOT build a real summary tree — it's a terminal
renderer-style strategy that produces the final artifact in one LLM
call. The StrategySelector still treats it as a peer of the hierarchical
strategy so the preference chain can fall back to it when hierarchical
is overkill or temporarily disabled.

The "tree" it returns is therefore a single-node wrapper around the
final cliff notes result. Phase 4 renderers that consume the tree
output of hierarchical can co-exist with this strategy because they
look at :class:`SummaryNode` metadata rather than recursing into
children; see ``renderers.py`` for the contract.
"""

from __future__ import annotations

import json
import uuid
from dataclasses import dataclass

import structlog

from workers.common.llm.provider import LLMProvider
from workers.comprehension.corpus import CorpusSource
from workers.comprehension.strategy import (
    CapabilityRequirements,
    ComprehensionStrategy,
    ProgressCallback,
    _noop_progress,
)
from workers.comprehension.tree import SummaryNode, SummaryTree
from workers.knowledge.cliff_notes import generate_cliff_notes
from workers.knowledge.types import CliffNotesResult
from workers.reasoning.types import LLMUsageRecord

log = structlog.get_logger()


@dataclass
class SingleShotConfig:
    """Runtime knobs for the legacy single-shot strategy."""

    repository_name: str = ""
    audience: str = "developer"
    depth: str = "medium"
    scope_type: str = "repository"
    scope_path: str = ""
    snapshot_json: str = ""
    model_override: str | None = None


class SingleShotStrategy(ComprehensionStrategy):
    """The legacy single-shot cliff notes path, wrapped as a strategy.

    Because this strategy skips the hierarchical tree and calls the
    renderer in a single LLM call, it also *acts* as its own renderer.
    The ``last_result`` attribute exposes the CliffNotesResult + usage
    from the most recent ``build_tree`` invocation so the servicer can
    persist them without a second pass.
    """

    name: str = "single_shot"

    def __init__(
        self,
        provider: LLMProvider,
        config: SingleShotConfig,
    ) -> None:
        self._provider = provider
        self._config = config
        self.last_result: CliffNotesResult | None = None
        self.last_usage: LLMUsageRecord | None = None

    def capability_requirements(self) -> CapabilityRequirements:
        # The legacy path works with any model; the only hard
        # requirement is that the full snapshot fits the context.
        # That check is enforced by the existing check_prompt_budget
        # inside generate_cliff_notes — so at the capability layer we
        # only set a modest floor.
        return CapabilityRequirements(
            min_context_tokens=4096,
            min_instruction_following="low",
        )

    async def build_tree(
        self,
        corpus: CorpusSource,
        *,
        progress: ProgressCallback = _noop_progress,
    ) -> SummaryTree:
        """Run the legacy path and package the result as a one-node tree.

        The corpus argument is accepted for protocol conformance but is
        not walked — single-shot consumes the raw snapshot JSON directly
        because the legacy prompt expects that shape.
        """
        from workers.comprehension.hierarchical import _maybe_await

        await _maybe_await(progress("llm", 0.1, "Running single-shot cliff notes call"))

        cfg = self._config
        result, usage = await generate_cliff_notes(
            provider=self._provider,
            repository_name=cfg.repository_name,
            audience=cfg.audience,
            depth=cfg.depth,
            scope_type=cfg.scope_type,
            scope_path=cfg.scope_path,
            snapshot_json=cfg.snapshot_json,
            model_override=cfg.model_override,
        )
        self.last_result = result
        self.last_usage = usage

        await _maybe_await(progress("ready", 1.0, "Single-shot result stored"))

        # Package the result as a minimal tree so downstream code that
        # expects a SummaryTree keeps working. The node carries a JSON
        # payload of the sections so a renderer can recover them if
        # needed; the servicer prefers reading last_result directly.
        tree = SummaryTree(
            corpus_id=corpus.corpus_id,
            corpus_type=corpus.corpus_type,
            strategy=self.name,
            revision_fp=corpus.revision_fingerprint(),
        )
        tree.add(
            SummaryNode(
                id=str(uuid.uuid4()),
                corpus_id=corpus.corpus_id,
                unit_id=corpus.root().id,
                level=3,
                parent_id=None,
                summary_text=_result_as_text(result),
                headline=result.sections[0].title if result.sections else "Single-shot result",
                summary_tokens=usage.output_tokens if usage else 0,
                source_tokens=len(cfg.snapshot_json) // 4,
                model_used=usage.model if usage else "",
                strategy=self.name,
                revision_fp=corpus.revision_fingerprint(),
                metadata={
                    "single_shot_payload": json.dumps(
                        [
                            {
                                "title": s.title,
                                "content": s.content,
                                "summary": s.summary,
                                "confidence": s.confidence,
                                "inferred": s.inferred,
                            }
                            for s in result.sections
                        ]
                    ),
                },
            )
        )
        return tree


def _result_as_text(result: CliffNotesResult) -> str:
    """Flatten a CliffNotesResult into a readable markdown blob.

    Used as the root node's summary_text so anything that iterates the
    tree (e.g. a future persistence layer) gets a meaningful string
    rather than an empty placeholder.
    """
    if result is None or not result.sections:
        return ""
    parts: list[str] = []
    for sec in result.sections:
        parts.append(f"## {sec.title}\n\n{sec.content.strip()}")
    return "\n\n".join(parts)
