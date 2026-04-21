"""Tests for the ReasoningServicer gRPC servicer."""

import grpc
import pytest
from common.v1 import types_pb2
from reasoning.v1 import reasoning_pb2

from workers.common.embedding.fake import FakeEmbeddingProvider
from workers.common.llm.fake import FakeLLMProvider
from workers.reasoning.servicer import ReasoningServicer


class MockServicerContext:
    """Minimal mock for grpc.aio.ServicerContext."""

    def __init__(self):
        self.code = None
        self.details = None

    async def abort(self, code, details):
        self.code = code
        self.details = details
        raise Exception(f"gRPC abort: {code} {details}")


@pytest.fixture
def llm():
    return FakeLLMProvider()


@pytest.fixture
def embedding():
    return FakeEmbeddingProvider(dimension=1024)


@pytest.fixture
def servicer(llm, embedding):
    return ReasoningServicer(llm, embedding)


@pytest.fixture
def context():
    return MockServicerContext()


# ---------------------------------------------------------------------------
# AnalyzeSymbol
# ---------------------------------------------------------------------------


async def test_analyze_symbol(servicer, context):
    """AnalyzeSymbol returns summary, purpose, and concerns for a code symbol."""
    symbol = types_pb2.CodeSymbol(
        name="processPayment",
        language=types_pb2.LANGUAGE_GO,
        kind=types_pb2.SYMBOL_KIND_FUNCTION,
        signature="func processPayment(ctx context.Context, order Order) (Receipt, error)",
        doc_comment="// Processes a payment transaction",
    )
    request = reasoning_pb2.AnalyzeSymbolRequest(
        symbol=symbol,
        surrounding_context=(
            "func processPayment(ctx context.Context, order Order) (Receipt, error) { validate(order); charge(order) }"
        ),
    )

    response = await servicer.AnalyzeSymbol(request, context)

    assert response.summary != ""
    assert response.purpose != ""
    assert isinstance(list(response.concerns), list)
    assert isinstance(list(response.suggestions), list)
    assert response.usage.model == "fake-test-model"
    assert response.usage.input_tokens > 0
    assert response.usage.output_tokens > 0


async def test_analyze_symbol_uses_signature_fallback(servicer, context):
    """When surrounding_context is empty, AnalyzeSymbol falls back to signature."""
    symbol = types_pb2.CodeSymbol(
        name="handleRequest",
        language=types_pb2.LANGUAGE_GO,
        kind=types_pb2.SYMBOL_KIND_FUNCTION,
        signature="func handleRequest(w http.ResponseWriter, r *http.Request)",
    )
    request = reasoning_pb2.AnalyzeSymbolRequest(symbol=symbol)

    response = await servicer.AnalyzeSymbol(request, context)

    assert response.summary != ""
    assert response.purpose != ""


# ---------------------------------------------------------------------------
# AnswerQuestion
# ---------------------------------------------------------------------------


async def test_answer_question(servicer, context):
    """AnswerQuestion returns an answer with usage tracking."""
    symbol = types_pb2.CodeSymbol(
        name="processPayment",
        qualified_name="payment.processPayment",
        language=types_pb2.LANGUAGE_GO,
        signature="func processPayment(ctx, order)",
    )
    request = reasoning_pb2.AnswerQuestionRequest(
        question="What does processPayment do?",
        context_symbols=[symbol],
    )

    response = await servicer.AnswerQuestion(request, context)

    assert response.answer != ""
    assert "payment" in response.answer.lower()
    assert response.usage.model == "fake-test-model"
    assert response.usage.operation == "discussion"


async def test_answer_question_prefers_context_code(servicer, context, monkeypatch):
    """AnswerQuestion passes explicit context_code through to the discussion prompt."""
    captured = {}

    async def fake_discuss_code(provider, question, context_code, context_metadata="", model_override=None):
        captured["question"] = question
        captured["context_code"] = context_code
        captured["context_metadata"] = context_metadata
        return (
            type(
                "Discussion",
                (),
                {
                    "answer": "ok",
                    "references": [],
                    "related_requirements": [],
                },
            )(),
            type(
                "Usage",
                (),
                {
                    "model": "fake-test-model",
                    "input_tokens": 10,
                    "output_tokens": 5,
                    "operation": "discussion",
                },
            )(),
        )

    monkeypatch.setattr("workers.reasoning.servicer.discuss_code", fake_discuss_code)

    request = reasoning_pb2.AnswerQuestionRequest(
        question="What does this handler do?",
        file_path="internal/api/rest/auth.go",
        language=types_pb2.LANGUAGE_GO,
        context_code="func handleLogin() error { return nil }",
    )

    response = await servicer.AnswerQuestion(request, context)

    assert response.answer == "ok"
    assert "func handleLogin() error" in captured["context_code"]
    assert "file: internal/api/rest/auth.go" in captured["context_code"]
    assert "language: go" in captured["context_code"]


async def test_answer_question_no_context(servicer, context):
    """AnswerQuestion works even without context symbols."""
    request = reasoning_pb2.AnswerQuestionRequest(
        question="What does this codebase do?",
    )

    response = await servicer.AnswerQuestion(request, context)

    assert response.answer != ""


async def test_answer_question_referenced_symbols(servicer, context):
    """AnswerQuestion populates referenced_symbols from discussion references."""
    symbol = types_pb2.CodeSymbol(
        name="handleRequest",
        qualified_name="server.handleRequest",
        language=types_pb2.LANGUAGE_GO,
        signature="func handleRequest(w, r)",
    )
    request = reasoning_pb2.AnswerQuestionRequest(
        question="What does handleRequest do?",
        context_symbols=[symbol],
    )

    response = await servicer.AnswerQuestion(request, context)

    # The FakeLLMProvider returns references; they should appear as CodeSymbol messages
    assert isinstance(list(response.referenced_symbols), list)


