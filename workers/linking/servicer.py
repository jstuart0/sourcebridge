"""gRPC servicer for the LinkingService."""

from __future__ import annotations

import uuid

import grpc
import structlog
from common.v1 import types_pb2
from linking.v1 import linking_pb2, linking_pb2_grpc

from workers.common.embedding.provider import EmbeddingProvider
from workers.common.llm.provider import LLMProvider
from workers.linking.comment import extract_comment_links
from workers.linking.confidence import score_links
from workers.linking.semantic import RequirementText, cosine_similarity, extract_semantic_links
from workers.linking.types import CodeEntity, Link

log = structlog.get_logger()

# Map proto Confidence enum values to float thresholds and back
_CONFIDENCE_TO_PROTO: dict[str, int] = {
    "low": types_pb2.CONFIDENCE_LOW,
    "medium": types_pb2.CONFIDENCE_MEDIUM,
    "high": types_pb2.CONFIDENCE_HIGH,
    "verified": types_pb2.CONFIDENCE_VERIFIED,
}


def _float_to_confidence_enum(score: float) -> int:
    """Map a 0-1 float to a proto Confidence enum value."""
    if score >= 0.95:
        return types_pb2.CONFIDENCE_VERIFIED
    elif score >= 0.75:
        return types_pb2.CONFIDENCE_HIGH
    elif score >= 0.50:
        return types_pb2.CONFIDENCE_MEDIUM
    elif score > 0.0:
        return types_pb2.CONFIDENCE_LOW
    return types_pb2.CONFIDENCE_UNSPECIFIED


def _candidate_to_entity(candidate: linking_pb2.CandidateSymbol) -> CodeEntity:
    """Convert a proto CandidateSymbol into our internal CodeEntity."""
    sym = candidate.symbol
    return CodeEntity(
        file_path=sym.location.path if sym.location else "",
        name=sym.name,
        kind=_symbol_kind_name(sym.kind),
        start_line=sym.location.start_line if sym.location else 0,
        end_line=sym.location.end_line if sym.location else 0,
        content=candidate.content,
        doc_comment=sym.doc_comment,
        language=_language_name(sym.language),
    )


def _symbol_kind_name(kind: int) -> str:
    """Convert proto SymbolKind to string."""
    mapping = {
        types_pb2.SYMBOL_KIND_FUNCTION: "function",
        types_pb2.SYMBOL_KIND_METHOD: "method",
        types_pb2.SYMBOL_KIND_CLASS: "class",
        types_pb2.SYMBOL_KIND_STRUCT: "struct",
        types_pb2.SYMBOL_KIND_INTERFACE: "interface",
        types_pb2.SYMBOL_KIND_ENUM: "enum",
        types_pb2.SYMBOL_KIND_CONSTANT: "constant",
        types_pb2.SYMBOL_KIND_VARIABLE: "variable",
        types_pb2.SYMBOL_KIND_MODULE: "module",
        types_pb2.SYMBOL_KIND_PACKAGE: "package",
        types_pb2.SYMBOL_KIND_TYPE: "type",
        types_pb2.SYMBOL_KIND_TEST: "test",
    }
    return mapping.get(kind, "unknown")


_LANGUAGE_MAP: dict[int, str] = {
    types_pb2.LANGUAGE_UNSPECIFIED: "unknown",
    types_pb2.LANGUAGE_GO: "go",
    types_pb2.LANGUAGE_PYTHON: "python",
    types_pb2.LANGUAGE_TYPESCRIPT: "typescript",
    types_pb2.LANGUAGE_JAVASCRIPT: "javascript",
    types_pb2.LANGUAGE_JAVA: "java",
    types_pb2.LANGUAGE_RUST: "rust",
    types_pb2.LANGUAGE_CSHARP: "csharp",
    types_pb2.LANGUAGE_CPP: "cpp",
    types_pb2.LANGUAGE_RUBY: "ruby",
    types_pb2.LANGUAGE_PHP: "php",
}


def _language_name(proto_lang: int) -> str:
    return _LANGUAGE_MAP.get(proto_lang, "unknown")


def _link_to_proto(link: Link) -> types_pb2.RequirementLink:
    """Convert an internal Link to a proto RequirementLink message."""
    return types_pb2.RequirementLink(
        id=str(uuid.uuid4()),
        requirement_id=link.requirement_id,
        symbol_id=f"{link.entity.file_path}:{link.entity.name}",
        confidence=_float_to_confidence_enum(link.confidence),
        rationale=link.rationale,
    )


