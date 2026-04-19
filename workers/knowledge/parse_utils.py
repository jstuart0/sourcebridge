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
from collections.abc import Callable, Iterable
from typing import Any

from workers.knowledge.evidence import is_valid_evidence_path
from workers.knowledge.types import EvidenceRef

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


def load_json_dict(raw: str) -> dict[str, Any]:
    """Parse a JSON object, returning ``{}`` for invalid or non-dict input."""

    try:
        parsed = json.loads(raw) if raw else {}
    except (json.JSONDecodeError, TypeError, ValueError):
        return {}
    return parsed if isinstance(parsed, dict) else {}


def normalize_text(raw: object) -> str:
    """Flatten nested content values into readable text."""

    if raw is None:
        return ""
    if isinstance(raw, str):
        text = raw.strip()
        if text.startswith("{") or text.startswith("["):
            try:
                decoded = json.loads(text)
            except (json.JSONDecodeError, TypeError, ValueError):
                return text
            return normalize_text(decoded)
        return text
    if isinstance(raw, dict):
        if "content" in raw:
            return normalize_text(raw.get("content"))
        if "text" in raw:
            return normalize_text(raw.get("text"))
        if "summary" in raw:
            return normalize_text(raw.get("summary"))
        return json.dumps(raw, ensure_ascii=False)
    if isinstance(raw, list):
        parts = [normalize_text(item) for item in raw]
        parts = [part for part in parts if part]
        return "\n".join(parts)
    return str(raw).strip()


def normalize_section_object(raw: dict[str, object], *, title_summary_max_chars: int) -> dict[str, object]:
    """Flatten nested section-shaped objects into the expected top-level shape."""

    try:
        content = raw.get("content")
        if isinstance(content, dict):
            nested = content
            merged = dict(nested)
            merged.setdefault("title", raw.get("title"))
            merged.setdefault("summary", raw.get("summary", ""))
            merged.setdefault("confidence", raw.get("confidence", "medium"))
            merged.setdefault("inferred", raw.get("inferred", False))
            outer_ev = raw.get("evidence", [])
            inner_ev = nested.get("evidence", [])
            fallback_ev = outer_ev if isinstance(outer_ev, list) else (inner_ev if isinstance(inner_ev, list) else [])
            merged.setdefault("evidence", fallback_ev)
            raw = merged

        evidence = raw.get("evidence", [])
        if not isinstance(evidence, list):
            evidence = []

        content_text = normalize_text(raw.get("content", ""))
        summary_text = normalize_text(raw.get("summary", ""))
        if not summary_text and content_text:
            summary_text = content_text.splitlines()[0][:title_summary_max_chars]

        return {
            "title": normalize_text(raw.get("title", "")),
            "content": content_text,
            "summary": summary_text,
            "confidence": normalize_text(raw.get("confidence", "medium")) or "medium",
            "inferred": bool(raw.get("inferred", False)),
            "evidence": evidence,
        }
    except (AttributeError, TypeError, KeyError):
        return {
            "title": "",
            "content": normalize_text(raw) if raw else "",
            "summary": "",
            "confidence": "low",
            "inferred": True,
            "evidence": [],
        }


def parse_json_sections(raw: str) -> list[dict[str, object]]:
    """Parse JSON array from LLM response, tolerating common LLM quirks."""

    text = (raw or "").strip()
    text = re.sub(r"<think>.*?</think>", "", text, flags=re.DOTALL).strip()

    if text.startswith("```"):
        first_newline = text.find("\n")
        text = text[first_newline + 1 :] if first_newline != -1 else text[3:]
        text = text.rstrip()
        if text.endswith("```"):
            text = text[:-3].rstrip()

    try:
        parsed = json.loads(text)
    except json.JSONDecodeError:
        match = re.search(r"\[.*\]", text, flags=re.DOTALL)
        if match:
            parsed = json.loads(match.group())
        else:
            raise

    if isinstance(parsed, dict):
        for key in ("sections", "data", "items", "results", "steps", "stops"):
            if key in parsed and isinstance(parsed[key], list):
                return parsed[key]  # type: ignore[no-any-return]
        if len(parsed) == 1:
            sole_value = next(iter(parsed.values()))
            if isinstance(sole_value, list):
                return sole_value  # type: ignore[no-any-return]
            if isinstance(sole_value, dict) and all(isinstance(v, dict) for v in sole_value.values()):
                return [{"title": k, **v} for k, v in sole_value.items()]
        if "title" in parsed and "content" in parsed:
            return [parsed]
        if all(isinstance(v, dict) for v in parsed.values()):
            return [{"title": k, **v} for k, v in parsed.items()]
        if all(isinstance(v, str) for v in parsed.values()):
            return [{"title": k, "content": v} for k, v in parsed.items()]
        raise ValueError("expected a JSON array of sections")

    if not isinstance(parsed, list):
        raise ValueError("expected a JSON array of sections")
    return parsed  # type: ignore[no-any-return]


