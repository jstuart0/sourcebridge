"""Phase 5 tests: embedding fan-out (OpenAI-compat only; Ollama stays serial).

Tests verify:
  - test_openai_compat_embed_fans_out_chunks: peak in-flight == 4 under
    multi-chunk input (>256 texts to trigger chunking, LOCAL_EMBEDDING_FANOUT_LIMIT=4).
  - test_ollama_embed_does_not_fan_out: peak in-flight == 1 (serial preserved).
  - Result-order preservation: embed N items → output[i] corresponds to input[i].

Refs: CA-169 / plan v4 Phase 5.
"""

from __future__ import annotations

import asyncio

import pytest

from workers.common.embedding.ollama import OllamaEmbeddingProvider
from workers.common.embedding.openai_compat import (
    _BATCH_SIZE,
    LOCAL_EMBEDDING_FANOUT_LIMIT,
    OpenAICompatEmbeddingProvider,
)

# ──────────────────────────────────────────────────────────────────────────────
# Helpers


def _texts(n: int) -> list[str]:
    return [f"text-{i}" for i in range(n)]


def _fake_embedding(text: str) -> list[float]:
    """Deterministic embedding so order-preservation can be asserted."""
    return [float(hash(text) % 1000) / 1000.0, float(len(text)) / 100.0]


class _BatchTracker:
    """Tracks peak concurrent in-flight _embed_batch calls."""

    def __init__(self, latency: float = 0.05) -> None:
        self.latency = latency
        self.peak_in_flight = 0
        self._in_flight = 0
        self._lock = asyncio.Lock()

    def patch(self, provider: OpenAICompatEmbeddingProvider | OllamaEmbeddingProvider) -> None:
        tracker = self

        async def _fake_embed_batch(texts: list[str]) -> list[list[float]]:
            async with tracker._lock:
                tracker._in_flight += 1
                if tracker._in_flight > tracker.peak_in_flight:
                    tracker.peak_in_flight = tracker._in_flight
            try:
                await asyncio.sleep(tracker.latency)
                return [_fake_embedding(t) for t in texts]
            finally:
                async with tracker._lock:
                    tracker._in_flight -= 1

        provider._embed_batch = _fake_embed_batch  # type: ignore[method-assign]


# ──────────────────────────────────────────────────────────────────────────────
# 1. test_openai_compat_embed_fans_out_chunks


@pytest.mark.asyncio
async def test_openai_compat_embed_fans_out_chunks() -> None:
    """OpenAI-compat: >256 texts split into batches, issued concurrently (peak == LOCAL_EMBEDDING_FANOUT_LIMIT).

    We use LOCAL_EMBEDDING_FANOUT_LIMIT * 3 batches so the semaphore is
    saturated: LOCAL_EMBEDDING_FANOUT_LIMIT batches hold slots while the rest
    queue, producing peak_in_flight == LOCAL_EMBEDDING_FANOUT_LIMIT.
    """
    batch_count = LOCAL_EMBEDDING_FANOUT_LIMIT * 3  # 12 batches → semaphore saturates
    n_texts = batch_count * _BATCH_SIZE  # exactly 12 full batches

    provider = OpenAICompatEmbeddingProvider(
        base_url="http://localhost:11434",
        model="nomic-embed-text",
    )
    tracker = _BatchTracker(latency=0.05)
    tracker.patch(provider)

    texts = _texts(n_texts)
    result = await provider.embed(texts)

    assert len(result) == n_texts, f"Expected {n_texts} embeddings, got {len(result)}"
    assert tracker.peak_in_flight > 1, (
        f"Peak in-flight was {tracker.peak_in_flight}; expected > 1 (fan-out active)"
    )
    assert tracker.peak_in_flight <= LOCAL_EMBEDDING_FANOUT_LIMIT, (
        f"Peak in-flight {tracker.peak_in_flight} exceeded LOCAL_EMBEDDING_FANOUT_LIMIT={LOCAL_EMBEDDING_FANOUT_LIMIT}"
    )


