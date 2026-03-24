# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""gRPC servicer for the KnowledgeService."""

from __future__ import annotations

import grpc
import structlog
from common.v1 import types_pb2
from knowledge.v1 import knowledge_pb2, knowledge_pb2_grpc

from workers.common.embedding.provider import EmbeddingProvider
from workers.common.llm.provider import LLMProvider
from workers.knowledge.cliff_notes import generate_cliff_notes
from workers.knowledge.code_tour import generate_code_tour
from workers.knowledge.explain_system import explain_system
from workers.knowledge.learning_path import generate_learning_path
from workers.knowledge.retrieval import (
    build_overview_query,
    retrieve_relevant_snapshot,
)
from workers.knowledge.snapshot_truncate import condense_snapshot
from workers.knowledge.workflow_story import generate_workflow_story

log = structlog.get_logger()


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
        return condense_snapshot(snapshot_json)

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

        audience = request.audience or "developer"
        depth = request.depth or "medium"
        query = build_overview_query(request.repository_name, "cliff_notes")
        snapshot = await self._prepare_snapshot(request.snapshot_json, query)

        try:
            result, usage = await generate_cliff_notes(
                provider=self._llm,
                repository_name=request.repository_name,
                audience=audience,
                depth=depth,
                scope_type=request.scope_type or "repository",
                scope_path=request.scope_path,
                snapshot_json=snapshot,
            )
        except Exception as exc:
            log.error("generate_cliff_notes_failed", error=str(exc))
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

        audience = request.audience or "developer"
        depth = request.depth or "medium"
        query = build_overview_query(request.repository_name, "learning_path")
        if request.focus_area:
            query = f"{request.focus_area} {query}"
        snapshot = await self._prepare_snapshot(request.snapshot_json, query)

        try:
            result, usage = await generate_learning_path(
                provider=self._llm,
                repository_name=request.repository_name,
                audience=audience,
                depth=depth,
                snapshot_json=snapshot,
                focus_area=request.focus_area,
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

        audience = request.audience or "developer"
        depth = request.depth or "medium"
        query = build_overview_query(request.repository_name, "workflow_story")
        if request.anchor_label:
            query = f"{request.anchor_label} {query}"
        snapshot = await self._prepare_snapshot(request.snapshot_json, query)

        try:
            result, usage = await generate_workflow_story(
                provider=self._llm,
                repository_name=request.repository_name,
                audience=audience,
                depth=depth,
                scope_type=request.scope_type or "repository",
                scope_path=request.scope_path,
                anchor_label=request.anchor_label,
                execution_path_json=request.execution_path_json,
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

        audience = request.audience or "developer"
        # For Q&A, use the actual question for retrieval
        query = request.question or build_overview_query(
            request.repository_name, "explain"
        )
        snapshot = await self._prepare_snapshot(request.snapshot_json, query)

        try:
            result, usage = await explain_system(
                provider=self._llm,
                repository_name=request.repository_name,
                audience=audience,
                question=request.question,
                snapshot_json=snapshot,
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

        audience = request.audience or "developer"
        depth = request.depth or "medium"
        query = build_overview_query(request.repository_name, "code_tour")
        if request.theme:
            query = f"{request.theme} {query}"
        snapshot = await self._prepare_snapshot(request.snapshot_json, query)

        try:
            result, usage = await generate_code_tour(
                provider=self._llm,
                repository_name=request.repository_name,
                audience=audience,
                depth=depth,
                snapshot_json=snapshot,
                theme=request.theme,
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
