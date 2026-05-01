"""Tests for worker configuration."""

import os

import pytest
from pydantic import ValidationError

from workers.common.config import (
    SUPPORTED_EMBEDDING_PROVIDERS,
    SUPPORTED_LLM_PROVIDERS,
    WorkerConfig,
)


def test_default_config(monkeypatch: object) -> None:
    """Test default configuration values."""
    assert hasattr(monkeypatch, "delenv")
    monkeypatch.delenv("SOURCEBRIDGE_WORKER_TEST_MODE", raising=False)  # type: ignore[attr-defined]
    monkeypatch.delenv("SOURCEBRIDGE_TEST_MODE", raising=False)  # type: ignore[attr-defined]
    config = WorkerConfig()
    assert config.grpc_port == 50051
    assert config.max_workers == 10
    assert config.llm_provider == "anthropic"
    assert config.embedding_dimension == 768
    assert config.test_mode is False


def test_env_override(monkeypatch: object) -> None:
    """Test configuration from environment variables."""
    os.environ["SOURCEBRIDGE_WORKER_GRPC_PORT"] = "50052"
    os.environ["SOURCEBRIDGE_WORKER_TEST_MODE"] = "true"
    try:
        config = WorkerConfig()
        assert config.grpc_port == 50052
        assert config.test_mode is True
    finally:
        del os.environ["SOURCEBRIDGE_WORKER_GRPC_PORT"]
        del os.environ["SOURCEBRIDGE_WORKER_TEST_MODE"]


def test_llm_provider_types() -> None:
    """Test that valid LLM providers are recognized."""
    for provider in ["anthropic", "openai", "ollama", "vllm"]:
        config = WorkerConfig(llm_provider=provider)
        assert config.llm_provider == provider


def test_global_test_mode_fallback() -> None:
    """Test the repo-wide SOURCEBRIDGE_TEST_MODE fallback."""
    os.environ["SOURCEBRIDGE_TEST_MODE"] = "true"
    try:
        config = WorkerConfig()
        assert config.test_mode is True
    finally:
        del os.environ["SOURCEBRIDGE_TEST_MODE"]


# ─── Provider validators (CA-125, tester report 2026-04-30) ──────────


@pytest.mark.parametrize("provider", sorted(SUPPORTED_LLM_PROVIDERS))
def test_llm_provider_validator_accepts_supported(provider: str) -> None:
    """Every value in SUPPORTED_LLM_PROVIDERS must be accepted by the
    validator. Pinning the full set here means a future edit that drops
    a provider from the supported list (without also removing it from
    the factory dispatch) breaks this test."""
    config = WorkerConfig(llm_provider=provider)
    assert config.llm_provider == provider


@pytest.mark.parametrize("provider", sorted(SUPPORTED_EMBEDDING_PROVIDERS))
def test_embedding_provider_validator_accepts_supported(provider: str) -> None:
    config = WorkerConfig(embedding_provider=provider)
    assert config.embedding_provider == provider


def test_llm_provider_validator_rejects_unknown_with_supported_set() -> None:
    """Unknown LLM provider → ValidationError naming the supported
    providers in the message. Tester report Issue R2."""
    with pytest.raises(ValidationError) as exc_info:
        WorkerConfig(llm_provider="bogus-provider")
    msg = str(exc_info.value)
    assert "bogus-provider" in msg
    # Every supported provider must appear in the message — the user
    # should be able to copy-paste from the error.
    for provider in SUPPORTED_LLM_PROVIDERS:
        assert repr(provider) in msg, f"supported provider {provider} not surfaced in error: {msg}"


def test_embedding_provider_validator_rejects_unknown() -> None:
    """Unknown embedding provider → ValidationError naming the supported
    providers. Tester report Issue 3."""
    with pytest.raises(ValidationError) as exc_info:
        WorkerConfig(embedding_provider="bogus-provider")
    msg = str(exc_info.value)
    assert "bogus-provider" in msg
    for provider in SUPPORTED_EMBEDDING_PROVIDERS:
        assert repr(provider) in msg, f"supported provider {provider} not surfaced in error: {msg}"


def test_embedding_provider_validator_anthropic_gets_specific_hint() -> None:
    """Setting embedding_provider=anthropic is the specific footgun the
    tester hit (the README's LLM-providers table lists anthropic; it's
    reasonable to assume embedding parity). The error must call out
    that Anthropic doesn't offer an embeddings API."""
    with pytest.raises(ValidationError) as exc_info:
        WorkerConfig(embedding_provider="anthropic")
    msg = str(exc_info.value)
    assert "Anthropic" in msg
    assert "embeddings API" in msg
    # Suggested alternatives should be in the message so the user knows
    # exactly what to switch to.
    assert "ollama" in msg
    assert "openai" in msg


def test_llm_report_provider_validator_accepts_empty() -> None:
    """Empty string for llm_report_provider is valid — it means "fall
    back to llm_provider for reports too"."""
    config = WorkerConfig(llm_report_provider="")
    assert config.llm_report_provider == ""


def test_llm_report_provider_validator_rejects_unknown() -> None:
    with pytest.raises(ValidationError):
        WorkerConfig(llm_report_provider="bogus-provider")


def test_llm_provider_validator_runs_on_env_var() -> None:
    """SOURCEBRIDGE_WORKER_LLM_PROVIDER=<bad> must fail at WorkerConfig()
    construction — that's the tester's actual code path (a user setting
    the env var and starting the worker)."""
    os.environ["SOURCEBRIDGE_WORKER_LLM_PROVIDER"] = "definitely-not-a-provider"
    try:
        with pytest.raises(ValidationError):
            WorkerConfig()
    finally:
        del os.environ["SOURCEBRIDGE_WORKER_LLM_PROVIDER"]


def test_embedding_provider_validator_runs_on_env_var() -> None:
    os.environ["SOURCEBRIDGE_WORKER_EMBEDDING_PROVIDER"] = "anthropic"
    try:
        with pytest.raises(ValidationError) as exc_info:
            WorkerConfig()
        # Even via env var, the anthropic-specific guidance must surface.
        assert "Anthropic" in str(exc_info.value)
    finally:
        del os.environ["SOURCEBRIDGE_WORKER_EMBEDDING_PROVIDER"]