def parse_evidence(raw_evidence: list[dict]) -> list[EvidenceRef]:
    """Parse evidence entries from the LLM response."""

    result = []
    for ev in raw_evidence:
        if not isinstance(ev, dict):
            continue
        file_path = str(ev.get("file_path", "") or "").strip()
        if not is_valid_evidence_path(file_path):
            continue
        result.append(
            EvidenceRef(
                source_type=ev.get("source_type", "file"),
                source_id=ev.get("source_id", ""),
                file_path=file_path,
                line_start=coerce_int(ev.get("line_start")),
                line_end=coerce_int(ev.get("line_end")),
                rationale=ev.get("rationale", ""),
            )
        )
    return result


def coerce_section(raw: object, *, fallback_title: str, title_summary_max_chars: int) -> dict[str, object]:
    """Coerce a raw LLM section candidate into the expected dict shape."""

    if isinstance(raw, dict):
        normalized = normalize_section_object(raw, title_summary_max_chars=title_summary_max_chars)
        if not normalized.get("title"):
            normalized["title"] = fallback_title
        return normalized

    text = normalize_text(raw)
    summary = text.splitlines()[0] if text else "LLM output could not be structured for this section."
    return {
        "title": fallback_title,
        "content": text or "*Insufficient structured content returned for this section.*",
        "summary": summary[:title_summary_max_chars],
        "confidence": "low",
        "inferred": True,
        "evidence": [],
    }


def parse_with_fallback(
    raw: str,
    *,
    fallback_item_fn: Callable[[str], dict[str, object]],
) -> list[dict[str, object]]:
    """Parse JSON sections, or return a single fallback item on parse failure."""

    try:
        return parse_json_sections(raw)
    except (json.JSONDecodeError, ValueError, TypeError):
        return [fallback_item_fn(raw)]


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
    """Return the set of file paths the snapshot explicitly names.

    This is the strict "exact-match" set — paths here appear verbatim
    in a symbol list or module/file array. Compare against
    :func:`collect_snapshot_path_signals` which also returns parent
    directories for a softer grounded-ness check.
    """

    return collect_snapshot_path_signals(snapshot_json)[0]


def collect_snapshot_path_signals(snapshot_json: str) -> tuple[set[str], set[str]]:
    """Return (known_full_paths, known_directories) from the snapshot.

    The ``KnowledgeSnapshot`` only materialises file paths for symbols
    it tracks — not every file in the repo. Using ``known_full_paths``
    alone as the ground truth drops real-but-untracked files (e.g.
    ``internal/db/migrations.go`` when only ``internal/db/store.go`` has
    a tracked symbol), which torpedoes the HIGH-confidence floor on the
    learning path. Using ``known_directories`` lets a filter say "this
    looks like it belongs to a real directory in the repo, keep it"
    while still catching clear inventions like ``internal/worker/
    queue.go`` when there is no ``internal/worker/`` anywhere.
    """

    try:
        snap = json.loads(snapshot_json) if snapshot_json else {}
    except (json.JSONDecodeError, TypeError, ValueError):
        return set(), set()
    if not isinstance(snap, dict):
        return set(), set()

    paths: set[str] = set()

    def _add(raw: object) -> None:
        if not isinstance(raw, str):
            return
        text = raw.strip()
        if text:
            paths.add(text)

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
                _add(sym.get("file_path"))
    for module in snap.get("modules") or []:
        if isinstance(module, dict):
            _add(module.get("path"))
            for f in module.get("files") or []:
                if isinstance(f, dict):
                    _add(f.get("path") or f.get("file_path"))
                else:
                    _add(f)
    for f in snap.get("files") or []:
        if isinstance(f, dict):
            _add(f.get("path") or f.get("file_path"))
        else:
            _add(f)
    for doc in snap.get("docs") or []:
        if isinstance(doc, dict):
            _add(doc.get("path"))

    dirs: set[str] = set()
    for p in paths:
        # Walk every ancestor directory so a cited file under a known
        # module directory counts as grounded even when its exact path
        # isn't tracked as a symbol.
        parent = p
        while True:
            idx = parent.rfind("/")
            if idx < 0:
                break
            parent = parent[:idx]
            if not parent:
                break
            dirs.add(parent)

    # Pure path values like ``internal/db`` are also directories.
    dirs.update(p for p in paths if "/" in p and "." not in p.rsplit("/", 1)[-1])

    return paths, dirs


def path_looks_grounded(
    file_path: str,
    known_paths: set[str],
    known_dirs: set[str],
) -> bool:
    """Return True when the path is either exactly known or sits inside a known directory.

    The directory-prefix check is deliberately lenient: it keeps
    real-but-untracked file citations while still rejecting obvious
    inventions where the entire directory is absent from the snapshot.
    """

    fp = (file_path or "").strip()
    if not fp:
        return False
    if fp in known_paths:
        return True
    idx = fp.rfind("/")
    if idx < 0:
        # Bare basenames (README, go.mod) — trust them.
        return True
    parent = fp[:idx]
    return parent in known_dirs
