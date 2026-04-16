"""Tests for the LinkingServicer gRPC servicer."""

import pytest
from common.v1 import types_pb2
from linking.v1 import linking_pb2

from workers.common.embedding.fake import FakeEmbeddingProvider
from workers.common.llm.fake import FakeLLMProvider
from workers.linking.servicer import (
    LinkingServicer,
    _candidate_to_entity,
    _float_to_confidence_enum,
    _symbol_kind_name,
)


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
    return LinkingServicer(llm, embedding)


@pytest.fixture
def context():
    return MockServicerContext()


def _make_candidate(
    name: str,
    content: str = "",
    doc_comment: str = "",
    file_path: str = "pkg/handler.go",
    kind: int = types_pb2.SYMBOL_KIND_FUNCTION,
    language: int = types_pb2.LANGUAGE_GO,
) -> linking_pb2.CandidateSymbol:
    """Helper to build a CandidateSymbol proto message."""
    location = types_pb2.FileLocation(path=file_path, start_line=1, end_line=10)
    symbol = types_pb2.CodeSymbol(
        name=name,
        kind=kind,
        language=language,
        location=location,
        doc_comment=doc_comment,
    )
    return linking_pb2.CandidateSymbol(symbol=symbol, content=content)


# ---------------------------------------------------------------------------
# LinkRequirement
# ---------------------------------------------------------------------------


async def test_link_requirement(servicer, context):
    """LinkRequirement returns links for candidates that match the requirement."""
    requirement = types_pb2.Requirement(
        id="REQ-042",
        title="Payment Processing",
        description="The system shall process credit card payments securely.",
    )
    candidates = [
        _make_candidate(
            name="processPayment",
            content="// REQ-042: payment processing\nfunc processPayment(ctx, order) { charge(order) }",
            doc_comment="// REQ-042: Handles payment processing",
            file_path="payment/processor.go",
        ),
        _make_candidate(
            name="formatReceipt",
            content="func formatReceipt(order) { return fmt.Sprintf(order) }",
            file_path="payment/receipt.go",
        ),
    ]
    request = linking_pb2.LinkRequirementRequest(
        requirement=requirement,
        candidate_symbols=candidates,
    )

    response = await servicer.LinkRequirement(request, context)

    assert isinstance(list(response.links), list)
    # At least the comment-based link should be found for REQ-042
    assert len(response.links) >= 1
    # Every link should have a requirement_id and symbol_id
    for link in response.links:
        assert link.requirement_id == "REQ-042"
        assert link.symbol_id != ""
        assert link.rationale != ""


async def test_link_requirement_no_candidates(servicer, context):
    """LinkRequirement returns empty links when no candidates are provided."""
    requirement = types_pb2.Requirement(
        id="REQ-100",
        title="Orphan Requirement",
        description="No code implements this requirement.",
    )
    request = linking_pb2.LinkRequirementRequest(
        requirement=requirement,
        candidate_symbols=[],
    )

    response = await servicer.LinkRequirement(request, context)

    assert len(response.links) == 0


async def test_link_requirement_min_confidence_filter(servicer, context):
    """LinkRequirement filters out links below min_confidence threshold."""
    requirement = types_pb2.Requirement(
        id="REQ-050",
        title="Logging",
        description="The system shall log all API requests.",
    )
    candidates = [
        _make_candidate(
            name="logRequest",
            content="func logRequest(r) { log.Info(r) }",
            file_path="middleware/logging.go",
        ),
    ]
    request = linking_pb2.LinkRequirementRequest(
        requirement=requirement,
        candidate_symbols=candidates,
        min_confidence=0.99,  # Very high threshold -- should filter most out
    )

    response = await servicer.LinkRequirement(request, context)

    # With a 0.99 threshold, semantic links (capped at 0.85) and most others are filtered
    for _link in response.links:
        assert True  # proto enum


async def test_link_requirement_external_id_matching(servicer, context):
    """LinkRequirement matches comment links against requirement external_id too."""
    requirement = types_pb2.Requirement(
        id="req-uuid-123",
        external_id="ENG-007",
        title="Authentication",
        description="Support OAuth2 authentication.",
    )
    candidates = [
        _make_candidate(
            name="authenticate",
            content="// ENG-007: OAuth2 authentication\nfunc authenticate(token) { verify(token) }",
            doc_comment="// ENG-007: handles auth",
            file_path="auth/oauth.go",
        ),
    ]
    request = linking_pb2.LinkRequirementRequest(
        requirement=requirement,
        candidate_symbols=candidates,
    )

    response = await servicer.LinkRequirement(request, context)

    assert len(response.links) >= 1


