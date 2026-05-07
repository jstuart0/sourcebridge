"""Phase 6 tests for tok/s ring buffer, aggregator task, and streaming usage extraction.

Tests verify:
  - Gate emits llm_provider_gate_metrics structlog lines from the aggregator task
  - record_completion → snapshot_tokens_per_second returns non-zero (non-streaming path)
  - OpenAICompatGatedProvider stream extracts usage from the final chunk's
    completion_tokens and records it in the ring buffer
  - AnthropicGatedProvider stream extracts usage from get_final_message() and records it
  - 400 with "stream_options" text falls back to raw.stream() silently
  - streaming_usage_unsupported flag is set after the 400 fallback fires
  - CancelledError during stream does NOT record tokens in the ring buffer
  - close() cancels and awaits the aggregator task (no hang or unraisable warning)

Refs: CA-169 / plan v4 Phase 6 Verification list.
"""

from __future__ import annotations

import asyncio
from collections.abc import AsyncIterator
from unittest.mock import AsyncMock, MagicMock, patch

import openai
import pytest
import pytest_asyncio  # noqa: F401

from workers.common.llm.concurrency import (
    AnthropicGatedProvider,
    ConcurrencyConfig,
    OpenAICompatGatedProvider,
    ProviderGateRegistry,
    _GateBase,
)

# ──────────────────────────────────────────────────────────────────────────────
# Helpers


def _registry(
    wrapper_enabled: bool = True,
    metrics_interval_seconds: float = 9999.0,
) -> ProviderGateRegistry:
    """Return a registry with a very long aggregator interval so it never fires
    during tests that don't explicitly test the aggregator."""
    cfg = ConcurrencyConfig(
        wrapper_enabled=wrapper_enabled,
        metrics_interval_seconds=metrics_interval_seconds,
    )
    return ProviderGateRegistry(cfg)


def _gate_base(max_concurrent: int = 4) -> _GateBase:
    return _GateBase(max_concurrent=max_concurrent)


# ──────────────────────────────────────────────────────────────────────────────
# 1. Aggregator emits structlog lines


@pytest.mark.asyncio
async def test_gate_emits_metrics_log() -> None:
    """Aggregator task emits llm_provider_gate_metrics within its first interval."""
    cfg = ConcurrencyConfig(metrics_interval_seconds=0.05)
    reg = ProviderGateRegistry(cfg)

    # Register a gate so snapshot() returns at least one entry.
    await reg.lookup("ollama", "http://localhost:11434/v1", "llm")

    emitted: list[dict] = []

    def _capture(event: str, **kwargs):
        if event == "llm_provider_gate_metrics":
            emitted.append({"event": event, **kwargs})

    # Patch the module-level log bound to concurrency.py's logger.
    with patch("workers.common.llm.concurrency.log") as mock_log:
        mock_log.info.side_effect = _capture
        # Wait a bit longer than one interval for the aggregator to fire.
        await asyncio.sleep(0.15)

    await reg.close()

    assert len(emitted) >= 1, f"expected at least one metrics log, got {emitted}"
    entry = emitted[0]
    assert entry["provider"] == "ollama"
    assert "tokens_per_second_60s" in entry


# ──────────────────────────────────────────────────────────────────────────────
# 2. record_completion → snapshot_tokens_per_second (non-streaming path)


def test_gate_tracks_tokens_per_second_non_streaming() -> None:
    """record_completion appends to the ring; snapshot_tokens_per_second is non-zero."""
    gate = _gate_base()
    assert gate.snapshot_tokens_per_second() == 0.0

    gate.record_completion(300)
    gate.record_completion(700)

    tps = gate.snapshot_tokens_per_second()
    # 1000 tokens / 60 s window ≈ 16.7 tok/s — just verify it's positive.
    assert tps > 0.0, f"expected positive tok/s, got {tps}"


# ──────────────────────────────────────────────────────────────────────────────
# 3. OpenAICompatGatedProvider extracts usage from streaming final chunk


