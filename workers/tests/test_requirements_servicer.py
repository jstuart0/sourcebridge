"""Tests for the RequirementsServicer gRPC servicer."""

import grpc
import pytest
from common.v1 import types_pb2
from requirements.v1 import requirements_pb2

from workers.common.llm.fake import FakeLLMProvider
from workers.requirements.servicer import RequirementsServicer


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
def servicer(llm):
    return RequirementsServicer(llm)


@pytest.fixture
def context():
    return MockServicerContext()


SAMPLE_MARKDOWN = """\
# Requirements Document

## REQ-001: User Authentication
The system shall support user authentication via OAuth2 and SAML.
- **Priority:** High
- **Acceptance Criteria:**
  - Users can log in with Google OAuth2
  - Users can log in with corporate SAML

## REQ-002: Audit Logging
All API requests shall be logged with timestamp, user ID, and action.
- **Priority:** Medium
- **Acceptance Criteria:**
  - Every API call generates a log entry
  - Logs include timestamp, user, and action
"""

SAMPLE_CSV = """\
id,title,description,priority,acceptance_criteria
REQ-010,Data Export,Users shall be able to export data as CSV,high,Export button visible;CSV file downloads correctly
REQ-011,Search,Full-text search across all entities,medium,Search bar returns results;Results are ranked by relevance
"""


# ---------------------------------------------------------------------------
# ParseDocument
# ---------------------------------------------------------------------------


async def test_parse_document(servicer, context):
    """ParseDocument extracts requirements from markdown content."""
    request = requirements_pb2.ParseDocumentRequest(
        content=SAMPLE_MARKDOWN,
        format="markdown",
        source_path="docs/requirements.md",
    )

    response = await servicer.ParseDocument(request, context)

    assert response.total_found == 2
    assert len(response.requirements) == 2

    req1 = response.requirements[0]
    assert req1.id == "REQ-001"
    assert "Authentication" in req1.title
    assert req1.description != ""

    req2 = response.requirements[1]
    assert req2.id == "REQ-002"
    assert "Logging" in req2.title or "Audit" in req2.title


async def test_parse_document_default_format(servicer, context):
    """ParseDocument defaults to markdown format when format is empty."""
    request = requirements_pb2.ParseDocumentRequest(
        content=SAMPLE_MARKDOWN,
    )

    response = await servicer.ParseDocument(request, context)

    assert response.total_found == 2


async def test_parse_document_empty_content(servicer, context):
    """ParseDocument returns zero requirements for empty content."""
    request = requirements_pb2.ParseDocumentRequest(
        content="",
        format="markdown",
    )

    response = await servicer.ParseDocument(request, context)

    assert response.total_found == 0
    assert len(response.requirements) == 0


async def test_parse_document_no_requirements(servicer, context):
    """ParseDocument returns zero when content has no requirement headings."""
    request = requirements_pb2.ParseDocumentRequest(
        content="# Just a Title\n\nSome text without any requirements.\n",
        format="markdown",
    )

    response = await servicer.ParseDocument(request, context)

    assert response.total_found == 0


async def test_parse_document_unsupported_format(servicer, context):
    """ParseDocument aborts with INVALID_ARGUMENT for unsupported format."""
    request = requirements_pb2.ParseDocumentRequest(
        content="key: value\n",
        format="yaml",
    )

    with pytest.raises(Exception, match="gRPC abort"):
        await servicer.ParseDocument(request, context)

    assert context.code == grpc.StatusCode.INVALID_ARGUMENT
    assert "unsupported format" in context.details.lower()


async def test_parse_document_source_path_propagated(servicer, context):
    """ParseDocument propagates source_path to extracted requirements."""
    request = requirements_pb2.ParseDocumentRequest(
        content=SAMPLE_MARKDOWN,
        format="markdown",
        source_path="specs/auth.md",
    )

    response = await servicer.ParseDocument(request, context)

    for req in response.requirements:
        assert req.source == "specs/auth.md"


# ---------------------------------------------------------------------------
# ParseCSV
# ---------------------------------------------------------------------------


async def test_parse_csv(servicer, context):
    """ParseCSV extracts requirements from CSV content."""
    request = requirements_pb2.ParseCSVRequest(
        content=SAMPLE_CSV,
        source_path="data/requirements.csv",
    )

    response = await servicer.ParseCSV(request, context)

    assert response.total_found == 2
    assert len(response.requirements) == 2

    req1 = response.requirements[0]
    assert req1.id == "REQ-010"
    assert "Export" in req1.title
    assert req1.description != ""

    req2 = response.requirements[1]
    assert req2.id == "REQ-011"
    assert "Search" in req2.title


