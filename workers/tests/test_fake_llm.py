"""Tests for the fake LLM provider."""

import json

import pytest

from workers.common.llm.fake import FakeLLMProvider


@pytest.mark.asyncio
async def test_fake_summary() -> None:
    """Test fake provider returns structured summary."""
    provider = FakeLLMProvider()
    response = await provider.complete("Summarize this function")
    data = json.loads(response.content)
    assert "purpose" in data
    assert "inputs" in data
    assert "outputs" in data
    assert response.model == "fake-test-model"
    assert response.input_tokens > 0


@pytest.mark.asyncio
async def test_fake_review() -> None:
    """Test fake provider returns review findings for review prompts."""
    provider = FakeLLMProvider()
    response = await provider.complete("Review this code for security issues")
    data = json.loads(response.content)
    assert "findings" in data
    assert len(data["findings"]) > 0
    assert data["findings"][0]["severity"] == "high"


@pytest.mark.asyncio
async def test_fake_discuss() -> None:
    """Test fake provider returns discussion response."""
    provider = FakeLLMProvider()
    response = await provider.complete("What does processPayment do?")
    data = json.loads(response.content)
    assert "answer" in data
    assert "references" in data


@pytest.mark.asyncio
async def test_fake_stream() -> None:
    """Test fake provider streams word by word."""
    provider = FakeLLMProvider()
    words = []
    async for word in provider.stream("Summarize this function"):
        words.append(word.strip())
    assert len(words) > 0
