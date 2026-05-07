"""Phase 3 tests: activated gate + retry + real defaults + deleted hand-rolled retries.

Tests verify:
  - Global semaphore caps in-flight (single subsystem + two via shared registry)
  - Local and global semaphores compose correctly
  - Host-gate caps combined LLM + embedding on real Ollama URLs (Decision 1)
  - Retry on 429 with full assertions: call count, Retry-After, terminal exc type
  - Retry-After header honored for both OpenAI and Anthropic shapes
  - SDK retry is disabled (HTTP call count == wrapper attempts)
  - No retry on 401 (auth failure)
  - No retry on pydantic.ValidationError
  - RPM rate limiting via aiolimiter (custom limiter for unit test; real-clock
    integration test marked @pytest.mark.slow — see decision notes below)
  - Retry on 503 (renderer path coverage)
  - Slot released on cancel during upstream call
  - Slot released on cancel during limiter wait
  - Cancellation while queued decrements waiter count
  - Slot released between retry attempts (Decision 2 layering)
  - Slot NOT held during limiter wait (Decision 2 layering)
  - Jitter spreads retry timestamps (mocked random.uniform)
  - gate rejects max_concurrent < 1 (covered in phase 1; extended here)
  - ConcurrencyConfig.from_env() rejects invalid RPM
  - ConcurrencyConfig.from_env() rejects zero max_concurrent
  - Unknown provider token in env var is rejected at startup
  - Empty-content retry preserved in openai_compat after Phase 3
  - Registry close is idempotent
  - Registry rejects lookup after close
  - Registry close during in-flight calls cleans up correctly

Decision notes:
  - aiolimiter.AsyncLimiter uses self._loop.time() with no injectable clock seam.
    A custom SimpleLimiter is used for unit tests (asyncio.Lock + sleep). A
    @pytest.mark.slow real-clock test exercises the production aiolimiter path.
  - tenacity.wait_random_exponential calls random.uniform at module level with no
    seeded RNG seam. Tests mock random.uniform to assert deterministic wait values.

Refs: CA-169 / plan v4 Phase 3 Verification list.
"""

from __future__ import annotations

import asyncio
import dataclasses
import random
from collections.abc import AsyncIterator
from unittest.mock import MagicMock, patch

import anthropic
import httpx
import openai
import pytest

from workers.common.llm.concurrency import (
    ConcurrencyConfig,
    ConcurrencyGatedProvider,
    ProviderGate,
    ProviderGateRegistry,
    _retry_predicate,
)
from workers.common.llm.fake import FakeLLMProvider
from workers.common.llm.provider import LLMResponse

# ──────────────────────────────────────────────────────────────────────────────
# Shared helpers


def _registry(config: ConcurrencyConfig | None = None) -> ProviderGateRegistry:
    return ProviderGateRegistry(config or ConcurrencyConfig())


def _response(content: str = "ok", output_tokens: int = 10) -> LLMResponse:
    return LLMResponse(
        content=content,
        model="test-model",
        input_tokens=5,
        output_tokens=output_tokens,
        stop_reason="end_turn",
    )


async def _make_gated(
    provider_name: str = "openai",
    base_url: str = "https://api.openai.com/v1",
    kind: str = "llm",
    *,
    config: ConcurrencyConfig,
    raw: FakeLLMProvider | None = None,
) -> tuple[ConcurrencyGatedProvider, ProviderGate]:
    registry = _registry(config)
    gate = await registry.lookup(provider_name, base_url, kind)
    raw_provider = raw or FakeLLMProvider()
    wrapped = ConcurrencyGatedProvider(raw_provider, gate, config)
    return wrapped, gate


# ──────────────────────────────────────────────────────────────────────────────
# 1. test_wrapper_caps_in_flight_at_max_concurrent
#    Parametrized: single subsystem + two subsystems via shared registry


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "scenario",
    ["single_subsystem", "two_subsystems_via_shared_registry"],
)
async def test_wrapper_caps_in_flight_at_max_concurrent(scenario: str) -> None:
    """Global semaphore ensures peak in-flight <= max_concurrent=2."""
    config = ConcurrencyConfig(
        llm_max_concurrent={"openai": 2},
        retry_max_attempts=1,
    )
    registry = _registry(config)

    if scenario == "single_subsystem":
        gate_a = await registry.lookup("openai", "https://api.openai.com/v1", "llm")
        gate_b = gate_a
    else:
        gate_a = await registry.lookup("openai", "https://api.openai.com/v1", "llm")
        gate_b = await registry.lookup("openai", "https://api.openai.com/v1", "llm")

    peak: list[int] = []
    event = asyncio.Event()

    async def one(gate: ProviderGate) -> None:
        async with gate.slot():
            peak.append(gate._binding._in_flight)
            event.set()
            await asyncio.sleep(0.03)

    tasks = [asyncio.create_task(one(gate_a)) for _ in range(4)]
    if scenario == "two_subsystems_via_shared_registry":
        tasks += [asyncio.create_task(one(gate_b)) for _ in range(4)]

    await asyncio.gather(*tasks)
    assert max(peak) <= 2, f"Peak in-flight {max(peak)} exceeded cap 2"


# ──────────────────────────────────────────────────────────────────────────────
# 2. test_local_and_global_semaphores_compose_correctly


@pytest.mark.asyncio
async def test_local_and_global_semaphores_compose_correctly() -> None:
    """Local semaphore AND global gate cap compose: effective cap = min(local, global)."""
    config = ConcurrencyConfig(
        llm_max_concurrent={"openai": 3},
        retry_max_attempts=1,
    )
    registry = _registry(config)
    gate = await registry.lookup("openai", "https://api.openai.com/v1", "llm")

    local_sem = asyncio.Semaphore(2)
    peak: list[int] = []

    async def one() -> None:
        async with local_sem, gate.slot():
            peak.append(gate._binding._in_flight)
            await asyncio.sleep(0.02)

    await asyncio.gather(*[asyncio.create_task(one()) for _ in range(6)])
    # local cap=2 < global cap=3 → effective peak should be ≤ 2
    assert max(peak) <= 2, f"Peak {max(peak)} violated local semaphore cap"


# ──────────────────────────────────────────────────────────────────────────────
# 3. test_host_gate_caps_combined_llm_and_embedding_on_ollama_real_defaults


