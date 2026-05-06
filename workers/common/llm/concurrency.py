"""Per-provider LLM concurrency gate: host-level semaphore + per-kind counters.

Architecture (Decision 1, 2, 5b, v4 plan):

  One ``ProviderGateRegistry`` is constructed once in ``__main__.py`` and
  threaded by reference into every factory call.  It maintains two internal
  maps:

  * ``_host_gates``  — keyed ``(provider, normalized_origin)`` — one binding
    semaphore shared across LLM + embedding for local servers (Ollama, vLLM,
    llama.cpp, sglang, LM Studio).
  * ``_kind_gates``  — keyed ``(provider, base_url_raw, kind)`` — one binding
    semaphore per API-kind for frontier providers (openai, anthropic, gemini,
    openrouter) which have independent quotas for chat vs. embeddings.
  * ``_kind_counters`` — per-``(provider, normalized_origin, kind)`` counter-
    only sub-records under host-gated providers; observability only, no
    gating.

  ``lookup(provider, base_url, kind) -> ProviderGate`` returns a façade that
  acquires whichever binding gate is appropriate and updates the matching
  counter.

Phase 1 ships **sentinel-uncapped defaults** (``_UNCAPPED = sys.maxsize``) so
that wiring the wrapper in Phase 2 is behavior-equivalent to today.

The tenacity retry predicate is a no-op in Phase 1 (``lambda exc: False``),
activated to its real whitelist in Phase 3.

The aggregator task (emitting ``llm_provider_gate_metrics`` log lines) is
deferred to Phase 6; the start hook is a placeholder comment here.
# TODO(phase-6): start aggregator task in ProviderGateRegistry.__init__

See plan: thoughts/shared/plans/active-2026-05-06-deliver-worker-llm-concurrency.md
"""

from __future__ import annotations

import asyncio
import contextlib
import os
import sys
import time
from collections import deque
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from dataclasses import dataclass, field
from typing import Any

import structlog
from tenacity import (
    AsyncRetrying,
    retry_if_exception,
    stop_after_attempt,
    wait_random_exponential,
)

from workers.common.llm.provider import LLMProvider, LLMResponse

log = structlog.get_logger()

# ──────────────────────────────────────────────────────────────────────────────
# Sentinel: uncapped defaults for Phase 1 / kill-switch-off behavior.
# Phase 3 replaces these with the real caps from Decision 6.
_UNCAPPED: int = sys.maxsize

# Providers that share one host-level semaphore across LLM + embedding kinds.
_HOST_GATED_PROVIDERS: frozenset[str] = frozenset(
    {"ollama", "vllm", "llama-cpp", "sglang", "lmstudio"}
)

# Providers that use independent per-kind semaphores (frontier cloud APIs).
_KIND_GATED_PROVIDERS: frozenset[str] = frozenset(
    {"openai", "anthropic", "gemini", "openrouter"}
)

# openai-compatible default gating mode (can be flipped per Decision 7).
_GATING_ENV_VAR = "SOURCEBRIDGE_LLM_PROVIDER_OPENAI_COMPATIBLE_GATING"
_DEFAULT_OPENAI_COMPAT_GATING = "host"


# ──────────────────────────────────────────────────────────────────────────────
# URL normalization helper (Decision 1, v4)


def _normalize_host_key(provider: str, base_url: str | None) -> tuple[str, str]:
    """Canonical form: ``(provider, "scheme://host:port")``.

    Strips path (e.g. ``/v1``), trailing slash, query, and fragment.  This
    ensures that Ollama's LLM endpoint ``http://localhost:11434/v1`` and its
    embedding endpoint ``http://localhost:11434`` both map to
    ``("ollama", "http://localhost:11434")`` and therefore share the same
    host-level semaphore.
    """
    if not base_url:
        return (provider, "")
    from urllib.parse import urlsplit

    u = urlsplit(base_url)
    origin = f"{u.scheme}://{u.netloc}".rstrip("/")
    return (provider, origin)


