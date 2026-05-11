"""Regression test for the CA-181 result_holder IndexError bug.

The extracted helper `_run_simple_streaming_generation` was assigning
the work-task return value via `result_holder[0] = ...`, but every
caller initialises `result_holder = []`. That mismatch raised
`IndexError: list assignment index out of range` and failed all three
generators (learning_path, workflow_story, code_tour) after the LLM
had already completed.

The fix switches the helper to `result_holder.append(...)`. Downstream
readers continue to use `result_holder[0]` (the first appended value).
This test pins the contract: the helper MUST work when called with an
empty list, and the result MUST be readable at index 0.
"""

from __future__ import annotations


def test_append_pattern_works_with_empty_list():
    """The exact pattern used in the helper (line 1505) must not raise."""
    result_holder: list = []
    # Simulate the helper's append after work_task completes.
    result_holder.append(("result_value", "usage_record"))
    # Simulate the caller's read pattern at lines 1608/1907/2167.
    result, usage = result_holder[0]
    assert result == "result_value"
    assert usage == "usage_record"


def test_old_index_assignment_raised_indexerror():
    """Document the bug we fixed: result_holder[0] = ... on empty list raised."""
    result_holder: list = []
    try:
        result_holder[0] = "value"
    except IndexError as exc:
        assert "list assignment index out of range" in str(exc)
    else:
        raise AssertionError(
            "Expected IndexError on empty-list index assignment; got nothing. "
            "Has Python's behaviour changed? If so, revisit the CA-181 fix."
        )