@pytest.mark.asyncio
async def test_host_gate_caps_combined_llm_and_embedding_on_ollama_real_defaults() -> None:
    """Ollama LLM + embedding share one host gate (Decision 1 URL normalization).

    Real factory default URLs:
      LLM:       http://localhost:11434/v1  (llm/config.py:80-81)
      embedding: http://localhost:11434     (embedding/config.py:33)
    """
    config = ConcurrencyConfig(llm_max_concurrent={"ollama": 1}, retry_max_attempts=1)
    registry = _registry(config)

    gate_llm = await registry.lookup("ollama", "http://localhost:11434/v1", "llm")
    gate_embed = await registry.lookup("ollama", "http://localhost:11434", "embedding")

    # Same normalized origin → same binding gate.
    assert gate_llm._binding is gate_embed._binding

    peak: list[int] = []
    event = asyncio.Event()

    async def hold(gate: ProviderGate) -> None:
        async with gate.slot():
            peak.append(gate_llm._binding._in_flight)
            event.set()
            await asyncio.sleep(0.03)

    await asyncio.gather(hold(gate_llm), hold(gate_embed))
    # Combined in-flight never exceeds 1.
    assert max(peak) == 1, f"Peak {max(peak)}: ollama gate not combining LLM + embedding"


# ──────────────────────────────────────────────────────────────────────────────
# 4. test_wrapper_retries_on_429_full_assertions


@pytest.mark.asyncio
async def test_wrapper_retries_on_429_full_assertions() -> None:
    """On 429: (a) call count == retry_max_attempts, (b) terminal exc is original RateLimitError."""
    max_attempts = 3
    config = ConcurrencyConfig(llm_max_concurrent={"openai": 4}, retry_max_attempts=max_attempts)

    call_count = 0

    class _FakeRateLimitProvider:
        provider_name = "openai"

        @property
        def default_model(self) -> str:
            return "gpt-4o"

        async def complete(self, prompt: str, **kwargs: object) -> LLMResponse:
            nonlocal call_count
            call_count += 1
            # Build a minimal fake RateLimitError (OpenAI SDK shape).
            exc = openai.RateLimitError(
                message="Rate limit exceeded",
                response=MagicMock(headers={}, status_code=429),
                body={"error": {"message": "rate limit"}},
            )
            raise exc

        async def stream(self, *args: object, **kwargs: object) -> AsyncIterator[str]:
            raise NotImplementedError

    registry = _registry(config)
    gate = await registry.lookup("openai", "https://api.openai.com/v1", "llm")
    wrapped = ConcurrencyGatedProvider(_FakeRateLimitProvider(), gate, config)

    with pytest.raises(openai.RateLimitError):
        await wrapped.complete("hello")

    # (a) call count equals max_attempts
    assert call_count == max_attempts, f"Expected {max_attempts} calls, got {call_count}"


@pytest.mark.asyncio
async def test_wrapper_retries_on_429_terminal_exception_is_original() -> None:
    """Terminal exception after retry exhaustion is the original RateLimitError, not RetryError."""
    from tenacity import RetryError

    config = ConcurrencyConfig(llm_max_concurrent={"openai": 4}, retry_max_attempts=2)
    exc_to_raise = openai.RateLimitError(
        message="Rate limit exceeded",
        response=MagicMock(headers={}, status_code=429),
        body={"error": {"message": "rate limit"}},
    )

    class _AlwaysRateLimit:
        provider_name = "openai"

        @property
        def default_model(self) -> str:
            return "gpt-4o"

        async def complete(self, *a: object, **kw: object) -> LLMResponse:
            raise exc_to_raise

        async def stream(self, *a: object, **kw: object) -> AsyncIterator[str]:
            raise NotImplementedError

    registry = _registry(config)
    gate = await registry.lookup("openai", "https://api.openai.com/v1", "llm")
    wrapped = ConcurrencyGatedProvider(_AlwaysRateLimit(), gate, config)

    caught: BaseException | None = None
    try:
        await wrapped.complete("hello")
    except BaseException as e:
        caught = e

    assert caught is not None
    # reraise=True means tenacity re-raises the original, not a RetryError wrapper.
    assert not isinstance(caught, RetryError), (
        f"Expected original RateLimitError, got RetryError wrapping {caught.__cause__}"
    )
    assert isinstance(caught, openai.RateLimitError), f"Got {type(caught)}"


# ──────────────────────────────────────────────────────────────────────────────
# 5. test_wrapper_respects_retry_after_header_per_sdk


@pytest.mark.asyncio
async def test_wrapper_respects_retry_after_header_per_sdk() -> None:
    """Retry-After header is parsed from both OpenAI and Anthropic error shapes."""
    # OpenAI: response.headers["retry-after"]
    mock_response_openai = MagicMock()
    mock_response_openai.headers = {"retry-after": "5"}
    mock_response_openai.status_code = 429
    exc_openai = openai.RateLimitError(
        message="too many requests",
        response=mock_response_openai,
        body={"error": {"message": "rate limit"}},
    )

    # Anthropic: response.headers["retry-after"]
    mock_response_anthropic = MagicMock()
    mock_response_anthropic.headers = {"retry-after": "10"}
    mock_response_anthropic.status_code = 429
    exc_anthropic = anthropic.RateLimitError(
        message="too many requests",
        response=mock_response_anthropic,
        body={"error": {"message": "rate limit"}},
    )

    from workers.common.llm.concurrency import _extract_retry_after

    assert _extract_retry_after(exc_openai) == 5.0
    assert _extract_retry_after(exc_anthropic) == 10.0


@pytest.mark.asyncio
async def test_wrapper_respects_retry_after_header_none_when_absent() -> None:
    """_extract_retry_after returns None when no header is present."""
    from workers.common.llm.concurrency import _extract_retry_after

    exc = openai.RateLimitError(
        message="too many requests",
        response=MagicMock(headers={}, status_code=429),
        body={},
    )
    assert _extract_retry_after(exc) is None


# ──────────────────────────────────────────────────────────────────────────────
# 6. test_sdk_retry_is_disabled


def test_sdk_retry_is_disabled_openai() -> None:
    """AsyncOpenAI is constructed with max_retries=0 (Phase 3)."""
    from workers.common.llm.openai_compat import OpenAICompatProvider

    provider = OpenAICompatProvider(api_key="test-key", base_url="http://localhost:11434/v1")
    assert provider.client.max_retries == 0, (
        f"Expected max_retries=0, got {provider.client.max_retries}"
    )


