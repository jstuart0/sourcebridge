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

Phase 3 (Decision 3, atomic): SDK retry disabled on AsyncOpenAI / AsyncAnthropic
(max_retries=0); tenacity predicate finalized (Decision 4 whitelist); real
Decision 6 defaults loaded by ``ConcurrencyConfig.from_env()``; local
hierarchical/renderer fan-out caps raised; both hand-rolled retries deleted.
The empty-content retry at ``openai_compat.py:_complete_once`` is preserved
(handles <think>-budget exhaustion, not network errors).

Kill switch: SOURCEBRIDGE_LLM_CONCURRENCY_WRAPPER_ENABLED=false reverts to
pre-refactor behavior without redeploy.

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

import anthropic
import httpx
import openai
import structlog
from aiolimiter import AsyncLimiter
from tenacity import (
    AsyncRetrying,
    RetryCallState,
    retry_if_exception,
    stop_after_attempt,
    wait_random_exponential,
)

from workers.common.llm.provider import LLMProvider, LLMResponse

# Lazy imports for provider-specific gated adapters (Decision 10).
# Imported inside wrap_provider to avoid circular imports at module load time.
# The actual check uses isinstance() against those classes.

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

    Decision 6 real defaults are loaded by ``from_env()`` when no env-var
    overrides are set.  The ``_UNCAPPED`` sentinel is retained as the registry
    fallback for unknown providers; it is not the default for any named provider.
    """

    # Per-provider max-concurrent overrides.  Key = canonical provider name.
    # from_env() pre-populates these from the Decision 6 table.
    llm_max_concurrent: dict[str, int] = field(default_factory=dict)
    embedding_max_concurrent: dict[str, int] = field(default_factory=dict)

    # Per-provider RPM limits.  None = no rate shaping (default).
    rpm: dict[str, int | None] = field(default_factory=dict)

    # openai-compatible gating mode: "host" (default) | "per_kind".
    openai_compatible_gating: str = _DEFAULT_OPENAI_COMPAT_GATING

    # Global kill switch.  When False, factories return raw providers.
    wrapper_enabled: bool = True

    # Tenacity: max attempts per call.  Default 5 (Phase 3 activates real retry).
    retry_max_attempts: int = 5

    # Aggregator task interval (seconds).  Phase 6 activates the task.
    metrics_interval_seconds: float = 30.0

    @classmethod
    def from_env(cls) -> ConcurrencyConfig:
        """Read all concurrency knobs from environment variables.

        Decision 6 default caps (applied when no env-var override is set):

          | Provider           | LLM max_concurrent | Embed max_concurrent |
          |--------------------|--------------------|----------------------|
          | ollama             | 1 (host total)     | (host-shared)        |
          | vllm               | 4 (host total)     | (host-shared)        |
          | llama-cpp          | 4 (host total)     | (host-shared)        |
          | sglang             | 4 (host total)     | (host-shared)        |
          | lmstudio           | 2 (host total)     | (host-shared)        |
          | openai             | 8                  | 8                    |
          | anthropic          | 4                  | n/a                  |
          | openrouter         | 8                  | 8                    |
          | gemini             | 8                  | 8                    |
          | openai-compatible  | 4 (host total)     | (host-shared)        |

        Env-var overrides take precedence (first-match wins).

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
        # Decision 6 real defaults (Outcome A — ollama cap=1 is safe per dick's
        # investigation; cap-raise to 4 is safe for vllm/llama-cpp/sglang).
        llm_defaults: dict[str, int] = {
            "ollama": 1,
            "vllm": 4,
            "llama-cpp": 4,
            "sglang": 4,
            "lmstudio": 2,
            "openai": 8,
            "anthropic": 4,
            "openrouter": 8,
            "gemini": 8,
            "openai-compatible": 4,
        }
        embed_defaults: dict[str, int] = {
            # Frontier providers have separate embedding caps.
            "openai": 8,
            "openrouter": 8,
            "gemini": 8,
            # Host-gated providers share the LLM cap; no separate embed entry needed.
        }

        all_providers = list(_HOST_GATED_PROVIDERS | _KIND_GATED_PROVIDERS) + [
            "openai-compatible"
        ]

        # Start with Decision 6 defaults; env-var overrides overwrite them.
        llm_max: dict[str, int] = dict(llm_defaults)
        embed_max: dict[str, int] = dict(embed_defaults)
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
        retry_max = 5  # Phase 3 default: 5 attempts (Decision 3)
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
                retry_max = 5

        interval_raw = os.environ.get("SOURCEBRIDGE_LLM_METRICS_AGGREGATION_INTERVAL_SECONDS", "").strip()
        interval = 30.0
        if interval_raw:
            with contextlib.suppress(ValueError):
                interval = float(interval_raw)

        _validate_known_provider_tokens(all_providers)

        return cls(
            llm_max_concurrent=llm_max,
            embedding_max_concurrent=embed_max,
            rpm=rpm_map,
            openai_compatible_gating=gating,
            wrapper_enabled=wrapper_enabled,
            retry_max_attempts=retry_max,
            metrics_interval_seconds=interval,
        )


