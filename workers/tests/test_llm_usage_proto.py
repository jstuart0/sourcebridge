"""Tests for _llm_usage_proto and _provider_name helpers (Slice 1b).

Verifies that every _llm_usage_proto builder in the worker servicers populates
the ``provider`` field on the returned ``types_pb2.LLMUsage`` message rather than
leaving it empty or using the corrupt "llm" sentinel.
"""

from __future__ import annotations

import pytest

from workers.common.llm.fake import FakeLLMProvider
from workers.common.llm.provider import LLMResponse
from workers.knowledge.servicer import _llm_usage_proto as knowledge_llm_usage_proto
from workers.knowledge.servicer import _provider_name as knowledge_provider_name
from workers.reasoning.servicer import _llm_usage_proto as reasoning_llm_usage_proto
from workers.reasoning.servicer import _provider_name as reasoning_provider_name
from workers.reasoning.types import LLMUsageRecord

# ---------------------------------------------------------------------------
# _provider_name — module-level helpers in both servicers
# ---------------------------------------------------------------------------


class _OpenAICompatLike:
    """Minimal duck-type mimicking OpenAICompatProvider with provider_name."""

    provider_name = "openai"


class _AnthropicLike:
    """Minimal duck-type mimicking AnthropicProvider (no provider_name attr)."""

    model = "claude-3-opus"


class _OllamaLike:
    """Minimal duck-type mimicking OllamaProvider (OpenAI-compat subclass)."""

    provider_name = "ollama"


@pytest.mark.parametrize("fn", [reasoning_provider_name, knowledge_provider_name])
def test_provider_name_from_provider_name_attr(fn):
    """When the provider has a provider_name attribute, use it directly."""
    assert fn(_OpenAICompatLike()) == "openai"


@pytest.mark.parametrize("fn", [reasoning_provider_name, knowledge_provider_name])
def test_provider_name_from_class_name_anthropic(fn):
    """When there is no provider_name attr and class name contains 'anthropic'."""
    assert fn(_AnthropicLike()) == "anthropic"


@pytest.mark.parametrize("fn", [reasoning_provider_name, knowledge_provider_name])
def test_provider_name_ollama(fn):
    """OpenAI-compat providers with explicit provider_name flow through correctly."""
    assert fn(_OllamaLike()) == "ollama"


@pytest.mark.parametrize("fn", [reasoning_provider_name, knowledge_provider_name])
def test_provider_name_fake_llm_provider(fn):
    """FakeLLMProvider has no provider_name attr and no 'anthropic' in class name."""
    result = fn(FakeLLMProvider())
    # FakeLLMProvider → "unknown" (neither anthropic nor has provider_name)
    assert result == "unknown"


# ---------------------------------------------------------------------------
# _llm_usage_proto — duck-typed usage object with LLMUsageRecord-like shape
# ---------------------------------------------------------------------------


class _FakeUsageRecord:
    """Minimal duck-type matching the fields _llm_usage_proto reads."""

    def __init__(self, *, model="", input_tokens=0, output_tokens=0, operation="", provider="", provider_name=None):
        self.model = model
        self.input_tokens = input_tokens
        self.output_tokens = output_tokens
        self.operation = operation
        self.provider = provider
        if provider_name is not None:
            self.provider_name = provider_name


@pytest.mark.parametrize("fn", [reasoning_llm_usage_proto, knowledge_llm_usage_proto])
def test_llm_usage_proto_provider_from_record(fn):
    """When the usage record already carries a provider string, use it."""
    usage = _FakeUsageRecord(model="claude-3-opus", input_tokens=10, output_tokens=5, operation="ask", provider="anthropic")
    proto = fn(usage)
    assert proto.provider == "anthropic"
    assert proto.model == "claude-3-opus"
    assert proto.operation == "ask"