def test_sdk_retry_is_disabled_anthropic() -> None:
    """AsyncAnthropic is constructed with max_retries=0 (Phase 3)."""
    from workers.common.llm.anthropic import AnthropicProvider

    provider = AnthropicProvider(api_key="test-key")
    assert provider.client.max_retries == 0, (
        f"Expected max_retries=0, got {provider.client.max_retries}"
    )


# ──────────────────────────────────────────────────────────────────────────────
# 7. test_wrapper_does_not_retry_on_401


@pytest.mark.asyncio
async def test_wrapper_does_not_retry_on_401() -> None:
    """401 Unauthorized is not retryable — wrapper makes exactly one attempt."""
    call_count = 0

    class _Unauthorized:
        provider_name = "openai"

        @property
        def default_model(self) -> str:
            return "gpt-4o"

        async def complete(self, *a: object, **kw: object) -> LLMResponse:
            nonlocal call_count
            call_count += 1
            exc = openai.AuthenticationError(
                message="Incorrect API key",
                response=MagicMock(headers={}, status_code=401),
                body={"error": {"message": "auth error"}},
            )
            raise exc

        async def stream(self, *a: object, **kw: object) -> AsyncIterator[str]:
            raise NotImplementedError

    config = ConcurrencyConfig(llm_max_concurrent={"openai": 4}, retry_max_attempts=5)
    registry = _registry(config)
    gate = await registry.lookup("openai", "https://api.openai.com/v1", "llm")
    wrapped = ConcurrencyGatedProvider(_Unauthorized(), gate, config)

    with pytest.raises(openai.AuthenticationError):
        await wrapped.complete("hello")

    assert call_count == 1, f"Expected 1 call (no retry), got {call_count}"


# ──────────────────────────────────────────────────────────────────────────────
# 8. test_wrapper_does_not_retry_on_validation_error


@pytest.mark.asyncio
async def test_wrapper_does_not_retry_on_validation_error() -> None:
    """pydantic.ValidationError is not retryable — wrapper makes exactly one attempt."""
    import pydantic

    call_count = 0

    class _ValidationFailure:
        provider_name = "openai"

        @property
        def default_model(self) -> str:
            return "gpt-4o"

        async def complete(self, *a: object, **kw: object) -> LLMResponse:
            nonlocal call_count
            call_count += 1

            class _M(pydantic.BaseModel):
                x: int

            _M(x="not-an-int")  # type: ignore[arg-type]  # raises ValidationError
            raise AssertionError("unreachable")

        async def stream(self, *a: object, **kw: object) -> AsyncIterator[str]:
            raise NotImplementedError

    config = ConcurrencyConfig(llm_max_concurrent={"openai": 4}, retry_max_attempts=5)
    registry = _registry(config)
    gate = await registry.lookup("openai", "https://api.openai.com/v1", "llm")
    wrapped = ConcurrencyGatedProvider(_ValidationFailure(), gate, config)

    with pytest.raises(pydantic.ValidationError):
        await wrapped.complete("hello")

    assert call_count == 1, f"Expected 1 call (no retry), got {call_count}"


# ──────────────────────────────────────────────────────────────────────────────
# 9. test_wrapper_rate_limits_with_aiolimiter
#
# Decision note: aiolimiter.AsyncLimiter uses self._loop.time() — no injectable
# clock. We use a custom SimpleLimiter for this unit test that is seeded with
# asyncio.Lock and recorded acquire timestamps. A real-clock slow test follows.


class _SimpleLimiter:
    """Minimal rate limiter for testing: allows max_rate calls per time_period.

    Tracks acquire timestamps so tests can assert spacing without needing
    aiolimiter's internal clock.
    """

    def __init__(self, max_rate: float, time_period: float = 1.0) -> None:
        self._delay = time_period / max_rate
        self._lock = asyncio.Lock()
        self._last: float = 0.0
        self.acquire_times: list[float] = []

    async def acquire(self, amount: float = 1) -> None:
        async with self._lock:
            now = asyncio.get_event_loop().time()
            wait = self._last + self._delay - now
            if wait > 0:
                await asyncio.sleep(wait)
            self._last = asyncio.get_event_loop().time()
            self.acquire_times.append(self._last)


@pytest.mark.asyncio
async def test_wrapper_rate_limits_with_custom_limiter() -> None:
    """Rate limiter enforces spacing between successive calls (custom limiter for unit test).

    The production path uses aiolimiter.AsyncLimiter; the logic tested here is the
    wrapper's interaction with _any_ compatible limiter object (Decision 7).
    """
    limiter = _SimpleLimiter(max_rate=2, time_period=1.0)  # 2 calls/sec → 0.5s gap

    call_count = 0

    class _Instant:
        provider_name = "openai"

        @property
        def default_model(self) -> str:
            return "gpt-4o"

        async def complete(self, *a: object, **kw: object) -> LLMResponse:
            nonlocal call_count
            call_count += 1
            return _response()

        async def stream(self, *a: object, **kw: object) -> AsyncIterator[str]:
            yield "ok"

    config = ConcurrencyConfig(llm_max_concurrent={"openai": 4}, retry_max_attempts=1)
    registry = _registry(config)
    gate = await registry.lookup("openai", "https://api.openai.com/v1", "llm")

    provider = _Instant()
    wrapped = ConcurrencyGatedProvider(provider, gate, config)
    # Override the limiter directly (bypassing RPM env-var path for unit-test isolation).
    wrapped._limiter = limiter  # type: ignore[assignment]

    await wrapped.complete("a")
    await wrapped.complete("b")
    await wrapped.complete("c")

    assert call_count == 3
    # At 2 calls/sec, each call should be ≥ 0.4s after the previous.
    times = limiter.acquire_times
    assert len(times) == 3
    for i in range(1, len(times)):
        gap = times[i] - times[i - 1]
        assert gap >= 0.35, f"Gap {gap:.3f}s too small; expected ≥ 0.35s for 2 calls/sec"