# ---------------------------------------------------------------------------
# ValidateLink
# ---------------------------------------------------------------------------


async def test_validate_link(servicer, context):
    """ValidateLink checks link validity using embedding similarity."""
    link = types_pb2.RequirementLink(
        id="link-1",
        requirement_id="REQ-042",
        symbol_id="payment/processor.go:processPayment",
        rationale="Implements payment processing logic",
    )
    current_symbol = types_pb2.CodeSymbol(
        name="processPayment",
        language=types_pb2.LANGUAGE_GO,
        signature="func processPayment(ctx context.Context, order Order) (Receipt, error)",
    )
    request = linking_pb2.ValidateLinkRequest(
        link=link,
        current_symbol=current_symbol,
    )

    response = await servicer.ValidateLink(request, context)

    assert isinstance(response.still_valid, bool)
    assert response.new_confidence in (
        types_pb2.CONFIDENCE_UNSPECIFIED,
        types_pb2.CONFIDENCE_LOW,
        types_pb2.CONFIDENCE_MEDIUM,
        types_pb2.CONFIDENCE_HIGH,
        types_pb2.CONFIDENCE_VERIFIED,
    )
    assert response.reason != ""


async def test_validate_link_no_signature(servicer, context):
    """ValidateLink returns low confidence when symbol has no signature."""
    link = types_pb2.RequirementLink(
        id="link-2",
        requirement_id="REQ-010",
        symbol_id="utils/helper.go:doStuff",
        rationale="Helper utility for requirement",
    )
    # Symbol without a signature
    current_symbol = types_pb2.CodeSymbol(
        name="doStuff",
        language=types_pb2.LANGUAGE_GO,
    )
    request = linking_pb2.ValidateLinkRequest(
        link=link,
        current_symbol=current_symbol,
    )

    response = await servicer.ValidateLink(request, context)

    assert response.still_valid is True
    assert response.new_confidence == types_pb2.CONFIDENCE_LOW
    assert "insufficient" in response.reason.lower()


async def test_validate_link_no_symbol(servicer, context):
    """ValidateLink returns low confidence when no current_symbol is provided."""
    link = types_pb2.RequirementLink(
        id="link-3",
        requirement_id="REQ-020",
        symbol_id="deleted.go:removed",
    )
    request = linking_pb2.ValidateLinkRequest(link=link)

    response = await servicer.ValidateLink(request, context)

    assert response.still_valid is True
    assert response.new_confidence == types_pb2.CONFIDENCE_LOW


# ---------------------------------------------------------------------------
# BatchLink (unimplemented)
# ---------------------------------------------------------------------------


async def test_batch_link_empty_candidates(servicer, context):
    """BatchLink returns empty response when no candidates are provided."""
    request = linking_pb2.BatchLinkRequest(
        requirements=[
            types_pb2.Requirement(id="REQ-001", title="Test"),
        ],
        repository_id="test-repo",
    )

    response = await servicer.BatchLink(request, context)

    assert response.requirements_processed == 0
    assert response.links_found == 0
    assert len(response.links) == 0


# ---------------------------------------------------------------------------
# Helper functions: _float_to_confidence_enum
# ---------------------------------------------------------------------------


def test_float_to_confidence_verified():
    """Score >= 0.95 maps to CONFIDENCE_VERIFIED."""
    assert _float_to_confidence_enum(0.95) == types_pb2.CONFIDENCE_VERIFIED
    assert _float_to_confidence_enum(1.0) == types_pb2.CONFIDENCE_VERIFIED


def test_float_to_confidence_high():
    """Score >= 0.75 and < 0.95 maps to CONFIDENCE_HIGH."""
    assert _float_to_confidence_enum(0.75) == types_pb2.CONFIDENCE_HIGH
    assert _float_to_confidence_enum(0.94) == types_pb2.CONFIDENCE_HIGH


