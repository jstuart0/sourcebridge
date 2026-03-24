"""Interactive code discussion mode."""

from __future__ import annotations

import json

from workers.common.llm.provider import LLMProvider
from workers.reasoning.prompts.discussion import DISCUSSION_SYSTEM, build_discussion_prompt
from workers.reasoning.types import DiscussionAnswer, LLMUsageRecord


def _parse_discussion(raw: str) -> DiscussionAnswer:
    """Parse LLM response into a DiscussionAnswer."""
    try:
        data = json.loads(raw)
    except json.JSONDecodeError:
        if "```" in raw:
            start = raw.index("```") + 3
            if raw[start:].startswith("json"):
                start += 4
            end = raw.index("```", start)
            data = json.loads(raw[start:end].strip())
        else:
            return DiscussionAnswer(answer=raw.strip())

    return DiscussionAnswer(
        answer=data.get("answer", ""),
        references=data.get("references", []),
        related_requirements=data.get("related_requirements", []),
    )


async def discuss_code(
    provider: LLMProvider,
    question: str,
    context_code: str,
    context_metadata: str = "",
) -> tuple[DiscussionAnswer, LLMUsageRecord]:
    """Answer a question about code."""
    prompt = build_discussion_prompt(question, context_code, context_metadata)

    response = await provider.complete(prompt, system=DISCUSSION_SYSTEM, temperature=0.2)

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
