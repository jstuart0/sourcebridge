"""Structured code review engine with 5 templates."""

from __future__ import annotations

import json

from workers.common.llm.provider import LLMProvider
from workers.reasoning.prompts.reviewer import TEMPLATE_SYSTEMS, build_review_prompt
from workers.reasoning.types import Finding, LLMUsageRecord, ReviewResult


def _parse_review(raw: str, template: str) -> ReviewResult:
    """Parse LLM response into a ReviewResult."""
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
            return ReviewResult(template=template)

    findings = []
    for f in data.get("findings", []):
        findings.append(Finding(
            category=f.get("category", template),
            severity=f.get("severity", "medium"),
            message=f.get("message", ""),
            file_path=f.get("file_path", ""),
            start_line=f.get("start_line", 0),
            end_line=f.get("end_line", 0),
            suggestion=f.get("suggestion", ""),
        ))

    return ReviewResult(
        template=template,
        findings=findings,
        score=data.get("score", 0.0),
    )


async def review_code(
    provider: LLMProvider,
    file_path: str,
    language: str,
    content: str,
    template: str = "security",
) -> tuple[ReviewResult, LLMUsageRecord]:
    """Run a structured code review using the specified template."""
    if template not in TEMPLATE_SYSTEMS:
        raise ValueError(f"Unknown review template: {template}. Valid: {list(TEMPLATE_SYSTEMS.keys())}")

    system = TEMPLATE_SYSTEMS[template]
    prompt = build_review_prompt(file_path, language, content)

    response = await provider.complete(prompt, system=system, temperature=0.0)

    result = _parse_review(response.content, template)

    usage = LLMUsageRecord(
        provider="llm",
        model=response.model,
        input_tokens=response.input_tokens,
        output_tokens=response.output_tokens,
        operation="review",
        entity_name=file_path,
    )

    return result, usage