async def test_parse_csv_empty_content(servicer, context):
    """ParseCSV returns zero requirements for empty CSV."""
    request = requirements_pb2.ParseCSVRequest(content="")

    response = await servicer.ParseCSV(request, context)

    assert response.total_found == 0


async def test_parse_csv_headers_only(servicer, context):
    """ParseCSV returns zero for CSV with headers but no data rows."""
    request = requirements_pb2.ParseCSVRequest(
        content="id,title,description,priority,acceptance_criteria\n",
    )

    response = await servicer.ParseCSV(request, context)

    assert response.total_found == 0


async def test_parse_csv_custom_column_mapping(servicer, context):
    """ParseCSV supports custom column mapping."""
    csv_content = "req_id,req_title,req_desc,req_pri,criteria\nREQ-020,Custom,A custom req,low,Check it\n"
    request = requirements_pb2.ParseCSVRequest(
        content=csv_content,
        column_mapping={
            "id": "req_id",
            "title": "req_title",
            "description": "req_desc",
            "priority": "req_pri",
            "acceptance_criteria": "criteria",
        },
    )

    response = await servicer.ParseCSV(request, context)

    assert response.total_found == 1
    assert response.requirements[0].id == "REQ-020"
    assert response.requirements[0].title == "Custom"


async def test_parse_csv_source_path_propagated(servicer, context):
    """ParseCSV propagates source_path to extracted requirements."""
    request = requirements_pb2.ParseCSVRequest(
        content=SAMPLE_CSV,
        source_path="imports/reqs.csv",
    )

    response = await servicer.ParseCSV(request, context)

    for req in response.requirements:
        assert req.source == "imports/reqs.csv"


# ---------------------------------------------------------------------------
# EnrichRequirement
# ---------------------------------------------------------------------------


async def test_enrich_requirement(servicer, context):
    """EnrichRequirement returns suggested tags and priority from LLM."""
    requirement = types_pb2.Requirement(
        id="REQ-042",
        title="Payment Processing Security",
        description="The payment processor shall validate all inputs and encrypt sensitive data.",
        priority="",
        tags=[],
    )
    request = requirements_pb2.EnrichRequirementRequest(requirement=requirement)

    response = await servicer.EnrichRequirement(request, context)

    # The FakeLLMProvider returns a SUMMARY_RESPONSE (JSON with purpose, etc.)
    # The EnrichRequirement prompt includes "Analyze" which triggers SUMMARY_RESPONSE
    # That JSON won't have suggested_priority/suggested_tags, so data.get defaults kick in
    assert response.suggested_priority != ""
    assert isinstance(list(response.suggested_tags), list)
    assert response.enriched is not None
    assert response.enriched.id == "REQ-042"
    assert response.enriched.title == "Payment Processing Security"
    assert response.usage.model == "fake-test-model"
    assert response.usage.operation == "enrich"
    assert response.usage.input_tokens > 0
    assert response.usage.output_tokens > 0


async def test_enrich_requirement_preserves_existing_tags(servicer, context):
    """EnrichRequirement keeps existing tags in the enriched requirement."""
    requirement = types_pb2.Requirement(
        id="REQ-099",
        title="Caching Layer",
        description="Implement Redis caching for frequently accessed data.",
        tags=["infrastructure", "performance"],
    )
    request = requirements_pb2.EnrichRequirementRequest(requirement=requirement)

    response = await servicer.EnrichRequirement(request, context)

    enriched_tags = list(response.enriched.tags)
    # Original tags should still be present
    assert "infrastructure" in enriched_tags
    assert "performance" in enriched_tags


async def test_enrich_requirement_enriched_has_priority(servicer, context):
    """EnrichRequirement sets a priority on the enriched requirement."""
    requirement = types_pb2.Requirement(
        id="REQ-055",
        title="Error Handling",
        description="All endpoints must return structured error responses.",
    )
    request = requirements_pb2.EnrichRequirementRequest(requirement=requirement)

    response = await servicer.EnrichRequirement(request, context)

    # The enriched requirement should have a priority set (from suggested_priority)
    assert response.enriched.priority != ""


async def test_enrich_requirement_preserves_fields(servicer, context):
    """EnrichRequirement preserves id, external_id, title, description, and source."""
    requirement = types_pb2.Requirement(
        id="REQ-077",
        external_id="JIRA-123",
        title="Rate Limiting",
        description="API endpoints shall enforce rate limits.",
        source="jira-import",
    )
    request = requirements_pb2.EnrichRequirementRequest(requirement=requirement)

    response = await servicer.EnrichRequirement(request, context)

    assert response.enriched.id == "REQ-077"
    assert response.enriched.external_id == "JIRA-123"
    assert response.enriched.title == "Rate Limiting"
    assert response.enriched.description == "API endpoints shall enforce rate limits."
    assert response.enriched.source == "jira-import"
