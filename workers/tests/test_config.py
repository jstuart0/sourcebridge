"""Tests for worker configuration."""

import os

from workers.common.config import WorkerConfig


def test_default_config() -> None:
    """Test default configuration values."""
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