# ──────────────────────────────────────────────────────────────────────────────
# Configuration


@dataclass
class ConcurrencyConfig:
    """Runtime concurrency knobs sourced from environment variables.

    All fields have sentinel-uncapped Phase 1 defaults.  Phase 3 introduces
    the real Decision 6 caps by changing the defaults here and in
    ``__main__.py``'s ``ConcurrencyConfig.from_env()`` call.
    """

    # Per-provider max-concurrent overrides.  Key = canonical provider name.
    # Phase 1 default: empty (sentinel uncapped applies).
    llm_max_concurrent: dict[str, int] = field(default_factory=dict)
    embedding_max_concurrent: dict[str, int] = field(default_factory=dict)

    # Per-provider RPM limits.  None = no rate shaping (default).
    rpm: dict[str, int | None] = field(default_factory=dict)

    # openai-compatible gating mode: "host" (default) | "per_kind".
    openai_compatible_gating: str = _DEFAULT_OPENAI_COMPAT_GATING

    # Global kill switch.  When False, factories return raw providers.
    wrapper_enabled: bool = True

    # Tenacity: max attempts per call.  1 = no retry (Phase 1 default).
    # Phase 3 raises this to 5.
    retry_max_attempts: int = 1

    # Aggregator task interval (seconds).  Phase 6 activates the task.
    metrics_interval_seconds: float = 30.0

    @classmethod
    def from_env(cls) -> ConcurrencyConfig:
        """Read all concurrency knobs from environment variables.

        Decision 7 env-var names (``SOURCEBRIDGE_LLM_*`` prefix):

          ``SOURCEBRIDGE_LLM_PROVIDER_<NAME>_MAX_CONCURRENT``
          ``SOURCEBRIDGE_EMBEDDING_PROVIDER_<NAME>_MAX_CONCURRENT``
          ``SOURCEBRIDGE_LLM_PROVIDER_<NAME>_RPM``
          ``SOURCEBRIDGE_LLM_PROVIDER_OPENAI_COMPATIBLE_GATING``
          ``SOURCEBRIDGE_LLM_CONCURRENCY_WRAPPER_ENABLED``
          ``SOURCEBRIDGE_LLM_RETRY_MAX_ATTEMPTS``
          ``SOURCEBRIDGE_LLM_METRICS_AGGREGATION_INTERVAL_SECONDS``

        The canonical provider-name → env-var-token mapping (L1 fix):
          openai → OPENAI, anthropic → ANTHROPIC, ollama → OLLAMA,
          vllm → VLLM, llama-cpp → LLAMA_CPP, sglang → SGLANG,
          gemini → GEMINI, openrouter → OPENROUTER, lmstudio → LMSTUDIO,
          openai-compatible → OPENAI_COMPATIBLE.
        """
        all_providers = list(_HOST_GATED_PROVIDERS | _KIND_GATED_PROVIDERS) + [
            "openai-compatible"
        ]

        llm_max: dict[str, int] = {}
        embed_max: dict[str, int] = {}
        rpm_map: dict[str, int | None] = {}

        for provider in all_providers:
            token = provider.upper().replace("-", "_")
            _read_max_concurrent(
                f"SOURCEBRIDGE_LLM_PROVIDER_{token}_MAX_CONCURRENT",
                provider,
                llm_max,
            )
            _read_max_concurrent(
                f"SOURCEBRIDGE_EMBEDDING_PROVIDER_{token}_MAX_CONCURRENT",
                provider,
                embed_max,
            )
            _read_rpm(
                f"SOURCEBRIDGE_LLM_PROVIDER_{token}_RPM",
                provider,
                rpm_map,
            )

        gating = os.environ.get(_GATING_ENV_VAR, _DEFAULT_OPENAI_COMPAT_GATING).strip().lower()
        if gating not in ("host", "per_kind"):
            log.warning(
                "concurrency_config_invalid_gating",
                env_var=_GATING_ENV_VAR,
                value=gating,
                using=_DEFAULT_OPENAI_COMPAT_GATING,
            )
            gating = _DEFAULT_OPENAI_COMPAT_GATING

        wrapper_raw = os.environ.get("SOURCEBRIDGE_LLM_CONCURRENCY_WRAPPER_ENABLED", "true").strip().lower()
        wrapper_enabled = wrapper_raw in ("true", "1", "yes", "on")

        retry_raw = os.environ.get("SOURCEBRIDGE_LLM_RETRY_MAX_ATTEMPTS", "").strip()
        retry_max = 1  # Phase 1: no-op; Phase 3 will default to 5
        if retry_raw:
            try:
                retry_max = int(retry_raw)
                if retry_max < 1:
                    raise ValueError("must be ≥ 1")
            except ValueError as exc:
                log.warning(
                    "concurrency_config_invalid_retry_max",
                    env_var="SOURCEBRIDGE_LLM_RETRY_MAX_ATTEMPTS",
                    value=retry_raw,
                    error=str(exc),
                )
                retry_max = 1

        interval_raw = os.environ.get("SOURCEBRIDGE_LLM_METRICS_AGGREGATION_INTERVAL_SECONDS", "").strip()
        interval = 30.0
        if interval_raw:
            with contextlib.suppress(ValueError):
                interval = float(interval_raw)

        return cls(
            llm_max_concurrent=llm_max,
            embedding_max_concurrent=embed_max,
            rpm=rpm_map,
            openai_compatible_gating=gating,
            wrapper_enabled=wrapper_enabled,
            retry_max_attempts=retry_max,
            metrics_interval_seconds=interval,
        )