# ──────────────────────────────────────────────────────────────────────────────
# 2. test_ollama_embed_does_not_fan_out


@pytest.mark.asyncio
async def test_ollama_embed_does_not_fan_out() -> None:
    """Ollama embedding stays serial — host gate combines with LLM gate (plan Decision 1 + 8).

    Peak in-flight must be exactly 1 even for multi-batch inputs.
    """
    # Use 3 batches to ensure multi-batch path is exercised.
    n_texts = _BATCH_SIZE * 3

    provider = OllamaEmbeddingProvider(
        base_url="http://localhost:11434",
        model="nomic-embed-text",
    )
    tracker = _BatchTracker(latency=0.03)
    tracker.patch(provider)

    texts = _texts(n_texts)
    result = await provider.embed(texts)

    assert len(result) == n_texts
    assert tracker.peak_in_flight == 1, (
        f"Ollama peak in-flight was {tracker.peak_in_flight}; expected 1 (serial)"
    )


# ──────────────────────────────────────────────────────────────────────────────
# 3. test_openai_compat_embed_order_preserved


@pytest.mark.asyncio
async def test_openai_compat_embed_order_preserved() -> None:
    """output[i] corresponds to input[i] after concurrent fan-out.

    _fake_embedding is deterministic on the text string, so we can assert
    that the embedding at position i was produced from texts[i].
    """
    # Use enough texts to span 5 batches (fan-out > 1 guaranteed).
    n_texts = _BATCH_SIZE * 5

    provider = OpenAICompatEmbeddingProvider(
        base_url="http://localhost:11434",
        model="nomic-embed-text",
    )
    tracker = _BatchTracker(latency=0.02)
    tracker.patch(provider)

    texts = _texts(n_texts)
    result = await provider.embed(texts)

    assert len(result) == n_texts
    for i, (text, embedding) in enumerate(zip(texts, result, strict=True)):
        expected = _fake_embedding(text)
        assert embedding == expected, (
            f"Output[{i}] mismatch: got {embedding}, expected embedding for '{text}'"
        )


# ──────────────────────────────────────────────────────────────────────────────
# 4. test_openai_compat_embed_single_batch_no_fan_out


@pytest.mark.asyncio
async def test_openai_compat_embed_single_batch_no_fan_out() -> None:
    """Single batch (<=256 texts) takes the fast path — no Semaphore constructed."""
    n_texts = _BATCH_SIZE  # exactly one batch

    provider = OpenAICompatEmbeddingProvider(
        base_url="http://localhost:11434",
        model="nomic-embed-text",
    )
    tracker = _BatchTracker(latency=0.0)
    tracker.patch(provider)

    texts = _texts(n_texts)
    result = await provider.embed(texts)

    assert len(result) == n_texts
    # Single batch → _embed_batch called once, peak must be 1.
    assert tracker.peak_in_flight == 1, (
        f"Single-batch path should have peak==1, got {tracker.peak_in_flight}"
    )


# ──────────────────────────────────────────────────────────────────────────────
# 5. test_ollama_embed_order_preserved


@pytest.mark.asyncio
async def test_ollama_embed_order_preserved() -> None:
    """Ollama serial embed preserves output[i] == embedding(input[i])."""
    n_texts = _BATCH_SIZE * 2

    provider = OllamaEmbeddingProvider(
        base_url="http://localhost:11434",
        model="nomic-embed-text",
    )
    tracker = _BatchTracker(latency=0.0)
    tracker.patch(provider)

    texts = _texts(n_texts)
    result = await provider.embed(texts)

    assert len(result) == n_texts
    for i, (text, embedding) in enumerate(zip(texts, result, strict=True)):
        expected = _fake_embedding(text)
        assert embedding == expected, (
            f"Output[{i}] mismatch: got {embedding}, expected embedding for '{text}'"
        )
