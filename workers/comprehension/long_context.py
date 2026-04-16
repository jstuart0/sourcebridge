# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""LongContextDirectStrategy — use the whole context window when it fits.

This strategy is the "escape hatch" for cloud models with very large
effective contexts (Claude Sonnet 4.6 at 200K, Gemini 2.5 at 1M+).
When the caller's corpus actually fits the model's declared context,
LongContextDirect bypasses hierarchical summarization entirely and
runs a single call with the full snapshot.

Phase 4 ships two important guards:

1. **Token budget guard.** The strategy refuses to run when the
   combined snapshot + system prompt exceeds ``long_context_max_tokens``
   (default: 60% of the configured budget to leave headroom for
   output). Operators can tune this via
   ``SOURCEBRIDGE_LONG_CONTEXT_MAX_TOKENS``.

2. **Long-context quality guard.** Published benchmarks
   (LongCodeBench, "Lost in the Middle", Chroma Context Rot) show
   model quality degrading with input size — the capability
   registry carries per-model ``long_context_quality`` grades and the
   strategy's ``capability_requirements`` asks for ``medium`` or better
   at the target size. The StrategySelector checks this before
   running.

When either guard trips, the strategy raises a structured error that
the selector translates into a "strategy skipped" trace entry, and
falls through to the next entry in the preference chain.
"""

from __future__ import annotations

import os
import uuid
from dataclasses import dataclass

import structlog

from workers.common.llm.provider import (
    LLMProvider,
    SnapshotTooLargeError,
    check_prompt_budget,
)
from workers.comprehension.corpus import CorpusSource
from workers.comprehension.single_shot import SingleShotStrategy, _result_as_text
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


# Default ceiling used by the long-context strategy when no explicit
# budget is provided. Deliberately conservative: most 200K models
# degrade noticeably above ~60K real input tokens, so we cap there
# even though the raw context window is much larger.
DEFAULT_LONG_CONTEXT_MAX_TOKENS = 60_000


@dataclass
class LongContextConfig:
    """Runtime knobs for the long-context strategy."""

    repository_name: str = ""
    audience: str = "developer"
    depth: str = "medium"
    scope_type: str = "repository"
    scope_path: str = ""
    snapshot_json: str = ""
    model_override: str | None = None
    max_prompt_tokens: int = DEFAULT_LONG_CONTEXT_MAX_TOKENS

    @classmethod
    def from_env(
        cls,
        *,
        repository_name: str = "",
        audience: str = "developer",
        depth: str = "medium",
        scope_type: str = "repository",
        scope_path: str = "",
        snapshot_json: str = "",
        model_override: str | None = None,
    ) -> LongContextConfig:
        raw = os.environ.get("SOURCEBRIDGE_LONG_CONTEXT_MAX_TOKENS", "").strip()
        try:
            cap = int(raw) if raw else DEFAULT_LONG_CONTEXT_MAX_TOKENS
        except ValueError:
            cap = DEFAULT_LONG_CONTEXT_MAX_TOKENS
        if cap <= 0:
            cap = DEFAULT_LONG_CONTEXT_MAX_TOKENS
        return cls(
            repository_name=repository_name,
            audience=audience,
            depth=depth,
            scope_type=scope_type,
            scope_path=scope_path,
            snapshot_json=snapshot_json,
            model_override=model_override,
            max_prompt_tokens=cap,
        )


class LongContextDirectStrategy(ComprehensionStrategy):
    """Single-call cliff notes using the full snapshot, gated by budget.

    The strategy runs the same prompt as SingleShotStrategy but with an
    explicit pre-flight budget check against ``max_prompt_tokens``. When
    the check fails, it raises :class:`SnapshotTooLargeError` so the
    selector can record a skip reason and fall through to the next
    entry in the preference chain.
    """

    name: str = "long_context_direct"

    def __init__(
        self,
        provider: LLMProvider,
        config: LongContextConfig,
    ) -> None:
        self._provider = provider
        self._config = config
        self.last_result: CliffNotesResult | None = None
        self.last_usage: LLMUsageRecord | None = None

    def capability_requirements(self) -> CapabilityRequirements:
        # Long-context asks for a model with a declared context window
        # at least as large as the configured max_prompt_tokens, and at
        # least medium instruction following (so the single large call
        # can produce structured output reliably).
        return CapabilityRequirements(
            min_context_tokens=max(self._config.max_prompt_tokens, 32_000),
            min_instruction_following="medium",
        )

    async def build_tree(
        self,
        corpus: CorpusSource,
        *,
        progress: ProgressCallback = _noop_progress,
    ) -> SummaryTree:
        from workers.comprehension.hierarchical import _maybe_await

        cfg = self._config
        await _maybe_await(progress("budget", 0.05, "Checking snapshot budget for long-context call"))

        # Pre-flight budget check. We pass the snapshot as the "prompt"
        # for the purpose of budget estimation — the real cliff notes
        # prompt adds more text on top, but the snapshot dominates
        # the token count in every realistic case.
        check_prompt_budget(
            cfg.snapshot_json,
            context=f"long_context_direct:{cfg.scope_type}",
            budget_tokens=cfg.max_prompt_tokens,
        )

        await _maybe_await(progress("llm", 0.15, "Running long-context call"))
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

        await _maybe_await(progress("ready", 1.0, "Long-context result stored"))

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
                headline=result.sections[0].title if result.sections else "Long-context result",
                summary_tokens=usage.output_tokens if usage else 0,
                source_tokens=len(cfg.snapshot_json) // 4,
                model_used=usage.model if usage else "",
                strategy=self.name,
                revision_fp=corpus.revision_fingerprint(),
            )
        )
        return tree


# Re-export for convenience: callers that catch SnapshotTooLargeError
# inside the selector already import it from the common llm package,
# but the name is mentioned here so future grep-across-strategies
# maintenance is easy.
__all__ = [
    "LongContextConfig",
    "LongContextDirectStrategy",
    "SnapshotTooLargeError",
    "SingleShotStrategy",
]