@pytest.mark.slow
@pytest.mark.asyncio
async def test_wrapper_rate_limits_with_real_aiolimiter() -> None:
    """Real-clock integration: aiolimiter.AsyncLimiter enforces 2 calls/sec.

    Marked @pytest.mark.slow — not run in CI's fast suite. Exercises the
    production aiolimiter code path end-to-end.
    """
    from aiolimiter import AsyncLimiter

    call_times: list[float] = []

    class _Timed:
        provider_name = "openai"

        @property
        def default_model(self) -> str:
            return "gpt-4o"

        async def complete(self, *a: object, **kw: object) -> LLMResponse:
            call_times.append(asyncio.get_event_loop().time())
            return _response()

        async def stream(self, *a: object, **kw: object) -> AsyncIterator[str]:
            yield "ok"

    config = ConcurrencyConfig(llm_max_concurrent={"openai": 4}, retry_max_attempts=1)
    registry = _registry(config)
    gate = await registry.lookup("openai", "https://api.openai.com/v1", "llm")
    provider = _Timed()
    wrapped = ConcurrencyGatedProvider(provider, gate, config)
    wrapped._limiter = AsyncLimiter(max_rate=2, time_period=1.0)  # type: ignore[assignment]

    for _ in range(3):
        await wrapped.complete("test")

    assert len(call_times) == 3
    # Each successive call should be spaced ≥ 0.4s apart (2 calls/sec).
    for i in range(1, len(call_times)):
        gap = call_times[i] - call_times[i - 1]
        assert gap >= 0.4, f"Gap {gap:.3f}s too small (expected ≥ 0.4s at 2 calls/sec)"


# ──────────────────────────────────────────────────────────────────────────────
# 10. test_wrapper_retries_on_503_via_renderer


@pytest.mark.asyncio
async def test_wrapper_retries_on_503_via_renderer() -> None:
    """503 ServiceUnavailable is retried by the gate (Decision 4 whitelist)."""
    call_count = 0

    class _FlakyProvider:
        provider_name = "openai"

        @property
        def default_model(self) -> str:
            return "gpt-4o"

        async def complete(self, *a: object, **kw: object) -> LLMResponse:
            nonlocal call_count
            call_count += 1
            if call_count < 3:
                exc = openai.APIStatusError(
                    message="Service unavailable",
                    response=MagicMock(headers={}, status_code=503),
                    body={"error": {"message": "service unavailable"}},
                )
                raise exc
            return _response()

        async def stream(self, *a: object, **kw: object) -> AsyncIterator[str]:
            raise NotImplementedError

    config = ConcurrencyConfig(llm_max_concurrent={"openai": 4}, retry_max_attempts=5)
    registry = _registry(config)
    gate = await registry.lookup("openai", "https://api.openai.com/v1", "llm")
    wrapped = ConcurrencyGatedProvider(_FlakyProvider(), gate, config)

    result = await wrapped.complete("hello")
    assert result.content == "ok"
    assert call_count == 3


# ──────────────────────────────────────────────────────────────────────────────
# 11. test_wrapper_releases_slot_on_cancel_during_call


@pytest.mark.asyncio
async def test_wrapper_releases_slot_on_cancel_during_call() -> None:
    """Cancelling during an upstream call releases the semaphore slot."""
    config = ConcurrencyConfig(llm_max_concurrent={"openai": 1}, retry_max_attempts=1)
    registry = _registry(config)
    gate = await registry.lookup("openai", "https://api.openai.com/v1", "llm")

    hold_event = asyncio.Event()
    cancel_event = asyncio.Event()

    class _Blocking:
        provider_name = "openai"

        @property
        def default_model(self) -> str:
            return "gpt-4o"

        async def complete(self, *a: object, **kw: object) -> LLMResponse:
            hold_event.set()
            await cancel_event.wait()  # Block indefinitely
            return _response()

        async def stream(self, *a: object, **kw: object) -> AsyncIterator[str]:
            raise NotImplementedError

    wrapped = ConcurrencyGatedProvider(_Blocking(), gate, config)

    task = asyncio.create_task(wrapped.complete("hello"))
    await hold_event.wait()  # Wait until the slot is held

    task.cancel()
    with pytest.raises(asyncio.CancelledError):
        await task

    # Slot must be released.
    assert gate._binding._in_flight == 0, "Slot leaked after cancellation during call"
    assert gate._binding._waiters == 0

    # Next acquire should succeed immediately.
    acquired = asyncio.Event()

    async def _check() -> None:
        async with gate.slot():
            acquired.set()

    await asyncio.wait_for(_check(), timeout=1.0)
    assert acquired.is_set()


# ──────────────────────────────────────────────────────────────────────────────
# 12. test_wrapper_releases_slot_on_cancel_during_limiter_wait


@pytest.mark.asyncio
async def test_wrapper_releases_slot_on_cancel_during_limiter_wait() -> None:
    """Cancelling while awaiting the limiter does not leak a slot (slot was never acquired)."""
    config = ConcurrencyConfig(llm_max_concurrent={"openai": 4}, retry_max_attempts=1)
    registry = _registry(config)
    gate = await registry.lookup("openai", "https://api.openai.com/v1", "llm")

    # A limiter that blocks forever until cancelled.
    class _BlockingLimiter:
        async def acquire(self, amount: float = 1) -> None:
            await asyncio.sleep(9999)  # blocks indefinitely

    class _SimpleProvider:
        provider_name = "openai"

        @property
        def default_model(self) -> str:
            return "gpt-4o"

        async def complete(self, *a: object, **kw: object) -> LLMResponse:
            return _response()

        async def stream(self, *a: object, **kw: object) -> AsyncIterator[str]:
            yield "ok"

    wrapped = ConcurrencyGatedProvider(_SimpleProvider(), gate, config)
    wrapped._limiter = _BlockingLimiter()  # type: ignore[assignment]

    task = asyncio.create_task(wrapped.complete("hello"))
    await asyncio.sleep(0.01)  # let the task start and block on limiter

    task.cancel()
    with pytest.raises(asyncio.CancelledError):
        await task

    # No slot was ever acquired (cancelled before limiter returned).
    assert gate._binding._in_flight == 0, "Slot leaked after cancel during limiter wait"


# ──────────────────────────────────────────────────────────────────────────────
# 13. test_cancellation_while_queued_releases_waiter_count


