"""Tests for summary caching and circuit breaker."""

import time

from workers.reasoning.cache import CircuitBreaker, SummaryCache, UsageTracker
from workers.reasoning.types import LLMUsageRecord, ReviewResult, Summary


def test_cache_put_get_summary():
    """Cached summary is retrievable."""
    cache = SummaryCache()
    summary = Summary(purpose="test", content_hash="abc123", level="function", entity_name="test")
    cache.put_summary(summary, "model-v1")

    result = cache.get_summary("abc123", "model-v1")
    assert result is not None
    assert result.purpose == "test"


def test_cache_miss_different_hash():
    """Different content hash is a cache miss."""
    cache = SummaryCache()
    summary = Summary(purpose="test", content_hash="abc123", level="function", entity_name="test")
    cache.put_summary(summary, "model-v1")

    result = cache.get_summary("different-hash", "model-v1")
    assert result is None


def test_cache_miss_different_model():
    """Different model version is a cache miss."""
    cache = SummaryCache()
    summary = Summary(purpose="test", content_hash="abc123", level="function", entity_name="test")
    cache.put_summary(summary, "model-v1")

    result = cache.get_summary("abc123", "model-v2")
    assert result is None


def test_cache_invalidation():
    """Invalidation removes entries for a content hash."""
    cache = SummaryCache()
    summary = Summary(purpose="test", content_hash="abc123", level="function", entity_name="test")
    cache.put_summary(summary, "model-v1")

    removed = cache.invalidate("abc123")
    assert removed == 1
    assert cache.get_summary("abc123", "model-v1") is None


def test_cache_lru_eviction():
    """LRU eviction removes oldest entries when max size exceeded."""
    cache = SummaryCache(max_size=2)
    for i in range(3):
        s = Summary(purpose=f"test-{i}", content_hash=f"hash-{i}", level="function", entity_name=f"fn-{i}")
        cache.put_summary(s, "model-v1")

    # First entry should be evicted
    assert cache.get_summary("hash-0", "model-v1") is None
    assert cache.get_summary("hash-1", "model-v1") is not None
    assert cache.get_summary("hash-2", "model-v1") is not None


def test_cache_review_with_ttl():
    """Review cache entries have TTL."""
    cache = SummaryCache()
    review = ReviewResult(template="security", findings=[], score=8.0)
    cache.put_review("hash-1", review, "model-v1")

    result = cache.get_review("hash-1", "model-v1", "security")
    assert result is not None
    assert result.score == 8.0


def test_cache_size():
    """Cache size reflects stored entries."""
    cache = SummaryCache()
    assert cache.size == 0
    s = Summary(purpose="t", content_hash="h1", level="function", entity_name="f")
    cache.put_summary(s, "v1")
    assert cache.size == 1


def test_usage_tracker():
    """Usage tracker records and retrieves events."""
    tracker = UsageTracker()
    tracker.record(
        LLMUsageRecord(provider="anthropic", model="claude-3", input_tokens=100, output_tokens=50, operation="summary")
    )
    tracker.record(
        LLMUsageRecord(provider="openai", model="gpt-4", input_tokens=200, output_tokens=100, operation="review")
    )

    records = tracker.records
    assert len(records) == 2
    assert records[0].provider == "anthropic"
    assert records[1].model == "gpt-4"


def test_usage_tracker_clear():
    """Usage tracker can be cleared."""
    tracker = UsageTracker()
    tracker.record(LLMUsageRecord(provider="test", model="test", input_tokens=0, output_tokens=0, operation="test"))
    tracker.clear()
    assert len(tracker.records) == 0


def test_circuit_breaker_closed_by_default():
    """Circuit breaker starts closed."""
    cb = CircuitBreaker()
    assert not cb.is_open


def test_circuit_breaker_opens_after_threshold():
    """Circuit opens after threshold failures."""
    cb = CircuitBreaker(failure_threshold=3)
    cb.record_failure()
    cb.record_failure()
    assert not cb.is_open
    cb.record_failure()
    assert cb.is_open


def test_circuit_breaker_resets_on_success():
    """Circuit resets after a success."""
    cb = CircuitBreaker(failure_threshold=2)
    cb.record_failure()
    cb.record_failure()
    assert cb.is_open
    cb.record_success()
    assert not cb.is_open


def test_circuit_breaker_resets_after_timeout():
    """Circuit resets after timeout period."""
    cb = CircuitBreaker(failure_threshold=1, reset_timeout=0.01)
    cb.record_failure()
    assert cb.is_open
    time.sleep(0.02)
    assert not cb.is_open