def _read_max_concurrent(env_var: str, provider: str, target: dict[str, int]) -> None:
    raw = os.environ.get(env_var, "").strip()
    if not raw:
        return
    try:
        val = int(raw)
        if val < 1:
            raise ValueError("must be ≥ 1")
        target[provider] = val
    except ValueError as exc:
        log.warning(
            "concurrency_config_invalid_max_concurrent",
            env_var=env_var,
            value=raw,
            error=str(exc),
        )


def _read_rpm(env_var: str, provider: str, target: dict[str, int | None]) -> None:
    raw = os.environ.get(env_var, "").strip()
    if not raw:
        return
    try:
        val = int(raw)
        if val <= 0:
            raise ValueError("must be > 0")
        target[provider] = val
    except ValueError as exc:
        log.warning(
            "concurrency_config_invalid_rpm",
            env_var=env_var,
            value=raw,
            error=str(exc),
        )


# ──────────────────────────────────────────────────────────────────────────────
# Snapshot dataclass


@dataclass
class GateSnapshotEntry:
    """Point-in-time snapshot of one gate's state (for Phase 7 admin endpoint)."""

    provider: str
    base_url_normalized: str
    kind: str
    in_flight: int
    queue_depth: int
    max_concurrent: int
    retries_since_start: int
    recent_429_count: int
    tokens_per_second: float
    rpm: int = 0  # 0 = no limiter


# ──────────────────────────────────────────────────────────────────────────────
# Internal gate primitives