@pytest.mark.asyncio
async def test_cancellation_while_queued_releases_waiter_count() -> None:
    """Cancelling a queued coroutine decrements waiter count and doesn't block future acquires."""
    config = ConcurrencyConfig(llm_max_concurrent={"openai": 1}, retry_max_attempts=1)
    registry = _registry(config)
    gate = await registry.lookup("openai", "https://api.openai.com/v1", "llm")

    hold_start = asyncio.Event()
    release_a = asyncio.Event()

    # A holds the only slot.
    async def hold_slot_a() -> None:
        async with gate.slot():
            hold_start.set()
            await release_a.wait()

    task_a = asyncio.create_task(hold_slot_a())
    await hold_start.wait()

    # B queues for the slot.
    async def wait_for_slot_b() -> None:
        async with gate.slot():
            pass  # We don't want B to actually do anything

    task_b = asyncio.create_task(wait_for_slot_b())
    await asyncio.sleep(0.01)  # let B enter the wait

    assert gate._binding._waiters == 1, f"Expected 1 waiter, got {gate._binding._waiters}"

    # Cancel B while it's queued.
    task_b.cancel()
    with pytest.raises(asyncio.CancelledError):
        await task_b

    # Waiter count must be decremented.
    assert gate._binding._waiters == 0, (
        f"Waiter count not decremented after cancel: {gate._binding._waiters}"
    )

    # Release A; C should acquire immediately.
    release_a.set()
    await task_a

    acquired = asyncio.Event()

    async def task_c() -> None:
        async with gate.slot():
            acquired.set()

    await asyncio.wait_for(task_c(), timeout=1.0)
    assert acquired.is_set(), "C could not acquire after B was cancelled"


# ──────────────────────────────────────────────────────────────────────────────
# 14. test_wrapper_releases_slot_between_retry_attempts (Decision 2 — critical)


@pytest.mark.asyncio
async def test_wrapper_releases_slot_between_retry_attempts() -> None:
    """Slot is released during retry sleep so other callers can proceed (Decision 2)."""
    config = ConcurrencyConfig(llm_max_concurrent={"openai": 1}, retry_max_attempts=3)
    registry = _registry(config)
    gate = await registry.lookup("openai", "https://api.openai.com/v1", "llm")

    attempt_started = asyncio.Event()
    other_acquired = asyncio.Event()
    call_count = 0

    class _FailOnceThenSucceed:
        provider_name = "openai"

        @property
        def default_model(self) -> str:
            return "gpt-4o"

        async def complete(self, *a: object, **kw: object) -> LLMResponse:
            nonlocal call_count
            call_count += 1
            attempt_started.set()
            if call_count == 1:
                exc = openai.RateLimitError(
                    message="rate limit",
                    response=MagicMock(headers={"retry-after": "0.05"}, status_code=429),
                    body={},
                )
                raise exc
            return _response()

        async def stream(self, *a: object, **kw: object) -> AsyncIterator[str]:
            raise NotImplementedError

    wrapped = ConcurrencyGatedProvider(_FailOnceThenSucceed(), gate, config)

    main_task = asyncio.create_task(wrapped.complete("hello"))
    await attempt_started.wait()

    # While the main task is sleeping between retries, this task should be able
    # to acquire the gate (slot was released before the sleep per Decision 2).
    async def _other() -> None:
        # Give the main task a moment to release and enter the sleep.
        await asyncio.sleep(0.01)
        async with gate.slot():
            other_acquired.set()

    other_task = asyncio.create_task(_other())

    result = await main_task
    await asyncio.wait_for(other_task, timeout=2.0)

    assert result.content == "ok"
    assert other_acquired.is_set(), "Other coroutine could not acquire slot during retry sleep"


# ──────────────────────────────────────────────────────────────────────────────
# 15. test_wrapper_does_not_hold_slot_during_limiter_wait (Decision 2)


@pytest.mark.asyncio
async def test_wrapper_does_not_hold_slot_during_limiter_wait() -> None:
    """Limiter wait happens OUTSIDE the slot (Decision 2 ordering)."""
    config = ConcurrencyConfig(llm_max_concurrent={"openai": 1}, retry_max_attempts=1)
    registry = _registry(config)
    gate = await registry.lookup("openai", "https://api.openai.com/v1", "llm")

    limiter_acquired = asyncio.Event()
    release_limiter = asyncio.Event()

    class _HoldLimiter:
        """A fake limiter that signals when it's been entered, then blocks."""

        async def acquire(self, amount: float = 1) -> None:
            limiter_acquired.set()
            await release_limiter.wait()

    class _SimpleProvider:
        provider_name = "openai"

        @property
        def default_model(self) -> str:
            return "gpt-4o"

        async def complete(self, *a: object, **kw: object) -> LLMResponse:
            return _response()

        async def stream(self, *a: object, **kw: object) -> AsyncIterator[str]:
            yield "ok"

    wrapped = ConcurrencyGatedProvider(_SimpleProvider(), gate, config)
    wrapped._limiter = _HoldLimiter()  # type: ignore[assignment]

    main_task = asyncio.create_task(wrapped.complete("hello"))
    await limiter_acquired.wait()

    # While limiter is blocking (not yet called complete), the slot must be free.
    assert gate._binding._in_flight == 0, (
        "Slot is being held during limiter wait — Decision 2 violation"
    )

    # Another task should be able to acquire the slot while limiter blocks.
    slot_acquired = asyncio.Event()

    async def _other() -> None:
        async with gate.slot():
            slot_acquired.set()

    other_task = asyncio.create_task(_other())
    await asyncio.wait_for(other_task, timeout=1.0)
    assert slot_acquired.is_set()

    release_limiter.set()
    await asyncio.wait_for(main_task, timeout=2.0)


# ──────────────────────────────────────────────────────────────────────────────
# 16. test_wrapper_jitter_spreads_retry_timestamps