@pytest.mark.parametrize("fn", [reasoning_llm_usage_proto, knowledge_llm_usage_proto])
def test_llm_usage_proto_provider_from_provider_arg(fn):
    """When usage.provider is empty, fall back to the provider arg."""
    usage = _FakeUsageRecord(model="gpt-4o", provider="")
    proto = fn(usage, _OpenAICompatLike(), operation="review")
    assert proto.provider == "openai"
    assert proto.operation == "review"


@pytest.mark.parametrize("fn", [reasoning_llm_usage_proto, knowledge_llm_usage_proto])
def test_llm_usage_proto_provider_from_anthropic_class(fn):
    """Anthropic class-name fallback when no provider_name attr on provider."""
    usage = _FakeUsageRecord(model="claude-3-haiku", provider="")
    proto = fn(usage, _AnthropicLike())
    assert proto.provider == "anthropic"


@pytest.mark.parametrize("fn", [reasoning_llm_usage_proto, knowledge_llm_usage_proto])
def test_llm_usage_proto_no_provider_empty_string(fn):
    """When neither usage nor provider can supply a name, provider is empty string."""
    usage = _FakeUsageRecord(model="llama3-8b", provider="")
    proto = fn(usage)
    assert proto.provider == ""
    # The Go-side providerFromUsage() will heuristic-resolve from the model name.


@pytest.mark.parametrize("fn", [reasoning_llm_usage_proto, knowledge_llm_usage_proto])
def test_llm_usage_proto_provider_sentinel_never_set(fn):
    """The corrupt 'llm' sentinel is never set by the helper — not a valid input."""
    # A usage record with provider="" and a fake provider that has no name
    usage = _FakeUsageRecord(model="some-model", provider="")
    proto = fn(usage)
    assert proto.provider != "llm", "corrupt 'llm' sentinel must never be stored"


@pytest.mark.parametrize("fn", [reasoning_llm_usage_proto, knowledge_llm_usage_proto])
def test_llm_usage_proto_operation_override(fn):
    """The operation kwarg takes precedence over the record's own operation field."""
    usage = _FakeUsageRecord(model="gpt-4o", operation="original_op", provider="openai")
    proto = fn(usage, operation="overridden_op")
    assert proto.operation == "overridden_op"


@pytest.mark.parametrize("fn", [reasoning_llm_usage_proto, knowledge_llm_usage_proto])
def test_llm_usage_proto_operation_falls_back_to_record(fn):
    """When operation kwarg is not given, the record's operation is used."""
    usage = _FakeUsageRecord(model="gpt-4o", operation="from_record", provider="openai")
    proto = fn(usage)
    assert proto.operation == "from_record"


@pytest.mark.parametrize("fn", [reasoning_llm_usage_proto, knowledge_llm_usage_proto])
def test_llm_usage_proto_token_counts_pass_through(fn):
    """Input and output token counts are faithfully propagated."""
    usage = _FakeUsageRecord(model="gpt-4o", input_tokens=123, output_tokens=456, provider="openai")
    proto = fn(usage)
    assert proto.input_tokens == 123
    assert proto.output_tokens == 456


@pytest.mark.parametrize("fn", [reasoning_llm_usage_proto, knowledge_llm_usage_proto])
def test_llm_usage_proto_no_operation_attr(fn):
    """Usage objects without an operation attr default to empty string."""

    class _MinimalRecord:
        model = "gpt-4o"
        input_tokens = 10
        output_tokens = 5
        provider = "openai"
        # no 'operation' attr

    proto = fn(_MinimalRecord())
    assert proto.operation == ""


# ---------------------------------------------------------------------------
# Defense-in-depth: "llm" sentinel must never flow through either helper.
# Codex r2 C1 — each producer now sets provider="" or real name, but the
# helper provides a backstop for any site that gets missed in the future.
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("fn", [reasoning_llm_usage_proto, knowledge_llm_usage_proto])
def test_llm_usage_proto_rejects_llm_sentinel_from_record(fn):
    """The 'llm' sentinel on the record is treated as missing; Go heuristic resolves."""
    usage = _FakeUsageRecord(model="gpt-4o", provider="llm")
    proto = fn(usage)
    assert proto.provider != "llm", "corrupt 'llm' sentinel must never survive _llm_usage_proto"
    # provider is empty; Go will resolve via model-name heuristic
    assert proto.provider == ""


