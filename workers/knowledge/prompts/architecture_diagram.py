# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

from __future__ import annotations

ARCHITECTURE_DIAGRAM_SYSTEM = """\
You are a software architect generating a high-level system-context architecture diagram.

Return Mermaid flowchart syntax only. No prose, no explanations, no Markdown fences.

Rules:
1. This is a 1000-foot system view, not a module call graph.
2. Use 5-8 boxes maximum, grouped around major subsystems and external actors.
3. Prefer labels like User Interfaces, API & Auth, Knowledge Orchestration,
   Background Workers, Code Graph & Index, Persistence, Repository Access.
4. Do not include call counts, file paths, or low-level module names as
   node labels unless they are the subsystem labels provided in the context.
5. Treat the system_components and system_flows context as the primary structure to render.
6. Treat the deterministic architecture scaffold as a grounding aid, not the thing to redraw literally.
7. If an edge is uncertain, omit it rather than guessing.
8. Avoid naming a subgraph and a node with the same identifier.
9. Output a single Mermaid flowchart diagram only.
10. Use concrete edge labels such as HTTP/API requests, dispatches jobs,
    stores artifacts, reads repository snapshots, calls LLM provider.
11. Do not use generic edge labels like primary flow, major flow, secondary flow, or data flow.
12. Avoid reciprocal edge pairs and avoid connecting every node to every other node.
"""


def build_architecture_diagram_prompt(
    repository_name: str,
    audience: str,
    depth: str,
    architecture_context_json: str,
    deterministic_diagram_json: str,
) -> str:
    return f"""\
Generate an AI architecture diagram for the repository "{repository_name}".

Target audience: {audience}
Depth: {depth}

Use the bounded architecture context below to create a visual system-context
diagram that stays structurally grounded and easy to scan.

Deterministic scaffold JSON:
{deterministic_diagram_json}

Bounded architecture context JSON:
{architecture_context_json}
"""


def build_architecture_diagram_retry_prompt(
    repository_name: str,
    audience: str,
    depth: str,
    deterministic_diagram_json: str,
    invalid_output: str,
) -> str:
    return f"""\
The previous attempt to generate an architecture diagram for "{repository_name}" was invalid Mermaid.

Target audience: {audience}
Depth: {depth}

You must return a single valid Mermaid flowchart only.
Requirements:
- Start with `flowchart LR` or `flowchart TD`
- Define explicit nodes or subgraphs
- Return a 1000-foot system view with subsystem boxes, not a dense module graph
- Use 5-8 boxes maximum and avoid dense cross-linking
- Use the provided system components and system flows as the main structure
- Do not include call counts or low-level module labels in the final diagram
- Use concrete edge labels, never generic labels like `primary flow` or `major flow`
- Omit uncertain edges instead of guessing
- No prose, no Markdown fences, no explanation

Deterministic scaffold JSON:
{deterministic_diagram_json}

Invalid previous output:
{invalid_output}
"""