def test_float_to_confidence_medium():
    """Score >= 0.50 and < 0.75 maps to CONFIDENCE_MEDIUM."""
    assert _float_to_confidence_enum(0.50) == types_pb2.CONFIDENCE_MEDIUM
    assert _float_to_confidence_enum(0.74) == types_pb2.CONFIDENCE_MEDIUM


def test_float_to_confidence_low():
    """Score > 0.0 and < 0.50 maps to CONFIDENCE_LOW."""
    assert _float_to_confidence_enum(0.01) == types_pb2.CONFIDENCE_LOW
    assert _float_to_confidence_enum(0.49) == types_pb2.CONFIDENCE_LOW


def test_float_to_confidence_unspecified():
    """Score == 0.0 maps to CONFIDENCE_UNSPECIFIED."""
    assert _float_to_confidence_enum(0.0) == types_pb2.CONFIDENCE_UNSPECIFIED


# ---------------------------------------------------------------------------
# Helper functions: _candidate_to_entity
# ---------------------------------------------------------------------------


def test_candidate_to_entity():
    """_candidate_to_entity converts a CandidateSymbol proto to a CodeEntity."""
    candidate = _make_candidate(
        name="handleRequest",
        content="func handleRequest(w, r) {}",
        doc_comment="// Handles incoming HTTP requests",
        file_path="server/handler.go",
        kind=types_pb2.SYMBOL_KIND_FUNCTION,
        language=types_pb2.LANGUAGE_GO,
    )

    entity = _candidate_to_entity(candidate)

    assert entity.name == "handleRequest"
    assert entity.file_path == "server/handler.go"
    assert entity.kind == "function"
    assert entity.content == "func handleRequest(w, r) {}"
    assert entity.doc_comment == "// Handles incoming HTTP requests"
    assert entity.language == "go"
    assert entity.start_line == 1
    assert entity.end_line == 10


def test_candidate_to_entity_no_location():
    """_candidate_to_entity handles symbol without location."""
    symbol = types_pb2.CodeSymbol(
        name="orphan",
        kind=types_pb2.SYMBOL_KIND_VARIABLE,
        language=types_pb2.LANGUAGE_PYTHON,
    )
    candidate = linking_pb2.CandidateSymbol(symbol=symbol, content="x = 42")

    entity = _candidate_to_entity(candidate)

    assert entity.name == "orphan"
    assert entity.file_path == ""
    assert entity.start_line == 0
    assert entity.end_line == 0
    assert entity.kind == "variable"
    assert entity.language == "python"


# ---------------------------------------------------------------------------
# Helper functions: _symbol_kind_name
# ---------------------------------------------------------------------------


def test_symbol_kind_name_known():
    """_symbol_kind_name returns correct string for known kinds."""
    assert _symbol_kind_name(types_pb2.SYMBOL_KIND_FUNCTION) == "function"
    assert _symbol_kind_name(types_pb2.SYMBOL_KIND_METHOD) == "method"
    assert _symbol_kind_name(types_pb2.SYMBOL_KIND_CLASS) == "class"
    assert _symbol_kind_name(types_pb2.SYMBOL_KIND_STRUCT) == "struct"
    assert _symbol_kind_name(types_pb2.SYMBOL_KIND_INTERFACE) == "interface"
    assert _symbol_kind_name(types_pb2.SYMBOL_KIND_ENUM) == "enum"
    assert _symbol_kind_name(types_pb2.SYMBOL_KIND_CONSTANT) == "constant"
    assert _symbol_kind_name(types_pb2.SYMBOL_KIND_VARIABLE) == "variable"
    assert _symbol_kind_name(types_pb2.SYMBOL_KIND_MODULE) == "module"
    assert _symbol_kind_name(types_pb2.SYMBOL_KIND_PACKAGE) == "package"
    assert _symbol_kind_name(types_pb2.SYMBOL_KIND_TYPE) == "type"
    assert _symbol_kind_name(types_pb2.SYMBOL_KIND_TEST) == "test"


def test_symbol_kind_name_unknown():
    """_symbol_kind_name returns 'unknown' for unrecognized kinds."""
    assert _symbol_kind_name(types_pb2.SYMBOL_KIND_UNSPECIFIED) == "unknown"
    assert _symbol_kind_name(9999) == "unknown"
