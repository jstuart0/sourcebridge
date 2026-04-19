# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Tests for the shared knowledge-artifact parse helpers."""

from __future__ import annotations

from workers.knowledge.parse_utils import (
    coerce_int,
    coerce_section,
    count_specific_identifiers,
    count_unique_file_paths,
    load_json_dict,
    meets_confidence_floor,
    normalize_text,
    parse_json_sections,
    parse_with_fallback,
)
from workers.knowledge.thresholds import TITLE_SUMMARY_MAX_CHARS


def test_coerce_int_handles_null_and_strings():
    assert coerce_int(None) == 0
    assert coerce_int("42") == 42
    assert coerce_int("  7 ") == 7
    assert coerce_int("not-a-number") == 0
    assert coerce_int("not-a-number", default=-1) == -1
    assert coerce_int(3.9) == 3
    assert coerce_int(True) == 0
    assert coerce_int({"x": 1}) == 0


def test_count_unique_file_paths_ignores_blanks():
    paths = [" internal/foo.go ", "internal/foo.go", "", None, "workers/bar.py"]
    assert count_unique_file_paths(p for p in paths if p is not None) == 2


def test_load_json_dict_returns_empty_dict_for_invalid_input():
    assert load_json_dict("") == {}
    assert load_json_dict("not-json") == {}
    assert load_json_dict('["x"]') == {}
    assert load_json_dict('{"ok": true}') == {"ok": True}


def test_normalize_text_flattens_nested_content():
    assert normalize_text({"content": {"text": " nested "}}) == "nested"
    assert normalize_text(["one", {"summary": "two"}]) == "one\ntwo"


def test_parse_json_sections_handles_fenced_payload():
    raw = """```json
    {"sections":[{"title":"System Purpose","content":"Hello"}]}
    ```"""
    parsed = parse_json_sections(raw)
    assert parsed == [{"title": "System Purpose", "content": "Hello"}]


def test_parse_with_fallback_returns_single_item_on_parse_error():
    parsed = parse_with_fallback(
        "not-json",
        fallback_item_fn=lambda text: {"title": "Fallback", "content": text},
    )
    assert parsed == [{"title": "Fallback", "content": "not-json"}]


def test_coerce_section_applies_title_and_summary_limits():
    section = coerce_section(
        "A" * 200,
        fallback_title="Fallback",
        title_summary_max_chars=TITLE_SUMMARY_MAX_CHARS,
    )
    assert section["title"] == "Fallback"
    assert len(section["summary"]) == TITLE_SUMMARY_MAX_CHARS


def test_count_specific_identifiers_finds_backticked_names():
    content = "The `FooService` calls `BarController.load` via `_private_helper`."
    # Only the plain identifiers inside single backticks count; qualified
    # names like `BarController.load` don't match the regex.
    assert count_specific_identifiers(content) == 2


def test_meets_confidence_floor_positive_path():
    assert meets_confidence_floor(
        current_confidence="low",
        unique_file_paths={"internal/foo.go", "internal/bar.go", "workers/baz.py"},
        content="Mentions `FooService` and `BarController` explicitly.",
        min_files=3,
        min_identifiers=2,
    ) is True


def test_meets_confidence_floor_already_high_returns_false():
    assert meets_confidence_floor(
        current_confidence="high",
        unique_file_paths={"a.go", "b.go", "c.go"},
        content="`X` and `Y` and `Z`.",
    ) is False


def test_meets_confidence_floor_requires_both_thresholds():
    # enough files but too few identifiers
    assert meets_confidence_floor(
        current_confidence="low",
        unique_file_paths={"a.go", "b.go", "c.go"},
        content="Only `Foo` here.",
    ) is False
    # enough identifiers but too few files
    assert meets_confidence_floor(
        current_confidence="low",
        unique_file_paths={"a.go"},
        content="`Foo` and `Bar` and `Baz`.",
    ) is False


def test_fact_hints_block_surfaces_real_anchors():
    """The block should extract key files, entry points, and deps so
    prompts don't rely on the LLM scanning the full snapshot for
    anchors."""
    import json as _json

    from workers.knowledge.prompts.fact_hints import build_fact_hints_block

    snapshot = _json.dumps(
        {
            "top_files": [
                {"file_path": "internal/foo.go"},
                {"file_path": "workers/bar.py"},
            ],
            "entry_points": [
                {"qualified_name": "FooService.Start", "file_path": "internal/foo.go"},
            ],
            "external_dependencies": ["grpc", "openai"],
        }
    )
    block = build_fact_hints_block(snapshot)
    assert "Representative files" in block
    assert "internal/foo.go" in block
    assert "FooService.Start" in block
    assert "grpc" in block


def test_fact_hints_block_returns_empty_when_snapshot_has_no_useful_data():
    from workers.knowledge.prompts.fact_hints import build_fact_hints_block

    assert build_fact_hints_block("") == ""
    assert build_fact_hints_block("{}") == ""
    assert build_fact_hints_block("not-json") == ""
