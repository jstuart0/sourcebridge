"""Summary caching with content-hash keying and LRU eviction."""

from __future__ import annotations

import time
from collections import OrderedDict
from dataclasses import dataclass, field
from threading import Lock

from workers.reasoning.types import LLMUsageRecord, ReviewResult, Summary


@dataclass
class CacheEntry:
    """A cached item with metadata."""

    value: object
    content_hash: str
    model_version: str
    created_at: float = field(default_factory=time.time)
    ttl: float = 0  # 0 = no expiry


class SummaryCache:
    """In-memory LRU cache for summaries and reviews.

    - Summaries: keyed by content_hash + model_version, no time expiry (invalidated by content change)
    - Reviews: keyed by content_hash + model_version + template, 7-day TTL
    - Discussion answers: NOT cached
    """

    REVIEW_TTL = 7 * 24 * 3600  # 7 days

    def __init__(self, max_size: int = 1000) -> None:
        self._max_size = max_size
        self._store: OrderedDict[str, CacheEntry] = OrderedDict()
        self._lock = Lock()

    def _make_key(self, content_hash: str, model_version: str, extra: str = "") -> str:
        parts = [content_hash, model_version]
        if extra:
            parts.append(extra)
        return ":".join(parts)

    def get_summary(self, content_hash: str, model_version: str) -> Summary | None:
        """Get a cached summary, or None if not cached or expired."""
        key = self._make_key(content_hash, model_version, "summary")
        result = self._get(key)
        return result if isinstance(result, Summary) else None

    def put_summary(self, summary: Summary, model_version: str) -> None:
        """Cache a summary (no time expiry — invalidated by content hash change)."""
        key = self._make_key(summary.content_hash, model_version, "summary")
        self._put(
            key,
            CacheEntry(
                value=summary,
                content_hash=summary.content_hash,
                model_version=model_version,
                ttl=0,
            ),
        )

    def get_review(self, content_hash: str, model_version: str, template: str) -> ReviewResult | None:
        """Get a cached review, or None if not cached or expired."""
        key = self._make_key(content_hash, model_version, f"review:{template}")
        result = self._get(key)
        return result if isinstance(result, ReviewResult) else None

    def put_review(self, content_hash: str, result: ReviewResult, model_version: str) -> None:
        """Cache a review result with 7-day TTL."""
        key = self._make_key(content_hash, model_version, f"review:{result.template}")
        self._put(
            key,
            CacheEntry(
                value=result,
                content_hash=content_hash,
                model_version=model_version,
                ttl=self.REVIEW_TTL,
            ),
        )

    def invalidate(self, content_hash: str) -> int:
        """Remove all entries for a given content hash. Returns count removed."""
        with self._lock:
            to_remove = [k for k, v in self._store.items() if v.content_hash == content_hash]
            for k in to_remove:
                del self._store[k]
            return len(to_remove)

    @property
    def size(self) -> int:
        """Current number of cache entries."""
        with self._lock:
            return len(self._store)

    def _get(self, key: str) -> object | None:
        with self._lock:
            entry = self._store.get(key)
            if entry is None:
                return None
            # Check TTL
            if entry.ttl > 0 and (time.time() - entry.created_at) > entry.ttl:
                del self._store[key]
                return None
            # Move to end (most recently used)
            self._store.move_to_end(key)
            return entry.value

    def _put(self, key: str, entry: CacheEntry) -> None:
        with self._lock:
            if key in self._store:
                del self._store[key]
            self._store[key] = entry
            # Evict oldest if over capacity
            while len(self._store) > self._max_size:
                self._store.popitem(last=False)


class UsageTracker:
    """Tracks LLM usage records."""

    def __init__(self) -> None:
        self._records: list[LLMUsageRecord] = []
        self._lock = Lock()

    def record(self, usage: LLMUsageRecord) -> None:
        """Record an LLM usage event."""
        with self._lock:
            self._records.append(usage)

    @property
    def records(self) -> list[LLMUsageRecord]:
        """Return all recorded usage events."""
        with self._lock:
            return list(self._records)

    def clear(self) -> None:
        """Clear all records."""
        with self._lock:
            self._records.clear()


class CircuitBreaker:
    """Simple circuit breaker for LLM provider failure detection."""

    def __init__(self, failure_threshold: int = 3, reset_timeout: float = 60.0) -> None:
        self._failure_threshold = failure_threshold
        self._reset_timeout = reset_timeout
        self._failures = 0
        self._last_failure_time = 0.0
        self._open = False
        self._lock = Lock()

    @property
    def is_open(self) -> bool:
        """Check if the circuit is open (provider considered unavailable)."""
        with self._lock:
            if self._open:
                # Check if reset timeout has elapsed
                if time.time() - self._last_failure_time > self._reset_timeout:
                    self._open = False
                    self._failures = 0
                    return False
                return True
            return False

    def record_success(self) -> None:
        """Record a successful call."""
        with self._lock:
            self._failures = 0
            self._open = False

    def record_failure(self) -> None:
        """Record a failed call."""
        with self._lock:
            self._failures += 1
            self._last_failure_time = time.time()
            if self._failures >= self._failure_threshold:
                self._open = True
