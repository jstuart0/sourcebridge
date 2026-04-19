# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Shared fact-hint block for knowledge-artifact prompts.

The repository snapshot is a large JSON document — the LLM has to hunt
through ``entry_points``, ``public_api``, ``symbols`` arrays, etc. to
find the concrete anchors it's supposed to reference. The cliff-notes
pipeline proved that surfacing a short, pre-distilled "Key facts"
block ahead of the full snapshot dramatically improves grounding and
specificity, even on small local models.

This helper builds that block from the same snapshot structure all
four artifacts receive, so learning path / code tour / workflow story
benefit from the same anchor hints cliff notes already uses internally.
"""

from __future__ import annotations

import json
from typing import Any


def _take_list(source: Any, key: str, limit: int) -> list[dict]:
    raw = source.get(key) if isinstance(source, dict) else None
    if not isinstance(raw, list):
        return []
    out: list[dict] = []
    for item in raw:
        if isinstance(item, dict):
            out.append(item)
        if len(out) >= limit:
            break
    return out


def build_fact_hints_block(snapshot_json: str, *, max_files: int = 10, max_symbols_per_group: int = 6) -> str:
    """Return a short prompt section summarising the most-cited anchors.

    Empty string if the snapshot can't be parsed or carries nothing
    useful — the caller should fall back to the original prompt.
    """

    try:
        snap = json.loads(snapshot_json) if snapshot_json else {}
    except (json.JSONDecodeError, TypeError, ValueError):
        return ""
    if not isinstance(snap, dict):
        return ""

    parts: list[str] = []

    key_files: list[str] = []
    for item in _take_list(snap, "top_files", max_files):
        path = (item.get("file_path") or item.get("path") or "").strip()
        if path:
            key_files.append(path)
    if not key_files:
        for item in _take_list(snap, "key_files", max_files):
            path = (item.get("file_path") or item.get("path") or "").strip()
            if path:
                key_files.append(path)
    if key_files:
        parts.append("Representative files: " + ", ".join(f"`{p}`" for p in key_files[:max_files]))

    for group_name, label in (
        ("entry_points", "Entry-point symbols"),
        ("public_api", "Public-API symbols"),
        ("high_fan_in_symbols", "High-fan-in symbols"),
    ):
        symbols = []
        for item in _take_list(snap, group_name, max_symbols_per_group):
            name = (item.get("qualified_name") or item.get("name") or "").strip()
            path = (item.get("file_path") or "").strip()
            if not name:
                continue
            if path:
                symbols.append(f"`{name}` (in `{path}`)")
            else:
                symbols.append(f"`{name}`")
        if symbols:
            parts.append(f"{label}: " + "; ".join(symbols))

    modules = _take_list(snap, "modules", max_symbols_per_group)
    module_names: list[str] = []
    for item in modules:
        name = (item.get("path") or item.get("name") or "").strip()
        if name:
            module_names.append(f"`{name}`")
    if module_names:
        parts.append("Modules: " + ", ".join(module_names[:max_symbols_per_group]))

    deps = snap.get("external_dependencies")
    if isinstance(deps, list):
        dep_names = [str(d).strip() for d in deps if str(d).strip()]
        if dep_names:
            parts.append("External dependencies: " + ", ".join(f"`{d}`" for d in dep_names[:max_symbols_per_group]))

    if not parts:
        return ""

    header = (
        "\n**Structured fact anchors (use these by name — they're real)**\n"
        "The snapshot below is large; the names listed here are the most\n"
        "load-bearing ones. Cite them explicitly when they apply, and when\n"
        "you reference a file, use one of the paths listed here whenever\n"
        "possible rather than paraphrasing.\n\n"
    )
    return header + "\n".join(f"- {line}" for line in parts) + "\n"
