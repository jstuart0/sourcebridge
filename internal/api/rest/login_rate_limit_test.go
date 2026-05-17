// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/auth"
)

// TestLoginRateLimiter_AllowsUnderLimit checks that attempts below the
// configured limit all return true.
func TestLoginRateLimiter_AllowsUnderLimit(t *testing.T) {
	l := newLoginRateLimiter(5, time.Minute)
	for i := 0; i < 5; i++ {
		if !l.Allow(localAuthUsername) {
			t.Fatalf("attempt %d should be allowed (under limit)", i+1)
		}
	}
}

// TestLoginRateLimiter_RejectsOnExceed checks that the (N+1)th attempt in the
// window is rejected.
func TestLoginRateLimiter_RejectsOnExceed(t *testing.T) {
	l := newLoginRateLimiter(5, time.Minute)
	for i := 0; i < 5; i++ {
		l.Allow(localAuthUsername)
	}
	if l.Allow(localAuthUsername) {
		t.Fatal("6th attempt should be rejected (exceeds limit)")
	}
}

// TestLoginRateLimiter_ZeroLimitDisabled checks that limit=0 disables the
// limiter entirely (always allows, regardless of call count).
func TestLoginRateLimiter_ZeroLimitDisabled(t *testing.T) {
	l := newLoginRateLimiter(0, time.Minute)
	for i := 0; i < 1000; i++ {
		if !l.Allow(localAuthUsername) {
			t.Fatalf("disabled limiter (limit=0) must always allow; rejected at call %d", i+1)
		}
	}
}

// TestLoginRateLimiter_IndependentBuckets checks that different usernames
// have independent windows.
func TestLoginRateLimiter_IndependentBuckets(t *testing.T) {
	l := newLoginRateLimiter(2, time.Minute)
	// Exhaust user-A
	l.Allow("user-a")
	l.Allow("user-a")
	if l.Allow("user-a") {
		t.Fatal("user-a 3rd attempt should be rejected")
	}
	// user-b must still be allowed
	if !l.Allow("user-b") {
		t.Fatal("user-b 1st attempt must be allowed — buckets are independent")
	}
}

// TestLoginRateLimiter_WindowExpiry checks that attempts outside the window
// are pruned and the bucket resets.
func TestLoginRateLimiter_WindowExpiry(t *testing.T) {
	window := 50 * time.Millisecond
	l := newLoginRateLimiter(2, window)

	// Exhaust the bucket.
	l.Allow("u")
	l.Allow("u")
	if l.Allow("u") {
		t.Fatal("3rd attempt must be rejected")
	}

	// Wait for the window to expire.
	time.Sleep(window + 10*time.Millisecond)

	// Should be allowed again.
	if !l.Allow("u") {
		t.Fatal("after window expiry, first attempt must be allowed again")
	}
}

// TestLoginRateLimiter_WriteRejection checks the HTTP response shape.
func TestLoginRateLimiter_WriteRejection(t *testing.T) {
	l := newLoginRateLimiter(1, 5*time.Minute)
	w := httptest.NewRecorder()
	l.WriteRejection(w)

	if w.Code != 429 {
		t.Fatalf("expected 429, got %d", w.Code)
	}
	body := strings.TrimSpace(w.Body.String())
	if body != `{"error":"too_many_requests"}` {
		t.Fatalf("unexpected body: %q", body)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After header must be set")
	}
	if w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", w.Header().Get("Content-Type"))
	}
}

