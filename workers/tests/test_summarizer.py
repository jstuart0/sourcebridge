"""Tests for the multi-level summarizer."""

import pytest

from workers.common.llm.fake import FakeLLMProvider
from workers.reasoning.summarizer import summarize_file, summarize_function, summarize_module


@pytest.mark.asyncio
async def test_function_summary_structure():
    """Summarizer returns structured JSON with all required fields."""
    provider = FakeLLMProvider()
    summary, usage = await summarize_function(
        provider, "processPayment", "go",
        "func processPayment(ctx context.Context, order Order) (Receipt, error) { ... }",
        "// Processes a payment transaction",
    )
    assert summary.purpose != ""
    assert isinstance(summary.inputs, list)
    assert isinstance(summary.outputs, list)
    assert isinstance(summary.dependencies, list)
    assert isinstance(summary.side_effects, list)
    assert isinstance(summary.risks, list)
    assert 0 <= summary.confidence <= 1
    assert summary.level == "function"
    assert summary.entity_name == "processPayment"


@pytest.mark.asyncio
async def test_function_summary_content():
    """Summary purpose contains relevant content."""
    provider = FakeLLMProvider()
    summary, _ = await summarize_function(
        provider, "processPayment", "go",
        "func processPayment(ctx, order) { validate(order); charge(order) }",
    )
    assert "payment" in summary.purpose.lower()


@pytest.mark.asyncio
async def test_function_summary_usage_tracking():
    """Summary returns usage record with token counts."""
    provider = FakeLLMProvider()
    _, usage = await summarize_function(provider, "test", "go", "func test() {}")
    assert usage.operation == "summary"
    assert usage.model == "fake-test-model"
    assert usage.input_tokens > 0
    assert usage.output_tokens > 0


@pytest.mark.asyncio
async def test_file_summary():
    """File-level summary returns structured result."""
    provider = FakeLLMProvider()
    summary, usage = await summarize_file(
        provider, "main.go", "go", ["StartServer", "handleRequest", "main"]
    )
    assert summary.purpose != ""
    assert summary.level == "file"
    assert summary.entity_name == "main.go"
    assert usage.operation == "summary"


@pytest.mark.asyncio
async def test_module_summary():
    """Module-level summary returns structured result."""
    provider = FakeLLMProvider()
    summary, usage = await summarize_module(
        provider, "payment", ["processor.go", "types.go"], ["ProcessPayment", "Refund"]
    )
    assert summary.purpose != ""
    assert summary.level == "module"
    assert summary.entity_name == "payment"
    assert usage.operation == "summary"


@pytest.mark.asyncio
async def test_content_hash_set():
    """Summary has a non-empty content hash."""
    provider = FakeLLMProvider()
    summary, _ = await summarize_function(provider, "test", "go", "func test() {}")
    assert summary.content_hash != ""
