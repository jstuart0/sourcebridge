# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Artifact renderers.

A renderer takes a :class:`SummaryTree` and produces a final artifact
(cliff notes, learning path, code tour, workflow story). Renderers are
strategy-agnostic — they consume the tree shape, not knowledge of how
it was built — so the same CliffNotesRenderer works against a
HierarchicalStrategy output today and a RAPTOR/GraphRAG output later.

Each renderer does one final LLM call: it feeds the model the root
summary plus a small number of the most substantial child summaries
and asks for structured JSON sections that match the existing cliff
notes UI contract. This keeps the final prompt small enough for any
model even when the original repo was huge.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from time import monotonic

import structlog

from workers.common.llm.provider import (
    LLMProvider,
    LLMResponse,
    check_prompt_budget,
    complete_with_optional_model,
    require_nonempty,
)
from workers.comprehension.tree import SummaryNode, SummaryTree
from workers.knowledge.cliff_notes import (
    _coerce_section,
    _parse_evidence,
    _parse_sections,
)
from workers.knowledge.prompts.cliff_notes import (
    CLIFF_NOTES_SYSTEM,
    REQUIRED_SECTIONS,
    REQUIRED_SECTIONS_BY_SCOPE,
)
from workers.knowledge.types import CliffNotesResult, CliffNotesSection
from workers.reasoning.types import LLMUsageRecord

log = structlog.get_logger()


def _is_provider_compute_error(exc: Exception) -> bool:
    text = str(exc).lower()
    return "compute error" in text or "server_error" in text


CLIFF_NOTES_RENDER_TEMPLATE = """\
You are writing a detailed field guide for developers joining this codebase. \
The repository has been analyzed — use the summaries below to write \
thorough, grounded documentation for each required section.

Repository: {repository_name}
Audience: {audience}
Depth: {depth}
Scope: {scope_type}{scope_path_suffix}

=== Repository summary ===
{root_summary}

=== Notable subsystems ===
{group_summaries}

=== Notable files ===
{file_summaries}

{pre_analysis_block}

=== Task ===
{depth_instructions}
Write a JSON array of {section_count} section objects.

Each section object has these keys:
  - "title": one of the required section titles listed below (string)
  - "content": a detailed markdown paragraph of 4-8 sentences. Name specific \
    files, components, functions, and patterns. Explain HOW things work, not \
    just WHAT they are. Minimum 80 words per section. (string)
  - "summary": a single-sentence takeaway (string)
  - "confidence": "high", "medium", or "low" (string)
  - "inferred": true if you're extrapolating beyond the summaries (boolean)
  - "evidence": array of objects with keys: source_type, source_id, file_path, \
    line_start, line_end, rationale. Reference actual file paths from above.

Required section titles (produce every one, in this order):
{required_sections}

Confidence rules:
- Set confidence to "high" and inferred to false when the summaries above \
  directly describe what you're writing about. The summaries ARE the evidence.
- Analyzing code patterns, complexity, risks, or architecture from the summaries \
  counts as direct evidence — use "high" confidence for these.
- Only use "medium" when you are connecting dots NOT mentioned in any summary.
- Only use "low" when the summaries provide no relevant information at all.
- Most sections should be "high" confidence since you have detailed summaries.

Output rules:
- Return ONLY the JSON array — no text before or after it.
- No markdown fences around the JSON.
- Every required title must appear exactly once.
"""


