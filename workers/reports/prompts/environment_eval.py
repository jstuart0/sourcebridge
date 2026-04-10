# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Section-specific instructions for Environment Evaluation reports."""

from __future__ import annotations

SECTION_INSTRUCTIONS: dict[str, str] = {
    "tech_stack": (
        "Create a comprehensive inventory of all technologies detected: programming languages "
        "(with version if detectable), frameworks, databases, cloud services, build tools. "
        "Use a table with columns: Category, Technology, Version, Repos Using It."
    ),
    "infrastructure": (
        "Map the deployment topology: where does each application run? What hosting providers? "
        "What deployment patterns (containerized, serverless, bare metal)? Include a description "
        "of each distinct deployment pattern."
    ),
    "code_quality": (
        "Report on measurable quality indicators: test coverage ratios, documentation coverage, "
        "understanding scores, dependency health. Use tables and comparative metrics across repos."
    ),
    "security_posture": (
        "Aggregate all security findings from OWASP scans and dependency audits. Summarize by "
        "severity tier. Identify portfolio-wide patterns (e.g., 'no application has security headers')."
    ),
    "modernization": (
        "Assess modernization readiness: which frameworks are current vs outdated? What migration "
        "paths exist? Estimate the effort to modernize. Identify the lowest-effort/highest-impact "
        "modernization targets."
    ),
}


def get_section_instructions(section_key: str) -> str:
    return SECTION_INSTRUCTIONS.get(section_key, "")