@pytest.mark.asyncio
async def test_gate_tracks_tokens_per_second_streaming_openai_compat_gated_provider() -> None:
    """OpenAICompatGatedProvider records ring-buffer tokens from the final chunk."""
    from workers.common.llm.fake import FakeLLMProvider

    reg = _registry()
    gate = await reg.lookup("openai", "https://api.openai.com/v1", "llm")

    # Build a minimal mock client that yields one text chunk + one usage chunk.
    usage_chunk = MagicMock()
    usage_chunk.choices = []
    usage_chunk.usage = MagicMock()
    usage_chunk.usage.completion_tokens = 42

    text_chunk = MagicMock()
    text_delta = MagicMock()
    text_delta.content = "hello"
    text_chunk.choices = [MagicMock(delta=text_delta)]
    text_chunk.usage = None

    async def _fake_stream_chunks(*args, **kwargs):
        yield text_chunk
        yield usage_chunk

    mock_client = MagicMock()
    mock_client.chat = MagicMock()
    mock_client.chat.completions = MagicMock()
    mock_client.chat.completions.create = AsyncMock(return_value=_fake_stream_chunks())

    raw = FakeLLMProvider()
    raw.client = mock_client  # inject the mock OpenAI client

    provider = OpenAICompatGatedProvider(raw, gate)
    chunks: list[str] = []
    async for chunk in provider.stream("ping"):
        chunks.append(chunk)

    assert "hello" in chunks
    tps = gate._binding.snapshot_tokens_per_second()
    assert tps > 0.0, f"expected ring buffer to have tokens after stream, got {tps}"

    await reg.close()


# ──────────────────────────────────────────────────────────────────────────────
# 4. AnthropicGatedProvider extracts usage from get_final_message()


@pytest.mark.asyncio
async def test_gate_tracks_tokens_per_second_streaming_anthropic_gated_provider() -> None:
    """AnthropicGatedProvider records ring-buffer tokens from get_final_message()."""
    from workers.common.llm.fake import FakeLLMProvider

    reg = _registry()
    gate = await reg.lookup("anthropic", None, "llm")

    # Mock the Anthropic async streaming context manager.
    final_message = MagicMock()
    final_message.usage = MagicMock()
    final_message.usage.output_tokens = 55

    mock_stream_cm = MagicMock()
    mock_stream_cm.__aenter__ = AsyncMock(return_value=mock_stream_cm)
    mock_stream_cm.__aexit__ = AsyncMock(return_value=False)
    mock_stream_cm.get_final_message = AsyncMock(return_value=final_message)

    async def _text_stream():
        yield "world"

    mock_stream_cm.text_stream = _text_stream()

    mock_client = MagicMock()
    mock_client.messages = MagicMock()
    mock_client.messages.stream = MagicMock(return_value=mock_stream_cm)

    raw = FakeLLMProvider()
    raw.client = mock_client

    provider = AnthropicGatedProvider(raw, gate)
    chunks: list[str] = []
    async for chunk in provider.stream("ping"):
        chunks.append(chunk)

    assert "world" in chunks
    tps = gate._binding.snapshot_tokens_per_second()
    assert tps > 0.0, f"expected ring buffer tokens after anthropic stream, got {tps}"

    await reg.close()


# ──────────────────────────────────────────────────────────────────────────────
# 5. stream_options 400 → falls back to raw.stream(), yields chunks


@pytest.mark.asyncio
async def test_stream_options_fallback_on_400() -> None:
    """When the client raises APIStatusError 400 with 'stream_options', fall back."""
    from workers.common.llm.fake import FakeLLMProvider

    reg = _registry()
    gate = await reg.lookup("openai-compatible", "http://localhost:11434/v1", "llm")

    # Mock client that raises 400 with stream_options in the message.
    mock_response = MagicMock()
    mock_response.status_code = 400
    api_err = openai.APIStatusError(
        "unsupported parameter: stream_options",
        response=mock_response,
        body=None,
    )
    mock_client = MagicMock()
    mock_client.chat = MagicMock()
    mock_client.chat.completions = MagicMock()
    mock_client.chat.completions.create = AsyncMock(side_effect=api_err)

    raw = FakeLLMProvider()
    raw.client = mock_client

    # Patch FakeLLMProvider.stream to return a known sequence.
    async def _raw_stream(*args, **kwargs) -> AsyncIterator[str]:
        yield "fallback"

    raw.stream = _raw_stream  # type: ignore[method-assign]

    provider = OpenAICompatGatedProvider(raw, gate)
    chunks: list[str] = []
    async for chunk in provider.stream("ping"):
        chunks.append(chunk)

    assert chunks == ["fallback"], f"expected fallback chunks, got {chunks}"

    await reg.close()


