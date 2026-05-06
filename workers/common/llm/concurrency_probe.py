"""Concurrency probe for local LLM backends.

Fires two concurrent tiny completion calls and measures the wall-time ratio
to estimate the upstream server's actual parallel inference capacity.

Phase 0 confirmed: Ollama 0.21.0 does NOT expose num_parallel via any HTTP
API surface (/api/ps, /api/version, /v1/models). The timing-heuristic probe
is therefore the only mechanism available.

Accuracy caveat (plan L5): probe accuracy is undefined when production traffic
is active, because the tiny probe requests contend with real LLM calls in
Ollama's queue. On a quiet system the ratio is reliable. Operators running
concurrent LW jobs should interpret any WARN as potentially noisy.

Probe path discipline: uses a raw httpx.AsyncClient against the upstream
OpenAI-compat endpoint rather than the shared provider.complete() call, so
the probe does not feed the shared completion queue or interfere with
production request accounting.

Dependency category: local-substitutable. Production path uses a real
OpenAI-compat endpoint. Tests inject a FakeConcurrencyProbeBackend.
"""

from __future__ import annotations

import asyncio
import time
from typing import TYPE_CHECKING, Protocol

import structlog

if TYPE_CHECKING:
    pass

log = structlog.get_logger()

# Minimum number of seconds below which the ratio measurement is unreliable
# (network jitter + cold-KV-cache effects dominate at this scale).
_MIN_RELIABLE_CALL_SECONDS = 0.2

# Ratio threshold: if total_2_calls / single_call < this, we conclude
# both calls ran in parallel (num_parallel >= 2).
_PARALLEL_RATIO_THRESHOLD = 1.6

# Hard ceiling from D9 / H1.
_HARD_CONCURRENCY_CEILING = 256


class ProbeBackend(Protocol):
    """Protocol for a probe call backend.

    The production backend fires a real HTTP request; tests inject a fake.
    """

    async def call(self) -> float:
        """Execute one tiny probe call and return wall-time in seconds."""
        ...


class OpenAICompatProbeBackend:
    """Fires a single tiny chat-completion via httpx (not the shared openai client).

    Uses max_tokens=4 with a trivial prompt so the call completes in <1s on
    any model. The content of the response is discarded — only the wall-time
    matters.
    """

    def __init__(self, base_url: str, model: str, api_key: str = "") -> None:
        self._base_url = base_url.rstrip("/")
        self._model = model
        self._api_key = api_key

    async def call(self) -> float:
        """Fire one probe call; return wall-time in seconds."""
        import httpx

        headers: dict[str, str] = {"Content-Type": "application/json"}
        if self._api_key:
            headers["Authorization"] = f"Bearer {self._api_key}"

        payload = {
            "model": self._model,
            "messages": [{"role": "user", "content": "Reply: ok"}],
            "max_tokens": 4,
            "temperature": 0.0,
        }

        start = time.monotonic()
        async with httpx.AsyncClient(timeout=30.0) as client:
            try:
                await client.post(
                    f"{self._base_url}/chat/completions",
                    json=payload,
                    headers=headers,
                )
            except Exception:
                pass  # timing measurement ignores response errors
        return time.monotonic() - start


async def probe_concurrency(
    backend: ProbeBackend,
    *,
    declared: int = 0,
) -> tuple[int, str]:
    """Estimate the upstream LLM server's parallel inference capacity.

    Strategy:
    1. Warm-up: fire one probe call to pre-load the KV cache and model runner.
       Discard the result (cold-start latency is noisy).
    2. Baseline: fire one probe call; record single_time.
    3. Parallel: fire two probe calls concurrently; record pair_time.
    4. If pair_time / single_time < 1.6 → both ran in parallel → return (2, confidence).
       If >= 1.6 → serialized → return (1, confidence).

    Confidence: "high" when single_time > _MIN_RELIABLE_CALL_SECONDS (the
    model actually took time, not just a cached response); "low" otherwise.

    Returns:
        (observed_parallelism, confidence) — observed_parallelism is 1 or 2.
        On any exception the function returns (0, "low") so callers treat the
        probe as inconclusive and do not warn.

    The result is informational. The caller logs a WARN when declared and
    observed disagree by >=2x; it does NOT auto-override the declared value.
    """
    try:
        # Warm-up: discard cold-start latency.
        await backend.call()

        # Baseline: single call.
        single_time = await backend.call()

        # Parallel pair.
        pair_start = time.monotonic()
        await asyncio.gather(backend.call(), backend.call())
        pair_time = time.monotonic() - pair_start

        confidence = "high" if single_time > _MIN_RELIABLE_CALL_SECONDS else "low"
        ratio = pair_time / single_time if single_time > 0 else 999.0

        log.debug(
            "llm_concurrency_probe_raw",
            single_time_s=round(single_time, 3),
            pair_time_s=round(pair_time, 3),
            ratio=round(ratio, 2),
            declared=declared,
            confidence=confidence,
        )

        observed = 1 if ratio >= _PARALLEL_RATIO_THRESHOLD else 2
        return observed, confidence

    except Exception as exc:
        log.warning(
            "llm_concurrency_probe_failed",
            error=str(exc),
        )
        return 0, "low"


async def run_startup_probe(
    backend: ProbeBackend,
    *,
    declared: int,
    provider_name: str,
    model: str,
) -> None:
    """Run the concurrency probe at startup and log the result.

    Fires asynchronously after worker bootstrap. The declared value is
    never overridden — this function is informational (D1).

    A WARN fires when declared and observed disagree by >=2x, which
    helps operators correlate "my LW jobs are slow" symptoms with a
    mis-configured OLLAMA_NUM_PARALLEL.
    """
    observed, confidence = await probe_concurrency(backend, declared=declared)

    log.info(
        "llm_concurrency_probe_complete",
        declared=declared,
        observed=observed,
        confidence=confidence,
        provider=provider_name,
        model=model,
    )

    if declared > 0 and observed > 0 and confidence != "low":
        # Disagree by >=2x: declared says N but we measured <=N/2.
        if declared >= 2 * observed:
            log.warning(
                "llm_concurrency_probe_disagreement",
                declared=declared,
                observed=observed,
                confidence=confidence,
                hint=(
                    "Declared max_concurrent_calls may be higher than the upstream "
                    "server's actual parallelism. Consider checking OLLAMA_NUM_PARALLEL "
                    "or the equivalent setting for your LLM backend."
                ),
            )
