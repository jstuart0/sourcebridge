from __future__ import annotations

from types import SimpleNamespace

import pytest

from workers.common.llm.openai_compat import (
    OpenAICompatProvider,
    _ollama_native_base_url,
    _strip_think_tags,
)


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
async def test_complete_llama_cpp_attaches_disable_thinking_override(monkeypatch: pytest.MonkeyPatch) -> None:
    """For non-Ollama providers (e.g. llama.cpp), disable_thinking sets
    chat_template_kwargs on the OpenAI-compat path. The native-endpoint branch
    must NOT fire for a non-Ollama provider."""
    monkeypatch.setattr("workers.common.llm.openai_compat.openai.AsyncOpenAI", _FakeAsyncOpenAI)
    provider = OpenAICompatProvider(
        api_key="x",
        model="qwen3.5:35b-a3b",
        base_url="http://localhost:8080/v1",
        provider_name="llama-cpp",
        disable_thinking=True,
    )

    await provider.complete("hello")

    create = provider.client.chat.completions.create
    assert create.calls
    # llama.cpp path: kwarg toggles the Jinja template variable.
    assert create.calls[0]["extra_body"] == {"chat_template_kwargs": {"enable_thinking": False}}
    assert provider.client.api_key == "x"


@pytest.mark.asyncio
async def test_stream_llama_cpp_attaches_disable_thinking_override(monkeypatch: pytest.MonkeyPatch) -> None:
    """For non-Ollama providers (e.g. llama.cpp), stream() sets
    chat_template_kwargs on the OpenAI-compat path."""
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
        base_url="http://localhost:8080/v1",
        provider_name="llama-cpp",
        disable_thinking=True,
    )

    chunks = []
    async for chunk in provider.stream("hello"):
        chunks.append(chunk)

    assert chunks == ["chunk"]
    create = provider.client.chat.completions.create
    assert create.calls
    assert create.calls[0]["extra_body"] == {"chat_template_kwargs": {"enable_thinking": False}}
    assert provider.client.api_key == "x"


@pytest.mark.asyncio
async def test_non_ollama_provider_prompt_unchanged(monkeypatch: pytest.MonkeyPatch) -> None:
    """Non-Ollama providers with disable_thinking=True use the OpenAI-compat path.
    No /no_think directive is injected into the user message content."""
    monkeypatch.setattr("workers.common.llm.openai_compat.openai.AsyncOpenAI", _FakeAsyncOpenAI)
    provider = OpenAICompatProvider(
        api_key="x",
        model="gpt-4o",
        base_url="https://api.openai.com/v1",
        provider_name="openai",
        disable_thinking=True,
    )
    await provider.complete("hello")
    call = provider.client.chat.completions.create.calls[0]
    user_msg = call["messages"][-1]
    # OpenAI-compat path: no /no_think injection (that was Ollama-specific).
    assert "/no_think" not in user_msg["content"]
    # chat_template_kwargs is set for llama.cpp-compatible servers.
    assert call["extra_body"] == {"chat_template_kwargs": {"enable_thinking": False}}


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
# _ollama_native_base_url unit tests
# ---------------------------------------------------------------------------


def test_ollama_native_base_url_strips_v1_suffix() -> None:
    assert _ollama_native_base_url("http://host:11434/v1") == "http://host:11434"
    assert _ollama_native_base_url("http://host:11434/v1/") == "http://host:11434"
    assert _ollama_native_base_url("http://host:11434") == "http://host:11434"


# ---------------------------------------------------------------------------
# Ollama native-endpoint dispatch tests (Phase 4 replacement)
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_ollama_native_dispatch_when_thinking_disabled(monkeypatch: pytest.MonkeyPatch) -> None:
    """provider=ollama + disable_thinking=True → native /api/chat endpoint,
    body includes think: false and options.num_predict (not max_tokens)."""
    captured: list[dict] = []

    class _FakeResponse:
        status_code = 200

        def raise_for_status(self) -> None:
            pass

        def json(self) -> dict:
            return {
                "message": {"content": "visible output"},
                "done_reason": "stop",
                "eval_count": 7,
                "prompt_eval_count": 12,
            }

    class _FakeHTTPXClient:
        def __init__(self, **kwargs):
            pass

        async def __aenter__(self):
            return self

        async def __aexit__(self, *_):
            pass

        async def post(self, url: str, *, json: dict, **_kw) -> _FakeResponse:  # noqa: A002
            captured.append({"url": url, "body": json})
            return _FakeResponse()

    monkeypatch.setattr("workers.common.llm.openai_compat.openai.AsyncOpenAI", _FakeAsyncOpenAI)
    monkeypatch.setattr("workers.common.llm.openai_compat.httpx.AsyncClient", _FakeHTTPXClient)

    provider = OpenAICompatProvider(
        api_key="x",
        model="qwen3.5:9b",
        base_url="http://localhost:11434/v1",
        provider_name="ollama",
        disable_thinking=True,
    )

    result = await provider.complete("hello", system="Be helpful.", max_tokens=1024)

    assert len(captured) == 1
    req = captured[0]
    assert req["url"] == "http://localhost:11434/api/chat"
    body = req["body"]
    assert body["think"] is False
    assert body["stream"] is False
    assert body["options"]["num_predict"] == 1024
    # OpenAI-compat client must NOT have been called.
    assert not provider.client.chat.completions.create.calls
    assert result.content == "visible output"