_LLM_SUFFIXES = ("_MAX_CONCURRENT", "_RPM")
_EMBED_SUFFIXES = ("_MAX_CONCURRENT",)


def _validate_known_provider_tokens(all_providers: list[str]) -> None:
    """Scan env vars for unknown provider tokens and emit a structlog WARNING.

    Decision 7 / codex r2 L1: an operator typo like
    ``SOURCEBRIDGE_LLM_PROVIDER_OPENAICOMPAT_MAX_CONCURRENT`` (missing the
    underscore in OPENAI_COMPATIBLE) would otherwise be silently ignored.
    This helper detects such typos and warns without raising, so existing
    deployments with stale env vars don't crash at boot.

    Scans for the three per-provider env-var patterns consumed by ``from_env()``:
      ``SOURCEBRIDGE_LLM_PROVIDER_<NAME>_MAX_CONCURRENT``
      ``SOURCEBRIDGE_LLM_PROVIDER_<NAME>_RPM``
      ``SOURCEBRIDGE_EMBEDDING_PROVIDER_<NAME>_MAX_CONCURRENT``
    """
    # Build the set of canonical tokens (uppercase, hyphens → underscores).
    canonical_tokens: frozenset[str] = frozenset(
        p.upper().replace("-", "_") for p in all_providers
    )

    for env_var in os.environ:
        token: str | None = None
        if env_var.startswith("SOURCEBRIDGE_LLM_PROVIDER_"):
            remainder = env_var[len("SOURCEBRIDGE_LLM_PROVIDER_"):]
            for suffix in _LLM_SUFFIXES:
                if remainder.endswith(suffix):
                    token = remainder[: -len(suffix)]
                    break
        elif env_var.startswith("SOURCEBRIDGE_EMBEDDING_PROVIDER_"):
            remainder = env_var[len("SOURCEBRIDGE_EMBEDDING_PROVIDER_"):]
            for suffix in _EMBED_SUFFIXES:
                if remainder.endswith(suffix):
                    token = remainder[: -len(suffix)]
                    break

        if token is not None and token not in canonical_tokens:
            log.warning(
                "concurrency_config_unknown_provider_token",
                env_var=env_var,
                unknown_token=token,
                canonical_tokens=sorted(canonical_tokens),
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
        # Phase 6: start the aggregator task. It runs indefinitely until
        # close() cancels it. Emits llm_provider_gate_metrics info-level
        # structlog lines every metrics_interval_seconds.
        self._aggregator_task: asyncio.Task[None] = asyncio.ensure_future(
            self._run_aggregator()
        )

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
        is sentinel-uncapped (unknown provider with no Decision 6 default).
        Phase 3: Decision 6 defaults are pre-populated by ``from_env()``, so
        all named providers now return a finite cap.
        """
        if not self._config.wrapper_enabled:
            return None
        cap = self._config.llm_max_concurrent.get(provider)
        if cap is None or cap == _UNCAPPED:
            return None  # Unknown provider or sentinel-uncapped.
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

    def snapshot_tokens_per_second(
        self, provider: str, base_url: str | None, kind: str
    ) -> float:
        """Return the 60-second ring-buffer tok/s for the given gate.

        Called by progress-emit sites (e.g. hierarchical strategy) once per
        progress event to populate KnowledgeStreamProgress.current_tokens_per_second.
        Returns 0.0 when the gate doesn't exist yet or the wrapper is disabled.
        """
        if not self._config.wrapper_enabled:
            return 0.0
        mode = self._classify(provider)
        if mode == "host":
            _, normalized_origin = _normalize_host_key(provider, base_url)
            host_key = (provider, normalized_origin)
            gate = self._host_gates.get(host_key)
            if gate is not None:
                return gate.snapshot_tokens_per_second()
            return 0.0
        else:
            raw_url = base_url or ""
            kind_key = (provider, raw_url, kind)
            gate = self._kind_gates.get(kind_key)
            if gate is not None:
                return gate.snapshot_tokens_per_second()
            return 0.0

    async def _run_aggregator(self) -> None:
        """Emit llm_provider_gate_metrics info-level structlog lines at a
        fixed cadence (metrics_interval_seconds from ConcurrencyConfig).

        Runs until cancelled by close(). Does not emit when the registry has
        no active gates yet (avoids log noise on startup).
        """
        interval = self._config.metrics_interval_seconds
        while True:
            await asyncio.sleep(interval)
            # Snapshot the current gate state and emit one log line per gate.
            entries = self.snapshot()
            for entry in entries:
                # Derive retries_since_last_tick from the raw gate counter.
                # We read it directly because GateSnapshotEntry records the
                # cumulative total — callers who want a delta should persist
                # the previous total themselves. For the log line, the
                # cumulative total is sufficient for operators.
                log.info(
                    "llm_provider_gate_metrics",
                    provider=entry.provider,
                    base_url_normalized=entry.base_url_normalized,
                    kind=entry.kind,
                    in_flight=entry.in_flight,
                    queue_depth=entry.queue_depth,
                    max_concurrent=entry.max_concurrent,
                    retries_since_start=entry.retries_since_start,
                    recent_429_count=entry.recent_429_count,
                    tokens_per_second_60s=round(entry.tokens_per_second, 2),
                )

    async def close(self) -> None:
        """Cancel the aggregator task and mark the registry closed.

        Idempotent — safe to call more than once.
        """
        if self._closed:
            return
        self._closed = True
        self._aggregator_task.cancel()
        with contextlib.suppress(asyncio.CancelledError):
            await self._aggregator_task


# ──────────────────────────────────────────────────────────────────────────────
# Tenacity predicate and retry hooks (Phase 3: Decision 4 whitelist)


def _retry_predicate(exc: BaseException) -> bool:
    """Return True when the exception is retryable (Decision 4 whitelist).

    1. RateLimitError — always retryable (both OpenAI and Anthropic SDKs).
    2. APIStatusError with status_code in {408, 429, 502, 503, 504}.
    3. Transient httpx errors: TimeoutException, ConnectError, ReadError.

    Returns False for everything else, including 4xx errors (except 408/429),
    pydantic.ValidationError, SnapshotTooLargeError, and any other non-transient
    failure.  The wrapper never retries auth failures (401/403) or bad requests
    (400/422) — those require operator intervention.
    """
    # 1. Rate-limit errors are always retryable.
    if isinstance(exc, (openai.RateLimitError, anthropic.RateLimitError)):
        return True
    # 2. Status-code-filtered API errors.
    if isinstance(exc, (openai.APIStatusError, anthropic.APIStatusError)):
        return getattr(exc, "status_code", None) in {408, 429, 502, 503, 504}
    # 3. Transient httpx transport errors.
    if isinstance(exc, (httpx.TimeoutException, httpx.ConnectError, httpx.ReadError)):
        return True
    return False


def _extract_retry_after(exc: BaseException) -> float | None:
    """Extract the Retry-After header value (seconds) from an SDK exception.

    Returns None when no header is present or parsing fails.
    """
    # OpenAI SDK exposes the response object on APIStatusError.
    response = getattr(exc, "response", None)
    if response is not None:
        headers = getattr(response, "headers", None)
        if headers is not None:
            raw = headers.get("retry-after") or headers.get("Retry-After")
            if raw:
                with contextlib.suppress(ValueError):
                    return float(raw)
    return None


def _make_before_sleep(gate_binding: Any) -> Any:
    """Factory returning a tenacity ``before_sleep`` callback.

    The callback:
    1. Increments the gate's retry counter and 429 counter (when applicable).
    2. Logs a structured debug line with attempt info.
    3. Extracts the ``Retry-After`` header from the exception and extends the
       tenacity wait duration when the header value exceeds the computed
       exponential backoff (Decision 2 — use the larger of the two so a tiny
       Retry-After: 1 doesn't subvert sustained-429 backoff).
    """

    def _before_sleep(retry_state: RetryCallState) -> None:
        exc = retry_state.outcome.exception() if retry_state.outcome else None
        gate_binding._retries += 1
        if isinstance(exc, (openai.RateLimitError, anthropic.RateLimitError)):
            gate_binding._recent_429 += 1
        elif isinstance(exc, (openai.APIStatusError, anthropic.APIStatusError)):
            if getattr(exc, "status_code", None) == 429:
                gate_binding._recent_429 += 1

        # Honor Retry-After header: extend the computed sleep when needed.
        retry_after = _extract_retry_after(exc) if exc is not None else None

        log.debug(
            "llm_gate_retry",
            attempt=retry_state.attempt_number,
            exc_type=type(exc).__name__ if exc else None,
            retry_after_header=retry_after,
        )

        if retry_after is not None and retry_state.next_action is not None:
            computed_sleep = getattr(retry_state.next_action, "sleep", 0.0)
            # Use the larger of the two; never let Retry-After: 1 undercut backoff.
            if retry_after > computed_sleep:
                retry_state.next_action.sleep = retry_after  # type: ignore[assignment]

    return _before_sleep


# ──────────────────────────────────────────────────────────────────────────────
# ConcurrencyGatedProvider


class ConcurrencyGatedProvider:
    """``LLMProvider`` decorator that routes calls through a ``ProviderGate``.

    Decision 2 ordering (slot held only during the upstream call):

      retry-loop → limiter-wait → acquire-slot → call-raw → release-slot

    Releasing the slot between retry attempts ensures a single 429 with a
    long ``Retry-After`` does not monopolize the only slot while other
    callers wait.

    Phase 3: tenacity predicate activated (Decision 4 whitelist), aiolimiter
    wired for RPM shaping when configured.

    ``stream()`` is pass-through (no usage extraction).
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
        # Wire aiolimiter for RPM rate-shaping when configured (Decision 7).
        # None = no RPM shaping (default for all providers; Phase 8 adds specific RPMs).
        provider_name = getattr(raw, "provider_name", None) or ""
        rpm = self._config.rpm.get(provider_name)
        self._limiter: AsyncLimiter | None = (
            AsyncLimiter(rpm, time_period=60) if rpm is not None and rpm > 0 else None
        )
        # Cache the before_sleep callback (captures the gate's binding).
        self._before_sleep = _make_before_sleep(gate._binding)

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
            before_sleep=self._before_sleep,
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
        """Pass-through streaming (slot held during the entire stream).

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
            before_sleep=self._before_sleep,
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
# Provider-specific gated adapters (Decision 10, Phase 6)
#
# These subclasses override ``stream()`` to extract final token counts from
# the SDK's streaming response and feed them into the gate's 60-second ring
# buffer. The base class ``ConcurrencyGatedProvider.stream()`` is a pass-through
# that does not record token counts; use these subclasses for providers whose
# SDKs surface streaming usage data.


class OpenAICompatGatedProvider(ConcurrencyGatedProvider):
    """Gated provider for OpenAI-compatible backends.

    Overrides ``stream()`` to pass ``stream_options={"include_usage": True}``
    so the final chunk carries ``chunk.usage.completion_tokens``. On 400
    responses indicating ``stream_options`` is unsupported the adapter falls
    back silently and marks the gate flag ``streaming_usage_unsupported``.

    Decision 10b / codex r2 M2.
    """

    async def stream(
        self,
        prompt: str,
        *,
        system: str = "",
        max_tokens: int = 4096,
        temperature: float = 0.0,
        model: str | None = None,
    ) -> AsyncIterator[str]:
        """Gate + retry wrapper around raw OpenAI-compat stream with usage extraction."""
        retry_max = self._config.retry_max_attempts

        async for attempt in AsyncRetrying(
            retry=retry_if_exception(_retry_predicate),
            wait=wait_random_exponential(multiplier=1, max=60),
            stop=stop_after_attempt(retry_max),
            reraise=True,
            before_sleep=self._before_sleep,
        ):
            with attempt:
                if self._limiter is not None:
                    await self._limiter.acquire()
                async with self._gate.slot():
                    output_tokens = 0
                    try:
                        async for chunk in self._stream_with_usage(
                            prompt,
                            system=system,
                            max_tokens=max_tokens,
                            temperature=temperature,
                            model=model,
                        ):
                            if isinstance(chunk, int):
                                # Sentinel: final usage token count.
                                output_tokens = chunk
                            else:
                                yield chunk
                    finally:
                        if output_tokens > 0:
                            self._gate.record_completion(output_tokens)
                    return  # Successful stream complete.

    async def _stream_with_usage(
        self,
        prompt: str,
        *,
        system: str = "",
        max_tokens: int = 4096,
        temperature: float = 0.0,
        model: str | None = None,
    ) -> AsyncIterator[str | int]:
        """Delegate to raw.stream, attempting to include usage data.

        Yields text chunks as str, then a final int sentinel with output_tokens.
        When stream_options is unsupported (400), falls back to plain streaming.
        """
        if self._gate.streaming_usage_unsupported:
            # Already know this gate doesn't support stream_options; use raw directly.
            async for chunk in self._raw.stream(
                prompt, system=system, max_tokens=max_tokens, temperature=temperature, model=model
            ):
                yield chunk
            return

        # Attempt stream with usage via the provider's own stream() first.
        # The OpenAICompatProvider.stream() doesn't pass stream_options; to extract
        # usage we need the underlying client. Try to access it directly.
        raw_client = getattr(self._raw, "client", None)
        if raw_client is None:
            # No direct SDK access — fall back to raw.stream() without usage.
            async for chunk in self._raw.stream(
                prompt, system=system, max_tokens=max_tokens, temperature=temperature, model=model
            ):
                yield chunk
            return

        use_model = model or getattr(self._raw, "model", None)
        messages: list[dict[str, str]] = []
        if system:
            messages.append({"role": "system", "content": system})
        messages.append({"role": "user", "content": prompt})

        # Build extra_body from raw provider if applicable.
        extra_body: dict[str, object] | None = None
        draft_model = getattr(self._raw, "draft_model", None)
        if draft_model:
            extra_body = {"draft_model": draft_model}
        disable_thinking = getattr(self._raw, "disable_thinking", False)
        if disable_thinking:
            extra_body = dict(extra_body or {})
            extra_body["chat_template_kwargs"] = {"enable_thinking": False}

        try:
            stream = await raw_client.chat.completions.create(
                model=use_model,
                messages=messages,  # type: ignore[arg-type]
                max_tokens=max_tokens,
                temperature=temperature,
                stream=True,
                stream_options={"include_usage": True},
                extra_body=extra_body,
            )
            completion_tokens = 0
            async for chunk in stream:
                if chunk.choices and chunk.choices[0].delta.content:
                    yield chunk.choices[0].delta.content
                # The final chunk (empty choices) carries usage when stream_options is set.
                if chunk.usage is not None:
                    completion_tokens = chunk.usage.completion_tokens or 0
            if completion_tokens > 0:
                yield completion_tokens  # type: ignore[misc]  # int sentinel
        except openai.APIStatusError as exc:
            if exc.status_code == 400 and "stream_options" in str(exc).lower():
                # Server doesn't support stream_options — mark the gate and fall back.
                self._gate.streaming_usage_unsupported = True
                log.warning(
                    "llm_gate_stream_options_unsupported",
                    provider=getattr(self._raw, "provider_name", "openai-compatible"),
                    error=str(exc),
                )
                async for chunk in self._raw.stream(
                    prompt, system=system, max_tokens=max_tokens, temperature=temperature, model=model
                ):
                    yield chunk
            else:
                raise


class AnthropicGatedProvider(ConcurrencyGatedProvider):
    """Gated provider for Anthropic's API.

    Overrides ``stream()`` to extract ``usage.output_tokens`` from the
    Anthropic SDK's streaming ``message_delta`` event and feed it into the
    gate's 60-second ring buffer.
    """

    async def stream(
        self,
        prompt: str,
        *,
        system: str = "",
        max_tokens: int = 4096,
        temperature: float = 0.0,
        model: str | None = None,
    ) -> AsyncIterator[str]:
        """Gate + retry wrapper around raw Anthropic stream with usage extraction."""
        retry_max = self._config.retry_max_attempts

        async for attempt in AsyncRetrying(
            retry=retry_if_exception(_retry_predicate),
            wait=wait_random_exponential(multiplier=1, max=60),
            stop=stop_after_attempt(retry_max),
            reraise=True,
            before_sleep=self._before_sleep,
        ):
            with attempt:
                if self._limiter is not None:
                    await self._limiter.acquire()
                async with self._gate.slot():
                    raw_client = getattr(self._raw, "client", None)
                    if raw_client is None:
                        # No direct SDK access; fall back to raw.stream().
                        async for chunk in self._raw.stream(
                            prompt, system=system, max_tokens=max_tokens, temperature=temperature, model=model
                        ):
                            yield chunk
                        return

                    use_model = model or getattr(self._raw, "model", None)
                    output_tokens = 0
                    # Build system prompt via raw provider helper if available.
                    build_system = getattr(self._raw, "_build_system", None)
                    system_block = build_system(system) if build_system is not None else system

                    try:
                        async with raw_client.messages.stream(
                            model=use_model,
                            max_tokens=max_tokens,
                            temperature=temperature,
                            system=system_block,
                            messages=[{"role": "user", "content": prompt}],
                        ) as stream:
                            async for text in stream.text_stream:
                                yield text
                            # After stream completes, get the final message for usage.
                            try:
                                final_msg = await stream.get_final_message()
                                if final_msg.usage:
                                    output_tokens = final_msg.usage.output_tokens or 0
                            except Exception:  # noqa: BLE001
                                pass
                    finally:
                        if output_tokens > 0:
                            self._gate.record_completion(output_tokens)
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
    """Wrap ``raw`` in a provider-specific gated adapter if the kill switch is on.

    Decision 10 (Phase 6): branch on the raw provider's type to select the
    correct gated subclass that can extract streaming usage tokens:

      - OpenAICompatProvider  → OpenAICompatGatedProvider (stream_options extraction)
      - AnthropicProvider     → AnthropicGatedProvider (message_delta extraction)
      - anything else         → ConcurrencyGatedProvider (no streaming usage)

    Returns ``raw`` unchanged when ``config.wrapper_enabled`` is False.
    """
    cfg = config or registry._config
    if not cfg.wrapper_enabled:
        return raw
    gate = await registry.lookup(provider_name, base_url, kind)

    # Lazy imports to avoid circular imports at module load time.
    from workers.common.llm.anthropic import AnthropicProvider  # noqa: PLC0415
    from workers.common.llm.openai_compat import OpenAICompatProvider  # noqa: PLC0415

    if isinstance(raw, OpenAICompatProvider):
        return OpenAICompatGatedProvider(raw, gate, cfg)
    if isinstance(raw, AnthropicProvider):
        return AnthropicGatedProvider(raw, gate, cfg)
    return ConcurrencyGatedProvider(raw, gate, cfg)
