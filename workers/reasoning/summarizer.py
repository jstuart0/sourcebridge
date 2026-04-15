"""Multi-level code summarizer."""

from __future__ import annotations

import hashlib

from workers.common.llm.provider import (
    LLMProvider,
    LLMResponse,
    complete_with_optional_model,
    require_nonempty,
)
from workers.reasoning.prompts.summarizer import (
    FILE_SUMMARY_SYSTEM,
    FUNCTION_SUMMARY_SYSTEM,
    MODULE_SUMMARY_SYSTEM,
    build_file_prompt,
    build_function_prompt,
    build_module_prompt,
)
from workers.reasoning.types import LLMUsageRecord, Summary, SummaryLevel


def _content_hash(text: str) -> str:
    return hashlib.sha256(text.encode()).hexdigest()[:16]


def _parse_summary(raw: str, level: str, entity_name: str, content_hash: str) -> Summary:
    """Parse LLM response into a Summary, tolerating minor formatting issues."""
    from workers.common.llm.parse import parse_json_response, strip_llm_wrapping

    data = parse_json_response(raw)
    if data is None or not isinstance(data, dict):
        return Summary(
            purpose=strip_llm_wrapping(raw),
            level=level,
            entity_name=entity_name,
            content_hash=content_hash,
        )

    return Summary(
        purpose=data.get("purpose", ""),
        inputs=data.get("inputs", []),
        outputs=data.get("outputs", []),
        dependencies=data.get("dependencies", []),
        side_effects=data.get("side_effects", []),
        risks=data.get("risks", []),
        confidence=data.get("confidence", 0.0),
        level=level,
        entity_name=entity_name,
        content_hash=content_hash,
    )


async def summarize_function(
    provider: LLMProvider,
    name: str,
    language: str,
    content: str,
    doc_comment: str = "",
    model_override: str | None = None,
) -> tuple[Summary, LLMUsageRecord]:
    """Generate a function-level summary."""
    prompt = build_function_prompt(name, language, content, doc_comment)
    ch = _content_hash(content)

    response: LLMResponse = require_nonempty(
        await complete_with_optional_model(
            provider,
            prompt,
            system=FUNCTION_SUMMARY_SYSTEM,
            temperature=0.0,
            model=model_override,
        ),
        context="summary:function",
    )

    summary = _parse_summary(response.content, SummaryLevel.FUNCTION, name, ch)

    usage = LLMUsageRecord(
        provider="llm",
        model=response.model,
        input_tokens=response.input_tokens,
        output_tokens=response.output_tokens,
        operation="summary",
        entity_name=name,
    )

    return summary, usage


async def summarize_file(
    provider: LLMProvider,
    file_path: str,
    language: str,
    symbols: list[str],
) -> tuple[Summary, LLMUsageRecord]:
    """Generate a file-level summary."""
    prompt = build_file_prompt(file_path, language, symbols)
    ch = _content_hash(file_path + "|" + "|".join(symbols))

    response = require_nonempty(
        await complete_with_optional_model(
            provider,
            prompt,
            system=FILE_SUMMARY_SYSTEM,
            temperature=0.0,
        ),
        context="summary:file",
    )

    summary = _parse_summary(response.content, SummaryLevel.FILE, file_path, ch)

    usage = LLMUsageRecord(
        provider="llm",
        model=response.model,
        input_tokens=response.input_tokens,
        output_tokens=response.output_tokens,
        operation="summary",
        entity_name=file_path,
    )

    return summary, usage


async def summarize_module(
    provider: LLMProvider,
    module_name: str,
    files: list[str],
    key_symbols: list[str],
) -> tuple[Summary, LLMUsageRecord]:
    """Generate a module-level summary."""
    prompt = build_module_prompt(module_name, files, key_symbols)
    ch = _content_hash(module_name + "|" + "|".join(files))

    response = require_nonempty(
        await complete_with_optional_model(
            provider,
            prompt,
            system=MODULE_SUMMARY_SYSTEM,
            temperature=0.0,
        ),
        context="summary:module",
    )

    summary = _parse_summary(response.content, SummaryLevel.MODULE, module_name, ch)

    usage = LLMUsageRecord(
        provider="llm",
        model=response.model,
        input_tokens=response.input_tokens,
        output_tokens=response.output_tokens,
        operation="summary",
        entity_name=module_name,
    )

    return summary, usage
