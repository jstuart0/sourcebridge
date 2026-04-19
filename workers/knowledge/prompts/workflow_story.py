# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""System and user prompts for workflow story generation."""

from __future__ import annotations

import json

from workers.knowledge.prompts.fact_hints import build_fact_hints_block

REQUIRED_WORKFLOW_STORY_SECTIONS = [
    "Goal",
    "Likely Actor",
    "Trigger",
    "Main Steps",
    "Behind the Scenes",
    "Key Branches or Failure Points",
    "Where to Inspect or Modify",
]

REQUIRED_WORKFLOW_STORY_SECTIONS_DEEP = [
    "Goal",
    "Likely Actor",
    "Trigger",
    "Main Steps",
    "Behind the Scenes",
    "Key Branches or Failure Points",
    "Error Recovery",
    "Observability",
    "Where to Inspect or Modify",
]

WORKFLOW_STORY_SYSTEM = """\
You are a senior product-minded software engineer creating a grounded workflow story.

A workflow story explains how a real person is likely to use a concrete feature or scope,
while staying tied to actual code and evidence from the repository snapshot.

Rules:
- Default to plain-language clarity for a smart but non-specialist reader.
- Stay grounded in the provided snapshot and execution-path evidence.
- Explain the likely happy path first, then note important branches or failure points.
- Keep it concise and useful, not theatrical or speculative.
- If something is inferred rather than directly shown in evidence, mark it as inferred.
- Use requirement evidence only when it adds real product intent. Do not make the story compliance-first.
- Every section must contain readable prose, not nested JSON or placeholder text.
- If evidence is thin, write the most grounded version you can and say what is not shown.
- Return JSON only. No markdown fences, no preamble.
"""


def build_workflow_story_prompt(
    repository_name: str,
    audience: str,
    depth: str,
    snapshot_json: str,
    scope_type: str = "repository",
    scope_path: str = "",
    anchor_label: str = "",
    execution_path_json: str = "",
) -> str:
    """Build the prompt for workflow story generation."""
    depth_guidance = {
        "summary": "Keep the story short: 5-7 concise sections and 3-5 main steps.",
        "medium": "Use 7 structured sections with 4-6 main steps.",
        "deep": (
            "Use 9 structured sections with 6-10 main steps. Every section must ground behavior in specific "
            "files and functions. Include Error Recovery and Observability."
        ),
    }
    required_sections = REQUIRED_WORKFLOW_STORY_SECTIONS_DEEP if depth == "deep" else REQUIRED_WORKFLOW_STORY_SECTIONS

    scope_line = f"Repository scope for {repository_name}"
    if scope_type and scope_type != "repository":
        scope_line = f'{scope_type.title()} scope "{scope_path}" in {repository_name}'

    anchor_block = ""
    if anchor_label:
        anchor_block = f"""
**Story Anchor**
- Use this as the plain-language center of gravity for the story: {anchor_label}
"""

    execution_block = ""
    if execution_path_json:
        execution_block = f"""
**Execution Path Evidence**
Use these traced steps when they help explain what the system does behind the scenes.
Do not copy them verbatim into every section; translate them into readable workflow language.
```json
{execution_path_json}
```
"""

    # Deep mode: extract pre-analysis from enriched snapshot
    pre_analysis_block = ""
    try:
        snap_data = json.loads(snapshot_json) if snapshot_json else {}
        pre_analysis = snap_data.get("_pre_analysis")
        if pre_analysis and isinstance(pre_analysis, list):
            lines = []
            for section in pre_analysis:
                if isinstance(section, dict):
                    title = section.get("title", "")
                    content = section.get("content", "")
                    if title and content:
                        lines.append(f"### {title}\n{content}")
            if lines:
                pre_analysis_block = (
                    "**Pre-computed Codebase Analysis (from field guide)**\n"
                    "Use this analysis as PRIMARY context — it contains detailed, "
                    "grounded information about each part of the codebase. "
                    "Sections referencing this analysis should have confidence: high.\n\n" + "\n\n".join(lines) + "\n\n"
                )
    except (json.JSONDecodeError, TypeError, ValueError):
        pass

    fact_hints_block = build_fact_hints_block(snapshot_json)

    return f"""\
Create a Workflow Story for this scope:

- **Repository:** {repository_name}
- **Audience:** {audience}
- **Depth:** {depth}
- **Scope:** {scope_line}
{depth_guidance.get(depth, depth_guidance["medium"])}
{anchor_block}
{execution_block}
**Output format**
Return a JSON array of section objects with exactly these section titles in this order:
{", ".join(required_sections)}

Each section object must have:
- "title": string
- "content": string
- "summary": string
- "confidence": "high" | "medium" | "low"
- "inferred": boolean
- "evidence": array of evidence objects

Each evidence object must have:
- "source_type": string
- "source_id": string
- "file_path": string
- "line_start": int
- "line_end": int
- "rationale": string

Writing guidance:
- Goal: what the person is trying to accomplish
- Likely Actor: who this is for in practical terms
- Trigger: what starts the workflow
- Main Steps: ordered, readable steps in the user's journey
- Behind the Scenes: what the app and backend do
- Key Branches or Failure Points: what can diverge or go wrong
- Error Recovery: how failures are caught, recovered, or surfaced
- Observability: relevant logs, metrics, traces, or explicit absence of them
- Where to Inspect or Modify: the most relevant files/symbols to read or change
- Do not leave sections blank or say "insufficient data" unless the snapshot is truly empty.
- Prefer specific files, symbols, routes, or steps from the execution path over generic architecture recap.
- Each section's "content" must be 4-8 substantial sentences. Name specific files, \
  components, functions, and routes. Minimum 80 words per section.

Confidence rules:
- If a section references specific files, symbols, or routes from the snapshot, \
  set confidence to "high" and inferred to false — the snapshot IS direct evidence.
- Only use "medium" confidence when you are connecting dots between separate pieces \
  of evidence (e.g. inferring a data flow from two separate function signatures).
- Only use "low" confidence when the snapshot provides no relevant evidence at all.
- Most sections should be "high" confidence when the snapshot contains relevant symbols.

{pre_analysis_block}{fact_hints_block}**Repository snapshot**
```json
{snapshot_json}
```

Return ONLY the JSON array.
"""
