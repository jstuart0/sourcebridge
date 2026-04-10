# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Section-specific instructions for SWOT Analysis reports."""

from __future__ import annotations

SECTION_INSTRUCTIONS: dict[str, str] = {
    "strengths": (
        "Identify concrete strengths grounded in evidence: well-architected components, "
        "consistent patterns, good test coverage, clean dependency management, active "
        "development, strong documentation. Reference specific repos and files. "
        "Group strengths by category: architecture, engineering practices, operations."
    ),
    "weaknesses": (
        "Identify concrete weaknesses with evidence: missing tests, outdated dependencies, "
        "security vulnerabilities, inconsistent patterns, poor documentation, high complexity. "
        "Quantify where possible (e.g., '0 of 7 repos have automated tests'). "
        "Reference specific repos. Be direct — don't soften findings."
    ),
    "opportunities": (
        "Identify actionable improvement opportunities: framework consolidation, "
        "test automation, CI/CD improvements, security hardening, platform standardization. "
        "Each opportunity should be specific and achievable (not aspirational). "
        "Include estimated effort for each."
    ),
    "threats": (
        "Identify risks: known vulnerabilities, compliance exposure, bus factor (key-person "
        "dependency), outdated frameworks approaching EOL, scaling limitations, vendor lock-in. "
        "Rate each threat's likelihood and impact."
    ),
    "recommendations": (
        "Synthesize the SWOT analysis into a prioritized action plan. Group into: "
        "Immediate (this week), Short-term (this month), Medium-term (this quarter), "
        "Long-term (this year). Each recommendation should reference which SWOT quadrant "
        "it addresses and include an effort estimate."
    ),
}


def get_section_instructions(section_key: str) -> str:
    return SECTION_INSTRUCTIONS.get(section_key, "")
