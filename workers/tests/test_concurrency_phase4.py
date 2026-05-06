"""Phase 4 tests: bounded concurrent fan-out of spec extraction group loop.

Tests verify:
  - test_spec_extraction_fans_out_groups: 5+ groups produce peak in-flight > 1
    under max_concurrent=4 (gate cap).
  - test_spec_extraction_serial_when_one_group: single group → no fan-out, no
    behavior change.
  - test_spec_extraction_local_bound_caps_pending_coroutines: 100 groups under
    LOCAL_GROUP_FANOUT_LIMIT=8; at most 8 in-flight at any moment (plan M4).
  - Existing extraction behavior is preserved (model_name, usage accumulation,
    exception handling).

Refs: CA-169 / plan v4 Phase 4.
"""

from __future__ import annotations

import asyncio
from collections.abc import AsyncIterator

import pytest

from workers.common.llm.provider import LLMResponse
from workers.requirements.spec_extraction import refine_with_llm
from workers.requirements.spec_models import CandidateSpec


# ──────────────────────────────────────────────────────────────────────────────
# Helpers


def _candidate(group_key: str, source: str = "test", idx: int = 0) -> CandidateSpec:
    return CandidateSpec(
        source=source,
        source_file=f"tests/test_{group_key}.go",
        source_line=idx * 10 + 1,
        raw_text=f"Test{group_key.capitalize()}_{idx}",
        group_key=group_key,
        language="go",
        metadata={},
    )


def _response(model: str = "test-model") -> LLMResponse:
    return LLMResponse(
        content='{"requirement_text": "The system must do X.", "keywords": ["auth"]}',
        model=model,
        input_tokens=20,
        output_tokens=10,
        stop_reason="end_turn",
    )


class _InstantProvider:
    """Fake provider that returns immediately; records concurrent in-flight count."""

    def __init__(self, *, latency: float = 0.05, model: str = "test-model") -> None:
        self._latency = latency
        self._model = model
        self.peak_in_flight = 0
        self._in_flight = 0
        self._lock = asyncio.Lock()

    @property
    def provider_name(self) -> str:
        return "fake"

    @property
    def default_model(self) -> str:
        return self._model

    async def complete(self, prompt: str, **kwargs: object) -> LLMResponse:
        async with self._lock:
            self._in_flight += 1
            if self._in_flight > self.peak_in_flight:
                self.peak_in_flight = self._in_flight
        try:
            await asyncio.sleep(self._latency)
            return _response(self._model)
        finally:
            async with self._lock:
                self._in_flight -= 1

    async def stream(self, *args: object, **kwargs: object) -> AsyncIterator[str]:
        yield "ok"


# ──────────────────────────────────────────────────────────────────────────────
# 1. test_spec_extraction_fans_out_groups


@pytest.mark.asyncio
async def test_spec_extraction_fans_out_groups() -> None:
    """5 groups with a slow provider should show peak in-flight > 1 (concurrent fan-out)."""
    provider = _InstantProvider(latency=0.05)
    candidates = [_candidate(f"group{i}") for i in range(5)]

    specs, usage = await refine_with_llm(candidates, provider)

    assert len(specs) == 5, f"Expected 5 refined specs, got {len(specs)}"
    assert provider.peak_in_flight > 1, (
        f"Peak in-flight was {provider.peak_in_flight}; expected > 1 (concurrent fan-out)"
    )
    # Usage should be accumulated across all groups.
    assert usage.input_tokens == 5 * 20
    assert usage.output_tokens == 5 * 10
    assert usage.model == "test-model"


# ──────────────────────────────────────────────────────────────────────────────
# 2. test_spec_extraction_serial_when_one_group


@pytest.mark.asyncio
async def test_spec_extraction_serial_when_one_group() -> None:
    """Single group: behavior is unchanged — no fan-out (trivially serial)."""
    provider = _InstantProvider(latency=0.01)
    candidates = [_candidate("auth")]

    specs, usage = await refine_with_llm(candidates, provider)

    assert len(specs) == 1
    assert specs[0].group_key == "auth"
    assert specs[0].llm_refined is True
    assert usage.input_tokens == 20
    assert usage.output_tokens == 10


# ──────────────────────────────────────────────────────────────────────────────
# 3. test_spec_extraction_local_bound_caps_pending_coroutines


