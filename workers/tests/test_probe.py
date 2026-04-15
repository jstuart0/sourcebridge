# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Tests for the model capability probe."""

import json

import pytest

from workers.common.llm.probe import ProbeResult, _grade_response, probe_model
from workers.common.llm.provider import LLMResponse


class FakeProvider:
    """Test double for LLMProvider."""

    def __init__(self, response_text: str = "", error: Exception | None = None):
        self._response_text = response_text
        self._error = error

    async def complete(self, prompt, *, system="", max_tokens=4096, temperature=0.0, model=None):
        if self._error:
            raise self._error
        return LLMResponse(
            content=self._response_text,
            model=model or "test-model",
            input_tokens=100,
            output_tokens=50,
            provider_name="test",
        )

    async def stream(self, prompt, *, system="", max_tokens=4096, temperature=0.0, model=None):
        raise NotImplementedError


# --- _grade_response tests ---


def test_grade_perfect_response():
    """Perfect JSON response gets high/prompted."""
    response = json.dumps(
        {
            "echo": "PROBE_MARKER_7f3a",
            "count_to_five": [1, 2, 3, 4, 5],
            "reverse": "edcba",
            "classify": "positive",
        }
    )
    instruction, json_mode = _grade_response(response)
    assert instruction == "high"
    assert json_mode == "prompted"


def test_grade_perfect_with_markdown_fences():
    """JSON wrapped in markdown fences still grades high."""
    inner = json.dumps(
        {
            "echo": "PROBE_MARKER_7f3a",
            "count_to_five": [1, 2, 3, 4, 5],
            "reverse": "edcba",
            "classify": "positive",
        }
    )
    response = f"```json\n{inner}\n```"
    instruction, json_mode = _grade_response(response)
    assert instruction == "high"
    assert json_mode == "prompted"


def test_grade_partial_response():
    """Two out of four correct gets medium."""
    response = json.dumps(
        {
            "echo": "PROBE_MARKER_7f3a",
            "count_to_five": [1, 2, 3, 4, 5],
            "reverse": "wrong",
            "classify": "wrong",
        }
    )
    instruction, json_mode = _grade_response(response)
    assert instruction == "medium"
    assert json_mode == "prompted"


def test_grade_one_correct():
    """One out of four correct gets low."""
    response = json.dumps(
        {
            "echo": "wrong",
            "count_to_five": [1],
            "reverse": "wrong",
            "classify": "positive",
        }
    )
    instruction, json_mode = _grade_response(response)
    assert instruction == "low"
    assert json_mode == "prompted"


def test_grade_invalid_json():
    """Non-JSON response gets low/none."""
    instruction, json_mode = _grade_response("I can't do JSON sorry")
    assert instruction == "low"
    assert json_mode == "none"


def test_grade_empty_response():
    """Empty response gets low/none."""
    instruction, json_mode = _grade_response("")
    assert instruction == "low"
    assert json_mode == "none"


def test_grade_json_array():
    """JSON array (not object) gets low/prompted."""
    instruction, json_mode = _grade_response("[1, 2, 3]")
    assert instruction == "low"
    assert json_mode == "prompted"


# --- probe_model tests ---


@pytest.mark.asyncio
async def test_probe_success():
    """Successful probe returns graded capabilities."""
    response = json.dumps(
        {
            "echo": "PROBE_MARKER_7f3a",
            "count_to_five": [1, 2, 3, 4, 5],
            "reverse": "edcba",
            "classify": "positive",
        }
    )
    provider = FakeProvider(response_text=response)
    result = await probe_model(provider, "test-model", "test-provider")

    assert isinstance(result, ProbeResult)
    assert result.success is True
    assert result.capabilities.instruction_following == "high"
    assert result.capabilities.json_mode == "prompted"
    assert result.capabilities.source == "probed"
    assert result.capabilities.model_id == "test-model"
    assert result.capabilities.provider == "test-provider"
    assert result.latency_ms > 0


@pytest.mark.asyncio
async def test_probe_failure():
    """Failed probe returns conservative capabilities."""
    provider = FakeProvider(error=RuntimeError("connection refused"))
    result = await probe_model(provider, "bad-model", "test")

    assert result.success is False
    assert "connection refused" in result.error
    assert result.capabilities.instruction_following == "low"
    assert result.capabilities.source == "probed"


@pytest.mark.asyncio
async def test_probe_weak_model():
    """Model that returns garbage JSON gets low grades."""
    provider = FakeProvider(response_text="Sure! Here's the JSON:\n{invalid json}")
    result = await probe_model(provider, "weak-model", "ollama")

    assert result.success is True
    assert result.capabilities.instruction_following == "low"
    assert result.capabilities.json_mode == "none"
