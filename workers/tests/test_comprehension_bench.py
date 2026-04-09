# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Tests for the OSS-safe comprehension benchmark runner."""

from __future__ import annotations

import json
from unittest.mock import patch

import pytest

from workers.benchmarks.run_comprehension_bench import (
    _effective_provider_mode,
    _load_manifest,
    _run_case,
    _sanitize_live_error,
)
from workers.common.llm.fake import FakeLLMProvider


def test_benchmark_manifest_loads_cases() -> None:
    cases = _load_manifest()
    assert len(cases) >= 4
    assert {case["artifact_type"] for case in cases} >= {
        "cliff_notes",
        "learning_path",
        "code_tour",
        "workflow_story",
    }


@pytest.mark.asyncio
async def test_benchmark_case_runs_successfully() -> None:
    case = {
        "id": "fixture_cliff_notes_fake",
        "corpus_id": "multi-lang-repo-fixture",
        "artifact_type": "cliff_notes",
        "provider_mode": "fake",
        "repository_name": "multi-lang-repo",
        "audience": "developer",
        "depth": "medium",
        "scope_type": "repository",
        "scope_path": "",
        "expected_checks": [
            "cliff_notes_required_sections",
            "cliff_notes_has_evidence",
        ],
    }

    result = await _run_case(case)

    assert result.success is True
    assert result.metrics["section_count"] >= 7
    assert result.checks["cliff_notes_required_sections"] is True
    assert result.input_tokens > 0


def test_benchmark_result_serializes_to_json() -> None:
    payload = {
        "case_id": "fixture_cliff_notes_fake",
        "corpus_id": "multi-lang-repo-fixture",
        "artifact_type": "cliff_notes",
        "provider_mode": "fake",
        "provider_name": "fake",
        "model_id": "fake-test-model",
        "success": True,
        "duration_ms": 10,
        "input_tokens": 20,
        "output_tokens": 30,
        "error": None,
        "checks": {"cliff_notes_required_sections": True},
        "metrics": {"section_count": 7},
    }
    encoded = json.dumps(payload)
    assert "fixture_cliff_notes_fake" in encoded


def test_provider_mode_override_applies() -> None:
    case = {"provider_mode": "fake"}

    assert _effective_provider_mode(case, None) == "fake"
    assert _effective_provider_mode(case, "manifest") == "fake"
    assert _effective_provider_mode(case, "live") == "live"


def test_live_errors_are_sanitized() -> None:
    assert _sanitize_live_error(RuntimeError("dial tcp 10.0.0.5:11434: connect refused")) == (
        "RuntimeError: live provider run failed"
    )


@pytest.mark.asyncio
async def test_live_provider_mode_uses_configured_provider() -> None:
    case = {
        "id": "fixture_cliff_notes_live",
        "corpus_id": "multi-lang-repo-fixture",
        "artifact_type": "cliff_notes",
        "provider_mode": "fake",
        "repository_name": "multi-lang-repo",
        "audience": "developer",
        "depth": "medium",
        "scope_type": "repository",
        "scope_path": "",
        "expected_checks": [
            "cliff_notes_required_sections",
            "cliff_notes_has_evidence",
        ],
    }

    with patch("workers.benchmarks.run_comprehension_bench.create_llm_provider", return_value=FakeLLMProvider()):
        result = await _run_case(case, provider_mode_override="live")

    assert result.provider_mode == "live"
    assert result.provider_name
    assert result.model_id
    assert result.success is True
