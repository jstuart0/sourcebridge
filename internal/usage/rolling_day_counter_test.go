// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package usage

import (
	"sync"
	"testing"
	"time"
)

// baseDay is a fixed reference epoch used across tests. Using a large
// unix-day value avoids accidental slot collisions with the zero-value
// empty-bucket sentinel (day == 0).
const baseDay int64 = 20000 // well beyond day 0 (year ~2024)

func clockAt(day int64) func() time.Time {
	return func() time.Time {
		return time.Unix(day*86400, 0).UTC()
	}
}

func TestRollingDayCounter_IncrementsTodayBucket(t *testing.T) {
	c := NewRollingDayCounterWithClock(30, clockAt(baseDay))
	c.Inc()
	c.Inc()
	c.Inc()
	if got := c.Total(); got != 3 {
		t.Fatalf("expected Total() == 3, got %d", got)
	}
}

func TestRollingDayCounter_AcrossDays(t *testing.T) {
	var mu sync.Mutex
	day := baseDay

	clock := func() time.Time {
		mu.Lock()
		d := day
		mu.Unlock()
		return time.Unix(d*86400, 0).UTC()
	}

	c := NewRollingDayCounterWithClock(30, clock)

	// Day N: 5 increments
	for range 5 {
		c.Inc()
	}

	// Advance to day N+1: 3 more increments — both days in window
	mu.Lock()
	day = baseDay + 1
	mu.Unlock()
	for range 3 {
		c.Inc()
	}
	if got := c.Total(); got != 8 {
		t.Fatalf("after day N+1: expected Total() == 8, got %d", got)
	}

	// Advance to day N+30: oldest bucket (day N) falls out of the 30-day window.
	// Window is [today-(30-1), today] = [N+1, N+30], so day N is excluded.
	// Add 1 more increment on day N+30.
	mu.Lock()
	day = baseDay + 30
	mu.Unlock()
	c.Inc()
	// Day N+1 (3 counts) + day N+30 (1 count) = 4. Day N is outside window.
	if got := c.Total(); got != 4 {
		t.Fatalf("after day N+30: expected Total() == 4, got %d", got)
	}
}

func TestRollingDayCounter_StaleSlotEviction(t *testing.T) {
	// Start with a counter whose clock is at baseDay+100 (well ahead of any
	// slot that started with zero-value), then verify a slot written on
	// baseDay (now 100 days in the past) is not counted.
	c := NewRollingDayCounterWithClock(30, clockAt(baseDay))
	c.Inc() // writes into slot for baseDay

	// Advance clock 100 days — baseDay is now outside the 30-day window.
	c.clock = clockAt(baseDay + 100)
	if got := c.Total(); got != 0 {
		t.Fatalf("stale slot should be excluded; got Total() == %d", got)
	}
}

func TestRollingDayCounter_ConcurrentInc(t *testing.T) {
	const goroutines = 50
	const incsEach = 200

	c := NewRollingDayCounterWithClock(30, clockAt(baseDay))

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range incsEach {
				c.Inc()
			}
		}()
	}
	wg.Wait()

	want := int64(goroutines * incsEach)
	if got := c.Total(); got != want {
		t.Fatalf("concurrent Inc: expected Total() == %d, got %d", want, got)
	}
}

func TestRollingDayCounter_ResetForTest(t *testing.T) {
	c := NewRollingDayCounterWithClock(30, clockAt(baseDay))
	c.Inc()
	c.Inc()
	c.ResetForTest()
	if got := c.Total(); got != 0 {
		t.Fatalf("after ResetForTest: expected Total() == 0, got %d", got)
	}
}

func TestResetCountersForTest(t *testing.T) {
	t.Cleanup(ResetCountersForTest)

	QueriesCounter.Inc()
	QueriesCounter.Inc()
	ArtifactsCounter.Inc()

	ResetCountersForTest()

	if got := QueriesCounter.Total(); got != 0 {
		t.Fatalf("QueriesCounter: expected 0 after reset, got %d", got)
	}
	if got := ArtifactsCounter.Total(); got != 0 {
		t.Fatalf("ArtifactsCounter: expected 0 after reset, got %d", got)
	}
}
