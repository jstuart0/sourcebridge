# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""System and user prompts for learning path generation."""

from __future__ import annotations

from workers.knowledge.prompts.fact_hints import build_fact_hints_block

LEARNING_PATH_SYSTEM = """\
You are a senior developer creating a structured learning path for a codebase. \
The path should guide a new contributor from zero context to productive work. \
You produce JSON output that strictly follows the schema described in the user prompt.

Rules:
- Order steps from simple/foundational to complex/specific.
- Each step should build on knowledge from previous steps.
- Include specific file paths and symbol references from the snapshot.
- Estimate realistic time for each step based on complexity.
- Adapt difficulty and pacing to the target audience.
"""

_AUDIENCE_INSTRUCTIONS = {
    "beginner": (
        "The reader is new to programming or this codebase. "
        "Start with the very basics. Explain what each file does before diving in. "
        "Keep steps small and include lots of context."
    ),
    "developer": (
        "The reader is an experienced developer joining the project. "
        "Focus on architecture patterns, key abstractions, and non-obvious design decisions. "
        "You can assume familiarity with the language and tooling."
    ),
}

_DEPTH_INSTRUCTIONS = {
    "summary": "Create 3-5 high-level steps. Keep objectives brief.",
    "medium": "Create 5-8 steps with moderate detail in objectives and content.",
    "deep": """Create a 10-15 step onboarding curriculum.

Per-step requirements:
- content must reference at least 2 specific files from the snapshot
- include 1-2 concrete exercises tied to named functions, tests, or patterns
- set prerequisite_steps when a step depends on earlier context
- include difficulty: beginner | intermediate | advanced
- include a concrete checkpoint for verifying understanding

GROUNDING RULE: never tell the reader to merely "explore the codebase" or "familiarize yourself".
Every step must name specific files and what to inspect in them.

FILE-PATH DISCIPLINE (violations lower the step's confidence):
- Every entry in "file_paths" MUST be a real file path you can see in the
  snapshot or in the "Representative files" / "Entry-point symbols" /
  "Public-API symbols" anchors above. Do not invent paths.
- Every entry MUST include a file extension (".go", ".py", ".ts",
  ".tsx", ".sql", ".proto", ".md", etc.). If you only know a directory
  (e.g. "internal/graph") drop it — do NOT list directories in
  "file_paths"; describe the directory in prose instead.
- Prefer paths that appear multiple times in the anchors — those are
  the load-bearing files. Citing a random matching file once is worse
  than citing the canonical one three times across steps.
- If you find yourself wanting to cite a file whose exact path you are
  not sure of, cite a file from the anchors instead and describe the
  uncertain component in prose rather than as a path.""",
}


def build_learning_path_prompt(
    repository_name: str,
    audience: str,
    depth: str,
    snapshot_json: str,
    focus_area: str = "",
) -> str:
    """Build the user prompt for learning path generation."""
    audience_instruction = _AUDIENCE_INSTRUCTIONS.get(audience, _AUDIENCE_INSTRUCTIONS["developer"])
    depth_instruction = _DEPTH_INSTRUCTIONS.get(depth, _DEPTH_INSTRUCTIONS["medium"])

    focus_line = ""
    if focus_area:
        focus_line = f"\n**Focus area:** {focus_area}\nPrioritize steps related to this area.\n"

    pre_analysis_block = ""
    try:
        import json

        snap_data = json.loads(snapshot_json) if snapshot_json else {}
        pre_analysis = snap_data.get("_pre_analysis")
        if isinstance(pre_analysis, list):
            lines = []
            for section in pre_analysis:
                if isinstance(section, dict) and section.get("title") and section.get("content"):
                    lines.append(f"### {section['title']}\n{section['content']}")
            if lines:
                pre_analysis_block = (
                    "\n**Codebase Field Guide (use as primary context)**\n"
                    "These sections come from the repository field guide. Use them directly when planning the path.\n\n"
                    + "\n\n".join(lines)
                    + "\n"
                )
    except (json.JSONDecodeError, TypeError, ValueError):
        pre_analysis_block = ""

    fact_hints = build_fact_hints_block(snapshot_json)

    return f"""\
Generate a learning path for the repository "{repository_name}".

**Audience:** {audience}
{audience_instruction}

**Depth:** {depth}
{depth_instruction}
{focus_line}
{pre_analysis_block}{fact_hints}
**Output format:** Return a JSON array of step objects, ordered from first to last. Each object must have:
- "order": int (1-based step number)
- "title": string (short step title)
- "objective": string (what the learner will understand after this step)
- "content": string (markdown body with guidance, explanations, and specific references)
- "file_paths": array of strings (files to read in this step)
- "symbol_ids": array of strings (symbol IDs from the snapshot relevant to this step)
- "estimated_time": string (e.g. "10 minutes", "30 minutes")
- "prerequisite_steps": array of ints
- "difficulty": "beginner" | "intermediate" | "advanced"
- "exercises": array of strings
- "checkpoint": string

**Repository snapshot:**
```json
{snapshot_json}
```

Return ONLY the JSON array. No markdown fences, no preamble."""
