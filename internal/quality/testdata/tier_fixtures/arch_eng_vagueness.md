# Architecture: Vagueness Fixture

## Overview

The caching subsystem stores computed results to reduce latency (internal/cache/cache.go:1-60).
The implementation uses a least-recently-used eviction policy (internal/cache/lru.go:1-40)
backed by a thread-safe concurrent map structure (internal/cache/map.go:1-50).
Cache entries are keyed by a deterministic hash of the input parameters.

## Design

Various components in the pipeline depend on the cache layer to avoid
redundant computation. The eviction algorithm removes entries when capacity
is reached, prioritising items with older access timestamps.

In some cases the cache may be bypassed when the caller supplies a no-cache
directive header. This bypass path does not log the skip by default.

Many of the configuration parameters are optional and fall back to compiled-in
defaults when absent from the runtime environment. Operators can override the
capacity limit, the TTL, and the eviction batch size independently.

## Consistency

The cache layer provides no cross-replica consistency guarantees. Each node
maintains an independent in-process store. Callers that require consistency
across nodes must coordinate through the persistence layer (internal/db/store.go:1-30)
rather than the local cache. This design keeps the cache implementation simple
and its failure domain isolated from the distributed coordination logic.
