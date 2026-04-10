# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""gRPC servicer for the KnowledgeService."""

from __future__ import annotations

import json
import os

import grpc
import structlog
from common.v1 import types_pb2
from knowledge.v1 import knowledge_pb2, knowledge_pb2_grpc

from workers.common.embedding.provider import EmbeddingProvider
from workers.common.grpc_metadata import resolve_model_override
from workers.common.llm.provider import LLMProvider, SnapshotTooLargeError
from workers.comprehension.adapters.code import CodeCorpus
from workers.comprehension.hierarchical import HierarchicalConfig, HierarchicalStrategy
from workers.comprehension.long_context import (
    LongContextConfig,
    LongContextDirectStrategy,
)
from workers.comprehension.renderers import CliffNotesRenderer
from workers.comprehension.selector import SelectionResult, StrategySelector
from workers.comprehension.single_shot import SingleShotConfig, SingleShotStrategy
from workers.comprehension.strategy import ComprehensionStrategy
from workers.knowledge.code_tour import generate_code_tour
from workers.knowledge.explain_system import explain_system
from workers.knowledge.learning_path import generate_learning_path
from workers.knowledge.retrieval import (
    build_overview_query,
    retrieve_relevant_snapshot,
)
from workers.knowledge.snapshot_truncate import condense_snapshot
from workers.knowledge.types import CliffNotesResult
from workers.knowledge.workflow_story import generate_workflow_story
from workers.reasoning.types import LLMUsageRecord

log = structlog.get_logger()


# SOURCEBRIDGE_CLIFF_NOTES_STRATEGY is a comma-separated preference chain
# the StrategySelector walks in order. Each entry is a strategy name:
#
#   - "hierarchical"       : Phase 3 bottom-up tree — works on any model
#   - "long_context_direct": Single call with the full snapshot; skipped
#                            when the snapshot doesn't fit the model's
#                            effective context window
#   - "single_shot"        : Legacy single-call path (also serves as the
#                            default-safe fallback)
#
# Default chain: "hierarchical,single_shot" — tries the new path first,
# falls back to legacy if hierarchical is unavailable for the current
# model. Operators can reorder, add, or remove entries to suit their
# deployment. When the variable is unset or empty, the default applies.
CLIFF_NOTES_STRATEGY_ENV = "SOURCEBRIDGE_CLIFF_NOTES_STRATEGY"
DEFAULT_CLIFF_NOTES_CHAIN: list[str] = ["hierarchical", "single_shot"]


def _cliff_notes_preference_chain() -> list[str]:
    """Parse the env var into a list of strategy names.

    Single-name values (e.g. ``"hierarchical"``) are still supported —
    they're treated as a one-entry chain. This keeps operators who
    already set the env var to a single strategy working unchanged.
    """
    raw = (os.environ.get(CLIFF_NOTES_STRATEGY_ENV) or "").strip()
    if not raw:
        return list(DEFAULT_CLIFF_NOTES_CHAIN)
    names: list[str] = []
    for part in raw.split(","):
        name = part.strip().lower()
        if name:
            names.append(name)
    return names or list(DEFAULT_CLIFF_NOTES_CHAIN)


# Back-compat alias used by tests that predate the chain format.
def _selected_cliff_notes_strategy() -> str:
    """Return the first entry in the preference chain — legacy shim.

    Tests that relied on the old env-var semantics call this to get a
    single strategy name. New code should call
    ``_cliff_notes_preference_chain`` and drive the selector.
    """
    chain = _cliff_notes_preference_chain()
    return chain[0] if chain else "single_shot"


def _llm_usage_proto(usage_record) -> types_pb2.LLMUsage:
    """Convert an LLMUsageRecord to a proto LLMUsage message."""
    return types_pb2.LLMUsage(
        model=usage_record.model,
        input_tokens=usage_record.input_tokens,
        output_tokens=usage_record.output_tokens,
        operation=usage_record.operation,
    )


