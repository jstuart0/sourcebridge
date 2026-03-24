# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""System and user prompts for learning path generation."""

from __future__ import annotations

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
    "deep": "Create 8-12 detailed steps. Include specific code references and exercises.",
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

    return f"""\
Generate a learning path for the repository "{repository_name}".

**Audience:** {audience}
{audience_instruction}

**Depth:** {depth}
{depth_instruction}
{focus_line}
**Output format:** Return a JSON array of step objects, ordered from first to last. Each object must have:
- "order": int (1-based step number)
- "title": string (short step title)
- "objective": string (what the learner will understand after this step)
- "content": string (markdown body with guidance, explanations, and specific references)
- "file_paths": array of strings (files to read in this step)
- "symbol_ids": array of strings (symbol IDs from the snapshot relevant to this step)
- "estimated_time": string (e.g. "10 minutes", "30 minutes")

**Repository snapshot:**
```json
{snapshot_json}
```

Return ONLY the JSON array. No markdown fences, no preamble."""
