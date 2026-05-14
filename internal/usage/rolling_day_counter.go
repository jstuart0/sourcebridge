// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package usage

import (
	"sync"
	"time"
)

// dayBucket holds the count for a single UTC day.
type dayBucket struct {
	day   int64 // unix-day index (UTC); 0 means empty slot
	count int64
}

// RollingDayCounter is a thread-safe, in-process rolling-window counter.
// It tracks increments across a fixed number of UTC calendar days using a
// ring of per-day buckets. Stale slots (days that have rolled out of the
// window) are evicted lazily on write and silently skipped on read.
//
// A fresh process starts at zero. The counter is intentionally not persisted
// across process restarts — see TELEMETRY.md for the disclosed caveat.
type RollingDayCounter struct {
	days   int
	clock  func() time.Time
	mu     sync.Mutex
	bucket []dayBucket // length == days; indexed by (unix-day % days)
}

// NewRollingDayCounter creates a counter spanning the given number of UTC days.
// days must be > 0; panics otherwise (this is a program-level invariant, not
// a runtime input error).
func NewRollingDayCounter(days int) *RollingDayCounter {
	return NewRollingDayCounterWithClock(days, time.Now)
}

// NewRollingDayCounterWithClock creates a counter with an injectable clock.
// The clock must return time values whose UTC interpretation is meaningful
// (i.e. callers must ensure UTC if testing date arithmetic). This constructor
// is intended for deterministic tests.
func NewRollingDayCounterWithClock(days int, clock func() time.Time) *RollingDayCounter {
	if days <= 0 {
		panic("usage: RollingDayCounter requires days > 0")
	}
	return &RollingDayCounter{
		days:   days,
		clock:  clock,
		bucket: make([]dayBucket, days),
	}
}

// Inc increments the counter for the current UTC day. If the slot for today
// was previously occupied by an older day, it is reset first so stale counts
// do not persist.
func (c *RollingDayCounter) Inc() {
	c.mu.Lock()
	defer c.mu.Unlock()
	today := c.clock().UTC().Unix() / 86400
	slot := int(today % int64(c.days))
	if c.bucket[slot].day != today {
		c.bucket[slot] = dayBucket{day: today, count: 0}
	}
	c.bucket[slot].count++
}

// Total returns the sum of counts across all buckets whose day falls within
// the rolling window [today - (days-1), today] (inclusive, UTC). Buckets
// outside this range are ignored — this handles both stale slots from prior
// days and the zero-value empty-slot sentinel.
func (c *RollingDayCounter) Total() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	today := c.clock().UTC().Unix() / 86400
	cutoff := today - int64(c.days) + 1
	var sum int64
	for _, b := range c.bucket {
		if b.day >= cutoff && b.day <= today {
			sum += b.count
		}
	}
	return sum
}

// ResetForTest zeros all buckets. This exists solely for test isolation;
// it must not be called in production code.
func (c *RollingDayCounter) ResetForTest() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.bucket {
		c.bucket[i] = dayBucket{}
	}
}
