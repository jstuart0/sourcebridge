# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Section-specific extra instructions for Architecture Baseline reports.

These are appended to the general section prompt when generating sections
of an Architecture Baseline report. They provide domain-specific guidance
that makes the output match the quality of a professional consulting
deliverable (modeled on the Hoegg Software MACU report template).
"""

from __future__ import annotations

SECTION_INSTRUCTIONS: dict[str, str] = {
    "executive_summary": (
        "Write an executive summary that a CEO can read in 2 minutes and understand the "
        "overall state of the application portfolio. Lead with the overall assessment "
        "(healthy/concerning/critical), then the 3 most important findings, then what "
        "needs to happen next. Do NOT repeat section content — synthesize it."
    ),
    "overall_assessment": (
        "Provide a one-paragraph overall assessment using one of: 'The portfolio delivers "
        "real value but carries significant unaddressed risk', 'The portfolio is operationally "
        "sound with specific areas for improvement', or craft a similar assessment. Then list "
        "3-5 key themes that emerged from the analysis."
    ),
    "applications_inventory": (
        "For each repository, list: name, URL (if known), source repo URL, architecture type "
        "(managed cloud, self-hosted, etc.), tech stack (language, framework, database), "
        "and a one-sentence description of what the application does. Use a table format."
    ),
    "owasp_findings": (
        "Summarize the OWASP scan results. Group findings by severity (Critical, High, Medium, "
        "Low). For each Critical and High finding, explain the risk in plain language and "
        "reference the evidence appendix. Do NOT list every finding — summarize patterns."
    ),
    "authentication": (
        "Document what authentication patterns are used across the portfolio. Note where "
        "SSO, MFA, RBAC, API keys, JWT, or session-based auth are present or absent. "
        "Call out any inconsistencies between applications. Include an 'Areas for "
        "Validation / Questions' subsection."
    ),
    "testing": (
        "Document what test frameworks, test files, and coverage configurations were detected. "
        "Be explicit: if no tests were found, say 'No automated tests were detected.' "
        "Do not soften the finding. Include a table of test file counts per repository."
    ),
    "deployment_architecture": (
        "Describe the deployment topology: what platforms (Vercel, AWS, Docker, etc.), "
        "what deployment workflow (push-to-main, PR-based, manual), what environments "
        "(dev/staging/prod or just prod). Include a description of each distinct deployment "
        "pattern found across the portfolio."
    ),
    "source_control": (
        "Analyze git workflow: Do they use branches? Pull requests? Code review? "
        "Direct-to-main pushes? What is the commit frequency? How many contributors? "
        "Is there a CODEOWNERS file? Do they use conventional commits?"
    ),
    "compliance": (
        "Based on the data types handled (student records → FERPA, financial → GLBA, "
        "health → HIPAA, payment → PCI-DSS), identify which compliance frameworks "
        "LIKELY apply. Frame as 'should be validated' not 'is required'. Include an "
        "'Applicable Requirements (Subject to Validation)' subsection."
    ),
    "arch_review_findings": (
        "This is the deep-dive section. Number your findings (1, 2, 3...). Each finding "
        "should have: a bold title, a 2-4 sentence explanation of what was found and why "
        "it matters, and specific evidence references. Order by severity/importance. "
        "Model after: '1. Authentication gaps are wider than previously identified...' "
        "This section should be the longest and most detailed in the report."
    ),
}


def get_section_instructions(section_key: str) -> str:
    """Return extra prompt instructions for a specific section, or empty string."""
    return SECTION_INSTRUCTIONS.get(section_key, "")