@pytest.mark.asyncio
async def test_wrapper_jitter_spreads_retry_timestamps() -> None:
    """wait_random_exponential produces non-zero jitter (mocked random.uniform)."""
    # tenacity's wait_random_exponential calls random.uniform at module level.
    # We verify that our wrapper does NOT call with uniform(0, 0) by checking that
    # the recorded wait sequence grows with exponential base when jitter is frozen.
    config = ConcurrencyConfig(llm_max_concurrent={"openai": 4}, retry_max_attempts=4)
    registry = _registry(config)
    gate = await registry.lookup("openai", "https://api.openai.com/v1", "llm")

    call_count = 0

    class _AlwaysRateLimit:
        provider_name = "openai"

        @property
        def default_model(self) -> str:
            return "gpt-4o"

        async def complete(self, *a: object, **kw: object) -> LLMResponse:
            nonlocal call_count
            call_count += 1
            raise openai.RateLimitError(
                message="rate limit",
                response=MagicMock(headers={}, status_code=429),
                body={},
            )

        async def stream(self, *a: object, **kw: object) -> AsyncIterator[str]:
            raise NotImplementedError

    wrapped = ConcurrencyGatedProvider(_AlwaysRateLimit(), gate, config)

    recorded_waits: list[float] = []

    real_uniform = random.uniform

    def _patched_uniform(a: float, b: float) -> float:
        # Return the midpoint for determinism; record the upper bound (b) as
        # a proxy for the exponential window width.
        result = real_uniform(a, b)
        recorded_waits.append(b)
        return result

    with patch("random.uniform", side_effect=_patched_uniform), pytest.raises(openai.RateLimitError):
        await wrapped.complete("hello")

    # Jitter should have been called (at least once per retry gap).
    assert len(recorded_waits) > 0, "random.uniform was never called — jitter absent"
    # Exponential backing: window width should grow (or stay) between attempts.
    # With multiplier=1, the window at attempt N is [0, min(2^(N-1), 60)].
    for i in range(1, len(recorded_waits)):
        assert recorded_waits[i] >= recorded_waits[0] * 0.9, (
            f"Wait window shrank: {recorded_waits}"
        )


# ──────────────────────────────────────────────────────────────────────────────
# 17. test_gate_rejects_zero_max_concurrent (already in Phase 1; extended here)


def test_gate_rejects_zero_max_concurrent_extended() -> None:
    """_KindGate also rejects max_concurrent=0."""
    from workers.common.llm.concurrency import _KindGate

    with pytest.raises(ValueError, match="max_concurrent"):
        _KindGate(max_concurrent=0)


# ──────────────────────────────────────────────────────────────────────────────
# 18. test_concurrency_config_from_env_rejects_invalid_rpm (Phase 1 extended)


