# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Condense a knowledge snapshot to fit within an LLM context window.

Instead of naively truncating, this progressively reduces detail while
preserving the architectural overview.  The LLM still sees every module,
language, and requirement — it just doesn't see all 2000+ individual
symbols when the repo is large.
"""

from __future__ import annotations

import json

import structlog

log = structlog.get_logger()

# Rough chars-per-token ratio for JSON (conservative)
_CHARS_PER_TOKEN = 3.5

# Reserve tokens for system prompt + user prompt wrapper + output
_OVERHEAD_TOKENS = 4_000

# Default max context size (tokens)
_DEFAULT_MAX_TOKENS = 100_000


def _estimated_tokens(text: str) -> int:
    return int(len(text) / _CHARS_PER_TOKEN)


def condense_snapshot(
    snapshot_json: str,
    max_tokens: int = _DEFAULT_MAX_TOKENS,
) -> str:
    """Progressively condense a snapshot JSON to fit within a token budget.

    Reduction strategy (applied in order until the snapshot fits):
      1. Strip doc_comment from symbol refs (verbose, rarely useful in bulk)
      2. Strip full content from docs (keep paths only)
      3. Cap links at 200, sorted by confidence
      4. Cap each symbol list at 100
      5. Cap each symbol list at 50
      6. Remove docs list entirely
      7. Remove links list entirely
      8. Remove all symbol lists, keep only counts

    At every stage the structural metadata (languages, modules, counts,
    requirements, coverage_ratio) is preserved in full.
    """
    budget_chars = int((max_tokens - _OVERHEAD_TOKENS) * _CHARS_PER_TOKEN)

    if len(snapshot_json) <= budget_chars:
        return snapshot_json

    try:
        snap = json.loads(snapshot_json)
    except (json.JSONDecodeError, TypeError):
        return snapshot_json[:budget_chars]

    original_tokens = _estimated_tokens(snapshot_json)
    symbol_keys = (
        "entry_points", "public_api", "test_symbols", "complex_symbols",
        "high_fan_out_symbols", "high_fan_in_symbols",
    )

    def _compact() -> str:
        return json.dumps(snap, separators=(",", ":"))

    # Step 1: strip doc_comment from all symbol lists
    for key in symbol_keys:
        for sym in snap.get(key, []):
            sym.pop("doc_comment", None)

    result = _compact()
    if len(result) <= budget_chars:
        log.info("snapshot_condensed", strategy="strip_doc_comments",
                 original_tokens=original_tokens, result_tokens=_estimated_tokens(result))
        return result

    # Step 2: strip doc content (keep paths for reference)
    for doc in snap.get("docs", []):
        doc.pop("content", None)

    result = _compact()
    if len(result) <= budget_chars:
        log.info("snapshot_condensed", strategy="strip_doc_content",
                 original_tokens=original_tokens, result_tokens=_estimated_tokens(result))
        return result

    # Step 3: cap links at 200, highest confidence first
    links = snap.get("links", [])
    if len(links) > 200:
        links.sort(key=lambda x: x.get("confidence", 0), reverse=True)
        snap["links"] = links[:200]

    result = _compact()
    if len(result) <= budget_chars:
        log.info("snapshot_condensed", strategy="cap_links_200",
                 original_tokens=original_tokens, result_tokens=_estimated_tokens(result))
        return result

    # Step 4: cap symbol lists at 100
    for key in symbol_keys:
        lst = snap.get(key, [])
        if len(lst) > 100:
            snap[key] = lst[:100]

    result = _compact()
    if len(result) <= budget_chars:
        log.info("snapshot_condensed", strategy="cap_symbols_100",
                 original_tokens=original_tokens, result_tokens=_estimated_tokens(result))
        return result

    # Step 5: cap symbol lists at 50
    for key in symbol_keys:
        lst = snap.get(key, [])
        if len(lst) > 50:
            snap[key] = lst[:50]

    result = _compact()
    if len(result) <= budget_chars:
        log.info("snapshot_condensed", strategy="cap_symbols_50",
                 original_tokens=original_tokens, result_tokens=_estimated_tokens(result))
        return result

    # Step 6: remove docs entirely
    snap["docs"] = []

    result = _compact()
    if len(result) <= budget_chars:
        log.info("snapshot_condensed", strategy="remove_docs",
                 original_tokens=original_tokens, result_tokens=_estimated_tokens(result))
        return result

    # Step 7: remove links entirely
    snap["links"] = []

    result = _compact()
    if len(result) <= budget_chars:
        log.info("snapshot_condensed", strategy="remove_links",
                 original_tokens=original_tokens, result_tokens=_estimated_tokens(result))
        return result

    # Step 8: remove all symbol lists, keep only counts
    for key in symbol_keys:
        snap[key] = []

    result = _compact()
    log.info("snapshot_condensed", strategy="remove_all_symbols",
             original_tokens=original_tokens, result_tokens=_estimated_tokens(result))
    return result