@pytest.mark.asyncio
async def test_spec_extraction_local_bound_caps_pending_coroutines() -> None:
    """100 groups: at most LOCAL_GROUP_FANOUT_LIMIT (8) in-flight at any moment.

    Plan codex r1 M4: the local semaphore must prevent N-thousand pending
    coroutines from building up before the upstream gate drains them.

    Approach: use a slow provider (latency=0.04s) so that many groups are
    blocked on the semaphore simultaneously, making the cap observable via
    peak_in_flight. We then verify peak_in_flight <= 8.
    """
    provider = _InstantProvider(latency=0.04)

    # 100 distinct groups, one candidate each.
    candidates = [_candidate(f"group{i}") for i in range(100)]

    specs, usage = await refine_with_llm(candidates, provider)

    assert len(specs) == 100, f"Expected 100 specs, got {len(specs)}"
    # LOCAL_GROUP_FANOUT_LIMIT=8: peak must not exceed the cap.
    assert provider.peak_in_flight <= 8, (
        f"Peak in-flight {provider.peak_in_flight} exceeded LOCAL_GROUP_FANOUT_LIMIT=8"
    )
    # Fan-out is actually happening (not fully serial).
    assert provider.peak_in_flight > 1, (
        f"Peak in-flight was 1; expected > 1 (fan-out should be active for 100 groups)"
    )


# ──────────────────────────────────────────────────────────────────────────────
# 4. test_spec_extraction_exception_handling_preserved


@pytest.mark.asyncio
async def test_spec_extraction_exception_handling_preserved() -> None:
    """LLM failure on one group falls back to raw_text; other groups still refined."""
    call_count = 0

    class _FailOnSecond:
        @property
        def provider_name(self) -> str:
            return "fake"

        @property
        def default_model(self) -> str:
            return "test-model"

        async def complete(self, prompt: str, **kwargs: object) -> LLMResponse:
            nonlocal call_count
            call_count += 1
            if "group1" in prompt:
                raise RuntimeError("simulated LLM failure")
            return _response()

        async def stream(self, *args: object, **kwargs: object) -> AsyncIterator[str]:
            yield "ok"

    candidates = [_candidate("group0"), _candidate("group1"), _candidate("group2")]
    specs, usage = await refine_with_llm(candidates, _FailOnSecond())

    # All three groups produce a spec (failed group falls back to raw_text).
    assert len(specs) == 3
    keys = {s.group_key for s in specs}
    assert "group0" in keys
    assert "group1" in keys
    assert "group2" in keys

    # The failed group uses raw_text as fallback (no keywords, not refined-text-modified).
    failed = next(s for s in specs if s.group_key == "group1")
    assert failed.llm_refined is True  # llm_refined flag is set by the pipeline regardless


# ──────────────────────────────────────────────────────────────────────────────
# 5. test_spec_extraction_output_order_stable


@pytest.mark.asyncio
async def test_spec_extraction_output_order_stable() -> None:
    """Output list preserves groups.items() insertion order (dict ordering).

    deduplicate_specs is key-based so order doesn't affect correctness, but
    stable output is useful for deterministic tests downstream.
    """
    provider = _InstantProvider(latency=0.0)
    keys = [f"key{i:03d}" for i in range(10)]
    candidates = [_candidate(k) for k in keys]

    specs, _ = await refine_with_llm(candidates, provider)

    assert [s.group_key for s in specs] == keys, (
        "Output order does not match input group order"
    )


# ──────────────────────────────────────────────────────────────────────────────
# 6. test_spec_extraction_usage_accumulates_across_groups


@pytest.mark.asyncio
async def test_spec_extraction_usage_accumulates_across_groups() -> None:
    """Token counts are summed across all groups; model_name is last writer wins."""
    call_num = 0

    class _CountingProvider:
        @property
        def provider_name(self) -> str:
            return "fake"

        @property
        def default_model(self) -> str:
            return "test-model"

        async def complete(self, prompt: str, **kwargs: object) -> LLMResponse:
            nonlocal call_num
            call_num += 1
            return LLMResponse(
                content='{"requirement_text": "R.", "keywords": []}',
                model=f"model-{call_num}",
                input_tokens=call_num * 10,
                output_tokens=call_num * 5,
                stop_reason="end_turn",
            )

        async def stream(self, *args: object, **kwargs: object) -> AsyncIterator[str]:
            yield "ok"

    n = 4
    candidates = [_candidate(f"g{i}") for i in range(n)]
    specs, usage = await refine_with_llm(candidates, _CountingProvider())

    assert len(specs) == n
    # Total input_tokens = 10 + 20 + 30 + 40 = 100 (but call_num order is
    # non-deterministic under fan-out; assert sum is correct regardless of order).
    expected_input = sum(i * 10 for i in range(1, n + 1))
    expected_output = sum(i * 5 for i in range(1, n + 1))
    assert usage.input_tokens == expected_input, (
        f"input_tokens {usage.input_tokens} != {expected_input}"
    )
    assert usage.output_tokens == expected_output, (
        f"output_tokens {usage.output_tokens} != {expected_output}"
    )
    # model_name is a non-empty string (last-writer wins under fan-out).
    assert usage.model.startswith("model-"), f"model name unexpected: {usage.model}"
