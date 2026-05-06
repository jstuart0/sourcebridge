from __future__ import annotations

from types import SimpleNamespace

import pytest

from workers.common.llm.openai_compat import OpenAICompatProvider, _strip_think_tags


class _FakeCreate:
    def __init__(self) -> None:
        self.calls: list[dict[str, object]] = []

    async def __call__(self, **kwargs):
        self.calls.append(kwargs)
        return SimpleNamespace(
            choices=[SimpleNamespace(message=SimpleNamespace(content="visible output"), finish_reason="stop")],
            usage=SimpleNamespace(prompt_tokens=12, completion_tokens=7),
            model_extra={},
        )


class _FakeAsyncOpenAI:
    def __init__(self, *args, **kwargs) -> None:
        self.api_key = kwargs.get("api_key")
        self.base_url = kwargs.get("base_url")
        self.timeout = kwargs.get("timeout")
        self.chat = SimpleNamespace(completions=SimpleNamespace(create=_FakeCreate()))


@pytest.mark.asyncio
async def test_complete_attaches_disable_thinking_override(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr("workers.common.llm.openai_compat.openai.AsyncOpenAI", _FakeAsyncOpenAI)
    provider = OpenAICompatProvider(
        api_key="x",
        model="qwen3.5:35b-a3b",
        base_url="http://localhost:11434/v1",
        provider_name="ollama",
        disable_thinking=True,
    )

    await provider.complete("hello")

    create = provider.client.chat.completions.create
    assert create.calls
    # llama.cpp path: kwarg toggles the Jinja template variable.
    assert create.calls[0]["extra_body"] == {"chat_template_kwargs": {"enable_thinking": False}}
    # Ollama path: `/no_think` directive appended to the user message.
    # (Both are sent on every call; each backend honors the one it
    # understands, the other is a no-op.)
    user_msg = create.calls[0]["messages"][-1]
    assert user_msg["role"] == "user"
    assert user_msg["content"].endswith("/no_think")
    assert provider.client.api_key == "x"


@pytest.mark.asyncio
async def test_stream_attaches_disable_thinking_override(monkeypatch: pytest.MonkeyPatch) -> None:
    class _FakeStreamCreate(_FakeCreate):
        async def __call__(self, **kwargs):
            self.calls.append(kwargs)

            async def _iter():
                yield SimpleNamespace(choices=[SimpleNamespace(delta=SimpleNamespace(content="chunk"))])

            return _iter()

    class _FakeStreamAsyncOpenAI:
        def __init__(self, *args, **kwargs) -> None:
            self.api_key = kwargs.get("api_key")
            self.base_url = kwargs.get("base_url")
            self.chat = SimpleNamespace(completions=SimpleNamespace(create=_FakeStreamCreate()))

    monkeypatch.setattr(
        "workers.common.llm.openai_compat.openai.AsyncOpenAI",
        _FakeStreamAsyncOpenAI,
    )
    provider = OpenAICompatProvider(
        api_key="x",
        model="qwen3.5:35b-a3b",
        base_url="http://localhost:11434/v1",
        provider_name="ollama",
        disable_thinking=True,
    )

    chunks = []
    async for chunk in provider.stream("hello"):
        chunks.append(chunk)

    assert chunks == ["chunk"]
    create = provider.client.chat.completions.create
    assert create.calls
    assert create.calls[0]["extra_body"] == {"chat_template_kwargs": {"enable_thinking": False}}
    user_msg = create.calls[0]["messages"][-1]
    assert user_msg["content"].endswith("/no_think")
    assert provider.client.api_key == "x"


@pytest.mark.asyncio
async def test_no_think_scoped_to_qwen_only(monkeypatch: pytest.MonkeyPatch) -> None:
    """Non-Qwen models must not receive the `/no_think` directive,
    even when disable_thinking is True, because the string would leak
    into those models' context as literal content rather than being
    interpreted as a directive."""
    monkeypatch.setattr("workers.common.llm.openai_compat.openai.AsyncOpenAI", _FakeAsyncOpenAI)
    provider = OpenAICompatProvider(
        api_key="x",
        model="gpt-4o",
        base_url="https://api.openai.com/v1",
        provider_name="openai",
        disable_thinking=True,
    )
    await provider.complete("hello")
    user_msg = provider.client.chat.completions.create.calls[0]["messages"][-1]
    assert "/no_think" not in user_msg["content"]


@pytest.mark.asyncio
async def test_no_think_not_duplicated_on_second_call(monkeypatch: pytest.MonkeyPatch) -> None:
    """A user whose prompt already contains `/no_think` (deliberate
    or from a prior pass) shouldn't get a second copy appended."""
    monkeypatch.setattr("workers.common.llm.openai_compat.openai.AsyncOpenAI", _FakeAsyncOpenAI)
    provider = OpenAICompatProvider(
        api_key="x",
        model="qwen3:14b",
        base_url="http://localhost:11434/v1",
        provider_name="ollama",
        disable_thinking=True,
    )
    await provider.complete("what is 2+2?\n\n/no_think")
    user_msg = provider.client.chat.completions.create.calls[0]["messages"][-1]
    assert user_msg["content"].count("/no_think") == 1


@pytest.mark.asyncio
async def test_disable_thinking_false_injects_nothing(monkeypatch: pytest.MonkeyPatch) -> None:
    """Callers that opt out of the disable_thinking flag should see
    the prompt pass through unchanged on Qwen models too."""
    monkeypatch.setattr("workers.common.llm.openai_compat.openai.AsyncOpenAI", _FakeAsyncOpenAI)
    provider = OpenAICompatProvider(
        api_key="x",
        model="qwen3.5:35b-a3b",
        base_url="http://localhost:11434/v1",
        provider_name="ollama",
        disable_thinking=False,
    )
    await provider.complete("hi")
    call = provider.client.chat.completions.create.calls[0]
    assert call["extra_body"] is None
    assert "/no_think" not in call["messages"][-1]["content"]


def test_ollama_placeholder_api_key_is_suppressed(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr("workers.common.llm.openai_compat.openai.AsyncOpenAI", _FakeAsyncOpenAI)
    provider = OpenAICompatProvider(
        api_key="not-needed",
        model="qwen3:14b",
        base_url="http://localhost:11434/v1",
        provider_name="ollama",
    )

    # openai>=2.34 rejects empty api_key at construction; the provider
    # substitutes a sentinel that won't be transmitted to unauthenticated servers.
    assert provider.client.api_key == "local-no-auth-required"


def test_openai_provider_keeps_explicit_api_key(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr("workers.common.llm.openai_compat.openai.AsyncOpenAI", _FakeAsyncOpenAI)
    provider = OpenAICompatProvider(
        api_key="real-key",
        model="gpt-5.4",
        base_url="https://api.openai.com/v1",
        provider_name="openai",
    )

    assert provider.client.api_key == "real-key"


def test_default_timeout_matches_worker_config_ceiling(monkeypatch: pytest.MonkeyPatch) -> None:
    """No explicit timeout → fall back to the 900s default matching WorkerConfig."""
    monkeypatch.setattr("workers.common.llm.openai_compat.openai.AsyncOpenAI", _FakeAsyncOpenAI)
    provider = OpenAICompatProvider(
        api_key="x",
        model="gpt-4o",
        base_url="https://api.openai.com/v1",
        provider_name="openai",
    )

    assert provider.timeout == 900.0
    assert provider.client.timeout == 900.0


def test_explicit_timeout_flows_through_to_async_client(monkeypatch: pytest.MonkeyPatch) -> None:
    """Admin-configured TimeoutSecs must reach the HTTP client."""
    monkeypatch.setattr("workers.common.llm.openai_compat.openai.AsyncOpenAI", _FakeAsyncOpenAI)
    provider = OpenAICompatProvider(
        api_key="x",
        model="gpt-4o",
        base_url="https://api.openai.com/v1",
        provider_name="openai",
        timeout=1500.0,
    )

    assert provider.timeout == 1500.0
    assert provider.client.timeout == 1500.0


def test_zero_or_negative_timeout_falls_back_to_default(monkeypatch: pytest.MonkeyPatch) -> None:
    """Guarding against operator mistakes where TimeoutSecs lands at 0."""
    monkeypatch.setattr("workers.common.llm.openai_compat.openai.AsyncOpenAI", _FakeAsyncOpenAI)
    provider = OpenAICompatProvider(
        api_key="x",
        model="gpt-4o",
        base_url="https://api.openai.com/v1",
        provider_name="openai",
        timeout=0,
    )

    assert provider.timeout == 900.0


# ---------------------------------------------------------------------------
# _strip_think_tags unit tests
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_phase4_strategy_f_injects_into_system_and_user(monkeypatch: pytest.MonkeyPatch) -> None:
    """Phase 4: strategy (f) — /no_think injected into BOTH system message and
    last user message, with 'Begin response directly' prefix in system.
    Validates the plan §D7 matrix winner for Qwen thinking models."""
    monkeypatch.setattr("workers.common.llm.openai_compat.openai.AsyncOpenAI", _FakeAsyncOpenAI)
    provider = OpenAICompatProvider(
        api_key="x",
        model="qwen3.5:9b",
        base_url="http://localhost:11434/v1",
        provider_name="ollama",
        disable_thinking=True,
    )

    await provider.complete("hello", system="Be helpful.")

    create = provider.client.chat.completions.create
    assert create.calls
    messages = create.calls[0]["messages"]

    sys_msg = next((m for m in messages if m["role"] == "system"), None)
    user_msg = next((m for m in messages if m["role"] == "user"), None)

    assert sys_msg is not None, "system message must be present"
    assert "/no_think" in sys_msg["content"], "system message must contain /no_think"
    assert "Begin your response" in sys_msg["content"], (
        "system message must contain the 'Begin response' prefix"
    )
    assert user_msg is not None, "user message must be present"
    assert "/no_think" in user_msg["content"], "last user message must contain /no_think"


@pytest.mark.asyncio
async def test_phase4_strategy_f_prepends_system_when_none(monkeypatch: pytest.MonkeyPatch) -> None:
    """Phase 4: when no system message is provided, _apply_no_think_strategy
    prepends a synthetic system message so the directive reaches the model."""
    monkeypatch.setattr("workers.common.llm.openai_compat.openai.AsyncOpenAI", _FakeAsyncOpenAI)
    provider = OpenAICompatProvider(
        api_key="x",
        model="qwen3.5:9b",
        base_url="http://localhost:11434/v1",
        provider_name="ollama",
        disable_thinking=True,
    )

    # No system= arg → no system message in the input list.
    await provider.complete("hello")

    messages = provider.client.chat.completions.create.calls[0]["messages"]
    roles = [m["role"] for m in messages]
    assert roles[0] == "system", "system message must be prepended when none provided"
    assert "/no_think" in messages[0]["content"]


@pytest.mark.asyncio
async def test_phase4_strategy_f_idempotent_on_retry(monkeypatch: pytest.MonkeyPatch) -> None:
    """Phase 4: calling _apply_no_think_strategy on messages that already have
    the directive does NOT add a second copy (retry path)."""
    from workers.common.llm.openai_compat import _apply_no_think_strategy
    messages = [
        {"role": "system", "content": "Be helpful.\n\n/no_think"},
        {"role": "user", "content": "hello\n\n/no_think"},
    ]
    result = _apply_no_think_strategy(messages, model="qwen3.5:9b", disable_thinking=True)
    sys_content = result[0]["content"]
    user_content = result[-1]["content"]
    assert sys_content.count("/no_think") == 1, "system /no_think must not be duplicated"
    assert user_content.count("/no_think") == 1, "user /no_think must not be duplicated"


def test_strip_think_tags_clean_response_unchanged() -> None:
    """Responses without think blocks must pass through unmodified."""
    clean = "This function manages the retry logic for LLM calls."
    assert _strip_think_tags(clean) == clean


def test_strip_think_tags_single_block_stripped() -> None:
    """A single closed <think>...</think> block is removed; visible text remains."""
    raw = "<think>Let me reason about this.\nOkay, it does X.</think>The function manages retries."
    assert _strip_think_tags(raw) == "The function manages retries."


def test_strip_think_tags_unclosed_trailing_block_stripped() -> None:
    """An unclosed <think> (model truncated mid-thought) is dropped through end-of-string."""
    raw = "Visible answer here.\n<think>I'm reasoning and ran out of"
    assert _strip_think_tags(raw) == "Visible answer here."


def test_strip_think_tags_multiple_blocks_all_stripped() -> None:
    """Multiple closed blocks in one response are all removed."""
    raw = "<think>first thought</think>Answer part one.<think>second thought</think>Answer part two."
    assert _strip_think_tags(raw) == "Answer part one.Answer part two."


def test_strip_think_tags_mixed_case() -> None:
    """Mixed-case tags (<Think>, <THINK>, <Thinking>) are stripped."""
    assert _strip_think_tags("<Think>stuff</Think>result") == "result"
    assert _strip_think_tags("<THINK>stuff</THINK>result") == "result"
    assert _strip_think_tags("<thinking>stuff</thinking>result") == "result"
    assert _strip_think_tags("<Thinking>stuff</Thinking>result") == "result"


# ---------------------------------------------------------------------------
# Auto-retry tests
# ---------------------------------------------------------------------------


class _CallTrackingCreate:
    """Fake completions.create that returns configurable per-call responses."""

    def __init__(self, responses: list) -> None:
        self.calls: list[dict] = []
        self._responses = responses
        self._idx = 0

    async def __call__(self, **kwargs):
        self.calls.append(kwargs)
        resp = self._responses[self._idx]
        self._idx = min(self._idx + 1, len(self._responses) - 1)
        return resp


def _make_response(content: str, finish_reason: str = "stop") -> SimpleNamespace:
    return SimpleNamespace(
        choices=[SimpleNamespace(message=SimpleNamespace(content=content), finish_reason=finish_reason)],
        usage=SimpleNamespace(prompt_tokens=10, completion_tokens=5),
        model_extra={},
    )


@pytest.mark.asyncio
async def test_retry_fires_on_empty_content_with_length_stop(monkeypatch: pytest.MonkeyPatch) -> None:
    """When stop_reason=length and content is empty (all think tokens), retry with doubled max_tokens."""
    think_only = "<think>I am reasoning forever</think>"
    real_answer = "The function initializes the config loader."

    create = _CallTrackingCreate([
        _make_response(think_only, "length"),  # first call: all-think, empty after strip
        _make_response(real_answer, "stop"),   # retry: real content
    ])

    class _FakeClient:
        def __init__(self, *a, **kw) -> None:
            self.api_key = kw.get("api_key", "")
            self.base_url = kw.get("base_url", "")
            self.timeout = kw.get("timeout", 900)
            self.default_headers = kw.get("default_headers", {})
            self.chat = SimpleNamespace(completions=SimpleNamespace(create=create))

    monkeypatch.setattr("workers.common.llm.openai_compat.openai.AsyncOpenAI", _FakeClient)

    provider = OpenAICompatProvider(
        api_key="x",
        model="qwen3.6:27b-q4_K_M",
        base_url="http://localhost:11434/v1",
        provider_name="ollama",
        disable_thinking=True,
    )

    result = await provider.complete("summarize this", max_tokens=384)

    assert result.content == real_answer
    assert len(create.calls) == 2
    # Retry must have doubled the token budget.
    assert create.calls[1]["max_tokens"] == 768


@pytest.mark.asyncio
async def test_no_retry_on_nonempty_content(monkeypatch: pytest.MonkeyPatch) -> None:
    """A non-empty first response must not trigger a retry regardless of stop_reason."""
    create = _CallTrackingCreate([
        _make_response("Good answer right away.", "stop"),
    ])

    class _FakeClient:
        def __init__(self, *a, **kw) -> None:
            self.api_key = kw.get("api_key", "")
            self.base_url = kw.get("base_url", "")
            self.timeout = kw.get("timeout", 900)
            self.default_headers = kw.get("default_headers", {})
            self.chat = SimpleNamespace(completions=SimpleNamespace(create=create))

    monkeypatch.setattr("workers.common.llm.openai_compat.openai.AsyncOpenAI", _FakeClient)

    provider = OpenAICompatProvider(
        api_key="x",
        model="gpt-4o",
        base_url="https://api.openai.com/v1",
        provider_name="openai",
    )

    result = await provider.complete("hello", max_tokens=512)

    assert result.content == "Good answer right away."
    assert len(create.calls) == 1


@pytest.mark.asyncio
async def test_retry_ceiling_respected(monkeypatch: pytest.MonkeyPatch) -> None:
    """Retry max_tokens is capped at _RETRY_MAX_TOKENS_CEILING even when doubling would exceed it."""
    from workers.common.llm.openai_compat import _RETRY_MAX_TOKENS_CEILING

    think_only = "<think>I am reasoning</think>"
    create = _CallTrackingCreate([
        _make_response(think_only, "length"),
        _make_response("answer", "stop"),
    ])

    class _FakeClient:
        def __init__(self, *a, **kw) -> None:
            self.api_key = kw.get("api_key", "")
            self.base_url = kw.get("base_url", "")
            self.timeout = kw.get("timeout", 900)
            self.default_headers = kw.get("default_headers", {})
            self.chat = SimpleNamespace(completions=SimpleNamespace(create=create))

    monkeypatch.setattr("workers.common.llm.openai_compat.openai.AsyncOpenAI", _FakeClient)

    provider = OpenAICompatProvider(api_key="x", model="qwen3.6:27b-q4_K_M", provider_name="ollama")

    # Start with a budget large enough that 2× would exceed the ceiling.
    await provider.complete("hi", max_tokens=_RETRY_MAX_TOKENS_CEILING)

    assert create.calls[1]["max_tokens"] == _RETRY_MAX_TOKENS_CEILING


@pytest.mark.asyncio
async def test_both_attempts_empty_returns_original(monkeypatch: pytest.MonkeyPatch) -> None:
    """If retry also returns empty, the original (first) response is returned so callers get real diagnostics."""
    think_only = "<think>endless thoughts</think>"

    create = _CallTrackingCreate([
        _make_response(think_only, "length"),
        _make_response(think_only, "length"),
    ])

    class _FakeClient:
        def __init__(self, *a, **kw) -> None:
            self.api_key = kw.get("api_key", "")
            self.base_url = kw.get("base_url", "")
            self.timeout = kw.get("timeout", 900)
            self.default_headers = kw.get("default_headers", {})
            self.chat = SimpleNamespace(completions=SimpleNamespace(create=create))

    monkeypatch.setattr("workers.common.llm.openai_compat.openai.AsyncOpenAI", _FakeClient)

    provider = OpenAICompatProvider(api_key="x", model="qwen3.6:27b-q4_K_M", provider_name="ollama")

    result = await provider.complete("hi", max_tokens=256)

    # Both attempts empty — caller gets back the original empty result.
    assert result.content == ""
    assert result.stop_reason == "length"


@pytest.mark.asyncio
async def test_complete_total_attempts_capped_at_three(monkeypatch: pytest.MonkeyPatch) -> None:
    """H3: shared attempt_budget=3 caps total calls regardless of how many times
    stop_reason=length and content is empty. Even if every call returns empty,
    exactly 2 calls are made (initial + 1 retry; the budget counter is shared
    so the second retry is 'attempt 3' = surface_empty, no fourth call)."""
    think_only = "<think>I reason forever</think>"

    # Provide 10 responses to ensure the loop cannot fire indefinitely.
    create = _CallTrackingCreate([_make_response(think_only, "length")] * 10)

    class _FakeClient:
        def __init__(self, *a, **kw) -> None:
            self.api_key = kw.get("api_key", "")
            self.base_url = kw.get("base_url", "")
            self.timeout = kw.get("timeout", 900)
            self.default_headers = kw.get("default_headers", {})
            self.chat = SimpleNamespace(completions=SimpleNamespace(create=create))

    monkeypatch.setattr("workers.common.llm.openai_compat.openai.AsyncOpenAI", _FakeClient)

    provider = OpenAICompatProvider(api_key="x", model="qwen3.6:27b-q4_K_M", provider_name="ollama")

    result = await provider.complete("hi", max_tokens=256)

    # Budget allows: 1 initial call + 1 retry (attempt 2 = max_tokens_double).
    # attempt 3 = surface_empty fires *without* a third network call (returns
    # the original response). So total calls == 2.
    assert len(create.calls) == 2, f"expected 2 calls, got {len(create.calls)}"
    # Surface the original empty result — caller gets real diagnostics.
    assert result.content == ""
    assert result.stop_reason == "length"


@pytest.mark.asyncio
async def test_retry_log_includes_telemetry_fields(monkeypatch: pytest.MonkeyPatch, caplog) -> None:
    """Phase 3: llm_empty_content_retry log must include attempt, strategy,
    original_latency_ms fields for observability."""
    import logging
    import structlog.testing

    think_only = "<think>reasoning</think>"
    real_answer = "The answer is 42."

    create = _CallTrackingCreate([
        _make_response(think_only, "length"),
        _make_response(real_answer, "stop"),
    ])

    class _FakeClient:
        def __init__(self, *a, **kw) -> None:
            self.api_key = kw.get("api_key", "")
            self.base_url = kw.get("base_url", "")
            self.timeout = kw.get("timeout", 900)
            self.default_headers = kw.get("default_headers", {})
            self.chat = SimpleNamespace(completions=SimpleNamespace(create=create))

    monkeypatch.setattr("workers.common.llm.openai_compat.openai.AsyncOpenAI", _FakeClient)

    with structlog.testing.capture_logs() as captured:
        provider = OpenAICompatProvider(api_key="x", model="qwen3.6:27b-q4_K_M", provider_name="ollama")
        result = await provider.complete("hi", max_tokens=512)

    retry_events = [e for e in captured if e.get("event") == "llm_empty_content_retry"]
    assert retry_events, "expected at least one llm_empty_content_retry log event"
    evt = retry_events[0]
    assert "attempt" in evt, "retry log must include attempt field"
    assert "strategy" in evt, "retry log must include strategy field"
    assert "original_latency_ms" in evt, "retry log must include original_latency_ms field"
    assert evt["attempt"] == 2
    assert evt["strategy"] == "max_tokens_double"
    assert result.content == real_answer
    assert len(create.calls) == 2
