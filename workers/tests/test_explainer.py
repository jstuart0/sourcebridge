"""Tests for the explanation generator."""

import pytest

from workers.common.llm.fake import FakeLLMProvider
from workers.reasoning.explainer import explain_code


@pytest.mark.asyncio
async def test_explain_returns_markdown():
    """Explainer returns markdown-formatted explanation."""
    provider = FakeLLMProvider()
    explanation, usage = await explain_code(
        provider,
        "processPayment",
        "go",
        "func processPayment(ctx, order) { validate(order); charge(order) }",
    )
    assert isinstance(explanation, str)
    assert len(explanation) > 0
    assert usage.operation == "explain"


@pytest.mark.asyncio
async def test_explain_non_empty():
    """Explanation is non-empty for valid code."""
    provider = FakeLLMProvider()
    explanation, _ = await explain_code(provider, "test", "go", "func test() {}")
    assert len(explanation) > 10


@pytest.mark.asyncio
async def test_explain_usage_tracking():
    """Explainer returns usage record."""
    provider = FakeLLMProvider()
    _, usage = await explain_code(provider, "test", "go", "func test() {}")
    assert usage.model == "fake-test-model"
    assert usage.entity_name == "test"
