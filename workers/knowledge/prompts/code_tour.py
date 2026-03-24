# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""System and user prompts for code tour generation."""

from __future__ import annotations

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
        "deep": "Create 8-12 stops with thorough explanations.",
    }

    theme_line = ""
    if theme:
        theme_line = f"\n**Theme:** {theme}\nFocus the tour around this theme.\n"

    return f"""\
Generate a code tour for the repository "{repository_name}".

**Audience:** {audience}
**Depth:** {depth}
{depth_guidance.get(depth, depth_guidance["medium"])}
{theme_line}
**Output format:** Return a JSON array of stop objects. Each object must have:
- "order": int (1-based)
- "title": string (short stop title)
- "description": string (markdown explanation)
- "file_path": string (file from the snapshot)
- "line_start": int (start line, 0 if unknown)
- "line_end": int (end line, 0 if unknown)

**Repository snapshot:**
```json
{snapshot_json}
```

Return ONLY the JSON array. No markdown fences, no preamble."""