# ──────────────────────────────────────────────────────────────────────────────
# 6. streaming_usage_unsupported flag is set after the 400 fallback


@pytest.mark.asyncio
async def test_streaming_usage_marked_unsupported_after_400() -> None:
    """After the 400 fallback fires, gate.streaming_usage_unsupported is True."""
    from workers.common.llm.fake import FakeLLMProvider

    reg = _registry()
    gate = await reg.lookup("openai-compatible", "http://localhost:8080/v1", "llm")

    mock_response = MagicMock()
    mock_response.status_code = 400
    api_err = openai.APIStatusError(
        "stream_options not supported",
        response=mock_response,
        body=None,
    )
    mock_client = MagicMock()
    mock_client.chat = MagicMock()
    mock_client.chat.completions = MagicMock()
    mock_client.chat.completions.create = AsyncMock(side_effect=api_err)

    raw = FakeLLMProvider()
    raw.client = mock_client

    async def _raw_stream(*args, **kwargs) -> AsyncIterator[str]:
        yield "ok"

    raw.stream = _raw_stream  # type: ignore[method-assign]

    provider = OpenAICompatGatedProvider(raw, gate)
    assert not gate.streaming_usage_unsupported

    async for _ in provider.stream("ping"):
        pass

    assert gate.streaming_usage_unsupported, "expected flag to be set after 400 fallback"

    await reg.close()


# ──────────────────────────────────────────────────────────────────────────────
# 7. CancelledError during stream → record_completion NOT called


@pytest.mark.asyncio
async def test_streaming_cancellation_no_usage_recorded() -> None:
    """When streaming is cancelled mid-flight, tokens are not recorded."""
    from workers.common.llm.fake import FakeLLMProvider

    reg = _registry()
    gate = await reg.lookup("openai", "https://api.openai.com/v1", "llm")

    # Simulate a client stream that hangs (never yields the usage chunk).
    cancel_event = asyncio.Event()

    async def _hanging_stream(*args, **kwargs):
        yield MagicMock(
            choices=[MagicMock(delta=MagicMock(content="partial"))],
            usage=None,
        )
        # Block indefinitely — the task will be cancelled.
        await cancel_event.wait()

    mock_client = MagicMock()
    mock_client.chat = MagicMock()
    mock_client.chat.completions = MagicMock()
    mock_client.chat.completions.create = AsyncMock(return_value=_hanging_stream())

    raw = FakeLLMProvider()
    raw.client = mock_client

    provider = OpenAICompatGatedProvider(raw, gate)

    async def _consume():
        async for _ in provider.stream("ping"):
            pass

    task = asyncio.create_task(_consume())
    # Give the stream one iteration to start.
    await asyncio.sleep(0.02)
    task.cancel()
    import contextlib
    with contextlib.suppress(asyncio.CancelledError, TimeoutError):
        await asyncio.wait_for(task, timeout=1.0)

    tps = gate._binding.snapshot_tokens_per_second()
    assert tps == 0.0, f"expected no tokens recorded on cancellation, got tps={tps}"

    await reg.close()


# ──────────────────────────────────────────────────────────────────────────────
# 8. close() cancels and awaits the aggregator task


@pytest.mark.asyncio
async def test_aggregator_task_cancelled_on_registry_close() -> None:
    """close() cancels the aggregator and the task is done afterward."""
    reg = _registry(metrics_interval_seconds=9999.0)
    task = reg._aggregator_task
    assert not task.done(), "aggregator task should be running before close()"

    await reg.close()

    assert task.done(), "aggregator task should be done after close()"
    # Closing a second time must be idempotent (no error).
    await reg.close()
