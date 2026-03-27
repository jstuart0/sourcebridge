# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Whole-system explanation using LLM."""

from __future__ import annotations

from dataclasses import dataclass

import structlog

from workers.common.llm.provider import LLMProvider, LLMResponse
from workers.knowledge.prompts.explain_system import (
    EXPLAIN_SYSTEM_SYSTEM,
    build_explain_system_prompt,
)
from workers.reasoning.types import LLMUsageRecord

log = structlog.get_logger()


@dataclass
class ExplainResult:
    """Transient whole-system explanation result."""

    explanation: str  # markdown


async def explain_system(
    provider: LLMProvider,
    repository_name: str,
    audience: str,
    question: str,
    snapshot_json: str,
    depth: str = "medium",
    model_override: str | None = None,
) -> tuple[ExplainResult, LLMUsageRecord]:
    """Generate a whole-system explanation from a repository snapshot."""
    prompt = build_explain_system_prompt(repository_name, audience, question, snapshot_json, depth)

    response: LLMResponse = await provider.complete(
        prompt, system=EXPLAIN_SYSTEM_SYSTEM, temperature=0.0, model=model_override,
    )

    usage = LLMUsageRecord(
        provider="llm",
        model=response.model,
        input_tokens=response.input_tokens,
        output_tokens=response.output_tokens,
        operation="explain_system",
        entity_name=repository_name,
    )

    return ExplainResult(explanation=response.content), usage