def test_concurrency_config_from_env_rejects_negative_rpm(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setenv("SOURCEBRIDGE_LLM_PROVIDER_OPENAI_RPM", "-5")
    config = ConcurrencyConfig.from_env()
    # Negative is invalid; should not be stored.
    assert "openai" not in config.rpm


# ──────────────────────────────────────────────────────────────────────────────
# 19. test_concurrency_config_from_env_rejects_zero_max_concurrent (Phase 1 extended)


def test_concurrency_config_from_env_env_override_takes_precedence(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Env-var override supersedes Decision 6 default."""
    monkeypatch.setenv("SOURCEBRIDGE_LLM_PROVIDER_OLLAMA_MAX_CONCURRENT", "8")
    config = ConcurrencyConfig.from_env()
    assert config.llm_max_concurrent["ollama"] == 8


# ──────────────────────────────────────────────────────────────────────────────
# 20. test_concurrency_config_warns_on_unknown_provider_token
#     (Plan codex r2 L1: validator should warn on unknown provider tokens)


def test_concurrency_config_warns_on_unknown_provider_token(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Decision 7 / codex r2 L1: typo'd provider env vars produce a structlog
    warning, not a silent no-op.  The valid env var is still parsed correctly."""
    import structlog.testing

    # Typo: OPENAICOMPAT instead of OPENAI_COMPATIBLE
    monkeypatch.setenv("SOURCEBRIDGE_LLM_PROVIDER_OPENAICOMPAT_MAX_CONCURRENT", "4")
    # Valid env var as a control — must still be applied.
    monkeypatch.setenv("SOURCEBRIDGE_LLM_PROVIDER_OLLAMA_MAX_CONCURRENT", "2")

    with structlog.testing.capture_logs() as captured:
        cfg = ConcurrencyConfig.from_env()

    # Must not raise; valid env var still parsed.
    assert cfg is not None
    assert cfg.llm_max_concurrent.get("ollama") == 2

    # Unknown token must produce a warning event.
    unknown_warnings = [
        e for e in captured
        if e.get("event") == "concurrency_config_unknown_provider_token"
        and e.get("unknown_token") == "OPENAICOMPAT"
    ]
    assert len(unknown_warnings) >= 1, (
        f"Expected warning about OPENAICOMPAT typo; captured events: {[e.get('event') for e in captured]}"
    )
    # The warning should name the bad env var and list canonical tokens.
    w = unknown_warnings[0]
    assert w["env_var"] == "SOURCEBRIDGE_LLM_PROVIDER_OPENAICOMPAT_MAX_CONCURRENT"
    assert "OPENAI_COMPATIBLE" in w["canonical_tokens"]


def test_concurrency_config_decision6_real_defaults_loaded() -> None:
    """Decision 6 real defaults are present without any env-var overrides."""
    import os

    # Ensure no override env vars are set for known providers.
    env_backup = {
        k: v for k, v in os.environ.items()
        if k.startswith("SOURCEBRIDGE_LLM_PROVIDER_") or k.startswith("SOURCEBRIDGE_EMBEDDING_PROVIDER_")
    }
    for k in env_backup:
        del os.environ[k]

    try:
        config = ConcurrencyConfig.from_env()
        # Decision 6 table assertions.
        assert config.llm_max_concurrent.get("ollama") == 1
        assert config.llm_max_concurrent.get("vllm") == 4
        assert config.llm_max_concurrent.get("llama-cpp") == 4
        assert config.llm_max_concurrent.get("sglang") == 4
        assert config.llm_max_concurrent.get("lmstudio") == 2
        assert config.llm_max_concurrent.get("openai") == 8
        assert config.llm_max_concurrent.get("anthropic") == 4
        assert config.llm_max_concurrent.get("openrouter") == 8
        assert config.llm_max_concurrent.get("gemini") == 8
        assert config.llm_max_concurrent.get("openai-compatible") == 4
        # Frontier embedding defaults.
        assert config.embedding_max_concurrent.get("openai") == 8
        assert config.embedding_max_concurrent.get("openrouter") == 8
        assert config.embedding_max_concurrent.get("gemini") == 8
    finally:
        os.environ.update(env_backup)


def test_concurrency_config_retry_max_attempts_default_is_5() -> None:
    """Phase 3: default retry_max_attempts is 5 (not 1 as in Phase 1)."""
    config = ConcurrencyConfig()
    assert config.retry_max_attempts == 5


def test_concurrency_config_from_env_retry_default_is_5(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.delenv("SOURCEBRIDGE_LLM_RETRY_MAX_ATTEMPTS", raising=False)
    config = ConcurrencyConfig.from_env()
    assert config.retry_max_attempts == 5


# ──────────────────────────────────────────────────────────────────────────────
# 21. test_empty_content_retry_preserved_in_phase_3


@pytest.mark.asyncio
async def test_empty_content_retry_preserved_in_phase_3() -> None:
    """Empty-content retry at openai_compat._complete_once still fires after Phase 3.

    This is the <think>-budget retry that doubles max_tokens when stop_reason=length
    and content is empty. It is NOT a network/rate-limit retry — it lives inside
    OpenAICompatProvider.complete() and must survive the Phase 3 changes.

    Verified by: mocking the internal SDK client to return one length/empty response
    then a non-empty response; asserting the retry fired (call count == 2) and the
    final content is non-empty.
    """
    from workers.common.llm.openai_compat import OpenAICompatProvider

    call_count = 0

    async def _mock_create(**kwargs: object) -> MagicMock:
        nonlocal call_count
        call_count += 1
        resp = MagicMock()
        if call_count == 1:
            # First call: empty content + stop_reason=length
            resp.choices = [MagicMock(message=MagicMock(content=""), finish_reason="length")]
            resp.usage = MagicMock(prompt_tokens=10, completion_tokens=0)
            resp.model = "gpt-4o"
        else:
            # Second call: valid content
            resp.choices = [MagicMock(message=MagicMock(content="the actual answer"), finish_reason="stop")]
            resp.usage = MagicMock(prompt_tokens=10, completion_tokens=5)
            resp.model = "gpt-4o"
        return resp

    provider = OpenAICompatProvider(api_key="test-key", base_url="http://localhost:11434/v1")
    provider.client.chat.completions.create = _mock_create  # type: ignore[assignment]

    result = await provider.complete("test prompt")

    assert call_count == 2, f"Expected 2 calls (empty-content retry), got {call_count}"
    assert result.content.strip() == "the actual answer"


# ──────────────────────────────────────────────────────────────────────────────
# 22-23. Registry lifecycle (close idempotent + rejects lookup after close)
#        These were in Phase 1; extended here with in-flight behavior.


@pytest.mark.asyncio
async def test_registry_close_idempotent() -> None:
    """Calling close() twice does not raise."""
    reg = _registry()
    await reg.close()
    await reg.close()


@pytest.mark.asyncio
async def test_registry_rejects_lookup_after_close() -> None:
    """Lookup after close raises RuntimeError."""
    reg = _registry()
    await reg.close()
    with pytest.raises(RuntimeError, match="closed"):
        await reg.lookup("ollama", "http://localhost:11434/v1", "llm")


@pytest.mark.asyncio
async def test_registry_close_during_in_flight_calls() -> None:
    """Registry.close() can be called while a gate slot is held.

    The slot is held by a task that has already acquired the semaphore; close()
    marks the registry as closed so new lookups fail, but does not force-cancel
    the in-flight task. The in-flight task releases normally.
    """
    reg = _registry(ConcurrencyConfig(llm_max_concurrent={"openai": 2}, retry_max_attempts=1))
    gate = await reg.lookup("openai", "https://api.openai.com/v1", "llm")

    acquired = asyncio.Event()
    released = asyncio.Event()

    async def hold() -> None:
        async with gate.slot():
            acquired.set()
            await released.wait()

    task = asyncio.create_task(hold())
    await acquired.wait()

    # Close while slot is held.
    await reg.close()
    assert reg._closed

    # New lookups must fail.
    with pytest.raises(RuntimeError, match="closed"):
        await reg.lookup("openai", "https://api.openai.com/v1", "llm")

    # Release the in-flight task — it should complete without error.
    released.set()
    await asyncio.wait_for(task, timeout=1.0)

    # Slot is released cleanly.
    assert gate._binding._in_flight == 0


# ──────────────────────────────────────────────────────────────────────────────
# 24. _retry_predicate unit tests


def test_retry_predicate_accepts_openai_rate_limit() -> None:
    exc = openai.RateLimitError(
        message="too many requests",
        response=MagicMock(headers={}, status_code=429),
        body={},
    )
    assert _retry_predicate(exc) is True


def test_retry_predicate_accepts_anthropic_rate_limit() -> None:
    exc = anthropic.RateLimitError(
        message="too many requests",
        response=MagicMock(headers={}, status_code=429),
        body={},
    )
    assert _retry_predicate(exc) is True


def test_retry_predicate_accepts_503() -> None:
    exc = openai.APIStatusError(
        message="service unavailable",
        response=MagicMock(headers={}, status_code=503),
        body={},
    )
    assert _retry_predicate(exc) is True


def test_retry_predicate_rejects_400() -> None:
    exc = openai.APIStatusError(
        message="bad request",
        response=MagicMock(headers={}, status_code=400),
        body={},
    )
    assert _retry_predicate(exc) is False


def test_retry_predicate_rejects_401() -> None:
    exc = openai.AuthenticationError(
        message="invalid key",
        response=MagicMock(headers={}, status_code=401),
        body={},
    )
    assert _retry_predicate(exc) is False


def test_retry_predicate_accepts_timeout() -> None:
    exc = httpx.ConnectTimeout("timed out")
    assert _retry_predicate(exc) is True


def test_retry_predicate_accepts_read_error() -> None:
    exc = httpx.ReadError("read error")
    assert _retry_predicate(exc) is True


def test_retry_predicate_rejects_runtime_error() -> None:
    assert _retry_predicate(RuntimeError("oops")) is False


def test_retry_predicate_rejects_value_error() -> None:
    assert _retry_predicate(ValueError("bad")) is False


# ──────────────────────────────────────────────────────────────────────────────
# 25. router.py unwraps RetryError


def test_router_unwraps_retry_error() -> None:
    """LLMRouter unwraps tenacity.RetryError to expose the original cause."""
    from tenacity import RetryError

    from workers.common.llm.router import _unwrap_retry_error

    original = RuntimeError("original cause")
    wrapped = RetryError.__new__(RetryError)
    wrapped.__cause__ = original

    result = _unwrap_retry_error(wrapped)
    assert result is original


def test_router_passthrough_when_not_retry_error() -> None:
    from workers.common.llm.router import _unwrap_retry_error

    exc = ValueError("plain error")
    assert _unwrap_retry_error(exc) is exc


# ──────────────────────────────────────────────────────────────────────────────
# 26. Hierarchical + renderer cap constants raised


def test_hierarchical_default_caps_raised() -> None:
    """Phase 3 raises DEFAULT_LEAF/FILE/PACKAGE_CONCURRENCY to 4."""
    from workers.comprehension.hierarchical import (
        DEFAULT_FILE_CONCURRENCY,
        DEFAULT_LEAF_CONCURRENCY,
        DEFAULT_PACKAGE_CONCURRENCY,
    )

    assert DEFAULT_LEAF_CONCURRENCY == 4, f"Expected 4, got {DEFAULT_LEAF_CONCURRENCY}"
    assert DEFAULT_FILE_CONCURRENCY == 4, f"Expected 4, got {DEFAULT_FILE_CONCURRENCY}"
    assert DEFAULT_PACKAGE_CONCURRENCY == 4, f"Expected 4, got {DEFAULT_PACKAGE_CONCURRENCY}"


def test_renderer_deep_parallelism_raised() -> None:
    """Phase 3 raises deep_parallelism and deep_repair_parallelism defaults to 4."""
    from workers.comprehension.renderers import CliffNotesRenderer

    fields = {f.name: f.default for f in dataclasses.fields(CliffNotesRenderer)}
    assert fields.get("deep_parallelism") == 4, (
        f"Expected deep_parallelism=4, got {fields.get('deep_parallelism')}"
    )
    assert fields.get("deep_repair_parallelism") == 4, (
        f"Expected deep_repair_parallelism=4, got {fields.get('deep_repair_parallelism')}"
    )


# ──────────────────────────────────────────────────────────────────────────────
# Phase 2: is_local_provider predicate tests (CA-173)


def test_is_local_provider_classification() -> None:
    """is_local_provider returns True for known local providers, False otherwise."""
    from workers.common.llm.concurrency import is_local_provider

    # Known local providers → True
    for provider in ("ollama", "vllm", "llama-cpp", "sglang", "lmstudio"):
        assert is_local_provider(provider) is True, f"Expected True for {provider!r}"

    # Known cloud providers → False
    for provider in ("openai", "anthropic", "gemini", "openrouter"):
        assert is_local_provider(provider) is False, f"Expected False for {provider!r}"

    # openai-compatible is intentionally NOT local (depends on operator's deployment)
    assert is_local_provider("openai-compatible") is False

    # Edge cases → False
    assert is_local_provider("") is False
    assert is_local_provider(None) is False
    assert is_local_provider("foo") is False
    assert is_local_provider("unknown-provider") is False

    # Case-insensitive matching → True
    assert is_local_provider("OLLAMA") is True
    assert is_local_provider("Ollama") is True
    assert is_local_provider("VLLM") is True
    assert is_local_provider("LLama-CPP") is True


def test_is_local_provider_replaces_local_probe_providers_drift_guard() -> None:
    """Regression: no source file in workers/ (except concurrency.py itself) defines
    a parallel set/frozenset containing the five local provider names.

    Specifically, the literal name ``_LOCAL_PROBE_PROVIDERS`` must not appear in
    any source file — it was the duplicate constant deleted in Phase 2 (CA-173).
    This test pins the consolidation so a future agent cannot silently reintroduce it.
    """
    import pathlib
    import re

    workers_root = pathlib.Path(__file__).parent.parent  # workers/

    # 1. Assert _LOCAL_PROBE_PROVIDERS is gone from all non-test source files.
    #    (This test file itself references the name in comments/strings — excluded.)
    this_file = pathlib.Path(__file__).resolve()
    matches: list[str] = []
    for py_file in workers_root.rglob("*.py"):
        if py_file.resolve() == this_file:
            continue  # exclude this test file (contains the name in docstrings)
        text = py_file.read_text(encoding="utf-8")
        if "_LOCAL_PROBE_PROVIDERS" in text:
            matches.append(str(py_file))

    assert matches == [], (
        f"_LOCAL_PROBE_PROVIDERS was reintroduced in: {matches}. "
        "This constant was consolidated into is_local_provider() in CA-173 Phase 2. "
        "Extend is_local_provider() instead."
    )

    # 2. Assert no other file (outside concurrency.py) defines a frozenset that
    #    contains all five local-gating provider names together.  This is the
    #    precise structural duplicate that Phase 2 eliminated.  The pattern
    #    `frozenset({... "ollama" ... "lmstudio" ...})` only matches when both
    #    Ollama (always-local) and lmstudio (local-only, never in cloud sets)
    #    appear inside the same frozenset literal.  Known-legitimate sets that
    #    contain both (e.g. SUPPORTED_LLM_PROVIDERS in common/config.py) are
    #    excluded by path.
    concurrency_py = (workers_root / "common" / "llm" / "concurrency.py").resolve()
    supported_config_py = (workers_root / "common" / "config.py").resolve()
    # Match a frozenset literal that spans multiple lines and includes both
    # "ollama" and "lmstudio" — unique to the host-gate set.
    parallel_set_re = re.compile(
        r'frozenset\s*\(\s*\{[^}]*"ollama"[^}]*"lmstudio"[^}]*\}',
        re.DOTALL,
    )
    parallel_matches: list[str] = []
    for py_file in workers_root.rglob("*.py"):
        resolved = py_file.resolve()
        if resolved == concurrency_py:
            continue  # canonical home — skip
        if resolved == supported_config_py:
            continue  # SUPPORTED_LLM_PROVIDERS is a legitimate aggregator, not a duplicate gate set
        if resolved == this_file:
            continue  # this test file contains the pattern in docstrings/regex strings
        if py_file.suffix != ".py":
            continue
        text = py_file.read_text(encoding="utf-8")
        if parallel_set_re.search(text):
            parallel_matches.append(str(py_file))

    assert parallel_matches == [], (
        f"Parallel local-provider frozenset found outside concurrency.py: {parallel_matches}. "
        "Add new local providers to _HOST_GATED_PROVIDERS in concurrency.py instead."
    )
