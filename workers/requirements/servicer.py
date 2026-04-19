"""gRPC servicer for the RequirementsService."""

from __future__ import annotations

import grpc
import structlog
from common.v1 import types_pb2
from requirements.v1 import requirements_pb2, requirements_pb2_grpc

from workers.common.config import WorkerConfig
from workers.common.grpc_metadata import resolve_llm_override, resolve_model_override
from workers.common.llm.config import create_llm_provider_for_request
from workers.common.llm.provider import LLMProvider
from workers.requirements.csv_parser import parse_csv
from workers.requirements.markdown import parse_markdown

log = structlog.get_logger()


def _req_to_proto(req) -> types_pb2.Requirement:
    """Convert an internal Requirement dataclass to a proto Requirement message."""
    return types_pb2.Requirement(
        id=req.id,
        title=req.title,
        description=req.description,
        priority=req.priority,
        tags=req.tags,
        source=req.source,
    )


class RequirementsServicer(requirements_pb2_grpc.RequirementsServiceServicer):
    """Implements the RequirementsService gRPC service."""

    def __init__(self, llm_provider: LLMProvider, worker_config: WorkerConfig | None = None) -> None:
        self._llm = llm_provider
        self._config = worker_config

    def _resolve_provider(self, context: grpc.aio.ServicerContext) -> tuple[LLMProvider, str | None]:
        override = resolve_llm_override(context)
        if override is None or self._config is None:
            return self._llm, resolve_model_override(context)
        provider, model = create_llm_provider_for_request(
            self._config,
            provider=override.provider,
            base_url=override.base_url,
            api_key=override.api_key,
            model=override.model,
            draft_model=override.draft_model,
            timeout_seconds=override.timeout_seconds,
        )
        return provider, model or None

    async def ParseDocument(  # noqa: N802
        self,
        request: requirements_pb2.ParseDocumentRequest,
        context: grpc.aio.ServicerContext,
    ) -> requirements_pb2.ParseDocumentResponse:
        """Parse a markdown document and extract requirements."""
        fmt = request.format or "markdown"
        source = request.source_path or ""
        log.info("parse_document", format=fmt, source=source, content_len=len(request.content))

        if fmt != "markdown":
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                f"Unsupported format '{fmt}'. Use ParseCSV for CSV files.",
            )

        try:
            reqs = parse_markdown(content=request.content, source=source)
        except Exception as exc:
            log.error("parse_document_failed", error=str(exc))
            await context.abort(grpc.StatusCode.INTERNAL, f"Parsing failed: {exc}")

        proto_reqs = [_req_to_proto(r) for r in reqs]

        return requirements_pb2.ParseDocumentResponse(
            requirements=proto_reqs,
            total_found=len(proto_reqs),
        )

    async def ParseCSV(  # noqa: N802
        self,
        request: requirements_pb2.ParseCSVRequest,
        context: grpc.aio.ServicerContext,
    ) -> requirements_pb2.ParseCSVResponse:
        """Parse a CSV file and extract requirements."""
        source = request.source_path or ""
        log.info("parse_csv", source=source, content_len=len(request.content))

        # Convert proto column_mapping (MapField) to a plain dict
        column_mapping: dict[str, str] | None = None
        if request.column_mapping:
            column_mapping = dict(request.column_mapping)

        try:
            reqs = parse_csv(
                content=request.content,
                source=source,
                column_mapping=column_mapping,
            )
        except Exception as exc:
            log.error("parse_csv_failed", error=str(exc))
            await context.abort(grpc.StatusCode.INTERNAL, f"CSV parsing failed: {exc}")

        proto_reqs = [_req_to_proto(r) for r in reqs]

        return requirements_pb2.ParseCSVResponse(
            requirements=proto_reqs,
            total_found=len(proto_reqs),
        )

    async def EnrichRequirement(  # noqa: N802
        self,
        request: requirements_pb2.EnrichRequirementRequest,
        context: grpc.aio.ServicerContext,
    ) -> requirements_pb2.EnrichRequirementResponse:
        """Enrich a requirement with LLM-suggested priority and tags."""
        req = request.requirement
        log.info("enrich_requirement", id=req.id, title=req.title[:60] if req.title else "")

        prompt = (
            f"Analyze this software requirement and suggest a priority level and tags.\n\n"
            f"ID: {req.id}\n"
            f"Title: {req.title}\n"
            f"Description: {req.description}\n"
            f"Current priority: {req.priority or '(none)'}\n"
            f"Current tags: {', '.join(req.tags) if req.tags else '(none)'}\n\n"
            f"Return ONLY valid JSON with these fields:\n"
            f'- "suggested_priority": one of "urgent", "high", "medium", "low"\n'
            f'- "suggested_tags": list of 2-5 short tags (e.g., "security", "performance", "api")\n'
            f'- "rationale": one sentence explaining your suggestions\n'
        )

        system = (
            "You are a requirements analyst. Analyze the requirement and suggest "
            "appropriate priority and classification tags. Return ONLY valid JSON."
        )

        provider, model_override = self._resolve_provider(context)
        try:
            response = await provider.complete(prompt, system=system, temperature=0.1, model=model_override)
        except Exception as exc:
            log.error("enrich_requirement_failed", error=str(exc))
            await context.abort(grpc.StatusCode.INTERNAL, f"Enrichment failed: {exc}")

        # Parse LLM response — strip <think> blocks and code fences
        from workers.common.llm.parse import parse_json_response

        data = parse_json_response(response.content)
        if data is None or not isinstance(data, dict):
            log.warning("enrich_parse_failed", raw_preview=response.content[:500])
            data = {}

        suggested_priority = data.get("suggested_priority", "medium")
        suggested_tags = data.get("suggested_tags", [])
        if not isinstance(suggested_tags, list):
            suggested_tags = []

        log.info(
            "enrich_result",
            id=req.id,
            suggested_priority=suggested_priority,
            suggested_tags=suggested_tags,
            rationale=data.get("rationale", ""),
        )

        # Build enriched copy of the requirement
        enriched = types_pb2.Requirement(
            id=req.id,
            external_id=req.external_id,
            title=req.title,
            description=req.description,
            source=req.source,
            priority=suggested_priority,
            tags=list(req.tags) + [t for t in suggested_tags if t not in list(req.tags)],
        )

        usage = types_pb2.LLMUsage(
            model=response.model,
            input_tokens=response.input_tokens,
            output_tokens=response.output_tokens,
            operation="enrich",
        )

        return requirements_pb2.EnrichRequirementResponse(
            enriched=enriched,
            suggested_tags=suggested_tags,
            suggested_priority=suggested_priority,
            usage=usage,
        )

    async def ExtractSpecs(  # noqa: N802
        self,
        request: requirements_pb2.ExtractSpecsRequest,
        context: grpc.aio.ServicerContext,
    ) -> requirements_pb2.ExtractSpecsResponse:
        """Extract implicit specifications from source files."""
        log.info(
            "extract_specs",
            repo_id=request.repository_id,
            file_count=len(request.files),
            skip_llm=request.skip_llm_refinement,
        )

        from workers.requirements.spec_extraction import extract_specs_pipeline

        provider, _ = self._resolve_provider(context)
        try:
            result = await extract_specs_pipeline(
                files=request.files,
                llm_provider=provider if not request.skip_llm_refinement else None,
            )
        except Exception as exc:
            log.error("extract_specs_failed", error=str(exc))
            await context.abort(grpc.StatusCode.INTERNAL, f"Extraction failed: {exc}")

        proto_specs = [
            requirements_pb2.DiscoveredSpec(
                source=spec.source,
                source_file=spec.source_file,
                source_line=spec.source_line,
                source_files=spec.source_files,
                text=spec.text,
                raw_text=spec.raw_text,
                group_key=spec.group_key,
                language=spec.language,
                keywords=spec.keywords,
                confidence=spec.confidence,
                llm_refined=spec.llm_refined,
            )
            for spec in result.specs
        ]

        usage = None
        if result.usage:
            usage = types_pb2.LLMUsage(
                model=result.usage.model,
                input_tokens=result.usage.input_tokens,
                output_tokens=result.usage.output_tokens,
                operation="spec_extraction",
            )

        return requirements_pb2.ExtractSpecsResponse(
            specs=proto_specs,
            total_candidates=result.total_candidates,
            total_refined=len(proto_specs),
            usage=usage,
            warnings=result.warnings,
        )