class _GateBase:
    """Shared state: semaphore, waiters, in-flight, ring buffer, retry counters."""

    __slots__ = (
        "_sem",
        "_max_concurrent",
        "_waiters",
        "_in_flight",
        "_retries",
        "_recent_429",
        "_ring",
        "_streaming_usage_unsupported",
    )

    def __init__(self, max_concurrent: int) -> None:
        if max_concurrent < 1:
            raise ValueError(f"max_concurrent must be ≥ 1, got {max_concurrent}")
        self._sem = asyncio.Semaphore(max_concurrent)
        self._max_concurrent = max_concurrent
        self._waiters: int = 0
        self._in_flight: int = 0
        self._retries: int = 0
        self._recent_429: int = 0
        # Ring buffer: deque of (timestamp_float, output_tokens_int).
        # Bounded by insertion-time eviction (keep last 60 s).
        self._ring: deque[tuple[float, int]] = deque()
        # Compatibility flag: set True when a server rejects stream_options.
        # See Decision 10b / M2 fallback.
        self._streaming_usage_unsupported: bool = False

    @asynccontextmanager
    async def slot(self) -> AsyncIterator[None]:
        """Acquire a slot (cancellation-safe).

        Decision 2 ordering:
          - Increment ``_waiters`` before awaiting acquire.
          - Decrement ``_waiters`` once acquired.
          - Increment ``_in_flight`` while the caller holds the slot.
          - Release and decrement in ``finally`` regardless of how the
            block exits (including cancellation).
        """
        self._waiters += 1
        try:
            await self._sem.acquire()
        except asyncio.CancelledError:
            self._waiters -= 1
            raise
        self._waiters -= 1
        self._in_flight += 1
        try:
            yield
        finally:
            self._in_flight -= 1
            self._sem.release()

    def record_completion(self, output_tokens: int) -> None:
        """Append to the 60-second ring buffer (Decision 10a)."""
        now = time.monotonic()
        self._ring.append((now, output_tokens))
        # Evict entries older than 60 s.
        cutoff = now - 60.0
        while self._ring and self._ring[0][0] < cutoff:
            self._ring.popleft()

    def snapshot_tokens_per_second(self) -> float:
        """Sum output_tokens over the last 60 s, divide by the window."""
        if not self._ring:
            return 0.0
        now = time.monotonic()
        cutoff = now - 60.0
        total = sum(tok for ts, tok in self._ring if ts >= cutoff)
        return total / 60.0

    def snapshot(self, provider: str, base_url_normalized: str, kind: str, rpm: int = 0) -> GateSnapshotEntry:
        return GateSnapshotEntry(
            provider=provider,
            base_url_normalized=base_url_normalized,
            kind=kind,
            in_flight=self._in_flight,
            queue_depth=self._waiters,
            max_concurrent=self._max_concurrent,
            retries_since_start=self._retries,
            recent_429_count=self._recent_429,
            tokens_per_second=self.snapshot_tokens_per_second(),
            rpm=rpm,
        )


class _HostGate(_GateBase):
    """Binding semaphore for host-gated (local) providers.

    A single semaphore is shared across all ``kind`` values (llm, embedding)
    routed through this daemon.  This is the fix for the Ollama
    ``OLLAMA_NUM_PARALLEL=1`` case where LLM + embedding calls both count
    against the same server-side slot budget.
    """


class _KindGate(_GateBase):
    """Binding semaphore for per-kind-gated (frontier) providers."""


class _KindCounter:
    """Observability-only counter under a host gate (no separate semaphore)."""

    __slots__ = ("_in_flight", "_waiters")

    def __init__(self) -> None:
        self._in_flight: int = 0
        self._waiters: int = 0


# ──────────────────────────────────────────────────────────────────────────────
# ProviderGate façade


class ProviderGate:
    """Façade returned by ``ProviderGateRegistry.lookup``.

    Acquires the binding gate (host or per-kind) and increments the per-kind
    counter when operating in host-gate mode.
    """

    __slots__ = ("_binding", "_counter", "_provider", "_normalized_origin", "_kind")

    def __init__(
        self,
        binding: _HostGate | _KindGate,
        counter: _KindCounter | None,
        provider: str,
        normalized_origin: str,
        kind: str,
    ) -> None:
        self._binding = binding
        self._counter = counter
        self._provider = provider
        self._normalized_origin = normalized_origin
        self._kind = kind

    @asynccontextmanager
    async def slot(self) -> AsyncIterator[None]:
        """Acquire the binding slot and update the per-kind counter."""
        if self._counter is not None:
            self._counter._waiters += 1
        try:
            async with self._binding.slot():
                if self._counter is not None:
                    self._counter._waiters -= 1
                    self._counter._in_flight += 1
                try:
                    yield
                finally:
                    if self._counter is not None:
                        self._counter._in_flight -= 1
        except asyncio.CancelledError:
            if self._counter is not None:
                self._counter._waiters -= 1
            raise

    def record_completion(self, output_tokens: int) -> None:
        self._binding.record_completion(output_tokens)

    def snapshot_tokens_per_second(self) -> float:
        return self._binding.snapshot_tokens_per_second()

    def snapshot(self) -> GateSnapshotEntry:
        return self._binding.snapshot(self._provider, self._normalized_origin, self._kind)

    @property
    def in_flight(self) -> int:
        return self._binding._in_flight

    @property
    def queue_depth(self) -> int:
        return self._binding._waiters

    @property
    def streaming_usage_unsupported(self) -> bool:
        return self._binding._streaming_usage_unsupported

    @streaming_usage_unsupported.setter
    def streaming_usage_unsupported(self, value: bool) -> None:
        self._binding._streaming_usage_unsupported = value


