"""gRPC servicer for the ReasoningService."""

from __future__ import annotations

import grpc
import structlog
from common.v1 import types_pb2
from reasoning.v1 import reasoning_pb2, reasoning_pb2_grpc

from workers.common.config import WorkerConfig
from workers.common.embedding.provider import EmbeddingProvider
from workers.common.grpc_metadata import resolve_llm_override, resolve_model_override
from workers.common.llm.config import create_llm_provider_for_request
from workers.common.llm.provider import LLMProvider
from workers.reasoning.discussion import discuss_code, discuss_code_stream
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


def _build_discussion_context(request: reasoning_pb2.AnswerQuestionRequest) -> str:
    """Assemble the `context_code` blob the discussion prompt expects.

    Shared by AnswerQuestion (unary) and AnswerQuestionStream. Keeping
    it in one place means the two RPCs can never drift on what the
    model sees, which matters because the streaming answer should be
    exactly the unary answer produced token-by-token.
    """
    parts: list[str] = []
    if request.context_code:
        header_bits: list[str] = []
        if request.file_path:
            header_bits.append(f"file: {request.file_path}")
        language_name = _language_name(request.language)
        if language_name != "unknown":
            header_bits.append(f"language: {language_name}")
        if header_bits:
            parts.append("// " + " | ".join(header_bits))
        parts.append(request.context_code)

    for sym in request.context_symbols:
        _language_name(sym.language)
        if sym.signature:
            parts.append(f"// {sym.qualified_name or sym.name}\n{sym.signature}")
        elif sym.doc_comment:
            parts.append(f"// {sym.qualified_name or sym.name}\n{sym.doc_comment}")
        else:
            parts.append(f"// {sym.qualified_name or sym.name}")

    return "\n\n".join(parts) if parts else "(no code context provided)"


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
        worker_config: WorkerConfig | None = None,
    ) -> None:
        self._llm = llm_provider
        self._embedding = embedding_provider
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

        provider, model_override = self._resolve_provider(context)
        try:
            summary, usage = await summarize_function(
                provider=provider,
                name=symbol.name,
                language=language,
                content=content,
                doc_comment=symbol.doc_comment,
                model_override=model_override,
            )
        except Exception as exc:
            log.error("analyze_symbol_failed", error=str(exc), name=symbol.name)
            await context.abort(grpc.StatusCode.INTERNAL, f"Analysis failed: {exc}")
            return  # type: ignore[return-value]

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

        context_code = _build_discussion_context(request)
        provider, model_override = self._resolve_provider(context)
        try:
            answer, usage = await discuss_code(
                provider=provider,
                question=request.question,
                context_code=context_code,
                model_override=model_override,
            )
        except Exception as exc:
            log.error("answer_question_failed", error=str(exc))
            await context.abort(grpc.StatusCode.INTERNAL, f"Question answering failed: {exc}")
            return  # type: ignore[return-value]

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

    async def AnswerQuestionStream(  # noqa: N802
        self,
        request: reasoning_pb2.AnswerQuestionRequest,
        context: grpc.aio.ServicerContext,
    ):
        """Stream a natural-language answer as the LLM generates it.

        Wire shape mirrors AnswerQuestion's prompt assembly so both
        variants share the same behavior — only the delivery differs.
        Emits one AnswerDelta per visible text chunk, then a terminal
        frame (finished=True) carrying referenced_symbols + usage.
        """
        log.info("answer_question_stream", question=request.question[:80])

        context_code = _build_discussion_context(request)
        provider, model_override = self._resolve_provider(context)

        max_tokens = request.max_tokens if request.max_tokens > 0 else 4096
        try:
            async for delta, final_answer, usage in discuss_code_stream(
                provider=provider,
                question=request.question,
                context_code=context_code,
                model_override=model_override,
                max_tokens=max_tokens,
            ):
                if delta is not None:
                    yield reasoning_pb2.AnswerDelta(content_delta=delta)
                    continue
                # Terminal frame: assemble the references + usage proto
                # the unary caller would have returned and send it as the
                # last AnswerDelta with finished=True set.
                assert final_answer is not None and usage is not None
                ref_symbols = [
                    types_pb2.CodeSymbol(qualified_name=ref)
                    for ref in final_answer.references
                ]
                yield reasoning_pb2.AnswerDelta(
                    finished=True,
                    referenced_symbols=ref_symbols,
                    usage=_llm_usage_proto(usage),
                )
        except Exception as exc:
            log.error("answer_question_stream_failed", error=str(exc))
            await context.abort(
                grpc.StatusCode.INTERNAL, f"Question streaming failed: {exc}"
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

        provider, model_override = self._resolve_provider(context)
        try:
            result, usage = await review_code(
                provider=provider,
                file_path=request.file_path,
                language=language,
                content=request.content,
                template=template,
                model_override=model_override,
            )
        except ValueError as exc:
            await context.abort(grpc.StatusCode.INVALID_ARGUMENT, str(exc))
            return  # type: ignore[return-value]
        except Exception as exc:
            log.error("review_file_failed", error=str(exc))
            await context.abort(grpc.StatusCode.INTERNAL, f"Review failed: {exc}")
            return  # type: ignore[return-value]

        findings = []
        for f in result.findings:
            findings.append(
                reasoning_pb2.ReviewFinding(
                    category=f.category,
                    severity=f.severity,
                    message=f.message,
                    file_path=f.file_path,
                    start_line=f.start_line,
                    end_line=f.end_line,
                    suggestion=f.suggestion,
                )
            )

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
            return  # type: ignore[return-value]

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

    async def SimulateChange(  # noqa: N802
        self,
        request: reasoning_pb2.SimulateChangeRequest,
        context: grpc.aio.ServicerContext,
    ) -> reasoning_pb2.SimulateChangeResponse:
        """Resolve symbols affected by a hypothetical change description."""
        from workers.reasoning.simulation import SymbolInfo, resolve_symbols

        log.info(
            "simulate_change",
            repo_id=request.repository_id,
            description_len=len(request.description),
            symbol_count=len(request.symbols),
            anchor_file=request.anchor_file or None,
            anchor_symbol=request.anchor_symbol or None,
        )

        if not request.description.strip():
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                "Description must be non-empty.",
            )
            return  # type: ignore[return-value]

        if len(request.description) > 2000:
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                "Description must be 2000 characters or fewer.",
            )
            return  # type: ignore[return-value]

        if not request.symbols:
            await context.abort(
                grpc.StatusCode.FAILED_PRECONDITION,
                "Repository has no indexed symbols. Please index the repository first.",
            )
            return  # type: ignore[return-value]

        # Convert proto symbols to SymbolInfo
        symbols = [
            SymbolInfo(
                id=s.id,
                name=s.name,
                qualified_name=s.qualified_name,
                kind=s.kind.name if hasattr(s.kind, "name") else str(s.kind),
                file_path=s.location.path if s.location else "",
                signature=s.signature,
                doc_comment=s.doc_comment,
            )
            for s in request.symbols
        ]

        top_n = request.top_n if request.top_n > 0 else 10
        threshold = request.confidence_threshold if request.confidence_threshold > 0 else 0.35

        try:
            resolved = await resolve_symbols(
                description=request.description,
                symbols=symbols,
                anchor_file=request.anchor_file or None,
                anchor_symbol=request.anchor_symbol or None,
                embedding_provider=self._embedding,
                top_n=top_n,
                confidence_threshold=threshold,
            )
        except ValueError as exc:
            await context.abort(grpc.StatusCode.INVALID_ARGUMENT, str(exc))
            return  # type: ignore[return-value]
        except Exception as exc:
            log.error("simulate_change_failed", error=str(exc))
            await context.abort(grpc.StatusCode.INTERNAL, f"Simulation failed: {exc}")
            return  # type: ignore[return-value]

        proto_matches = [
            reasoning_pb2.SimulatedSymbolMatch(
                symbol_id=r.symbol_id,
                name=r.name,
                qualified_name=r.qualified_name,
                kind=r.kind,
                file_path=r.file_path,
                similarity=r.similarity,
                is_anchor=r.is_anchor,
            )
            for r in resolved
        ]

        return reasoning_pb2.SimulateChangeResponse(
            resolved_symbols=proto_matches,
            symbols_evaluated=len(symbols),
        )
