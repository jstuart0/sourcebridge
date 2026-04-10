# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Section generator for reports.

Each report section gets a dedicated LLM call with:
1. Portfolio context (what repos, common patterns)
2. Section-specific data from analyzers
3. Audience-specific instructions
4. Evidence markers for the registry
"""

from __future__ import annotations

import json
import logging
import re
from dataclasses import dataclass

from workers.common.llm.provider import LLMProvider, LLMResponse, complete_with_optional_model
from workers.reports.evidence_registry import EvidenceRegistry
from workers.reports.prompts.audience import audience_prompt_block

# Section-specific instructions per report type
_SECTION_INSTRUCTION_MODULES: dict[str, object] = {}


def _get_section_instructions(report_type: str, section_key: str) -> str:
    """Get section-specific extra instructions for a report type + section."""
    if report_type not in _SECTION_INSTRUCTION_MODULES:
        try:
            mod = __import__(
                f"workers.reports.prompts.{report_type}",
                fromlist=["get_section_instructions"],
            )
            _SECTION_INSTRUCTION_MODULES[report_type] = mod
        except (ImportError, AttributeError):
            _SECTION_INSTRUCTION_MODULES[report_type] = None

    mod = _SECTION_INSTRUCTION_MODULES.get(report_type)
    if mod and hasattr(mod, "get_section_instructions"):
        return mod.get_section_instructions(section_key)
    return ""

logger = logging.getLogger(__name__)

SECTION_SYSTEM = (
    "You are a senior software consultant writing a professional report section. "
    "Write in third person, present tense. Be specific and evidence-based. "
    "Every claim should reference concrete files, technologies, patterns, or metrics. "
    "Output clean Markdown only — no preamble, no fences around the whole section."
)

SECTION_TEMPLATE = """\
You are writing the "{section_title}" section of a {report_type} report.

{audience_block}

**Portfolio context:**
{portfolio_context}

**Evidence for this section:**
{section_data}

**LOE estimation mode:** {loe_mode}
{loe_instructions}

**Requirements:**
- Write this as a standalone section of a professional report
- Use ## for the section title, ### for subsections
- Be specific: name files, technologies, versions, and patterns
- When repos differ, explain the variation
- If evidence is insufficient, note what is unknown and include an "Areas for Validation / Questions" subsection
- Minimum {min_words} words
- Include evidence markers like [E-SEC-01] when referencing specific findings — the evidence registry will map these to appendix entries
- If this section includes recommendations, format each as:
  **Recommendation: Title**
  - Effort: (estimate per LOE mode)
  - Complexity: High/Medium/Low
  - Prerequisites: ...
  - Risk: ...

{extra_instructions}

Output the section content in Markdown.
"""

LOE_INSTRUCTIONS = {
    "human_hours": (
        "Estimate effort assuming experienced human developers. "
        "Use person-hours for tasks, person-weeks for initiatives. "
        "Assume a mid-level developer familiar with the stack but new to the codebase."
    ),
    "ai_assisted": (
        "Split each estimate into AI agent time and human review time. "
        "AI excels at: repetitive changes, boilerplate, test generation, dependency updates. "
        "Humans needed for: architecture decisions, security review, novel design, final approval. "
        "Be realistic — not everything is faster with AI."
    ),
}


@dataclass
class GeneratedSection:
    """Output of generating one report section."""

    key: str
    title: str
    category: str
    markdown: str
    word_count: int
    input_tokens: int = 0
    output_tokens: int = 0


async def generate_section(
    provider: LLMProvider,
    *,
    section_key: str,
    section_title: str,
    section_category: str,
    report_type: str,
    audience: str,
    portfolio_context: str,
    section_data: str,
    loe_mode: str = "human_hours",
    min_words: int = 150,
    extra_instructions: str = "",
    model_override: str | None = None,
) -> GeneratedSection:
    """Generate a single report section via LLM."""
    # Auto-load section-specific instructions from the report type's prompt module
    type_instructions = _get_section_instructions(report_type, section_key)
    combined_extra = "\n".join(filter(None, [type_instructions, extra_instructions]))

    prompt = SECTION_TEMPLATE.format(
        section_title=section_title,
        report_type=report_type.replace("_", " ").title(),
        audience_block=audience_prompt_block(audience),
        portfolio_context=portfolio_context or "(No portfolio context available)",
        section_data=section_data or "(No data available for this section)",
        loe_mode=loe_mode,
        loe_instructions=LOE_INSTRUCTIONS.get(loe_mode, LOE_INSTRUCTIONS["human_hours"]),
        min_words=min_words,
        extra_instructions=combined_extra,
    )

    try:
        response: LLMResponse = await complete_with_optional_model(
            provider,
            prompt,
            system=SECTION_SYSTEM,
            max_tokens=16384,
            temperature=0.1,
            model=model_override,
        )
        content = response.content.strip()
        # Strip <think> tags if present
        content = re.sub(r"<think>.*?</think>", "", content, flags=re.DOTALL).strip()
    except Exception as e:
        logger.warning("section_generation_failed", section=section_key, error=str(e))
        content = (
            f"## {section_title}\n\n"
            f"> **This section could not be generated.** Error: {e}\n>\n"
            f"> _[PLACEHOLDER — Regenerate this section or provide data manually]_"
        )

    word_count = len(content.split())
    return GeneratedSection(
        key=section_key,
        title=section_title,
        category=section_category,
        markdown=content,
        word_count=word_count,
        input_tokens=getattr(response, "input_tokens", 0) if "response" in dir() else 0,
        output_tokens=getattr(response, "output_tokens", 0) if "response" in dir() else 0,
    )


def generate_placeholder_section(
    section_key: str,
    section_title: str,
    section_category: str,
    reason: str = "No data available",
) -> GeneratedSection:
    """Generate a placeholder section when no data exists."""
    content = (
        f"## {section_title}\n\n"
        f"> **{reason}** for this section across the selected repositories.\n"
        f"> This section would typically cover the relevant analysis. "
        f"Consider providing additional data or configuration to enable this section.\n>\n"
        f"> _[PLACEHOLDER — Update this section when data is available]_"
    )
    return GeneratedSection(
        key=section_key,
        title=section_title,
        category=section_category,
        markdown=content,
        word_count=len(content.split()),
    )
