"""Interactive code discussion mode."""

from __future__ import annotations

import json

from workers.common.llm.provider import LLMProvider
from workers.reasoning.prompts.discussion import DISCUSSION_SYSTEM, build_discussion_prompt
from workers.reasoning.types import DiscussionAnswer, LLMUsageRecord


def _parse_discussion(raw: str) -> DiscussionAnswer:
    """Parse LLM response into a DiscussionAnswer."""
    from workers.common.llm.parse import parse_json_response, strip_llm_wrapping

    data = parse_json_response(raw)
    if data is None or not isinstance(data, dict):
        return DiscussionAnswer(answer=strip_llm_wrapping(raw))

    refs = data.get("references", [])
    if isinstance(refs, list):
        refs = [r for r in refs if isinstance(r, str) and r.strip()]
    else:
        refs = []

    return DiscussionAnswer(
        answer=data.get("answer", ""),
        references=refs,
        related_requirements=data.get("related_requirements", []),
    )


async def discuss_code(
    provider: LLMProvider,
    question: str,
    context_code: str,
    context_metadata: str = "",
    model_override: str | None = None,
) -> tuple[DiscussionAnswer, LLMUsageRecord]:
    """Answer a question about code."""
    prompt = build_discussion_prompt(question, context_code, context_metadata)

    response = await provider.complete(prompt, system=DISCUSSION_SYSTEM, temperature=0.2, model=model_override)

    answer = _parse_discussion(response.content)

    usage = LLMUsageRecord(
        provider="llm",
        model=response.model,
        input_tokens=response.input_tokens,
        output_tokens=response.output_tokens,
        operation="discussion",
        entity_name="",
    )

    return answer, usage