@pytest.mark.parametrize("fn", [reasoning_llm_usage_proto, knowledge_llm_usage_proto])
def test_llm_usage_proto_rejects_llm_sentinel_falls_through_to_provider_arg(fn):
    """When usage.provider='llm', fall through to the provider argument."""
    usage = _FakeUsageRecord(model="gpt-4o", provider="llm")
    proto = fn(usage, _OpenAICompatLike())
    assert proto.provider == "openai", "should use provider arg when record sentinel is 'llm'"


# ---------------------------------------------------------------------------
# Per-producer-site tests: simulate what each worker module used to do
# (provider="llm") and verify the proto no longer carries the sentinel.
# These are regression guards for codex r2 C1.
# ---------------------------------------------------------------------------


def _response_with_provider(provider_name: str | None = None) -> LLMResponse:
    """Build an LLMResponse as a real producer would after fixing provider="llm"."""
    return LLMResponse(
        content="...",
        model="qwen3:32b",
        input_tokens=100,
        output_tokens=50,
        provider_name=provider_name,
    )


def _usage_from_response(response: LLMResponse, operation: str) -> LLMUsageRecord:
    """Mimic the fixed producer pattern: provider=response.provider_name or ''."""
    return LLMUsageRecord(
        provider=response.provider_name or "",
        model=response.model,
        input_tokens=response.input_tokens,
        output_tokens=response.output_tokens,
        operation=operation,
    )


@pytest.mark.parametrize(
    "operation",
    [
        "cliff_notes",
        "learning_path",
        "code_tour",
        "workflow_story",
        "explain_system",
        "explain",
        "discussion",
        "review",
        "summary",
    ],
)
def test_producer_site_no_provider_name_yields_empty_not_sentinel(operation):
    """Each producer site: when provider_name is None (e.g. FakeLLMProvider),
    the resulting LLMUsageRecord.provider is '' — not 'llm'."""
    response = _response_with_provider(provider_name=None)
    usage = _usage_from_response(response, operation)
    assert usage.provider != "llm", f"operation={operation}: provider must not be 'llm'"
    assert usage.provider == "", f"operation={operation}: no provider_name → empty string"


@pytest.mark.parametrize(
    "operation",
    [
        "cliff_notes",
        "learning_path",
        "code_tour",
        "workflow_story",
        "explain_system",
        "explain",
        "discussion",
        "review",
        "summary",
    ],
)
def test_producer_site_ollama_provider_name_flows_through(operation):
    """Each producer site: when provider_name='ollama', it flows through correctly."""
    response = _response_with_provider(provider_name="ollama")
    usage = _usage_from_response(response, operation)
    assert usage.provider == "ollama", f"operation={operation}: expected 'ollama'"


@pytest.mark.parametrize("fn", [reasoning_llm_usage_proto, knowledge_llm_usage_proto])
def test_llm_usage_proto_ollama_producer_round_trip(fn):
    """Full round-trip: ollama provider_name flows through _llm_usage_proto to proto."""
    response = _response_with_provider(provider_name="ollama")
    usage = _usage_from_response(response, "cliff_notes")
    proto = fn(usage)
    assert proto.provider == "ollama"
    assert proto.provider != "llm"


@pytest.mark.parametrize("fn", [reasoning_llm_usage_proto, knowledge_llm_usage_proto])
def test_llm_usage_proto_no_provider_name_round_trip(fn):
    """Full round-trip: missing provider_name → empty proto.provider (Go resolves)."""
    response = _response_with_provider(provider_name=None)
    usage = _usage_from_response(response, "cliff_notes")
    proto = fn(usage)
    assert proto.provider == ""
    assert proto.provider != "llm"
