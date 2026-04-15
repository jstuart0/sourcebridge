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
        "Write thorough sections with detailed explanations. Include specific code references and explain trade-offs."
    ),
}

REQUIRED_SECTIONS_BY_SCOPE = {
    "repository": [
        "System Purpose",
        "Architecture Overview",
        "External Dependencies",
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
    "requirement": [
        "Requirement Intent",
        "Implementation Summary",
        "Key Implementation Files",
        "Key Symbols",
        "Integration Points",
        "Coverage Assessment",
        "Change Impact",
    ],
}

REQUIRED_SECTIONS = REQUIRED_SECTIONS_BY_SCOPE["repository"]

_SCOPE_INSTRUCTIONS = {
    "repository": (
        "Treat this like an onboarding field guide for a new maintainer. "
        "Explain what the system is for, where to start, what is risky, and when "
        "requirements actually matter. Do not default to a requirements-first lens.\n"
        "Focus on:\n"
        "- What the system does and who it serves (one paragraph, not a product brief)\n"
        "- Which third-party services, SaaS/PaaS platforms, APIs, or infrastructure providers it depends on\n"
        "- How those external systems are used, and where the integration boundaries live\n"
        "- The critical paths a maintainer would trace first\n"
        "- Where complexity hides and what to watch out for\n"
        "- Concrete starting points (files, entry points) not abstract layers\n"
        "Avoid architecture-summary voice. Write as if handing the repo to a colleague."
    ),
    "module": (
        "Treat this like a guided handoff for one area of the codebase. "
        "Focus on the files, boundaries, and conventions that matter when working in this module."
    ),
    "file": (
        "Treat this like a maintainer note for a specific file. "
        "Explain why the file exists, what state or behavior it owns, how to read it, "
        "what changes here tend to affect, and where a maintainer should edit carefully.\n"
        "Focus on:\n"
        "- What this file is responsible for (not just what it imports)\n"
        "- The state it manages or transforms\n"
        "- Which edits are safe vs which have downstream ripple effects\n"
        "- Concrete symbol names and their roles, not generic 'depends on X' narration\n"
        "Avoid stock AI phrases like 'acts as a control panel' or 'serves as the backbone'."
    ),
    "symbol": (
        "Treat this like a change-safety note for a single symbol. "
        "Explain its purpose, inputs and outputs, main decisions, "
        "side effects, caller/callee impact, blast radius, and what someone "
        "should verify before changing it.\n"
        "STRICT GROUNDING RULES for symbol scope:\n"
        "- Only describe parameter types, return types, and signatures that appear literally in the snapshot.\n"
        "- If parameter types or signatures are not shown, write 'Parameter types not available in snapshot' — do not guess.\n"  # noqa: E501
        "- Do not invent runtime infrastructure (databases, caches, queues, HTTP clients) unless the snapshot "
        "shows explicit references to them.\n"
        "- Do not describe what happens 'downstream' beyond the direct callees listed in scope_context.\n"
        "- The 'Signature & Parameters' section must only contain information from the snapshot's "
        "target_symbol and scope_context fields. If those fields lack type info, say so.\n"
        "- Stay local: every claim must trace back to a symbol name, file path, or caller/callee "
        "relationship visible in the snapshot."
    ),
    "requirement": (
        "Treat this like an implementation trace for a single requirement. "
        "The snapshot's scope_context.target_requirement describes what was asked for "
        "including its full description; the linked symbols and files show how it was built.\n"
        "Focus on:\n"
        "- What the requirement asks for and why it matters (from the requirement description, "
        "not just the title)\n"
        "- How the implementation is spread across files and symbols — the cross-cutting shape\n"
        "- Which symbols carry the core logic vs which are supporting glue\n"
        "- Integration points: how do the implementing files connect to each other "
        "and to the rest of the system?\n"
        "- Gaps: linked symbols that seem tangential, or expected coverage that is missing\n"
        "- What a developer would need to change if this requirement evolved\n"
        "Avoid restating the requirement verbatim. Explain how the code realizes it.\n"
        "If there are very few linked symbols (< 5), focus on depth over breadth — "
        "explain the implementation in detail rather than trying to find cross-cutting patterns."
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
    sections_list = "\n".join(f"  {i + 1}. {s}" for i, s in enumerate(required_sections))
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
- For repository scope, explicitly separate external dependencies from internal architecture.
- In the external dependencies section, include third-party integrations, SaaS/PaaS services, external APIs,
  cloud infrastructure, authentication providers, messaging providers, storage services, or observability platforms
  only when the snapshot provides evidence they are actually used.
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
