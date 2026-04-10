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


CLIFF_NOTES_RENDER_TEMPLATE = """\
You are writing structured cliff notes for a codebase. The repository \
has already been summarized hierarchically — you do NOT see the raw \
code, only the pre-computed summaries below. Produce the required \
sections grounded in those summaries.

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

=== Output format ===
Return a JSON array of sections. Each section object must have:
  - "title":      one of the required section titles below
  - "content":    2-6 sentences of markdown
  - "summary":    a one-line takeaway
  - "confidence": "high" | "medium" | "low"
  - "inferred":   true if the section goes beyond the direct evidence
  - "evidence":   array of {{source_type, source_id, file_path, line_start, line_end, rationale}}

Required section titles (in order, include every one):
{required_sections}

Rules:
- Do NOT include any prose outside the JSON array.
- Do NOT wrap the JSON in markdown fences.
- Every title must appear exactly once.
- Each section's "content" must be 3-6 substantial sentences of markdown — \
  not one-liners. Reference specific files, components, and patterns by name.
- Use "low" confidence + "inferred": true when the summaries don't \
  directly support the section.
- The "evidence" array should reference actual file paths from the summaries above.
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

        group_nodes = self._select_groups(tree, root)
        file_nodes = self._select_files(tree, group_nodes)

        prompt = CLIFF_NOTES_RENDER_TEMPLATE.format(
            repository_name=repository_name or "repository",
            audience=audience,
            depth=depth,
            scope_type=scope_type,
            scope_path_suffix=f" ({scope_path})" if scope_path else "",
            root_summary=root.summary_text or "(no repository summary available)",
            group_summaries=self._format_summaries(group_nodes, label_prefix="Subsystem"),
            file_summaries=self._format_summaries(file_nodes, label_prefix="File"),
            required_sections="\n".join(f"- {t}" for t in required_sections),
        )

        check_prompt_budget(
            prompt,
            system=CLIFF_NOTES_SYSTEM,
            context=f"hierarchical_render:cliff_notes:{scope_type}",
        )

        response: LLMResponse = require_nonempty(
            await complete_with_optional_model(
                self.provider,
                prompt,
                system=CLIFF_NOTES_SYSTEM,
                temperature=0.0,
                max_tokens=self.max_tokens_per_call,
                model=self.model_override,
            ),
            context=f"hierarchical_render:cliff_notes:{scope_type}",
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

        log.info(
            "cliff_notes_renderer_completed",
            repository=repository_name,
            scope_type=scope_type,
            sections=len(sections),
            tree_nodes=len(tree.nodes),
            group_summaries=len(group_nodes),
            file_summaries=len(file_nodes),
        )

        return CliffNotesResult(sections=sections), usage

    # ------------------------------------------------------------------
    # Selection helpers

    def _select_groups(self, tree: SummaryTree, root: SummaryNode) -> list[SummaryNode]:
        """Pick up to N level-2 children under the root, preferring the
        ones with the most source tokens (roughly "biggest subsystems
        first"). Falls back to insertion order when source_tokens are
        all zero."""
        children = tree.children_of(root.unit_id)
        # Order deterministically: bigger subsystems first, then by
        # insertion order as a tiebreaker.
        ordered = sorted(
            enumerate(children),
            key=lambda pair: (-pair[1].source_tokens, pair[0]),
        )
        return [pair[1] for pair in ordered[: self.max_group_summaries]]

    def _select_files(
        self,
        tree: SummaryTree,
        group_nodes: list[SummaryNode],
    ) -> list[SummaryNode]:
        """Pick up to N level-1 summaries across the selected groups.

        We round-robin across the groups so a single dominant package
        doesn't eat the whole file budget.
        """
        per_group: list[list[SummaryNode]] = []
        for group in group_nodes:
            files = sorted(
                enumerate(tree.children_of(group.unit_id)),
                key=lambda pair: (-pair[1].source_tokens, pair[0]),
            )
            per_group.append([pair[1] for pair in files])

        picked: list[SummaryNode] = []
        idx = 0
        while len(picked) < self.max_file_summaries and any(per_group):
            # Round-robin: take one from each non-empty bucket.
            any_progress = False
            for bucket in per_group:
                if idx < len(bucket) and len(picked) < self.max_file_summaries:
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
