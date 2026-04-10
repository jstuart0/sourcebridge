# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Section-specific instructions for Technical Due Diligence reports."""

from __future__ import annotations

SECTION_INSTRUCTIONS: dict[str, str] = {
    "dd_executive_summary": (
        "Write an investment-grade executive summary. Lead with the overall risk rating, "
        "then the top 3 technical risks, then the estimated remediation timeline. "
        "This should read like a section from an M&A technical assessment."
    ),
    "tech_risk": (
        "Quantify technical debt: outdated dependencies, missing tests, security "
        "vulnerabilities, architectural complexity. Assign a risk rating per category. "
        "Include a technical debt heat map (table: Category | Severity | Repos Affected | Effort to Fix)."
    ),
    "scalability": (
        "Assess the architecture's ability to scale: is it stateless? Can it horizontally "
        "scale? What are the bottlenecks? Are there single points of failure?"
    ),
    "team_knowledge_risk": (
        "Analyze the bus factor: how many contributors? Is knowledge concentrated? "
        "What documentation exists? What happens if key people leave?"
    ),
    "ip_licensing": (
        "List all dependency licenses. Flag any copyleft licenses (GPL, AGPL) that could "
        "affect the acquirer's ability to use the code commercially. Note any missing licenses."
    ),
    "remediation_effort": (
        "Provide a prioritized remediation backlog as a table: "
        "Priority | Item | Effort | Risk Reduction | Dependencies. "
        "Include a total estimated remediation cost."
    ),
}


def get_section_instructions(section_key: str) -> str:
    return SECTION_INSTRUCTIONS.get(section_key, "")