@dataclass
class CliffNotesRenderer:
    """Renders cliff notes from a hierarchical summary tree.

    ``max_group_summaries`` bounds how many level-2 (package/subsystem)
    summaries are fed into the final prompt. This is the main knob for
    final-prompt size — keeping it small ensures the render call fits
    any model even when the tree itself is enormous.
    """

    provider: LLMProvider
    max_group_summaries: int = 8
    max_file_summaries: int = 12
    max_tokens_per_call: int = 16384  # thinking models need headroom for <think> chains before the JSON output
    model_override: str | None = None

    async def render(
        self,
        tree: SummaryTree,
        *,
        repository_name: str,
        audience: str = "developer",
        depth: str = "medium",
        scope_type: str = "repository",
        scope_path: str = "",
        pre_analysis: list[dict[str, str]] | None = None,
    ) -> tuple[CliffNotesResult, LLMUsageRecord]:
        """Render cliff notes from the supplied tree.

        Returns the structured result plus an LLM usage record so the
        servicer can persist billing metrics the same way the legacy
        single-shot path does.
        """
        required_sections = REQUIRED_SECTIONS_BY_SCOPE.get(
            scope_type or "repository", REQUIRED_SECTIONS
        )
        root = tree.root()
        if root is None:
            raise ValueError("cannot render cliff notes from an empty summary tree")
        render_started = monotonic()

        # Deep mode: widen the context window — include more summaries
        # and leaf-level detail so the output is noticeably richer.
        if depth == "deep":
            effective_max_groups = min(len(tree.at_level(2)), 20)
            effective_max_files = min(len(tree.at_level(1)), 30)
        else:
            effective_max_groups = self.max_group_summaries
            effective_max_files = self.max_file_summaries

        group_nodes = self._select_groups(tree, root, max_n=effective_max_groups)
        file_nodes = self._select_files(tree, group_nodes, max_n=effective_max_files)

        # Build pre-analysis block from repository-level cliff notes (deep mode)
        pa_block = ""
        if pre_analysis:
            lines = []
            for section in pre_analysis:
                title = section.get("title", "")
                content = section.get("content", "")
                if title and content:
                    lines.append(f"### {title}\n{content}")
            if lines:
                pa_block = (
                    "=== Repository-level analysis (from existing field guide) ===\n"
                    "Use this as PRIMARY grounded context. Sections that build on "
                    "this analysis should have confidence: high.\n\n"
                    + "\n\n".join(lines)
                )

        depth_instructions = {
            "summary": (
                "IMPORTANT: Keep sections concise — 2-3 sentences each, ~800 words total. "
                "Focus on the most important facts only."
            ),
            "medium": (
                "IMPORTANT: Your total output must be at least 1500 words across all sections. "
                "Each section MUST contain detailed, specific content — not vague summaries."
            ),
            "deep": (
                "IMPORTANT: This is a DEEP analysis. Your total output must be at least 2500 words "
                "across all sections. Each section must be 6-10 sentences with thorough, specific detail. "
                "Name every relevant file, component, function, and pattern. Explain HOW things work internally, "
                "what trade-offs were made, what the failure modes are, and what a new developer needs to know. "
                "For Complexity & Risk: identify specific files with high cyclomatic complexity, tight coupling, "
                "missing error handling, or implicit dependencies. "
                "For Architecture: describe the data flow, request lifecycle, and state management in detail. "
                "For Domain Model: list every entity, their relationships, and where they're defined. "
                "Do NOT be generic — every sentence should reference something concrete from the summaries."
            ),
        }.get(depth, "Your total output must be at least 1500 words across all sections.")

        prompt = CLIFF_NOTES_RENDER_TEMPLATE.format(
            repository_name=repository_name or "repository",
            audience=audience,
            depth=depth,
            scope_type=scope_type,
            scope_path_suffix=f" ({scope_path})" if scope_path else "",
            root_summary=root.summary_text or "(no repository summary available)",
            group_summaries=self._format_summaries(group_nodes, label_prefix="Subsystem"),
            file_summaries=self._format_summaries(file_nodes, label_prefix="File"),
            pre_analysis_block=pa_block,
            depth_instructions=depth_instructions,
            section_count=len(required_sections),
            required_sections="\n".join(f"- {t}" for t in required_sections),
        )

        check_prompt_budget(
            prompt,
            system=CLIFF_NOTES_SYSTEM,
            context=f"hierarchical_render:cliff_notes:{scope_type}",
        )

        log.info(
            "cliff_notes_renderer_started",
            repository=repository_name,
            scope_type=scope_type,
            tree_nodes=len(tree.nodes),
            selected_groups=len(group_nodes),
            selected_files=len(file_nodes),
        )

        try:
            response = await self._render_with_retry(
                prompt=prompt,
                scope_type=scope_type,
            )
            sections = self._parse_sections(response.content, required_sections)
            usage = LLMUsageRecord(
                provider="llm",
                model=response.model,
                input_tokens=response.input_tokens,
                output_tokens=response.output_tokens,
                operation="cliff_notes_render",
                entity_name=repository_name,
            )
        except Exception as exc:
            log.warning(
                "cliff_notes_renderer_fallback",
                repository=repository_name,
                scope_type=scope_type,
                error=str(exc),
            )
            sections = self._fallback_sections(
                required_sections=required_sections,
                root=root,
                groups=group_nodes,
                files=file_nodes,
                scope_type=scope_type,
                scope_path=scope_path,
            )
            usage = LLMUsageRecord(
                provider="llm",
                model=self.model_override or "fallback",
                input_tokens=0,
                output_tokens=0,
                operation="cliff_notes_render_fallback",
                entity_name=repository_name,
            )

        log.info(
            "cliff_notes_renderer_completed",
            repository=repository_name,
            scope_type=scope_type,
            sections=len(sections),
            tree_nodes=len(tree.nodes),
            group_summaries=len(group_nodes),
            file_summaries=len(file_nodes),
            elapsed_ms=int((monotonic() - render_started) * 1000),
        )

        return CliffNotesResult(sections=sections), usage

    async def _render_with_retry(
        self,
        *,
        prompt: str,
        scope_type: str,
    ) -> LLMResponse:
        context = f"hierarchical_render:cliff_notes:{scope_type}"
        last_exc: Exception | None = None
        for attempt in range(1, 4):
            try:
                return require_nonempty(
                    await complete_with_optional_model(
                        self.provider,
                        prompt,
                        system=CLIFF_NOTES_SYSTEM,
                        temperature=0.0,
                        max_tokens=self.max_tokens_per_call,
                        model=self.model_override,
                    ),
                    context=context,
                )
            except Exception as exc:
                last_exc = exc
                if _is_provider_compute_error(exc) and attempt < 3:
                    delay = 0.4 * (2 ** (attempt - 1))
                    log.warning(
                        "cliff_notes_renderer_retry",
                        scope_type=scope_type,
                        attempt=attempt,
                        delay_s=delay,
                        error=str(exc),
                    )
                    import asyncio

                    await asyncio.sleep(delay)
                    continue
                break
        assert last_exc is not None
        raise last_exc

    # ------------------------------------------------------------------
    # Selection helpers

    def _select_groups(self, tree: SummaryTree, root: SummaryNode, max_n: int | None = None) -> list[SummaryNode]:
        """Pick up to N level-2 children under the root, preferring the
        ones with the most source tokens (roughly "biggest subsystems
        first"). Falls back to insertion order when source_tokens are
        all zero."""
        limit = max_n if max_n is not None else self.max_group_summaries
        children = tree.children_of(root.unit_id)
        ordered = sorted(
            enumerate(children),
            key=lambda pair: (-pair[1].source_tokens, pair[0]),
        )
        return [pair[1] for pair in ordered[:limit]]

    def _select_files(
        self,
        tree: SummaryTree,
        group_nodes: list[SummaryNode],
        max_n: int | None = None,
    ) -> list[SummaryNode]:
        """Pick up to N level-1 summaries across the selected groups.

        We round-robin across the groups so a single dominant package
        doesn't eat the whole file budget.
        """
        limit = max_n if max_n is not None else self.max_file_summaries
        per_group: list[list[SummaryNode]] = []
        for group in group_nodes:
            files = sorted(
                enumerate(tree.children_of(group.unit_id)),
                key=lambda pair: (-pair[1].source_tokens, pair[0]),
            )
            per_group.append([pair[1] for pair in files])

        picked: list[SummaryNode] = []
        idx = 0
        while len(picked) < limit and any(per_group):
            any_progress = False
            for bucket in per_group:
                if idx < len(bucket) and len(picked) < limit:
                    picked.append(bucket[idx])
                    any_progress = True
            if not any_progress:
                break
            idx += 1
        return picked

    def _format_summaries(
        self,
        nodes: list[SummaryNode],
        *,
        label_prefix: str,
    ) -> str:
        if not nodes:
            return f"(no {label_prefix.lower()} summaries available)"
        lines: list[str] = []
        for node in nodes:
            label = _node_label(node) or label_prefix
            headline = node.headline or _first_line(node.summary_text)
            body = node.summary_text.strip()
            lines.append(f"{label_prefix}: {label}\n  {headline}\n  {body}")
        return "\n\n".join(lines)

    def _fallback_sections(
        self,
        *,
        required_sections: list[str],
        root: SummaryNode,
        groups: list[SummaryNode],
        files: list[SummaryNode],
        scope_type: str,
        scope_path: str,
    ) -> list[CliffNotesSection]:
        scope_label = scope_path or scope_type or "repository"
        root_summary = (root.summary_text or "No repository summary was available.").strip()
        group_lines = self._summary_bullets(groups, max_items=4)
        file_lines = self._summary_bullets(files, max_items=6)
        fallback_note = (
            "The model backend failed during the final render step, so this section was assembled "
            "from the hierarchical summaries that were already produced."
        )
        sections: list[CliffNotesSection] = []
        for title in required_sections:
            content = (
                f"{root_summary}\n\n"
                f"Scope: {scope_label}.\n\n"
                f"Notable subsystems:\n{group_lines}\n\n"
                f"Notable files:\n{file_lines}\n\n"
                f"{fallback_note}"
            )
            sections.append(
                CliffNotesSection(
                    title=title,
                    content=content,
                    summary=f"Fallback summary for {title.lower()} built from hierarchical repository notes.",
                    confidence="low",
                    inferred=True,
                    evidence=[],
                )
            )
        return sections

    def _summary_bullets(self, nodes: list[SummaryNode], *, max_items: int) -> str:
        if not nodes:
            return "- No grounded summaries were available."
        lines: list[str] = []
        for node in nodes[:max_items]:
            label = _node_label(node)
            summary = (node.summary_text or node.headline or "").strip().replace("\n", " ")
            if not summary:
                summary = "Summary unavailable."
            lines.append(f"- {label}: {summary[:220]}")
        return "\n".join(lines)

    def _parse_sections(
        self,
        raw_content: str,
        required_sections: list[str],
    ) -> list[CliffNotesSection]:
        """Parse the LLM JSON output into typed sections.

        Reuses the shared parser from the legacy path so behavior
        stays consistent — tolerant of markdown fences, <think> blocks,
        and preamble/postamble text.
        """
        try:
            raw_sections = _parse_sections(raw_content)
        except (json.JSONDecodeError, ValueError, TypeError) as exc:
            log.warning("hierarchical_render_parse_fallback", error=str(exc))
            raw_sections = [
                {
                    "title": required_sections[0] if required_sections else "System Purpose",
                    "content": raw_content,
                    "summary": "LLM output could not be parsed into structured sections.",
                    "confidence": "low",
                    "inferred": True,
                    "evidence": [],
                }
            ]

        sections: list[CliffNotesSection] = []
        seen_titles: set[str] = set()
        for index, raw in enumerate(raw_sections):
            fallback_title = (
                required_sections[index]
                if index < len(required_sections)
                else f"Section {index + 1}"
            )
            normalized = _coerce_section(raw, fallback_title=fallback_title)
            title = str(normalized.get("title", fallback_title))
            evidence_raw = normalized.get("evidence", [])
            if not isinstance(evidence_raw, list):
                evidence_raw = []
            seen_titles.add(title)
            sections.append(
                CliffNotesSection(
                    title=title,
                    content=str(normalized.get("content", "")),
                    summary=str(normalized.get("summary", "")),
                    confidence=str(normalized.get("confidence", "medium")),
                    inferred=bool(normalized.get("inferred", False)),
                    evidence=_parse_evidence(evidence_raw),
                )
            )

        for req_title in required_sections:
            if req_title not in seen_titles:
                sections.append(
                    CliffNotesSection(
                        title=req_title,
                        content="*Insufficient data to generate this section.*",
                        summary="Not enough information available.",
                        confidence="low",
                        inferred=True,
                    )
                )

        return sections


def _first_line(text: str) -> str:
    for line in (text or "").splitlines():
        line = line.strip()
        if line:
            return line[:140]
    return ""


def _node_label(node: SummaryNode) -> str:
    meta = node.metadata or {}
    return (
        str(meta.get("file_path"))
        or str(meta.get("module_label"))
        or str(meta.get("repository_name"))
        or node.unit_id
    )
