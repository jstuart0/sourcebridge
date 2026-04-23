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
from workers.common.llm.tools import (
    AgentMessage,
    ToolCall,
    ToolResult,
    ToolSchema,
    provider_supports_prompt_caching,
    provider_supports_tool_use,
    run_agent_turn_anthropic,
)
from workers.reasoning.classifier import classify_question
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

    # ------------------------------------------------------------------
    # Agentic retrieval (plan 2026-04-23-agentic-retrieval-for-deep-qa)
    # ------------------------------------------------------------------

    async def AnswerQuestionWithTools(  # noqa: N802
        self,
        request: reasoning_pb2.AnswerQuestionWithToolsRequest,
        context: grpc.aio.ServicerContext,
    ) -> reasoning_pb2.AnswerQuestionWithToolsResponse:
        """Single-turn tool-use call. The orchestrator runs the loop
        on the Go side; this servicer is stateless.
        """
        provider, model_override = self._resolve_provider(context)
        provider_name = getattr(provider, "__class__", type(provider)).__name__.lower()
        # Heuristic: AnthropicProvider → "anthropic"; we special-case
        # on the real provider name.
        provider_key = "anthropic" if "anthropic" in provider_name else ""
        model = model_override or getattr(provider, "default_model", "") or getattr(
            provider, "model", ""
        )

        if not provider_supports_tool_use(provider_key, model):
            log.info(
                "agent_turn_capability_unsupported",
                provider=provider_key,
                model=model,
            )
            return reasoning_pb2.AnswerQuestionWithToolsResponse(
                capability_supported=False,
                turn=reasoning_pb2.AgentMessage(role="assistant"),
                termination_hint=(
                    "provider or model does not support structured tool use; "
                    "orchestrator must fall back to single-shot AnswerQuestion"
                ),
            )

        messages = [
            AgentMessage(
                role=m.role,
                text=m.text,
                tool_calls=[
                    ToolCall(call_id=c.call_id, name=c.name, args_json=c.args_json)
                    for c in m.tool_calls
                ],
                tool_results=[
                    ToolResult(
                        call_id=r.call_id,
                        ok=r.ok,
                        data_json=r.data_json,
                        error=r.error,
                        hint=r.hint,
                    )
                    for r in m.tool_results
                ],
            )
            for m in request.messages
        ]
        tools = [
            ToolSchema(
                name=t.name,
                description=t.description,
                input_schema_json=t.input_schema_json,
            )
            for t in request.tools
        ]
        max_tokens = request.max_tokens if request.max_tokens > 0 else 2048

        try:
            turn_resp = await run_agent_turn_anthropic(
                client=getattr(provider, "client", None),
                model=model,
                messages=messages,
                tools=tools,
                max_tokens=max_tokens,
                enable_prompt_caching=bool(request.enable_prompt_caching),
            )
        except Exception as exc:
            log.error("agent_turn_failed", error=str(exc))
            await context.abort(
                grpc.StatusCode.INTERNAL, f"Agent turn failed: {exc}"
            )
            return  # type: ignore[return-value]

        if turn_resp.cache_read_input_tokens or turn_resp.cache_creation_input_tokens:
            log.info(
                "agent_turn_prompt_cache",
                cache_creation=turn_resp.cache_creation_input_tokens,
                cache_read=turn_resp.cache_read_input_tokens,
                input_tokens=turn_resp.input_tokens,
                model=turn_resp.model,
            )

        return reasoning_pb2.AnswerQuestionWithToolsResponse(
            capability_supported=True,
            turn=reasoning_pb2.AgentMessage(
                role=turn_resp.turn.role,
                text=turn_resp.turn.text,
                tool_calls=[
                    reasoning_pb2.ToolCall(
                        call_id=tc.call_id,
                        name=tc.name,
                        args_json=tc.args_json,
                    )
                    for tc in turn_resp.turn.tool_calls
                ],
            ),
            usage=types_pb2.LLMUsage(
                model=turn_resp.model,
                input_tokens=turn_resp.input_tokens,
                output_tokens=turn_resp.output_tokens,
                operation="agent_turn",
            ),
            termination_hint=turn_resp.termination_hint,
            cache_creation_input_tokens=turn_resp.cache_creation_input_tokens,
            cache_read_input_tokens=turn_resp.cache_read_input_tokens,
        )

    async def ClassifyQuestion(  # noqa: N802
        self,
        request: reasoning_pb2.ClassifyQuestionRequest,
        context: grpc.aio.ServicerContext,
    ) -> reasoning_pb2.ClassifyQuestionResponse:
        """LLM-backed question classifier. Quality-push Phase 2."""
        provider, _model_override = self._resolve_provider(context)
        provider_name = getattr(provider, "__class__", type(provider)).__name__.lower()
        provider_key = "anthropic" if "anthropic" in provider_name else ""

        # Only Anthropic has Haiku today; other providers can still
        # produce well-formed JSON but accuracy isn't validated here.
        # Signal unsupported so the Go side falls back to keyword.
        if provider_key != "anthropic":
            return reasoning_pb2.ClassifyQuestionResponse(
                capability_supported=False,
            )

        try:
            result = await classify_question(
                provider,
                question=request.question,
                file_path=request.file_path,
                pinned_code=request.pinned_code,
            )
        except Exception as exc:
            log.error("classify_question_failed", error=str(exc))
            return reasoning_pb2.ClassifyQuestionResponse(
                capability_supported=False,
            )

        return reasoning_pb2.ClassifyQuestionResponse(
            capability_supported=True,
            question_class=result.question_class,
            needs_call_graph=result.needs_call_graph,
            needs_requirements=result.needs_requirements,
            needs_tests=result.needs_tests,
            needs_summaries=result.needs_summaries,
            symbol_candidates=list(result.symbol_candidates),
            file_candidates=list(result.file_candidates),
            topic_terms=list(result.topic_terms),
            usage=types_pb2.LLMUsage(
                model=result.model,
                input_tokens=result.input_tokens,
                output_tokens=result.output_tokens,
                operation="classify_question",
            ),
        )

    async def GetProviderCapabilities(  # noqa: N802
        self,
        request: reasoning_pb2.GetProviderCapabilitiesRequest,
        context: grpc.aio.ServicerContext,
    ) -> reasoning_pb2.GetProviderCapabilitiesResponse:
        provider, _ = self._resolve_provider(context)
        provider_name = getattr(provider, "__class__", type(provider)).__name__.lower()
        provider_key = "anthropic" if "anthropic" in provider_name else ""
        model = getattr(provider, "default_model", "") or getattr(provider, "model", "")
        return reasoning_pb2.GetProviderCapabilitiesResponse(
            provider=provider_key,
            model=model,
            tool_use_supported=provider_supports_tool_use(provider_key, model),
            prompt_caching_supported=provider_supports_prompt_caching(provider_key),
        )