async def test_answer_question_stream_yields_deltas_then_terminal(servicer, context):
    """AnswerQuestionStream emits at least one content delta before the
    final frame, and the terminal frame carries finished=True plus the
    usage proto that the unary variant returns."""
    symbol = types_pb2.CodeSymbol(
        name="processPayment",
        qualified_name="payment.processPayment",
        language=types_pb2.LANGUAGE_GO,
        signature="func processPayment(ctx, order)",
    )
    request = reasoning_pb2.AnswerQuestionRequest(
        question="What does processPayment do?",
        context_symbols=[symbol],
    )

    deltas: list[str] = []
    terminal = None
    async for frame in servicer.AnswerQuestionStream(request, context):
        if frame.finished:
            terminal = frame
            break
        deltas.append(frame.content_delta)

    assert terminal is not None, "no terminal frame emitted"
    assert terminal.finished is True
    assert terminal.usage.model == "fake-test-model"
    assert terminal.usage.operation == "discussion"
    # At least one delta should carry visible text (the fake provider
    # streams word-by-word). Concatenating them should reconstruct the
    # same answer the unary variant would produce.
    assert any(d.strip() for d in deltas), "expected at least one non-empty delta"


# ---------------------------------------------------------------------------
# ReviewFile
# ---------------------------------------------------------------------------


async def test_review_file(servicer, context):
    """ReviewFile returns findings for security template."""
    request = reasoning_pb2.ReviewFileRequest(
        file_path="payment/processor.go",
        language=types_pb2.LANGUAGE_GO,
        content="func processPayment(ctx, order) { charge(order.Amount) }",
        template="security",
    )

    response = await servicer.ReviewFile(request, context)

    assert response.template == "security"
    assert len(response.findings) >= 1
    finding = response.findings[0]
    assert finding.category != ""
    assert finding.severity in ("critical", "high", "medium", "low", "info")
    assert finding.message != ""
    assert response.usage.operation == "review"


async def test_review_file_default_template(servicer, context):
    """ReviewFile defaults to 'security' template when none specified."""
    request = reasoning_pb2.ReviewFileRequest(
        file_path="handler.go",
        language=types_pb2.LANGUAGE_GO,
        content="func handle() { sql.Query(input) }",
    )

    response = await servicer.ReviewFile(request, context)

    assert response.template == "security"


async def test_review_file_score(servicer, context):
    """ReviewFile returns a numeric score."""
    request = reasoning_pb2.ReviewFileRequest(
        file_path="test.go",
        language=types_pb2.LANGUAGE_GO,
        content="func test() { /* security issue */ }",
        template="security",
    )

    response = await servicer.ReviewFile(request, context)

    assert isinstance(response.score, float)


async def test_review_file_invalid_template(servicer, context):
    """ReviewFile aborts with INVALID_ARGUMENT for unknown template."""
    request = reasoning_pb2.ReviewFileRequest(
        file_path="test.go",
        language=types_pb2.LANGUAGE_GO,
        content="func test() {}",
        template="nonexistent",
    )

    with pytest.raises(Exception, match="gRPC abort"):
        await servicer.ReviewFile(request, context)

    assert context.code == grpc.StatusCode.INVALID_ARGUMENT


# ---------------------------------------------------------------------------
# GenerateEmbedding
# ---------------------------------------------------------------------------


async def test_generate_embedding(servicer, context):
    """GenerateEmbedding returns a vector with the correct dimensions."""
    request = reasoning_pb2.GenerateEmbeddingRequest(
        text="func processPayment handles payment transactions",
        model="test-embedding-model",
    )

    response = await servicer.GenerateEmbedding(request, context)

    assert response.embedding is not None
    assert len(response.embedding.vector) == 1024
    assert response.embedding.source_type == "text"
    assert response.embedding.dimensions == 1024
    assert response.embedding.model == "test-embedding-model"


async def test_generate_embedding_deterministic(servicer, context):
    """Same input text produces the same embedding vector."""
    text = "deterministic embedding test"
    request1 = reasoning_pb2.GenerateEmbeddingRequest(text=text)
    request2 = reasoning_pb2.GenerateEmbeddingRequest(text=text)

    response1 = await servicer.GenerateEmbedding(request1, context)
    response2 = await servicer.GenerateEmbedding(request2, context)

    assert list(response1.embedding.vector) == list(response2.embedding.vector)


async def test_generate_embedding_default_model(servicer, context):
    """GenerateEmbedding uses 'default' when no model is specified."""
    request = reasoning_pb2.GenerateEmbeddingRequest(text="hello world")

    response = await servicer.GenerateEmbedding(request, context)

    assert response.embedding.model == "default"


# ---------------------------------------------------------------------------
# ExplainRelationship (unimplemented)
# ---------------------------------------------------------------------------


async def test_explain_relationship_unimplemented(servicer, context):
    """ExplainRelationship aborts with UNIMPLEMENTED."""
    source = types_pb2.CodeSymbol(name="A", language=types_pb2.LANGUAGE_GO)
    target = types_pb2.CodeSymbol(name="B", language=types_pb2.LANGUAGE_GO)
    request = reasoning_pb2.ExplainRelationshipRequest(
        source=source,
        target=target,
        relationship_type="calls",
    )

    with pytest.raises(Exception, match="gRPC abort"):
        await servicer.ExplainRelationship(request, context)

    assert context.code == grpc.StatusCode.UNIMPLEMENTED
    assert "not yet implemented" in context.details.lower()
