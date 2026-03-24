"""Code explanation generator."""

from __future__ import annotations

from workers.common.llm.provider import LLMProvider
from workers.reasoning.prompts.explainer import EXPLAIN_SYSTEM, build_explain_prompt
from workers.reasoning.types import LLMUsageRecord


async def explain_code(
    provider: LLMProvider,
    name: str,
    language: str,
    content: str,
) -> tuple[str, LLMUsageRecord]:
    """Generate a markdown explanation of code."""
    prompt = build_explain_prompt(name, language, content)

    response = await provider.complete(prompt, system=EXPLAIN_SYSTEM, temperature=0.2)

    usage = LLMUsageRecord(
        provider="llm",
        model=response.model,
        input_tokens=response.input_tokens,
        output_tokens=response.output_tokens,
        operation="explain",
        entity_name=name,
    )

    return response.content.strip(), usage
