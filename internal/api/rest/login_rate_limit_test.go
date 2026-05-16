// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
