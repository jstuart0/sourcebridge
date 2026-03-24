"""Tests for the code discussion mode."""

import pytest

from workers.common.llm.fake import FakeLLMProvider
from workers.reasoning.discussion import discuss_code


@pytest.mark.asyncio
async def test_discussion_returns_answer():
    """Discussion returns answer + references + related_requirements."""
    provider = FakeLLMProvider()
    answer, usage = await discuss_code(
        provider,
        "What does processPayment do?",
        "func processPayment(ctx, order) { validate(order); charge(order) }",
    )
    assert answer.answer != ""
    assert isinstance(answer.references, list)
    assert isinstance(answer.related_requirements, list)
    assert usage.operation == "discussion"


@pytest.mark.asyncio
async def test_discussion_answer_content():
    """Answer contains relevant content about the question."""
    provider = FakeLLMProvider()
    answer, _ = await discuss_code(
        provider,
        "What does processPayment do?",
        "func processPayment(ctx, order) { validate(order); charge(order) }",
    )
    assert "payment" in answer.answer.lower()


@pytest.mark.asyncio
async def test_discussion_references():
    """Answer includes code references."""
    provider = FakeLLMProvider()
    answer, _ = await discuss_code(
        provider,
        "What does this function do?",
        "func handleRequest(w, r) { parse(r); respond(w) }",
    )
    assert isinstance(answer.references, list)


@pytest.mark.asyncio
async def test_discussion_usage_tracking():
    """Discussion returns usage record."""
    provider = FakeLLMProvider()
    _, usage = await discuss_code(provider, "question?", "code")
    assert usage.model == "fake-test-model"
    assert usage.input_tokens > 0
