"""Interactive code discussion mode."""

from __future__ import annotations

from collections.abc import AsyncIterator

from workers.common.llm.provider import LLMProvider, complete_with_optional_model, require_nonempty
from workers.reasoning.answer_stream import StreamingAnswerExtractor
from workers.reasoning.prompts.discussion import DISCUSSION_SYSTEM, build_discussion_prompt
from workers.reasoning.types import DiscussionAnswer, LLMUsageRecord


def _parse_discussion(raw: str) -> DiscussionAnswer:
    """Parse LLM response into a DiscussionAnswer."""
    from workers.common.llm.parse import parse_json_response, strip_llm_wrapping

    data = parse_json_response(raw)
    if data is None or not isinstance(data, dict):
        return DiscussionAnswer(answer=strip_llm_wrapping(raw))

    refs = data.get("references", [])
    refs = [r for r in refs if isinstance(r, str) and r.strip()] if isinstance(refs, list) else []

    answer_value = data.get("answer", "")
    if not isinstance(answer_value, str):
        return DiscussionAnswer(answer=strip_llm_wrapping(raw), references=refs)

    reqs = data.get("related_requirements", [])
    reqs = [r for r in reqs if isinstance(r, str) and r.strip()] if isinstance(reqs, list) else []

    return DiscussionAnswer(
        answer=answer_value,
        references=refs,
        related_requirements=reqs,
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

    response = require_nonempty(
        await complete_with_optional_model(
            provider,
            prompt,
            system=DISCUSSION_SYSTEM,
            temperature=0.2,
            model=model_override,
        ),
        context="discussion",
    )

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


async def discuss_code_stream(
    provider: LLMProvider,
    question: str,
    context_code: str,
    context_metadata: str = "",
    model_override: str | None = None,
    max_tokens: int = 4096,
    temperature: float = 0.2,
) -> AsyncIterator[tuple[str | None, DiscussionAnswer | None, LLMUsageRecord | None]]:
    """Stream a discussion answer.

    Yields `(content_delta, None, None)` tuples for each chunk of
    visible answer text as it is generated, followed by a single
    terminal `(None, answer, usage)` tuple once generation is done.

    Callers should ignore `content_delta` when it is an empty string
    (some providers emit no-op chunks for keepalive) and treat the
    terminal frame as the signal to stop appending.

    The per-token extractor only surfaces the JSON ``answer`` field
    so the user does not have to watch the raw ``{"answer": "...` noise
    scroll by. The full ``references`` and ``related_requirements``
    are parsed from the completed JSON in the terminal frame.
    """
    prompt = build_discussion_prompt(question, context_code, context_metadata)
    model = model_override or getattr(provider, "default_model", None) or getattr(provider, "model", None)

    extractor = StreamingAnswerExtractor()
    input_tokens = 0
    output_tokens = 0
    try:
        async for chunk in provider.stream(
            prompt=prompt,
            system=DISCUSSION_SYSTEM,
            max_tokens=max_tokens,
            temperature=temperature,
            model=model,
        ):
            if not chunk:
                continue
            delta = extractor.feed(chunk)
            if delta:
                yield delta, None, None
    except Exception as exc:
        # Surface the provider error as the terminal frame so callers
        # can show it in the UI — don't let the generator swallow it.
        raise exc

    answer_text, refs, reqs = extractor.finalize()
    final = DiscussionAnswer(
        answer=answer_text or "",
        references=refs,
        related_requirements=reqs,
    )
    usage = LLMUsageRecord(
        provider="llm",
        model=getattr(provider, "default_model", "") or getattr(provider, "model", ""),
        input_tokens=input_tokens,
        output_tokens=output_tokens,
        operation="discussion",
        entity_name="",
    )
    yield None, final, usage
