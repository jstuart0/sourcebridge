// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// rate_limiter.go implements per-sink token-bucket rate limiting to ensure
// living-wiki HTTP write operations stay within each provider's published API
// limits. The limiter is called by sink HTTP clients before each API request
// and after each successful response.
//
// Default limits (per provider documentation as of early 2026):
//
//	Confluence:  100 requests per 5 minutes (Atlassian documented limit)
//	Notion:      100 requests per 5 minutes (Notion API)
//	GitHub:     5000 requests per hour (GitHub REST API)
//	GitLab:     2000 requests per minute (GitLab API)
//
// The limiter uses a sliding-window token bucket: tokens refill continuously
// at the configured rate. If the bucket is empty, Allow blocks until a token
// becomes available or ctx is cancelled. Record is called after a successful
// response to track consumption (note: the token-bucket already deducts a
// token on Allow, so Record is used to track "what was actually observed" for
// metric purposes and is a no-op for the bucket logic itself).
package markdown

import (
	"context"
	"sync"
	"time"
)

// SinkKind classifies the type of a living-wiki sink.
// Duplicated here (mirrors internal/settings/livingwiki.RepoWikiSinkKind) to
// avoid a circular import between the markdown and settings packages.
type SinkKind string

const (
	SinkKindConfluence SinkKind = "CONFLUENCE"
	SinkKindNotion     SinkKind = "NOTION"
	SinkKindGitHub     SinkKind = "GITHUB_WIKI"
	SinkKindGitLab     SinkKind = "GITLAB_WIKI"
	SinkKindGitRepo    SinkKind = "GIT_REPO"
)

// SinkRateLimiter controls the rate at which HTTP calls may be made to each
// sink type. Implementations must be safe for concurrent use.
type SinkRateLimiter interface {
	// Allow blocks until one call to the given sink kind is permitted, or until
	// ctx is cancelled. Returns ctx.Err() if the context is cancelled while
	// waiting for a token.
	Allow(ctx context.Context, kind SinkKind) error

	// Record is called after each successful sink API call. Used to track
	// observed consumption in metrics; the rate limiter itself deducts tokens
	// in Allow so Record does not affect bucket refill.
	Record(kind SinkKind)
}

// ─────────────────────────────────────────────────────────────────────────────
// Token bucket
// ─────────────────────────────────────────────────────────────────────────────

// tokenBucket is a thread-safe sliding-window token bucket.
type tokenBucket struct {
	mu       sync.Mutex
	tokens   float64    // current token count
	capacity float64    // maximum token count
	rate     float64    // tokens added per nanosecond
	lastTick time.Time
}

func newTokenBucket(capacity int, per time.Duration) *tokenBucket {
	ratePerNs := float64(capacity) / float64(per)
	return &tokenBucket{
		tokens:   float64(capacity), // start full
		capacity: float64(capacity),
		rate:     ratePerNs,
		lastTick: time.Now(),
	}
}

// take attempts to consume one token. If available, deducts it and returns
// true. Otherwise returns false and the estimated wait duration until one
// token is available.
func (b *tokenBucket) take() (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(b.lastTick)
	b.lastTick = now

	// Refill based on elapsed time, capped at capacity.
	b.tokens += float64(elapsed) * b.rate
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}

	// Estimate when one token will be available.
	deficit := 1 - b.tokens
	wait := time.Duration(deficit / b.rate)
	return false, wait
}

// ─────────────────────────────────────────────────────────────────────────────
// TokenBucketRateLimiter
// ─────────────────────────────────────────────────────────────────────────────

// TokenBucketRateLimiter is the production [SinkRateLimiter] implementation.
// Each sink kind has its own independent token bucket.
type TokenBucketRateLimiter struct {
	buckets map[SinkKind]*tokenBucket
}

// SinkRateConfig specifies the rate limit for one sink kind.
type SinkRateConfig struct {
	// Capacity is the burst capacity (number of requests allowed in the window).
	Capacity int
	// Per is the window over which Capacity requests are allowed.
	Per time.Duration
}

// DefaultSinkRates returns the provider-documented default rate limits.
func DefaultSinkRates() map[SinkKind]SinkRateConfig {
	return map[SinkKind]SinkRateConfig{
		SinkKindConfluence: {Capacity: 100, Per: 5 * time.Minute},
		SinkKindNotion:     {Capacity: 100, Per: 5 * time.Minute},
		SinkKindGitHub:     {Capacity: 5000, Per: time.Hour},
		SinkKindGitLab:     {Capacity: 2000, Per: time.Minute},
		// GIT_REPO writes are local filesystem operations; no external rate limit.
		// Use a generous limit to avoid blocking on high-concurrency local writes.
		SinkKindGitRepo: {Capacity: 10000, Per: time.Minute},
	}
}

// NewTokenBucketRateLimiter creates a limiter with the given per-sink configs.
// Pass [DefaultSinkRates]() for production usage.
func NewTokenBucketRateLimiter(rates map[SinkKind]SinkRateConfig) *TokenBucketRateLimiter {
	buckets := make(map[SinkKind]*tokenBucket, len(rates))
	for kind, cfg := range rates {
		buckets[kind] = newTokenBucket(cfg.Capacity, cfg.Per)
	}
	return &TokenBucketRateLimiter{buckets: buckets}
}

// Allow blocks until a token for kind is available or ctx is cancelled.
// If no bucket is configured for kind, Allow returns immediately (unthrottled).
func (l *TokenBucketRateLimiter) Allow(ctx context.Context, kind SinkKind) error {
	b, ok := l.buckets[kind]
	if !ok {
		return nil // no limit configured for this sink kind
	}

	for {
		ok, wait := b.take()
		if ok {
			return nil
		}
		// Back off for the estimated wait duration, then retry.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
			// token should be available; loop and try again
		}
	}
}

// Record is a no-op for the token-bucket implementation: token consumption
// is already tracked by Allow. Retained in the interface for metrics hooks.
func (l *TokenBucketRateLimiter) Record(_ SinkKind) {}

// ─────────────────────────────────────────────────────────────────────────────
// NoopRateLimiter
// ─────────────────────────────────────────────────────────────────────────────

// NoopRateLimiter is a [SinkRateLimiter] that never blocks. Use in unit tests
// or when rate limiting is not needed.
type NoopRateLimiter struct{}

func (NoopRateLimiter) Allow(_ context.Context, _ SinkKind) error { return nil }
func (NoopRateLimiter) Record(_ SinkKind)                         {}
