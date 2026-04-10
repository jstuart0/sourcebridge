# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Section-specific instructions for Compliance Gap Analysis reports."""

from __future__ import annotations

SECTION_INSTRUCTIONS: dict[str, str] = {
    "framework_overview": (
        "Provide a concise overview of the selected compliance framework: what it requires, "
        "who it applies to, what the penalties are for non-compliance. Keep it factual and "
        "reference the official framework version."
    ),
    "control_mapping": (
        "Map detected code evidence to specific framework controls. Use a table: "
        "Control ID | Control Description | Evidence Found | Status (Met/Partial/Gap). "
        "Be precise about what evidence satisfies which control."
    ),
    "gap_identification": (
        "List every control that has insufficient evidence. For each gap: "
        "the control ID, what's missing, why it matters, and what the risk is."
    ),
    "risk_rating": (
        "Rate each gap by risk: Critical (immediate regulatory exposure), "
        "High (material risk), Medium (should address), Low (best practice). "
        "Use a risk matrix table."
    ),
    "remediation_roadmap": (
        "For each gap, provide: what needs to be implemented, estimated effort, "
        "dependencies, and priority order. Frame as a project plan, not a wishlist."
    ),
    "evidence_inventory": (
        "Catalog all evidence found in the codebase that supports compliance: "
        "authentication mechanisms, audit logging, encryption, access controls, etc. "
        "Use a table: Evidence Type | Description | Location | Applicable Controls."
    ),
}


def get_section_instructions(section_key: str) -> str:
    return SECTION_INSTRUCTIONS.get(section_key, "")
