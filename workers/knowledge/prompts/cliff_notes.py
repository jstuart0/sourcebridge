# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""System and user prompts for cliff notes generation."""

from __future__ import annotations

CLIFF_NOTES_SYSTEM = """\
You are a senior software engineer writing codebase field-guide notes — a structured \
report that helps someone quickly understand and safely work in a repository. You produce JSON \
output that strictly follows the schema described in the user prompt.

Rules:
- Every claim must be grounded in evidence from the provided snapshot.
- Use the "evidence" field to cite specific files, symbols, or docs.
- Mark sections as "inferred": true when the conclusion goes beyond \
  direct evidence (e.g. inferred architectural patterns).
- Set "confidence" to "high" when multiple pieces of evidence converge, \
  "medium" for reasonable inferences, "low" for speculative observations.
- Write in clear, concise technical prose. Avoid marketing language.
- Prefer maintainer guidance over abstract architecture recap.
- Prefer code-local evidence over requirements evidence unless the requirement \
  genuinely explains user intent or business purpose for the scope.
- Avoid generic phrases like "acts as a control panel" unless the snapshot \
  provides concrete support.
- Adapt tone and depth to the target audience.
"""

_AUDIENCE_INSTRUCTIONS = {
    "beginner": (
        "The reader is new to programming or this codebase. "
        "Explain concepts simply, avoid jargon, and provide context for technical terms. "
        "Focus on the big picture and how things connect."
    ),
    "developer": (
        "The reader is an experienced developer joining this project. "
        "Be precise and technical. Focus on architecture decisions, key abstractions, "
        "and the non-obvious parts of the system."
    ),
}

_DEPTH_INSTRUCTIONS = {
    "summary": "Keep each section to 2-3 sentences. Prioritize breadth over depth.",
    "medium": "Write 1-2 paragraphs per section. Balance breadth and depth.",
    "deep": (
        "Write thorough sections with detailed explanations. "
        "Include specific code references and explain trade-offs."
    ),
}

REQUIRED_SECTIONS_BY_SCOPE = {
    "repository": [
        "System Purpose",
        "Architecture Overview",
        "Domain Model",
        "Core System Flows",
        "Code Structure",
        "Complexity & Risk Areas",
        "Suggested Starting Points",
    ],
    "module": [
        "Module Purpose",
        "Key Files",
        "Public API",
        "Internal Architecture",
        "Dependencies & Interactions",
        "Key Patterns & Conventions",
    ],
    "file": [
        "File Purpose",
        "Key Symbols",
        "Dependencies",
        "Usage Patterns",
        "Complexity Notes",
    ],
    "symbol": [
        "Purpose",
        "Signature & Parameters",
        "Call Chain",
        "Impact Analysis",
        "Side Effects & State Changes",
        "Usage Examples",
        "Related Symbols",
    ],
}

REQUIRED_SECTIONS = REQUIRED_SECTIONS_BY_SCOPE["repository"]

_SCOPE_INSTRUCTIONS = {
    "repository": (
        "Treat this like an onboarding field guide for a new maintainer. "
        "Explain what the system is for, where to start, what is risky, and when "
        "requirements actually matter. Do not default to a requirements-first lens."
    ),
    "module": (
        "Treat this like a guided handoff for one area of the codebase. "
        "Focus on the files, boundaries, and conventions that matter when working in this module."
    ),
    "file": (
        "Treat this like a maintainer note for a specific file. "
        "Explain why the file exists, what state or behavior it owns, how to read it, "
        "what changes here tend to affect, and where a maintainer should edit carefully."
    ),
    "symbol": (
        "Treat this like a change-safety note for a single symbol. "
        "Explain its purpose, inputs and outputs, main decisions, "
        "side effects, caller/callee impact, blast radius, and what someone "
        "should verify before changing it. If parameter types, side effects, or downstream "
        "systems are not explicitly shown in the snapshot, say that they are not shown rather than inventing them."
    ),
}


def build_cliff_notes_prompt(
    repository_name: str,
    audience: str,
    depth: str,
    snapshot_json: str,
    scope_type: str = "repository",
    scope_path: str = "",
) -> str:
    """Build the user prompt for cliff notes generation."""
    audience_instruction = _AUDIENCE_INSTRUCTIONS.get(audience, _AUDIENCE_INSTRUCTIONS["developer"])
    depth_instruction = _DEPTH_INSTRUCTIONS.get(depth, _DEPTH_INSTRUCTIONS["medium"])

    required_sections = REQUIRED_SECTIONS_BY_SCOPE.get(scope_type or "repository", REQUIRED_SECTIONS)
    scope_label = scope_path or repository_name
    sections_list = "\n".join(f"  {i+1}. {s}" for i, s in enumerate(required_sections))
    scope_instruction = _SCOPE_INSTRUCTIONS.get(scope_type or "repository", _SCOPE_INSTRUCTIONS["repository"])

    intro = (
        f'Generate cliff notes for the {scope_type or "repository"} scope "{scope_label}" '
        f'inside the repository "{repository_name}".'
    )

    return f"""\
{intro}

**Audience:** {audience}
{audience_instruction}

**Depth:** {depth}
{depth_instruction}

**Scope type:** {scope_type or "repository"}
**Scope path:** {scope_path or "(repository root)"}

**Scope guidance:** {scope_instruction}

**Writing priorities:**
- Write like a maintainer helping the next maintainer.
- Explain what matters operationally, not just structurally.
- Prefer concrete editing guidance over generic dependency narration.
- Use requirements evidence only when it clarifies purpose or user intent for this specific scope.
- For file and symbol scopes, prioritize local code behavior over platform-wide framing.
- For symbol scope, never invent runtime layers, storage systems, or parameter details
  that do not appear in the snapshot.
- If the snapshot only shows names and relationships, describe only those names and relationships.
- Do not write literal curl examples unless a route or request shape is actually present in the snapshot.

**Required sections (in order):**
{sections_list}

**Output format:** Return a JSON array of section objects. Each object must have:
- "title": string (must match one of the required section titles exactly)
- "content": string (markdown body)
- "summary": string (one-line summary)
- "confidence": "high" | "medium" | "low"
- "inferred": boolean
- "evidence": array of objects with:
  - "source_type": "file" | "symbol" | "requirement" | "doc"
  - "source_id": string (ID from the snapshot, or empty)
  - "file_path": string (file path if applicable)
  - "line_start": int (0 if not applicable)
  - "line_end": int (0 if not applicable)
  - "rationale": string (why this evidence supports the section)

**Repository snapshot:**
```json
{snapshot_json}
```

Return ONLY the JSON array. No markdown fences, no preamble."""
