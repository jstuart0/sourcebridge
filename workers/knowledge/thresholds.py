# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Shared code-constant thresholds for knowledge artifacts."""

TITLE_SUMMARY_MAX_CHARS = 160

MIN_IDENTIFIERS_DEFAULT = 2
MIN_FILES_CLIFF_NOTES = 3
MIN_FILES_LEARNING_PATH = 2
MIN_FILES_CODE_TOUR = 1

DEEP_MIN_EVIDENCE = {
    "System Purpose": 2,
    "Architecture Overview": 5,
    "External Dependencies": 3,
    "Domain Model": 5,
    "Core System Flows": 5,
    "Code Structure": 3,
    "Security Model": 4,
    "Error Handling Patterns": 3,
    "Data Flow & Request Lifecycle": 5,
    "Concurrency & State Management": 3,
    "Configuration & Feature Flags": 3,
    "Testing Strategy": 3,
    "Key Abstractions": 5,
    "Module Deep Dives": 5,
    "Complexity & Risk Areas": 4,
    "Suggested Starting Points": 3,
}
