# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Section-specific instructions for Portfolio Health Dashboard reports."""

from __future__ import annotations

SECTION_INSTRUCTIONS: dict[str, str] = {
    "portfolio_overview": (
        "Provide a dashboard-style overview: total repos, total files, total symbols, "
        "total LOC (estimated), primary languages, deployment targets. Use a summary "
        "table with one row per repo."
    ),
    "understanding_scores": (
        "Show the understanding score breakdown for each repo in a table: "
        "Repo | Overall | Traceability | Documentation | Review | Test | Knowledge. "
        "Highlight repos below 50 as needing attention."
    ),
    "security_summary": (
        "Aggregate security findings across all repos. Total vulnerabilities by severity. "
        "Top 5 most critical findings. Which repos have the most findings."
    ),
    "cross_repo_patterns": (
        "Identify patterns that span multiple repos: shared dependencies, common frameworks, "
        "consistent vs inconsistent practices. Call out any repos that diverge significantly "
        "from the portfolio norm."
    ),
}


def get_section_instructions(section_key: str) -> str:
    return SECTION_INSTRUCTIONS.get(section_key, "")