# ──────────────────────────────────────────────────────────────────────────────
# Registry


class ProviderGateRegistry:
    """Single registry for all LLM and embedding provider gates.

    Constructed once in ``__main__.py``; threaded by reference into all
    factory calls (never used as a module-level singleton).

    Thread/task safety: gate creation is protected by ``asyncio.Lock``; all
    other operations are non-blocking reads/increments.
    """

    def __init__(self, config: ConcurrencyConfig | None = None) -> None:
        self._config = config or ConcurrencyConfig()
        self._lock = asyncio.Lock()
        # (provider, normalized_origin) → _HostGate
        self._host_gates: dict[tuple[str, str], _HostGate] = {}
        # (provider, base_url_raw, kind) → _KindGate
        self._kind_gates: dict[tuple[str, str, str], _KindGate] = {}
        # (provider, normalized_origin, kind) → _KindCounter (observability only)
        self._kind_counters: dict[tuple[str, str, str], _KindCounter] = {}
        self._closed: bool = False
        # TODO(phase-6): start the aggregator task here.
        #   self._aggregator_task = asyncio.create_task(self._run_aggregator())

    def _classify(self, provider: str) -> str:
        """Return "host", "per_kind", or the resolved mode for openai-compatible."""
        if provider in _HOST_GATED_PROVIDERS:
            return "host"
        if provider in _KIND_GATED_PROVIDERS:
            return "per_kind"
        if provider == "openai-compatible":
            return self._config.openai_compatible_gating
        # Unknown provider: default to host gating (safe / conservative).
        return "host"

    def _max_concurrent_for(self, provider: str, kind: str) -> int:
        """Effective max_concurrent for this provider+kind (Phase 1: _UNCAPPED)."""
        if kind == "embedding":
            cap = self._config.embedding_max_concurrent.get(provider)
            if cap is not None:
                return cap
        cap = self._config.llm_max_concurrent.get(provider)
        return cap if cap is not None else _UNCAPPED

    async def lookup(self, provider: str, base_url: str | None, kind: str) -> ProviderGate:
        """Return the ``ProviderGate`` for ``(provider, base_url, kind)``.

        Safe to call concurrently; gate objects are created at most once per key.
        """
        if self._closed:
            raise RuntimeError("ProviderGateRegistry has been closed; cannot look up gates")

        mode = self._classify(provider)
        if mode == "host":
            return await self._lookup_host(provider, base_url, kind)
        else:
            return await self._lookup_kind(provider, base_url, kind)

    async def _lookup_host(self, provider: str, base_url: str | None, kind: str) -> ProviderGate:
        _, normalized_origin = _normalize_host_key(provider, base_url)
        host_key = (provider, normalized_origin)
        counter_key = (provider, normalized_origin, kind)

        async with self._lock:
            if host_key not in self._host_gates:
                cap = self._max_concurrent_for(provider, "llm")
                self._host_gates[host_key] = _HostGate(max_concurrent=cap)
            if counter_key not in self._kind_counters:
                self._kind_counters[counter_key] = _KindCounter()

        binding = self._host_gates[host_key]
        counter = self._kind_counters[counter_key]
        return ProviderGate(binding, counter, provider, normalized_origin, kind)

    async def _lookup_kind(self, provider: str, base_url: str | None, kind: str) -> ProviderGate:
        raw_url = base_url or ""
        kind_key = (provider, raw_url, kind)
        _, normalized_origin = _normalize_host_key(provider, base_url)

        async with self._lock:
            if kind_key not in self._kind_gates:
                cap = self._max_concurrent_for(provider, kind)
                self._kind_gates[kind_key] = _KindGate(max_concurrent=cap)

        binding = self._kind_gates[kind_key]
        return ProviderGate(binding, None, provider, normalized_origin, kind)

    def effective_llm_max_concurrent(
        self, provider: str, base_url: str | None
    ) -> int | None:
        """The effective LLM cap for this provider+base_url.

        Returns ``None`` when the wrapper is disabled (kill switch) or the gate
        is uncapped (sentinel).  Phase 2 uses this to populate
        ``GetProviderCapabilities.max_concurrent_calls``.
        """
        if not self._config.wrapper_enabled:
            return None
        cap = self._config.llm_max_concurrent.get(provider)
        if cap is None:
            return None  # No per-provider override; sentinel uncapped in Phase 1.
        return cap

    def canonical_key_for(self, provider: str, base_url: str | None, kind: str) -> tuple[str, ...]:
        """Return the internal lookup key (Decision 5b helper for capability contract)."""
        mode = self._classify(provider)
        if mode == "host":
            _, origin = _normalize_host_key(provider, base_url)
            return (provider, origin, kind)
        return (provider, base_url or "", kind)

    def snapshot(self) -> list[GateSnapshotEntry]:
        """Point-in-time snapshot of all active gates (for Phase 7)."""
        entries: list[GateSnapshotEntry] = []
        for (provider, origin), gate in self._host_gates.items():
            # Emit one entry per kind counter that has ever been registered.
            kinds_seen = [k for (p, o, k) in self._kind_counters if p == provider and o == origin]
            if not kinds_seen:
                entries.append(gate.snapshot(provider, origin, "llm"))
            else:
                for kind in kinds_seen:
                    entries.append(gate.snapshot(provider, origin, kind))
        for (provider, raw_url, kind), gate in self._kind_gates.items():
            _, origin = _normalize_host_key(provider, raw_url or None)
            entries.append(gate.snapshot(provider, origin, kind))
        return entries

    async def close(self) -> None:
        """Cancel aggregator tasks and mark the registry closed.

        Idempotent — safe to call more than once.
        """
        if self._closed:
            return
        self._closed = True
        # TODO(phase-6): cancel self._aggregator_task here.


