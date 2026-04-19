# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Shared helpers for parsing LLM responses across the knowledge artifacts.

Cliff notes, learning path, code tour, and workflow story all parse
JSON returned from the model and need to coerce ints / preserve
identifiers / evaluate mechanical grounding criteria. Keeping the
helpers here means a bug fix (like the ``null`` line-number coerce
that originally took down cliff notes DEEP runs) propagates to every
artifact at once rather than being copy-pasted per module.
"""

from __future__ import annotations

import json
import re
from collections.abc import Iterable

# Backtick-wrapped identifiers like ``CliffNotesRenderer`` or
# ``GenerateArchitectureDiagram``. Used as a lightweight "specificity"
# signal when evaluating whether a unit (section / step / stop) is
# grounded enough to earn high-confidence status.
SPECIFIC_IDENTIFIER_RE = re.compile(r"`([A-Za-z_][A-Za-z0-9_]{2,})`")


def coerce_int(value: object, default: int = 0) -> int:
    """Coerce an LLM-provided number-like value to ``int``.

    LLMs occasionally emit ``null``, stringified numbers, floats, or
    booleans where the schema expects an int (line_start, line_end,
    order, etc.). The original cliff-notes DEEP pipeline crashed with
    ``NoneType > int`` when ``null`` survived into downstream
    comparisons — the fix is to normalise at parse time.
    """

    if isinstance(value, bool):
        return default
    if isinstance(value, int):
        return value
    if isinstance(value, float):
        return int(value)
    if isinstance(value, str):
        try:
            return int(value.strip())
        except ValueError:
            return default
    return default


def count_unique_file_paths(paths: Iterable[str]) -> int:
    """Count unique non-empty file paths from a set of citations."""
    return len({p.strip() for p in paths if p and p.strip()})


def count_specific_identifiers(text: str) -> int:
    """Count unique backtick-wrapped identifiers in a markdown body."""
    return len({m.group(1) for m in SPECIFIC_IDENTIFIER_RE.finditer(text or "")})


def meets_confidence_floor(
    *,
    current_confidence: str,
    unique_file_paths: Iterable[str],
    content: str,
    min_files: int = 3,
    min_identifiers: int = 2,
) -> bool:
    """Return True when a unit is grounded enough to earn HIGH confidence.

    Mirrors the cliff-notes ``_enforce_deep_confidence_floor`` policy
    but works on any artifact's "unit" shape — pass whatever the unit's
    current confidence, its cited files, and its rendered text are. The
    caller decides how to apply the result (upgrade confidence, clear a
    refinement flag, etc.) because each artifact stores those fields
    differently.
    """

    if (current_confidence or "").lower() == "high":
        return False
    if count_unique_file_paths(unique_file_paths) < min_files:
        return False
    if count_specific_identifiers(content) < min_identifiers:
        return False
    return True


def collect_snapshot_file_paths(snapshot_json: str) -> set[str]:
    """Extract the set of real file paths present in the repository snapshot.

    Shared by learning_path and code_tour post-parse hallucination
    filters. The filters drop any LLM-emitted file path that isn't in
    this set — haiku occasionally invents plausible-looking paths like
    ``internal/worker/queue.go`` even when no such file exists. This
    walker pulls paths from every symbol list + module/file array the
    assembler emits.
    """

    try:
        snap = json.loads(snapshot_json) if snapshot_json else {}
    except (json.JSONDecodeError, TypeError, ValueError):
        return set()
    if not isinstance(snap, dict):
        return set()

    paths: set[str] = set()
    for key in (
        "entry_points",
        "public_api",
        "test_symbols",
        "complex_symbols",
        "high_fan_in_symbols",
        "high_fan_out_symbols",
    ):
        for sym in snap.get(key) or []:
            if isinstance(sym, dict):
                p = (sym.get("file_path") or "").strip()
                if p:
                    paths.add(p)
    for module in snap.get("modules") or []:
        if not isinstance(module, dict):
            continue
        for f in module.get("files") or []:
            if isinstance(f, dict):
                p = (f.get("path") or f.get("file_path") or "").strip()
                if p:
                    paths.add(p)
            elif isinstance(f, str) and f.strip():
                paths.add(f.strip())
    for f in snap.get("files") or []:
        if isinstance(f, dict):
            p = (f.get("path") or f.get("file_path") or "").strip()
            if p:
                paths.add(p)
        elif isinstance(f, str) and f.strip():
            paths.add(f.strip())
    return paths
