# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""System and user prompts for whole-system explanation."""

from __future__ import annotations

EXPLAIN_SYSTEM_SYSTEM = """\
You are a senior software engineer explaining a codebase to a colleague. \
Provide a clear, evidence-based explanation that answers the given question \
(or gives an overview if no question is provided). Your response should be \
in markdown format. Reference specific files and symbols from the snapshot.
"""


_DEPTH_GUIDANCE = {
    "summary": "Keep it brief — a high-level overview in a few paragraphs. Focus on the big picture, skip implementation details.",
    "medium": "Provide a balanced explanation with enough detail to understand the design and key decisions, but don't go line-by-line.",
    "deep": "Be thorough and detailed. Explain architecture, design patterns, edge cases, and implementation specifics. Include code-level references.",
}


def build_explain_system_prompt(
    repository_name: str,
    audience: str,
    question: str,
    snapshot_json: str,
    depth: str = "medium",
) -> str:
    """Build the user prompt for whole-system explanation."""
    q = question or f'Give me a comprehensive overview of the "{repository_name}" repository.'
    depth_guide = _DEPTH_GUIDANCE.get(depth, _DEPTH_GUIDANCE["medium"])

    return f"""\
**Question:** {q}

**Audience:** {audience}

**Depth:** {depth} — {depth_guide}

**Repository snapshot:**
```json
{snapshot_json}
```

Provide a thorough markdown explanation. Reference specific files, symbols, and \
architectural elements from the snapshot. Be concrete — cite evidence."""