# ──────────────────────────────────────────────────────────────────────────────
# Tenacity predicate (Phase 1: no-op; Phase 3 wires the real whitelist)


def _retry_predicate(exc: BaseException) -> bool:
    """Return True when the exception is retryable.

    Phase 1: always False (retry is disabled — wrapper makes exactly one
    attempt).  Phase 3 replaces this body with the Decision 4 whitelist
    (RateLimitError, retryable status codes, transient httpx errors).
    """
    return False


def _record_retry(retry_state: Any) -> None:
    """Called by tenacity before each sleep window (Decision 2)."""
    log.debug(
        "llm_gate_retry",
        attempt=retry_state.attempt_number,
        exc=str(retry_state.outcome.exception()) if retry_state.outcome else None,
    )


# ──────────────────────────────────────────────────────────────────────────────
# ConcurrencyGatedProvider


class ConcurrencyGatedProvider:
    """``LLMProvider`` decorator that routes calls through a ``ProviderGate``.

    Decision 2 ordering (slot held only during the upstream call):

      retry-loop → limiter-wait → acquire-slot → call-raw → release-slot

    Releasing the slot between retry attempts ensures a single 429 with a
    long ``Retry-After`` does not monopolize the only slot while other
    callers wait.

    ``stream()`` is pass-through in Phase 1 (no usage extraction).
    Provider-specific streaming subclasses (``OpenAICompatGatedProvider``,
    ``AnthropicGatedProvider``) that extract final usage tokens are added in
    Phase 6.  A ``# TODO(phase-6)`` comment marks the extension point.
    """

    def __init__(
        self,
        raw: LLMProvider,
        gate: ProviderGate,
        config: ConcurrencyConfig | None = None,
    ) -> None:
        self._raw = raw
        self._gate = gate
        self._config = config or ConcurrencyConfig()
        # aiolimiter.AsyncLimiter instance; None = no RPM shaping (Phase 1).
        self._limiter: Any = None  # TODO(phase-3): wire from config.rpm

    async def complete(
        self,
        prompt: str,
        *,
        system: str = "",
        max_tokens: int = 4096,
        temperature: float = 0.0,
        frequency_penalty: float = 0.0,
        model: str | None = None,
    ) -> LLMResponse:
        """Gate + retry wrapper around ``raw.complete``."""
        retry_max = self._config.retry_max_attempts

        async for attempt in AsyncRetrying(
            retry=retry_if_exception(_retry_predicate),
            wait=wait_random_exponential(multiplier=1, max=60),
            stop=stop_after_attempt(retry_max),
            reraise=True,
            before_sleep=_record_retry,
        ):
            with attempt:
                if self._limiter is not None:
                    await self._limiter.acquire()
                async with self._gate.slot():
                    response = await self._raw.complete(
                        prompt,
                        system=system,
                        max_tokens=max_tokens,
                        temperature=temperature,
                        frequency_penalty=frequency_penalty,
                        model=model,
                    )
                    self._gate.record_completion(response.output_tokens)
                    return response  # type: ignore[return-value]

        # Unreachable (reraise=True above), but keeps type-checkers happy.
        raise RuntimeError("AsyncRetrying exited without raising or returning")

    async def stream(
        self,
        prompt: str,
        *,
        system: str = "",
        max_tokens: int = 4096,
        temperature: float = 0.0,
        model: str | None = None,
    ) -> AsyncIterator[str]:
        """Pass-through streaming (no usage extraction in Phase 1).

        TODO(phase-6): replace with provider-specific subclasses
        (``OpenAICompatGatedProvider``, ``AnthropicGatedProvider``) that
        call the SDK directly and extract the final usage chunk for tok/s
        ring-buffer recording.  Until Phase 6, this wrapper simply acquires
        the slot, delegates to ``raw.stream``, and releases on completion.
        """
        retry_max = self._config.retry_max_attempts

        async for attempt in AsyncRetrying(
            retry=retry_if_exception(_retry_predicate),
            wait=wait_random_exponential(multiplier=1, max=60),
            stop=stop_after_attempt(retry_max),
            reraise=True,
            before_sleep=_record_retry,
        ):
            with attempt:
                if self._limiter is not None:
                    await self._limiter.acquire()
                async with self._gate.slot():
                    async for chunk in self._raw.stream(
                        prompt,
                        system=system,
                        max_tokens=max_tokens,
                        temperature=temperature,
                        model=model,
                    ):
                        yield chunk
                    return  # Successful stream complete.


# ──────────────────────────────────────────────────────────────────────────────
# Factory helper


async def wrap_provider(
    raw: LLMProvider,
    provider_name: str,
    base_url: str | None,
    kind: str,
    registry: ProviderGateRegistry,
    config: ConcurrencyConfig | None = None,
) -> LLMProvider:
    """Wrap ``raw`` in a ``ConcurrencyGatedProvider`` if the kill switch is on.

    Returns ``raw`` unchanged when ``config.wrapper_enabled`` is False.
    """
    cfg = config or registry._config
    if not cfg.wrapper_enabled:
        return raw
    gate = await registry.lookup(provider_name, base_url, kind)
    return ConcurrencyGatedProvider(raw, gate, cfg)
