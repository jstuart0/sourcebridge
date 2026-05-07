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


def test_parse_json_sections_recovers_ndjson_pair():
    """Two complete JSON objects joined by a newline are recovered as a 2-element list."""
    obj1 = '{"title": "System Purpose", "content": "Provides core services.", "confidence": "high"}'
    obj2 = '{"title": "Architecture Overview", "content": "Layered architecture.", "confidence": "medium"}'
    raw = obj1 + "\n" + obj2
    result = parse_json_sections(raw)
    assert len(result) == 2
    assert result[0]["title"] == "System Purpose"
    assert result[1]["title"] == "Architecture Overview"


def test_parse_json_sections_recovers_ndjson_with_whitespace():
    """CRLF + extra blank line between objects is tolerated."""
    obj1 = '{"title": "System Purpose", "content": "Core services.", "confidence": "high"}'
    obj2 = '{"title": "Domain Model", "content": "Key entities.", "confidence": "high"}'
    # obj1 ends with `}`, obj2 starts with `{` — join them with CRLF whitespace.
    raw = obj1 + "\r\n\r\n  " + obj2
    result = parse_json_sections(raw)
    assert len(result) == 2
    assert result[0]["title"] == "System Purpose"
    assert result[1]["title"] == "Domain Model"


def test_parse_json_sections_rejects_single_truncated_object():
    """A truncated single object (no closing brace) must re-raise JSONDecodeError."""
    import json as _json

    raw = '{"title": "System Purpose", "content": "Missing close brace"'
    try:
        parse_json_sections(raw)
        raise AssertionError("expected JSONDecodeError")
    except _json.JSONDecodeError:
        pass


def test_parse_json_sections_passes_through_valid_array():
    """A well-formed JSON array parses correctly via the primary json.loads path.

    The NDJSON branch is never entered when json.loads succeeds on the first try,
    so this test guards that the happy path is not accidentally rerouted.
    """
    raw = '[{"title": "A", "content": "body A"}, {"title": "B", "content": "body B"}]'
    result = parse_json_sections(raw)
    assert len(result) == 2
    assert result[0]["title"] == "A"
    assert result[1]["title"] == "B"


def test_parse_json_sections_recovers_qwen_artifact_fragment():
    """Synthetic fixture mirroring the qwen3.6 NDJSON regression artifact.

    This fixture is constructed inline to stay stable when the real benchmark
    artifact rotates.  For verification against the actual bench artifact, see
    Phase 4: workers/benchmarks/repro_qwen_confidence_regression.py.
    """
    # Two section-level objects joined by a bare newline — the pattern emitted
    # by qwen3.6:35b-a3b-moe under KV-cache pressure (deep_parallelism=4).
    obj1 = (
        '{"title": "System Purpose", '
        '"content": "Automates knowledge graph generation via GraphQL + workers.", '
        '"summary": "Core purpose.", '
        '"confidence": "high", '
        '"inferred": false, '
        '"evidence": []}'
    )
    obj2 = (
        '{"title": "Architecture Overview", '
        '"content": "GraphQL API + background workers + LLM pipeline.", '
        '"summary": "Layered architecture.", '
        '"confidence": "high", '
        '"inferred": false, '
        '"evidence": []}'
    )
    raw = obj1 + "\n" + obj2
    result = parse_json_sections(raw)
    assert len(result) == 2
    assert result[0]["confidence"] == "high"
    assert result[1]["confidence"] == "high"


def test_parse_json_sections_ndjson_does_not_split_embedded_braces():
    """H4 false-positive trap: a single valid JSON object whose `content` field
    contains the literal substring '}\\n{' must NOT be incorrectly split into
    two elements.  The original JSONDecodeError must be re-raised.

    Decision 2 (plan) pins this contract: the "≥2 dict fragments" floor prevents
    the NDJSON branch from corrupting single-object output.

    Note: the floor actually fires here because the bracket-wrapped fragments fail
    json.loads entirely (the malformed outer string's halves are not parseable),
    causing the inner json.JSONDecodeError to re-raise the original exception.
    The all-dict guard is a second layer of protection that would catch cases where
    wrapping accidentally parses but produces non-dict items.
    """
    import json as _json

    # A valid JSON object — json.loads succeeds first, before the NDJSON branch
    # is ever reached, so this implicitly tests that the happy path is unaffected.
    # The brace-trap matters when the outer object is *malformed* but looks like
    # two objects to the splitter.  We simulate that by using a deliberately
    # truncated outer wrapper with an embedded boundary in the string value.
    single_obj = '{"title": "Logs", "content": "block A\\n}\\n{ block B"}'
    # This is valid JSON — json.loads should succeed directly, returning a dict.
    result = parse_json_sections(single_obj)
    # The function unwraps dicts with title+content into a list of one.
    assert isinstance(result, list)
    assert len(result) == 1
    assert result[0]["title"] == "Logs"

    # Now test the actual false-positive trap: a *malformed* outer string that
    # would naively split into two fragments, but only one is a valid dict.
    # Both halves must fail json.loads so the original JSONDecodeError re-raises.
    malformed = '{"title": "Logs", "content": "A\n}\n{ B"'  # truncated — no closing }
    try:
        parse_json_sections(malformed)
        raise AssertionError("expected JSONDecodeError")
    except _json.JSONDecodeError:
        pass


