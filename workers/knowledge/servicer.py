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
from workers.common.llm.provider import LLMProvider
from workers.comprehension.adapters.code import CodeCorpus
from workers.comprehension.hierarchical import HierarchicalConfig, HierarchicalStrategy
from workers.comprehension.renderers import CliffNotesRenderer
from workers.knowledge.cliff_notes import generate_cliff_notes
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


# SOURCEBRIDGE_CLIFF_NOTES_STRATEGY selects which comprehension strategy
# runs for the cliff_notes RPC:
#
#   - "single_shot"  (default): the legacy pipeline — one LLM call with
#     the whole snapshot, condensed or retrieved if too large.
#   - "hierarchical": the Phase 3 bottom-up tree pipeline that works on
#     any model including small local Ollama variants.
#
# Operators flip this on thor once the hierarchical path is validated.
# Keeping single_shot as the default means zero behavior change for
# existing deployments until an operator explicitly opts in.
CLIFF_NOTES_STRATEGY_ENV = "SOURCEBRIDGE_CLIFF_NOTES_STRATEGY"


def _selected_cliff_notes_strategy() -> str:
    return (os.environ.get(CLIFF_NOTES_STRATEGY_ENV, "single_shot") or "single_shot").strip().lower()


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
    ) -> None:
        self._llm = llm_provider
        self._embedding = embedding_provider

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

    async def _generate_cliff_notes_hierarchical(
        self,
        *,
        request: knowledge_pb2.GenerateCliffNotesRequest,
        audience: str,
        depth: str,
        scope_type: str,
        model_override: str | None,
    ) -> tuple[CliffNotesResult, LLMUsageRecord]:
        """Run the Phase 3 hierarchical pipeline for cliff notes.

        Steps:
          1. Parse the snapshot JSON into a dict.
          2. Wrap it in a CodeCorpus adapter.
          3. Build a SummaryTree with HierarchicalStrategy — each LLM
             call sees only one segment / one file's children / one
             package's children / one repo's children, so the prompt
             always fits even small-context models.
          4. Render the final structured cliff notes from the tree
             via CliffNotesRenderer.
        """
        try:
            snapshot_dict = json.loads(request.snapshot_json)
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
        query = build_overview_query(
            request.repository_name,
            "cliff_notes",
            scope_type=scope_type,
            scope_path=request.scope_path,
        )

        strategy_name = _selected_cliff_notes_strategy()
        model_override = resolve_model_override(context)

        try:
            if strategy_name == "hierarchical":
                result, usage = await self._generate_cliff_notes_hierarchical(
                    request=request,
                    audience=audience,
                    depth=depth,
                    scope_type=scope_type,
                    model_override=model_override,
                )
            else:
                snapshot = await self._prepare_snapshot(
                    request.snapshot_json, query, scope_type=scope_type,
                )
                result, usage = await generate_cliff_notes(
                    provider=self._llm,
                    repository_name=request.repository_name,
                    audience=audience,
                    depth=depth,
                    scope_type=scope_type,
                    scope_path=request.scope_path,
                    snapshot_json=snapshot,
                    model_override=model_override,
                )
        except Exception as exc:
            log.error(
                "generate_cliff_notes_failed",
                strategy=strategy_name,
                error=str(exc),
            )
            await context.abort(
                grpc.StatusCode.INTERNAL,
                f"Cliff notes generation failed: {exc}",
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
            log.error("generate_workflow_story_failed", error=str(exc))
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
