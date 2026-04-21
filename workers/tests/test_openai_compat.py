from __future__ import annotations

from types import SimpleNamespace

import pytest

from workers.common.llm.openai_compat import OpenAICompatProvider


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

    assert provider.client.api_key == ""


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
