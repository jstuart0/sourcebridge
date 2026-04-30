// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package changewatch

import (
	"time"
)

// tokenBucket is a simple per-key rate limiter. Tokens accrue
// continuously at ratePerMin / minute up to a burst capacity equal to
// ratePerMin. allow consumes one token; consume failures return false
// without changing state.
//
// Used for the per-(repo, source.kind) throttle. When ratePerMin <= 0
// the bucket is in unlimited mode (allow always returns true).
//
// The bucket is NOT goroutine-safe; the router holds r.mu around every
// invocation. Embedding our own mutex here would just re-lock the same
// state.
type tokenBucket struct {
	ratePerMin int
	tokens     float64
	last       time.Time
}

// newTokenBucket constructs a bucket. A zero or negative ratePerMin
// disables the bucket entirely (allow always true).
func newTokenBucket(ratePerMin int) *tokenBucket {
	return &tokenBucket{
		ratePerMin: ratePerMin,
		tokens:     float64(ratePerMin), // start full so first burst goes through
	}
}

// allow attempts to consume one token at time `now`. Returns true on
// consume; false when the bucket is empty.
func (b *tokenBucket) allow(now time.Time) bool {
	if b.ratePerMin <= 0 {
		return true
	}
	if b.last.IsZero() {
		b.last = now
	}
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		// Tokens accrue at ratePerMin per 60s.
		b.tokens += elapsed * float64(b.ratePerMin) / 60.0
		if cap := float64(b.ratePerMin); b.tokens > cap {
			b.tokens = cap
		}
		b.last = now
	}
	if b.tokens >= 1.0 {
		b.tokens -= 1.0
		return true
	}
	return false
}

// windowBreaker is the per-repo aggregate circuit breaker. It tracks
// per-minute event counts in a 5-minute sliding window. The breaker
// trips when every minute in the window is at or above the configured
// threshold (5 consecutive minutes over the line).
//
// When ratePerMin <= 0 the breaker is in unlimited mode (tripped
// always returns false).
//
// NOT goroutine-safe; the router holds r.mu.
//
// The plan calls for "60 events/min for 5 consecutive minutes." We
// implement this exactly: every observe call lands in its minute
// bucket; tripped checks the most-recent 5 minute buckets and returns
// true iff every one is >= ratePerMin.
type windowBreaker struct {
	ratePerMin int

	// bucket head — the (UTC) minute the head represents. counts[0] is
	// the count for `head`; counts[1] for head-1min; ...; counts[4] for
	// head-4min.
	head     time.Time
	counts   [5]int
	cooldown time.Time // when set, the breaker is open until this time
}

// newWindowBreaker constructs a breaker.
func newWindowBreaker(ratePerMin int) *windowBreaker {
	return &windowBreaker{ratePerMin: ratePerMin}
}

// observe records one event at time `now`. Advances the window if `now`
// falls in a later minute than head.
func (b *windowBreaker) observe(now time.Time) {
	if b.ratePerMin <= 0 {
		return
	}
	b.advanceTo(now)
	b.counts[0]++
	if b.allOverThreshold() {
		// Trip: stay open for the next minute. After the cooldown the
		// next observe rebuilds the window from zero, giving the system
		// a chance to recover.
		b.cooldown = b.head.Add(1 * time.Minute)
	}
}

// tripped reports whether the breaker is currently open at time `now`.
func (b *windowBreaker) tripped(now time.Time) bool {
	if b.ratePerMin <= 0 {
		return false
	}
	if b.cooldown.IsZero() {
		return false
	}
	if now.Before(b.cooldown) {
		return true
	}
	// Cooldown expired — clear and resume normal observation.
	b.cooldown = time.Time{}
	b.counts = [5]int{}
	b.head = truncateToMinute(now)
	return false
}

// advanceTo rolls the window forward so b.head represents the minute
// containing `now`. Newly-vacated buckets reset to zero.
func (b *windowBreaker) advanceTo(now time.Time) {
	currentMin := truncateToMinute(now)
	if b.head.IsZero() {
		b.head = currentMin
		return
	}
	if !currentMin.After(b.head) {
		return
	}
	deltaMin := int(currentMin.Sub(b.head).Minutes())
	if deltaMin >= len(b.counts) {
		// Skipped past the entire window; everything resets.
		b.counts = [5]int{}
		b.head = currentMin
		return
	}
	// Shift right by deltaMin: counts[deltaMin] becomes counts[0]'s old
	// value; new buckets fill with zero. We rebuild from the back.
	var fresh [5]int
	for i := 0; i < len(b.counts); i++ {
		src := i - deltaMin
		if src >= 0 && src < len(b.counts) {
			fresh[i] = b.counts[src]
		}
	}
	b.counts = fresh
	b.head = currentMin
}

// allOverThreshold returns true when every minute in the window is at
// or above ratePerMin. A bucket of zero (an idle minute) would fail
// the test; this matches the plan's "5 consecutive minutes over the
// line."
func (b *windowBreaker) allOverThreshold() bool {
	for _, c := range b.counts {
		if c < b.ratePerMin {
			return false
		}
	}
	return true
}

// truncateToMinute zeroes out sub-minute components.
func truncateToMinute(t time.Time) time.Time {
	return t.Truncate(time.Minute)
}
