"""gRPC servicer for the ReasoningService."""

from __future__ import annotations

import grpc
import structlog
from common.v1 import types_pb2
from reasoning.v1 import reasoning_pb2, reasoning_pb2_grpc

from workers.common.embedding.provider import EmbeddingProvider
from workers.common.llm.provider import LLMProvider
from workers.reasoning.discussion import discuss_code
from workers.reasoning.reviewer import review_code
from workers.reasoning.summarizer import summarize_function

log = structlog.get_logger()

# Proto Language enum -> human-readable string
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


def _llm_usage_proto(usage_record) -> types_pb2.LLMUsage:
    """Convert an LLMUsageRecord to a proto LLMUsage message."""
    return types_pb2.LLMUsage(
        model=usage_record.model,
        input_tokens=usage_record.input_tokens,
        output_tokens=usage_record.output_tokens,
        operation=usage_record.operation,
    )


class ReasoningServicer(reasoning_pb2_grpc.ReasoningServiceServicer):
    """Implements the ReasoningService gRPC service."""

    def __init__(
        self,
        llm_provider: LLMProvider,
        embedding_provider: EmbeddingProvider,
    ) -> None:
        self._llm = llm_provider
        self._embedding = embedding_provider

    async def AnalyzeSymbol(  # noqa: N802
        self,
        request: reasoning_pb2.AnalyzeSymbolRequest,
        context: grpc.aio.ServicerContext,
    ) -> reasoning_pb2.AnalyzeSymbolResponse:
        """Analyze a code symbol using summarize_function."""
        log.info("analyze_symbol", name=request.symbol.name)

        symbol = request.symbol
        language = _language_name(symbol.language)

        # Build content from signature + surrounding_context
        content = request.surrounding_context or symbol.signature or ""

        try:
            summary, usage = await summarize_function(
                provider=self._llm,
                name=symbol.name,
                language=language,
                content=content,
                doc_comment=symbol.doc_comment,
            )
        except Exception as exc:
            log.error("analyze_symbol_failed", error=str(exc), name=symbol.name)
            await context.abort(grpc.StatusCode.INTERNAL, f"Analysis failed: {exc}")

        # Map concerns from summary.risks, suggestions from summary.side_effects
        return reasoning_pb2.AnalyzeSymbolResponse(
            summary=summary.purpose,
            purpose=summary.purpose,
            concerns=summary.risks,
            suggestions=summary.side_effects,
            usage=_llm_usage_proto(usage),
        )

    async def ExplainRelationship(  # noqa: N802
        self,
        request: reasoning_pb2.ExplainRelationshipRequest,
        context: grpc.aio.ServicerContext,
    ) -> reasoning_pb2.ExplainRelationshipResponse:
        """Deferred -- no business logic exists yet."""
        await context.abort(
            grpc.StatusCode.UNIMPLEMENTED,
            "ExplainRelationship is not yet implemented",
        )

    async def AnswerQuestion(  # noqa: N802
        self,
        request: reasoning_pb2.AnswerQuestionRequest,
        context: grpc.aio.ServicerContext,
    ) -> reasoning_pb2.AnswerQuestionResponse:
        """Answer a natural-language question about the codebase."""
        log.info("answer_question", question=request.question[:80])

        context_parts: list[str] = []
        if request.context_code:
            header_bits = []
            if request.file_path:
                header_bits.append(f"file: {request.file_path}")
            language_name = _language_name(request.language)
            if language_name != "unknown":
                header_bits.append(f"language: {language_name}")
            if header_bits:
                context_parts.append("// " + " | ".join(header_bits))
            context_parts.append(request.context_code)

        # Add symbol metadata as supporting repository context.
        for sym in request.context_symbols:
            _language_name(sym.language)
            if sym.signature:
                context_parts.append(f"// {sym.qualified_name or sym.name}\n{sym.signature}")
            elif sym.doc_comment:
                context_parts.append(f"// {sym.qualified_name or sym.name}\n{sym.doc_comment}")
            else:
                context_parts.append(f"// {sym.qualified_name or sym.name}")

        context_code = "\n\n".join(context_parts) if context_parts else "(no code context provided)"

        try:
            answer, usage = await discuss_code(
                provider=self._llm,
                question=request.question,
                context_code=context_code,
            )
        except Exception as exc:
            log.error("answer_question_failed", error=str(exc))
            await context.abort(grpc.StatusCode.INTERNAL, f"Question answering failed: {exc}")

        # Map referenced_symbols from answer.references -- these are strings like
        # "main.go:10-25", not full CodeSymbol messages, so we return empty symbols
        # with the reference as qualified_name.
        ref_symbols = []
        for ref in answer.references:
            ref_symbols.append(types_pb2.CodeSymbol(qualified_name=ref))

        return reasoning_pb2.AnswerQuestionResponse(
            answer=answer.answer,
            referenced_symbols=ref_symbols,
            usage=_llm_usage_proto(usage),
        )

    async def ReviewFile(  # noqa: N802
        self,
        request: reasoning_pb2.ReviewFileRequest,
        context: grpc.aio.ServicerContext,
    ) -> reasoning_pb2.ReviewFileResponse:
        """Perform a template-based code review."""
        language = _language_name(request.language)
        template = request.template or "security"
        log.info("review_file", file_path=request.file_path, template=template)

        try:
            result, usage = await review_code(
                provider=self._llm,
                file_path=request.file_path,
                language=language,
                content=request.content,
                template=template,
            )
        except ValueError as exc:
            await context.abort(grpc.StatusCode.INVALID_ARGUMENT, str(exc))
        except Exception as exc:
            log.error("review_file_failed", error=str(exc))
            await context.abort(grpc.StatusCode.INTERNAL, f"Review failed: {exc}")

        findings = []
        for f in result.findings:
            findings.append(reasoning_pb2.ReviewFinding(
                category=f.category,
                severity=f.severity,
                message=f.message,
                file_path=f.file_path,
                start_line=f.start_line,
                end_line=f.end_line,
                suggestion=f.suggestion,
            ))

        return reasoning_pb2.ReviewFileResponse(
            template=result.template,
            findings=findings,
            score=result.score,
            usage=_llm_usage_proto(usage),
        )

    async def GenerateEmbedding(  # noqa: N802
        self,
        request: reasoning_pb2.GenerateEmbeddingRequest,
        context: grpc.aio.ServicerContext,
    ) -> reasoning_pb2.GenerateEmbeddingResponse:
        """Generate an embedding vector for text."""
        log.info("generate_embedding", text_len=len(request.text))

        try:
            vectors = await self._embedding.embed([request.text])
        except Exception as exc:
            log.error("generate_embedding_failed", error=str(exc))
            await context.abort(grpc.StatusCode.INTERNAL, f"Embedding failed: {exc}")

        vector = vectors[0]

        embedding_msg = types_pb2.Embedding(
            source_type="text",
            vector=vector,
            model=request.model or "default",
            dimensions=len(vector),
        )

        return reasoning_pb2.GenerateEmbeddingResponse(
            embedding=embedding_msg,
        )
