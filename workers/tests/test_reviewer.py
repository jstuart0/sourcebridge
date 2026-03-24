"""Tests for the structured code reviewer."""

import pytest

from workers.common.llm.fake import FakeLLMProvider
from workers.reasoning.reviewer import review_code


@pytest.mark.asyncio
async def test_security_review_returns_findings():
    """Security review returns findings array with required fields."""
    provider = FakeLLMProvider()
    result, usage = await review_code(
        provider, "processor.go", "go",
        "func processPayment(ctx, order) { charge(order.Amount) }",
        template="security",
    )
    assert result.template == "security"
    assert len(result.findings) >= 1
    finding = result.findings[0]
    assert finding.category != ""
    assert finding.severity in ("critical", "high", "medium", "low", "info")
    assert finding.message != ""
    assert usage.operation == "review"


@pytest.mark.asyncio
async def test_all_templates_valid():
    """All 5 review templates produce valid results."""
    provider = FakeLLMProvider()
    for template in ("security", "solid", "performance", "reliability", "maintainability"):
        result, _ = await review_code(
            provider, "test.go", "go", "func test() {}",
            template=template,
        )
        assert result.template == template
        assert isinstance(result.findings, list)


@pytest.mark.asyncio
async def test_review_findings_have_file_path():
    """Findings include file path."""
    provider = FakeLLMProvider()
    result, _ = await review_code(
        provider, "processor.go", "go",
        "func process() { sql.Query(userInput) }",
        template="security",
    )
    if result.findings:
        assert result.findings[0].file_path != ""


@pytest.mark.asyncio
async def test_review_findings_have_line_numbers():
    """Findings include start_line and end_line."""
    provider = FakeLLMProvider()
    result, _ = await review_code(
        provider, "processor.go", "go",
        "func process() { charge(amount) }",
        template="security",
    )
    if result.findings:
        assert result.findings[0].start_line >= 0
        assert result.findings[0].end_line >= 0


@pytest.mark.asyncio
async def test_invalid_template_raises():
    """Invalid template name raises ValueError."""
    provider = FakeLLMProvider()
    with pytest.raises(ValueError, match="Unknown review template"):
        await review_code(provider, "test.go", "go", "func test() {}", template="invalid")


@pytest.mark.asyncio
async def test_review_score():
    """Review returns a numeric score."""
    provider = FakeLLMProvider()
    result, _ = await review_code(
        provider, "test.go", "go", "func test() { /* security issue */ }",
        template="security",
    )
    assert isinstance(result.score, float)