// TestSecondsString checks the Retry-After header value formatting.
func TestSecondsString(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{5 * time.Minute, "300"},
		{1 * time.Second, "1"},
		{30 * time.Second, "30"},
		{10 * time.Millisecond, "1"}, // rounds up to 1
	}
	for _, tc := range cases {
		got := secondsString(tc.d)
		if got != tc.want {
			t.Errorf("secondsString(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// TestSecondsString_ClampZeroToOne pins that a zero duration is floored to 1,
// confirming the unreachable secs==0 branch was correctly removed (X-L1 / CA-469).
func TestSecondsString_ClampZeroToOne(t *testing.T) {
	got := secondsString(0)
	if got != "1" {
		t.Fatalf("secondsString(0) = %q, want %q", got, "1")
	}
}

// TestLocalAuthUsernameMatchesAuthPackageEmail (CA-505) asserts that the
// rate-limit key in this package matches the canonical email in auth.LocalAdminEmail().
// If they diverge, all OSS local-auth login attempts bypass the per-username
// rate limiter because they're keyed on a different string than the constant
// that drives the bucket.
func TestLocalAuthUsernameMatchesAuthPackageEmail(t *testing.T) {
	if localAuthUsername != auth.LocalAdminEmail() {
		t.Fatalf("cohesion broken: localAuthUsername=%q auth.LocalAdminEmail()=%q",
			localAuthUsername, auth.LocalAdminEmail())
	}
}

// TestLoginRateLimiter_SweepRemovesStaleBuckets (CA-518) verifies that sweep()
// removes buckets whose attempts are empty AND whose lastSeen is older than the
// window. Uses a fake clock so the test is deterministic.
func TestLoginRateLimiter_SweepRemovesStaleBuckets(t *testing.T) {
	window := time.Minute
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := base
	fakeClock := func() time.Time { return now }

	l := newLoginRateLimiter(10, window)
	l.now = fakeClock

	// Seed 10 distinct buckets via Allow, so they each have a lastSeen at base.
	for i := 0; i < 10; i++ {
		l.Allow(fmt.Sprintf("user-%d", i))
	}

	// Advance clock past the window so all buckets are stale.
	now = base.Add(window + time.Second)

	// Each bucket was seeded with one attempt at base; that attempt is now
	// outside the window. To make them empty, we need to prune by calling
	// Allow again — but that would update lastSeen. Instead, manually reset
	// attempts and lastSeen to simulate the "expired and idle" state.
	l.mu.Range(func(k, v any) bool {
		b := v.(*loginBucket)
		b.mu.Lock()
		b.attempts = nil
		b.lastSeen = base // old lastSeen, now stale
		b.mu.Unlock()
		return true
	})

	// Allow on a single key to trigger the sweep (call 256 times to hit the boundary).
	for i := 0; i < 256; i++ {
		l.Allow("survivor")
	}

	// Count remaining buckets. Should be exactly 1 (the survivor).
	count := 0
	l.mu.Range(func(k, v any) bool { count++; return true })
	if count != 1 {
		t.Fatalf("expected 1 bucket after sweep, got %d", count)
	}
}

// TestLoginRateLimiter_SweepKeepsRecentEmptyBuckets (CA-518) verifies that sweep()
// does NOT delete a bucket whose lastSeen is within the window, even if attempts is
// empty. This is the load-bearing race guard from X-M1.
func TestLoginRateLimiter_SweepKeepsRecentEmptyBuckets(t *testing.T) {
	window := time.Minute
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := base
	fakeClock := func() time.Time { return now }

	l := newLoginRateLimiter(10, window)
	l.now = fakeClock

	// Seed one bucket.
	l.Allow("recent-user")

	// Manually clear its attempts but keep lastSeen recent (within window).
	l.mu.Range(func(k, v any) bool {
		b := v.(*loginBucket)
		b.mu.Lock()
		b.attempts = nil
		b.lastSeen = base // lastSeen = now (within window)
		b.mu.Unlock()
		return true
	})

	// Do NOT advance the clock — lastSeen is still within the window.

	// Trigger sweep via 256 Allow calls on a different key.
	for i := 0; i < 256; i++ {
		l.Allow("trigger")
	}

	// The recent-user bucket must still be present.
	_, ok := l.mu.Load("recent-user")
	if !ok {
		t.Fatal("sweep must not delete a bucket whose lastSeen is within the window")
	}
}

// TestLoginRateLimiter_SweepRace (CA-518) runs with -race to verify that
// concurrent Allow calls on different keys while sweep fires produce no data races.
func TestLoginRateLimiter_SweepRace(t *testing.T) {
	l := newLoginRateLimiter(5, time.Minute)
	var wg sync.WaitGroup
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			key := fmt.Sprintf("goroutine-%d", id)
			for i := 0; i < 300; i++ {
				l.Allow(key)
			}
		}(g)
	}
	wg.Wait()
}
