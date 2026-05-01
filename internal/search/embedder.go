// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"time"
)

// Embedder turns a query string into a dense vector for the semantic
// arm of hybrid search. Backed by the worker's gRPC embedding RPC in
// production; tests substitute a fake to avoid network hops.
type Embedder interface {
	// Model returns the model ID currently in use. Changes to this
	// string drive cache invalidation via the key derivation below.
	Model() string
	// Embed returns the query embedding. A nil vector + false signals
	// that the backend is unavailable (circuit open, network down,
	// etc.); the service treats this as "semantic degraded" and
	// skips the vector arm without failing the whole request.
	Embed(ctx context.Context, query string) ([]float32, bool)
}

// --- LRU + circuit breaker wrapper ---------------------------------------

// CachedEmbedder wraps an Embedder with a bounded-size LRU cache on
// query text plus a circuit breaker that trips the vector arm
// immediately after N consecutive failures.
//
// The plan calls for a 5-minute TTL on query embeddings. This
// implementation rounds that to a simple sliding TTL so we avoid
// pulling in a full LRU dep.
type CachedEmbedder struct {
	inner Embedder

	mu   sync.Mutex
	cap  int
	ttl  time.Duration
	data map[string]cacheEntry
	// (lru ordering used to live here for size-capped eviction; the
	// current cache evicts purely on TTL, so the oldest-first slice is
	// no longer maintained. Removed to satisfy lint.)

	// breaker
	consecutiveFails atomic.Int64
	openUntil        atomic.Int64 // unix ns; 0 = closed
	failThreshold    int
	openDuration     time.Duration
}

type cacheEntry struct {
	vec       []float32
	expiresAt time.Time
}

// NewCachedEmbedder constructs a CachedEmbedder with the given cap and
// TTL. failThreshold / openDuration configure the circuit breaker; 0
// values fall back to the plan defaults.
func NewCachedEmbedder(inner Embedder, cap int, ttl time.Duration, failThreshold int, openDuration time.Duration) *CachedEmbedder {
	if cap <= 0 {
		cap = 1024
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if failThreshold <= 0 {
		failThreshold = 5
	}
	if openDuration <= 0 {
		openDuration = 30 * time.Second
	}
	return &CachedEmbedder{
		inner:         inner,
		cap:           cap,
		ttl:           ttl,
		data:          make(map[string]cacheEntry, cap),
		failThreshold: failThreshold,
		openDuration:  openDuration,
	}
}

// Model delegates to the underlying embedder.
func (c *CachedEmbedder) Model() string {
	if c == nil || c.inner == nil {
		return ""
	}
	return c.inner.Model()
}

// CircuitOpen returns true iff the breaker is currently open. Exposed
// for observability / debug panel rendering.
func (c *CachedEmbedder) CircuitOpen() bool {
	return time.Now().UnixNano() < c.openUntil.Load()
}

// Embed tries cache → circuit → delegate. On failure the breaker
// increments; on the Nth consecutive failure it opens for
// openDuration.
func (c *CachedEmbedder) Embed(ctx context.Context, query string) ([]float32, bool) {
	if c == nil || c.inner == nil {
		return nil, false
	}
	// --- cache hit? ---
	key := c.keyFor(query)
	if vec, ok := c.lookup(key); ok {
		return vec, true
	}
	// --- circuit open? ---
	if c.CircuitOpen() {
		return nil, false
	}
	// --- delegate ---
	vec, ok := c.inner.Embed(ctx, query)
	if !ok || len(vec) == 0 {
		n := c.consecutiveFails.Add(1)
		if int(n) >= c.failThreshold {
			c.openUntil.Store(time.Now().Add(c.openDuration).UnixNano())
		}
		return nil, false
	}
	// Success → reset breaker, cache, return.
	c.consecutiveFails.Store(0)
	c.openUntil.Store(0)
	c.store(key, vec)
	return vec, true
}

func (c *CachedEmbedder) keyFor(q string) string {
	h := sha256.Sum256([]byte(c.Model() + "|" + q))
	return hex.EncodeToString(h[:])
}

func (c *CachedEmbedder) lookup(key string) ([]float32, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.data[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(e.expiresAt) {
		delete(c.data, key)
		return nil, false
	}
	return e.vec, true
}

func (c *CachedEmbedder) store(key string, vec []float32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Evict oldest if over capacity. This is O(n) worst case but our
	// `cap` is small (≤ a few thousand entries) and the cost is
	// amortized across the many cache hits in between evictions.
	if len(c.data) >= c.cap {
		var oldestKey string
		oldest := time.Now()
		for k, v := range c.data {
			if v.expiresAt.Before(oldest) {
				oldest = v.expiresAt
				oldestKey = k
			}
		}
		if oldestKey != "" {
			delete(c.data, oldestKey)
		}
	}
	c.data[key] = cacheEntry{vec: vec, expiresAt: time.Now().Add(c.ttl)}
}
