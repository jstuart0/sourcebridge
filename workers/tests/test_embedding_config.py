"""Tests for the embedding provider factory's defense-in-depth path.

The WorkerConfig validator (workers/common/config.py) rejects unknown
embedding providers at config-load time. This file pins the *factory*
behavior — create_embedding_provider must also raise a friendly
ValueError when an unknown provider reaches it via a path that bypasses
the validator (notably ``config.model_copy(update={...})``, which does
not re-run validators in pydantic v2 by default).

Tester report 2026-04-30 (Pazaryna) Issue 3 / CA-125.
"""

import pytest

from workers.common.config import SUPPORTED_EMBEDDING_PROVIDERS, WorkerConfig
from workers.common.embedding.config import create_embedding_provider


def test_create_embedding_provider_rejects_unknown_with_actionable_message() -> None:
    cfg = WorkerConfig(embedding_provider="ollama")
    # Bypass the validator the same way per-request overrides do.
    bypassed = cfg.model_copy(update={"embedding_provider": "totally-fake"})
    with pytest.raises(ValueError) as exc_info:
        create_embedding_provider(bypassed)
    msg = str(exc_info.value)
    assert "totally-fake" in msg
    for provider in SUPPORTED_EMBEDDING_PROVIDERS:
        assert repr(provider) in msg, f"supported provider {provider} not surfaced in error: {msg}"


def test_create_embedding_provider_anthropic_gets_specific_hint() -> None:
    """The tester-report footgun: setting embedding_provider=anthropic
    is reasonable on first read of the README. The factory error
    surface must explicitly explain why anthropic isn't here, not just
    "anthropic is not in the supported set"."""
    cfg = WorkerConfig(embedding_provider="ollama")
    bypassed = cfg.model_copy(update={"embedding_provider": "anthropic"})
    with pytest.raises(ValueError) as exc_info:
        create_embedding_provider(bypassed)
    msg = str(exc_info.value)
    assert "Anthropic" in msg
    assert "embeddings API" in msg
    # Suggested alternatives.
    assert "ollama" in msg
    assert "openai" in msg


def test_create_embedding_provider_no_longer_raises_notimplementederror() -> None:
    """Pre-fix the factory raised NotImplementedError. New behavior:
    raises ValueError. Pinning the type is a regression guard so a
    well-meaning future refactor doesn't revert to NotImplementedError
    (which is harder for callers to catch and discriminate from
    "the implementation literally doesn't exist yet")."""
    cfg = WorkerConfig(embedding_provider="ollama")
    bypassed = cfg.model_copy(update={"embedding_provider": "fake"})
    with pytest.raises(ValueError):
        create_embedding_provider(bypassed)
    # And explicitly NOT NotImplementedError.
    try:
        create_embedding_provider(bypassed)
    except NotImplementedError:
        pytest.fail("create_embedding_provider must not raise NotImplementedError")
    except ValueError:
        pass


def test_create_embedding_provider_test_mode_unaffected() -> None:
    """test_mode short-circuits to FakeEmbeddingProvider before the
    provider check. Pinning so a future refactor that moves the
    check earlier doesn't break the test fixture path that thousands
    of unit tests already use."""
    cfg = WorkerConfig(test_mode=True, embedding_provider="ollama")
    provider = create_embedding_provider(cfg)
    # FakeEmbeddingProvider is the contract; assert it's a non-None
    # provider instance at minimum.
    assert provider is not None