class KnowledgeServicer(knowledge_pb2_grpc.KnowledgeServiceServicer):
    """Implements the KnowledgeService gRPC service."""

    def __init__(
        self,
        llm_provider: LLMProvider,
        embedding_provider: EmbeddingProvider | None = None,
        *,
        default_model_id: str = "",
    ) -> None:
        self._llm = llm_provider
        self._embedding = embedding_provider
        # default_model_id is the best-effort identifier of the model
        # the LLM provider is configured with. The selector uses it to
        # look up the model's capability profile when no per-call
        # override is provided via gRPC metadata. Operators set this
        # from cfg.llm.knowledge_model when constructing the servicer.
        self._default_model_id = (default_model_id or "").strip()
        self._selector = StrategySelector()

    async def _prepare_snapshot(
        self,
        snapshot_json: str,
        query: str,
        scope_type: str = "repository",
    ) -> str:
        """Select the best context-building strategy for the snapshot.

        If an embedding provider is available and the snapshot is large,
        uses retrieval to build a focused snapshot.  Otherwise falls back
        to the condensation strategy (progressive stripping).
        """
        # Small snapshots don't need any reduction
        if len(snapshot_json) < 300_000:
            return snapshot_json

        # Try retrieval first (best quality)
        if self._embedding is not None and query:
            try:
                return await retrieve_relevant_snapshot(
                    snapshot_json,
                    query,
                    self._embedding,
                )
            except Exception as exc:
                log.warn("retrieval_failed_falling_back", error=str(exc))

        # Fall back to condensation
        return condense_snapshot(snapshot_json, scope_type=scope_type)

    def _resolve_model_id(self, override: str | None) -> str:
        """Pick the best model id for capability lookup.

        Prefers the per-call override supplied via gRPC metadata, falls
        back to the servicer's configured default, and finally to a
        generic label so the selector always has something to look up.
        """
        if override:
            return override.strip()
        if self._default_model_id:
            return self._default_model_id
        return "unknown"

    def _build_cliff_notes_strategies(
        self,
        *,
        request: knowledge_pb2.GenerateCliffNotesRequest,
        audience: str,
        depth: str,
        scope_type: str,
        model_override: str | None,
        snapshot_json: str,
    ) -> dict[str, ComprehensionStrategy]:
        """Instantiate every cliff-notes strategy with per-call context.

        Each strategy is constructed eagerly so the selector can inspect
        their capability requirements without running them. The actual
        LLM work only happens inside ``build_tree``.
        """
        repo_name = request.repository_name
        return {
            "hierarchical": HierarchicalStrategy(
                provider=self._llm,
                config=HierarchicalConfig.from_env(repository_name=repo_name),
            ),
            "long_context_direct": LongContextDirectStrategy(
                provider=self._llm,
                config=LongContextConfig.from_env(
                    repository_name=repo_name,
                    audience=audience,
                    depth=depth,
                    scope_type=scope_type,
                    scope_path=request.scope_path,
                    snapshot_json=snapshot_json,
                    model_override=model_override,
                ),
            ),
            "single_shot": SingleShotStrategy(
                provider=self._llm,
                config=SingleShotConfig(
                    repository_name=repo_name,
                    audience=audience,
                    depth=depth,
                    scope_type=scope_type,
                    scope_path=request.scope_path,
                    snapshot_json=snapshot_json,
                    model_override=model_override,
                ),
            ),
        }

    async def _run_cliff_notes_strategy_chain(
        self,
        *,
        request: knowledge_pb2.GenerateCliffNotesRequest,
        audience: str,
        depth: str,
        scope_type: str,
        model_override: str | None,
    ) -> tuple[CliffNotesResult, LLMUsageRecord, SelectionResult]:
        """Walk the preference chain and run the first viable strategy.

        If a strategy passes capability gating but then raises
        :class:`SnapshotTooLargeError` at runtime (the common failure
        mode for ``long_context_direct`` on a corpus that declared a
        fit but didn't actually fit), the exception is recorded and
        the chain advances to the next entry. Other exceptions
        propagate.

        Returns the final result, usage record, and the selector's
        trace so the caller can log why a particular strategy was used.
        """
        # Condense once up-front and share the same snapshot across all
        # strategies in the chain. The hierarchical path still walks
        # the CodeCorpus built from this JSON, so the retrieval /
        # condensation step from the legacy path is preserved.
        query = build_overview_query(
            request.repository_name,
            "cliff_notes",
            scope_type=scope_type,
            scope_path=request.scope_path,
        )
        snapshot = await self._prepare_snapshot(
            request.snapshot_json, query, scope_type=scope_type,
        )

        chain = _cliff_notes_preference_chain()
        model_id = self._resolve_model_id(model_override)
        strategies = self._build_cliff_notes_strategies(
            request=request,
            audience=audience,
            depth=depth,
            scope_type=scope_type,
            model_override=model_override,
            snapshot_json=snapshot,
        )

        last_error: Exception | None = None
        tried: list[str] = []

        # Walk the chain manually so runtime failures (e.g. the long
        # context guard trips on a snapshot that didn't fit after all)
        # can skip to the next viable entry. The selector runs once per
        # iteration so the trace reflects the actual path taken.
        remaining_chain = list(chain)
        while remaining_chain:
            selection = self._selector.select(
                strategies=strategies,
                preference_chain=remaining_chain,
                model_id=model_id,
            )
            if selection.strategy is None:
                if last_error is not None:
                    raise last_error
                raise RuntimeError(
                    f"no viable strategy for model {model_id}: "
                    f"{selection.trace.summary()}"
                )

            name = selection.strategy_name
            tried.append(name)
            try:
                result, usage = await self._run_one_cliff_notes_strategy(
                    strategy=selection.strategy,
                    strategy_name=name,
                    request=request,
                    audience=audience,
                    depth=depth,
                    scope_type=scope_type,
                    model_override=model_override,
                    snapshot_json=snapshot,
                )
                return result, usage, selection
            except SnapshotTooLargeError as exc:
                log.warning(
                    "cliff_notes_strategy_runtime_skip",
                    strategy=name,
                    reason=f"snapshot too large: {exc}",
                )
                last_error = exc
                # Drop this strategy from the chain and retry with the
                # next one.
                remaining_chain = [n for n in remaining_chain if n != name]
                continue

        # Chain exhausted without success — re-raise the last error we
        # saw so the caller can translate it into a gRPC status.
        if last_error is not None:
            raise last_error
        raise RuntimeError(f"no strategies succeeded; tried: {','.join(tried)}")

    async def _run_one_cliff_notes_strategy(
        self,
        *,
        strategy: ComprehensionStrategy,
        strategy_name: str,
        request: knowledge_pb2.GenerateCliffNotesRequest,
        audience: str,
        depth: str,
        scope_type: str,
        model_override: str | None,
        snapshot_json: str,
    ) -> tuple[CliffNotesResult, LLMUsageRecord]:
        """Actually run a single strategy and produce the final cliff
        notes result. Kept separate from the chain walker so the logic
        is easy to unit-test."""
        if strategy_name == "hierarchical":
            # Hierarchical: build tree from the CodeCorpus, then render.
            return await self._generate_cliff_notes_hierarchical(
                request=request,
                audience=audience,
                depth=depth,
                scope_type=scope_type,
                model_override=model_override,
                snapshot_json=snapshot_json,
            )

        # Single-shot and long-context strategies both produce the
        # final CliffNotesResult directly inside build_tree; they
        # expose it via last_result / last_usage for the caller.
        corpus = CodeCorpus(snapshot=json.loads(snapshot_json))
        await strategy.build_tree(corpus)
        result = getattr(strategy, "last_result", None)
        usage = getattr(strategy, "last_usage", None)
        if result is None or usage is None:
            raise RuntimeError(
                f"strategy {strategy_name!r} did not populate last_result/last_usage",
            )
        return result, usage

    async def _generate_cliff_notes_hierarchical(
        self,
        *,
        request: knowledge_pb2.GenerateCliffNotesRequest,
        audience: str,
        depth: str,
        scope_type: str,
        model_override: str | None,
        snapshot_json: str | None = None,
    ) -> tuple[CliffNotesResult, LLMUsageRecord]:
        """Run the Phase 3 hierarchical pipeline for cliff notes.

        Steps:
          1. Parse the snapshot JSON into a dict (accepts a pre-condensed
             ``snapshot_json`` from the chain walker, or falls back to
             the raw request payload for direct callers).
          2. Wrap it in a CodeCorpus adapter.
          3. Build a SummaryTree with HierarchicalStrategy — each LLM
             call sees only one segment / one file's children / one
             package's children / one repo's children, so the prompt
             always fits even small-context models.
          4. Render the final structured cliff notes from the tree
             via CliffNotesRenderer.
        """
        raw_snapshot = snapshot_json if snapshot_json is not None else request.snapshot_json
        try:
            snapshot_dict = json.loads(raw_snapshot)
        except json.JSONDecodeError as exc:
            raise ValueError(f"snapshot_json is not valid JSON: {exc}") from exc

        corpus = CodeCorpus(snapshot=snapshot_dict)
        strategy = HierarchicalStrategy(
            provider=self._llm,
            config=HierarchicalConfig.from_env(repository_name=request.repository_name or corpus.root().label),
        )

        log.info(
            "cliff_notes_hierarchical_started",
            repository_id=request.repository_id,
            scope_type=scope_type,
            scope_path=request.scope_path,
        )

        tree = await strategy.build_tree(corpus)

        log.info(
            "cliff_notes_hierarchical_tree_built",
            repository_id=request.repository_id,
            stats=tree.stats(),
        )

        # Extract pre-analysis from enriched snapshot (deep mode injects
        # repository-level cliff notes as _pre_analysis)
        pre_analysis = snapshot_dict.get("_pre_analysis") if isinstance(snapshot_dict, dict) else None

        renderer = CliffNotesRenderer(
            provider=self._llm,
            model_override=model_override,
        )
        result, usage = await renderer.render(
            tree,
            repository_name=request.repository_name,
            audience=audience,
            depth=depth,
            scope_type=scope_type,
            scope_path=request.scope_path,
            pre_analysis=pre_analysis,
        )

        log.info(
            "cliff_notes_hierarchical_completed",
            repository_id=request.repository_id,
            sections=len(result.sections),
            input_tokens=usage.input_tokens,
            output_tokens=usage.output_tokens,
        )
        return result, usage

    async def GenerateCliffNotes(  # noqa: N802
        self,
        request: knowledge_pb2.GenerateCliffNotesRequest,
        context: grpc.aio.ServicerContext,
    ) -> knowledge_pb2.GenerateCliffNotesResponse:
        """Generate cliff notes for a repository from its assembled snapshot."""
        log.info(
            "generate_cliff_notes",
            repository_id=request.repository_id,
            audience=request.audience,
            depth=request.depth,
            scope_type=request.scope_type or "repository",
            scope_path=request.scope_path,
        )

        if not request.snapshot_json:
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                "snapshot_json is required",
            )
            return  # type: ignore[return-value]  # abort raises but mypy doesn't know

        audience = request.audience or "developer"
        depth = request.depth or "medium"
        scope_type = request.scope_type or "repository"
        model_override = resolve_model_override(context)

        # Run through the StrategySelector with the preference chain from
        # the environment. The selector handles capability gating and
        # records a trace that we emit on every generation for operator
        # visibility.
        try:
            result, usage, selection = await self._run_cliff_notes_strategy_chain(
                request=request,
                audience=audience,
                depth=depth,
                scope_type=scope_type,
                model_override=model_override,
            )
        except Exception as exc:
            log.error(
                "generate_cliff_notes_failed",
                error=str(exc),
            )
            await context.abort(
                grpc.StatusCode.INTERNAL,
                f"Cliff notes generation failed: {exc}",
            )

        log.info(
            "cliff_notes_strategy_selection",
            strategy=selection.strategy_name,
            trace=selection.trace.summary(),
        )

        sections = []
        for sec in result.sections:
            evidence = []
            for ev in sec.evidence:
                evidence.append(
                    knowledge_pb2.KnowledgeEvidence(
                        source_type=ev.source_type,
                        source_id=ev.source_id,
                        file_path=ev.file_path,
                        line_start=ev.line_start,
                        line_end=ev.line_end,
                        rationale=ev.rationale,
                    )
                )
            sections.append(
                knowledge_pb2.KnowledgeSection(
                    title=sec.title,
                    content=sec.content,
                    summary=sec.summary,
                    confidence=sec.confidence,
                    inferred=sec.inferred,
                    evidence=evidence,
                )
            )

        return knowledge_pb2.GenerateCliffNotesResponse(
            sections=sections,
            usage=_llm_usage_proto(usage),
        )

    async def GenerateLearningPath(  # noqa: N802
        self,
        request: knowledge_pb2.GenerateLearningPathRequest,
        context: grpc.aio.ServicerContext,
    ) -> knowledge_pb2.GenerateLearningPathResponse:
        """Generate a learning path for a repository from its assembled snapshot."""
        log.info(
            "generate_learning_path",
            repository_id=request.repository_id,
            audience=request.audience,
            depth=request.depth,
        )

        if not request.snapshot_json:
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                "snapshot_json is required",
            )
            return  # type: ignore[return-value]  # abort raises but mypy doesn't know

        audience = request.audience or "developer"
        depth = request.depth or "medium"
        query = build_overview_query(request.repository_name, "learning_path")
        if request.focus_area:
            query = f"{request.focus_area} {query}"
        snapshot = await self._prepare_snapshot(request.snapshot_json, query)

        model_override = resolve_model_override(context)
        try:
            result, usage = await generate_learning_path(
                provider=self._llm,
                repository_name=request.repository_name,
                audience=audience,
                depth=depth,
                snapshot_json=snapshot,
                focus_area=request.focus_area,
                model_override=model_override,
            )
        except Exception as exc:
            log.error("generate_learning_path_failed", error=str(exc))
            await context.abort(
                grpc.StatusCode.INTERNAL,
                f"Learning path generation failed: {exc}",
            )

        steps = []
        for step in result.steps:
            steps.append(
                knowledge_pb2.LearningStep(
                    order=step.order,
                    title=step.title,
                    objective=step.objective,
                    content=step.content,
                    file_paths=step.file_paths,
                    symbol_ids=step.symbol_ids,
                    estimated_time=step.estimated_time,
                )
            )

        return knowledge_pb2.GenerateLearningPathResponse(
            steps=steps,
            usage=_llm_usage_proto(usage),
        )

    async def GenerateWorkflowStory(  # noqa: N802
        self,
        request: knowledge_pb2.GenerateWorkflowStoryRequest,
        context: grpc.aio.ServicerContext,
    ) -> knowledge_pb2.GenerateWorkflowStoryResponse:
        """Generate a grounded workflow story for a repository scope."""
        log.info(
            "generate_workflow_story",
            repository_id=request.repository_id,
            audience=request.audience,
            depth=request.depth,
            scope_type=request.scope_type or "repository",
            scope_path=request.scope_path,
        )

        if not request.snapshot_json:
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                "snapshot_json is required",
            )
            return  # type: ignore[return-value]  # abort raises but mypy doesn't know

        audience = request.audience or "developer"
        depth = request.depth or "medium"
        scope_type = request.scope_type or "repository"
        query = build_overview_query(request.repository_name, "workflow_story")
        if request.anchor_label:
            query = f"{request.anchor_label} {query}"
        snapshot = await self._prepare_snapshot(request.snapshot_json, query, scope_type=scope_type)

        model_override = resolve_model_override(context)
        try:
            result, usage = await generate_workflow_story(
                provider=self._llm,
                repository_name=request.repository_name,
                audience=audience,
                depth=depth,
                scope_type=scope_type,
                scope_path=request.scope_path,
                anchor_label=request.anchor_label,
                execution_path_json=request.execution_path_json,
                model_override=model_override,
                snapshot_json=snapshot,
            )
        except Exception as exc:
            import traceback
            log.error("generate_workflow_story_failed", error=str(exc), traceback=traceback.format_exc())
            await context.abort(
                grpc.StatusCode.INTERNAL,
                f"Workflow story generation failed: {exc}",
            )

        sections = []
        for sec in result.sections:
            evidence = []
            for ev in sec.evidence:
                evidence.append(
                    knowledge_pb2.KnowledgeEvidence(
                        source_type=ev.source_type,
                        source_id=ev.source_id,
                        file_path=ev.file_path,
                        line_start=ev.line_start,
                        line_end=ev.line_end,
                        rationale=ev.rationale,
                    )
                )
            sections.append(
                knowledge_pb2.KnowledgeSection(
                    title=sec.title,
                    content=sec.content,
                    summary=sec.summary,
                    confidence=sec.confidence,
                    inferred=sec.inferred,
                    evidence=evidence,
                )
            )

        return knowledge_pb2.GenerateWorkflowStoryResponse(
            sections=sections,
            usage=_llm_usage_proto(usage),
        )

    async def ExplainSystem(  # noqa: N802
        self,
        request: knowledge_pb2.ExplainSystemRequest,
        context: grpc.aio.ServicerContext,
    ) -> knowledge_pb2.ExplainSystemResponse:
        """Generate a transient whole-system explanation."""
        log.info(
            "explain_system",
            repository_id=request.repository_id,
            audience=request.audience,
        )

        if not request.snapshot_json:
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                "snapshot_json is required",
            )
            return  # type: ignore[return-value]  # abort raises but mypy doesn't know

        audience = request.audience or "developer"
        depth = request.depth or "medium"
        # For Q&A, use the actual question for retrieval
        query = request.question or build_overview_query(
            request.repository_name, "explain"
        )
        snapshot = await self._prepare_snapshot(request.snapshot_json, query)

        model_override = resolve_model_override(context)
        try:
            result, usage = await explain_system(
                provider=self._llm,
                repository_name=request.repository_name,
                audience=audience,
                depth=depth,
                question=request.question,
                snapshot_json=snapshot,
                model_override=model_override,
            )
        except Exception as exc:
            log.error("explain_system_failed", error=str(exc))
            await context.abort(
                grpc.StatusCode.INTERNAL,
                f"System explanation failed: {exc}",
            )

        return knowledge_pb2.ExplainSystemResponse(
            explanation=result.explanation,
            evidence=[],
            usage=_llm_usage_proto(usage),
        )

    async def GenerateCodeTour(  # noqa: N802
        self,
        request: knowledge_pb2.GenerateCodeTourRequest,
        context: grpc.aio.ServicerContext,
    ) -> knowledge_pb2.GenerateCodeTourResponse:
        """Generate a code tour for a repository from its assembled snapshot."""
        log.info(
            "generate_code_tour",
            repository_id=request.repository_id,
            audience=request.audience,
            depth=request.depth,
        )

        if not request.snapshot_json:
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                "snapshot_json is required",
            )
            return  # type: ignore[return-value]  # abort raises but mypy doesn't know

        audience = request.audience or "developer"
        depth = request.depth or "medium"
        query = build_overview_query(request.repository_name, "code_tour")
        if request.theme:
            query = f"{request.theme} {query}"
        snapshot = await self._prepare_snapshot(request.snapshot_json, query)

        model_override = resolve_model_override(context)
        try:
            result, usage = await generate_code_tour(
                provider=self._llm,
                repository_name=request.repository_name,
                audience=audience,
                depth=depth,
                snapshot_json=snapshot,
                theme=request.theme,
                model_override=model_override,
            )
        except Exception as exc:
            log.error("generate_code_tour_failed", error=str(exc))
            await context.abort(
                grpc.StatusCode.INTERNAL,
                f"Code tour generation failed: {exc}",
            )

        stops = []
        for stop in result.stops:
            stops.append(
                knowledge_pb2.CodeTourStop(
                    order=stop.order,
                    title=stop.title,
                    description=stop.description,
                    file_path=stop.file_path,
                    line_start=stop.line_start,
                    line_end=stop.line_end,
                )
            )

        return knowledge_pb2.GenerateCodeTourResponse(
            stops=stops,
            usage=_llm_usage_proto(usage),
        )
