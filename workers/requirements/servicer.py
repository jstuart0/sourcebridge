"""gRPC servicer for the RequirementsService."""

from __future__ import annotations

import json

import grpc
import structlog
from common.v1 import types_pb2
from requirements.v1 import requirements_pb2, requirements_pb2_grpc

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

    def __init__(self, llm_provider: LLMProvider) -> None:
        self._llm = llm_provider

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

        try:
            response = await self._llm.complete(prompt, system=system, temperature=0.1)
        except Exception as exc:
            log.error("enrich_requirement_failed", error=str(exc))
            await context.abort(grpc.StatusCode.INTERNAL, f"Enrichment failed: {exc}")

        # Parse LLM response
        try:
            data = json.loads(response.content)
        except json.JSONDecodeError:
            # Try extracting from markdown code block
            raw = response.content
            if "```" in raw:
                start = raw.index("```") + 3
                if raw[start:].startswith("json"):
                    start += 4
                end = raw.index("```", start)
                data = json.loads(raw[start:end].strip())
            else:
                data = {}

        suggested_priority = data.get("suggested_priority", "medium")
        suggested_tags = data.get("suggested_tags", [])

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