@pytest.mark.asyncio
async def test_ollama_native_response_normalization(monkeypatch: pytest.MonkeyPatch) -> None:
    """Native response shape (message.content, eval_count, done_reason) maps to
    the same LLMResponse fields as the OpenAI-compat path."""
    class _FakeResponse:
        status_code = 200

        def raise_for_status(self) -> None:
            pass

        def json(self) -> dict:
            return {
                "message": {"content": "The answer is 42."},
                "done_reason": "stop",
                "eval_count": 20,
                "prompt_eval_count": 30,
            }

    class _FakeHTTPXClient:
        def __init__(self, **kwargs):
            pass

        async def __aenter__(self):
            return self

        async def __aexit__(self, *_):
            pass

        async def post(self, url: str, *, json: dict, **_kw) -> _FakeResponse:  # noqa: A002
            return _FakeResponse()

    monkeypatch.setattr("workers.common.llm.openai_compat.openai.AsyncOpenAI", _FakeAsyncOpenAI)
    monkeypatch.setattr("workers.common.llm.openai_compat.httpx.AsyncClient", _FakeHTTPXClient)

    provider = OpenAICompatProvider(
        api_key="x",
        model="qwen3.5:9b",
        base_url="http://localhost:11434/v1",
        provider_name="ollama",
        disable_thinking=True,
    )

    result = await provider.complete("what is 6x7?")

    assert result.content == "The answer is 42."
    assert result.output_tokens == 20
    assert result.input_tokens == 30
    assert result.stop_reason == "stop"
    assert result.provider_name == "ollama"


@pytest.mark.asyncio
async def test_ollama_openai_compat_path_when_thinking_enabled(monkeypatch: pytest.MonkeyPatch) -> None:
    """provider=ollama + disable_thinking=False → OpenAI-compat path, no native call."""
    monkeypatch.setattr("workers.common.llm.openai_compat.openai.AsyncOpenAI", _FakeAsyncOpenAI)
    provider = OpenAICompatProvider(
        api_key="x",
        model="qwen3.5:9b",
        base_url="http://localhost:11434/v1",
        provider_name="ollama",
        disable_thinking=False,
    )

    await provider.complete("hello")

    call = provider.client.chat.completions.create.calls[0]
    # No thinking suppression kwargs since disable_thinking=False.
    assert call["extra_body"] is None
    # Prompt must be clean — no /no_think injected.
    assert "/no_think" not in call["messages"][-1]["content"]


@pytest.mark.asyncio
async def test_non_ollama_provider_uses_openai_compat_even_with_disable_thinking(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """provider=openai + disable_thinking=True → OpenAI-compat path (not native).
    The native branch is scoped exclusively to provider_name == 'ollama'."""
    monkeypatch.setattr("workers.common.llm.openai_compat.openai.AsyncOpenAI", _FakeAsyncOpenAI)
    provider = OpenAICompatProvider(
        api_key="real-key",
        model="gpt-4o",
        base_url="https://api.openai.com/v1",
        provider_name="openai",
        disable_thinking=True,
    )

    await provider.complete("hello")

    # OpenAI-compat client must have been called, not the native Ollama path.
    assert provider.client.chat.completions.create.calls
    call = provider.client.chat.completions.create.calls[0]
    # chat_template_kwargs is set for llama.cpp-compatible servers (openai path).
    assert call["extra_body"] == {"chat_template_kwargs": {"enable_thinking": False}}


# ---------------------------------------------------------------------------
# _strip_think_tags unit tests
# ---------------------------------------------------------------------------


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
    """When stop_reason=length and content is empty (all think tokens), retry with doubled max_tokens.
    Uses llama-cpp provider (OpenAI-compat path) — Ollama with disable_thinking now takes
    the native path and the empty-content failure mode is eliminated there."""
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
        base_url="http://localhost:8080/v1",
        provider_name="llama-cpp",
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