def test_parse_json_sections_fallback1_miss_falls_through_to_ndjson():
    """Pins the Fallback 1 contextlib.suppress fall-through to Fallback 2.

    Deviation from plan Decision 2: Fallback 1 was originally specified to either
    match-and-parse or not-match-at-all.  In practice, a `[...]`-shaped substring
    can match the regex but fail json.loads (e.g. when the content string embeds a
    `[broken ...` prefix that satisfies `re.search(r"\\[.*\\]", ..., DOTALL)` but
    is not valid JSON on its own).  contextlib.suppress swallows the JSONDecodeError
    so that Fallback 2 NDJSON recovery still runs.  Without suppress, the parse
    error from the malformed bracket-match would escape and prevent NDJSON recovery.

    Input shape: two JSON objects separated by a newline.  The first object's
    content field starts with `[broken ref here`, which satisfies the Fallback 1
    regex (greedy `.*` matches from that `[` to the last `]` in the text, spanning
    both objects).  json.loads on that extracted multi-object substring fails, so
    contextlib.suppress fires.  Fallback 2 then splits on the `}\\n{` boundary
    and successfully recovers both objects.
    """
    # These raw strings double as the expected round-trip values.
    obj1 = {"title": "System Purpose", "content": "[broken ref here", "evidence": []}
    obj2 = {"title": "Architecture", "content": "Layered design.", "evidence": []}
    # Serialize with sorted keys so the boundary regex sees a clean `}\n{` junction.
    import json as _json

    raw1 = _json.dumps(obj1)
    raw2 = _json.dumps(obj2)
    text = raw1 + "\n" + raw2

    result = parse_json_sections(text)
    assert len(result) == 2
    assert result[0] == obj1
    assert result[1] == obj2


def test_parse_json_sections_ndjson_rejects_non_dict_fragment():
    """Pins the all-dict floor in Fallback 2: at least two fragments present, but
    not all of them are dicts — the original JSONDecodeError must be re-raised.

    Note: the boundary regex `}\\s*(?:\\r?\\n)+\\s*{` splits only on `}\\n{`
    transitions, so every fragment is an interior slice of a `{...}` object
    string.  After the bracket-wrap (`[` + `},{`.join(fragments) + `]`) the
    result is structurally always a list of object-shaped values, making it
    impossible to produce a non-dict element through natural input alone.
    The floor therefore acts as a defensive guard against future regex changes
    rather than a currently-reachable runtime path.

    We exercise it via monkeypatching json.loads so the test actually exercises
    the guard branch without requiring an artificial regex-bypass input shape.
    """
    import json as _json
    import unittest.mock as _mock

    # Build valid NDJSON so Fallback 2's re.split produces ≥2 fragments.
    obj1 = '{"title": "First", "content": "A"}'
    obj2 = '{"title": "Second", "content": "B"}'
    text = obj1 + "\n" + obj2

    original_loads = _json.loads
    call_count = 0

    def patched_loads(s: str, **kwargs):
        nonlocal call_count
        call_count += 1
        result = original_loads(s, **kwargs)
        # On the Fallback 2 bracket-wrap call (the `[{...},{...}]` shaped string),
        # return a list where the second element is a list rather than a dict to
        # trigger the all-dict floor.
        if isinstance(result, list) and len(result) == 2 and all(isinstance(x, dict) for x in result):
            return [result[0], [1, 2, 3]]
        return result

    with _mock.patch("workers.knowledge.parse_utils.json.loads", side_effect=patched_loads):
        try:
            parse_json_sections(text)
            raise AssertionError("expected JSONDecodeError")
        except _json.JSONDecodeError:
            pass

    # Confirm we actually entered the patched path (i.e. the mock was reached).
    assert call_count >= 2  # initial json.loads attempt + Fallback 2 bracket-wrap attempt


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
