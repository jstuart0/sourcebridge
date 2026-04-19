# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""System and user prompts for code tour generation."""

from __future__ import annotations

from workers.knowledge.prompts.fact_hints import build_fact_hints_block

CODE_TOUR_SYSTEM = """\
You are a senior developer creating a guided code tour for a repository. \
A code tour is a sequence of stops, each pointing to a specific file and line \
range with a description explaining what the code does and why it matters. \
You produce JSON output that strictly follows the schema described in the user prompt.

Rules:
- Each stop must reference a real file from the snapshot.
- Order stops to tell a coherent story about the codebase.
- Descriptions should explain the "why" not just the "what".
- Use line ranges from actual symbols in the snapshot when possible.
"""


def build_code_tour_prompt(
    repository_name: str,
    audience: str,
    depth: str,
    snapshot_json: str,
    theme: str = "",
) -> str:
    """Build the user prompt for code tour generation."""
    depth_guidance = {
        "summary": "Create 3-5 stops covering the most important parts.",
        "medium": "Create 5-8 stops with moderate detail.",
        "deep": """Create a 10-15 stop code tour organized into 3-5 themed trails.

Per-stop requirements:
- include trail: a concrete functional grouping
- description must explain why the code is designed this way and
  MUST name AT LEAST TWO specific symbols from the file using
  backticks (e.g. `FunctionName`, `StructName`). These are the
  downstream quality signal — a stop that references only the file
  path without naming any types/functions gets treated as low-
  confidence even if the prose is otherwise solid.
- include 1-2 concrete modification_hints
- every stop must have valid file_path, line_start, and line_end from the snapshot

FILE-PATH DISCIPLINE (violations lower the stop's confidence):
- file_path MUST be a real path visible in the snapshot or in the
  "Representative files" / "Entry-point symbols" / "Public-API
  symbols" anchors. Do not invent paths.
- Every file_path MUST include a file extension (.go, .py, .ts, etc).
""",
    }

    theme_line = ""
    if theme:
        theme_line = f"\n**Theme:** {theme}\nFocus the tour around this theme.\n"

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
                    "These sections come from the repository field guide. Use them directly when structuring trails.\n\n"
                    + "\n\n".join(lines)
                    + "\n"
                )
    except (json.JSONDecodeError, TypeError, ValueError):
        pre_analysis_block = ""

    fact_hints = build_fact_hints_block(snapshot_json)

    return f"""\
Generate a code tour for the repository "{repository_name}".

**Audience:** {audience}
**Depth:** {depth}
{depth_guidance.get(depth, depth_guidance["medium"])}
{theme_line}
{pre_analysis_block}{fact_hints}
**Output format:** Return a JSON array of stop objects. Each object must have:
- "order": int (1-based)
- "title": string (short stop title)
- "description": string (markdown explanation)
- "file_path": string (file from the snapshot)
- "line_start": int (start line, 0 if unknown)
- "line_end": int (end line, 0 if unknown)
- "trail": string
- "modification_hints": array of strings

**Repository snapshot:**
```json
{snapshot_json}
```

Return ONLY the JSON array. No markdown fences, no preamble."""