class LinkingServicer(linking_pb2_grpc.LinkingServiceServicer):
    """Implements the LinkingService gRPC service."""

    def __init__(
        self,
        llm_provider: LLMProvider,
        embedding_provider: EmbeddingProvider,
    ) -> None:
        self._llm = llm_provider
        self._embedding = embedding_provider

    async def LinkRequirement(  # noqa: N802
        self,
        request: linking_pb2.LinkRequirementRequest,
        context: grpc.aio.ServicerContext,
    ) -> linking_pb2.LinkRequirementResponse:
        """Find code symbols that implement a requirement."""
        req = request.requirement
        log.info("link_requirement", requirement_id=req.id, candidates=len(request.candidate_symbols))

        # Convert candidates to CodeEntity objects
        entities = [_candidate_to_entity(c) for c in request.candidate_symbols]
        min_confidence = request.min_confidence or 0.0

        # --- Comment-based linking ---
        comment_result = extract_comment_links(entities)
        # Filter to only links referencing THIS requirement
        comment_links = [
            lk for lk in comment_result.links
            if lk.requirement_id == req.id or lk.requirement_id == req.external_id
        ]

        # --- Semantic linking ---
        req_text = f"{req.title} {req.description}".strip()
        semantic_links: list[Link] = []
        if req_text and entities:
            try:
                semantic_result = await extract_semantic_links(
                    requirements=[RequirementText(id=req.id, text=req_text)],
                    entities=entities,
                    embedding_provider=self._embedding,
                    threshold=max(min_confidence, 0.5),
                )
                semantic_links = semantic_result.links
            except Exception as exc:
                log.warning("semantic_linking_failed", error=str(exc))

        # --- Merge and score ---
        all_links = comment_links + semantic_links
        scored = score_links(all_links)

        # Apply min_confidence filter
        if min_confidence > 0:
            scored = [lk for lk in scored if lk.confidence >= min_confidence]

        proto_links = [_link_to_proto(lk) for lk in scored]

        return linking_pb2.LinkRequirementResponse(links=proto_links)

    async def BatchLink(  # noqa: N802
        self,
        request: linking_pb2.BatchLinkRequest,
        context: grpc.aio.ServicerContext,
    ) -> linking_pb2.BatchLinkResponse:
        """Not implemented -- callers should use repeated LinkRequirement calls."""
        await context.abort(
            grpc.StatusCode.UNIMPLEMENTED,
            "BatchLink is not implemented; use repeated LinkRequirement calls from the API layer",
        )

    async def ValidateLink(  # noqa: N802
        self,
        request: linking_pb2.ValidateLinkRequest,
        context: grpc.aio.ServicerContext,
    ) -> linking_pb2.ValidateLinkResponse:
        """Validate whether a requirement link is still valid using embedding similarity."""
        link = request.link
        current_symbol = request.current_symbol
        log.info("validate_link", requirement_id=link.requirement_id, symbol_id=link.symbol_id)

        # We need at least the symbol content to compare
        if not current_symbol or not current_symbol.signature:
            # If no content is provided, we can only report unknown validity
            return linking_pb2.ValidateLinkResponse(
                still_valid=True,
                new_confidence=types_pb2.CONFIDENCE_LOW,
                reason="Insufficient data to validate; symbol content not provided.",
            )

        try:
            # Embed the requirement rationale and symbol content
            texts = [link.rationale or link.requirement_id, current_symbol.signature]
            embeddings = await self._embedding.embed(texts)
            sim = cosine_similarity(embeddings[0], embeddings[1])

            still_valid = sim >= 0.4
            confidence_enum = _float_to_confidence_enum(sim)

            reason = f"Embedding similarity: {sim:.3f}"
            if not still_valid:
                reason = f"Link appears stale (similarity {sim:.3f} below threshold)"

            return linking_pb2.ValidateLinkResponse(
                still_valid=still_valid,
                new_confidence=confidence_enum,
                reason=reason,
            )
        except Exception as exc:
            log.error("validate_link_failed", error=str(exc))
            await context.abort(grpc.StatusCode.INTERNAL, f"Validation failed: {exc}")
