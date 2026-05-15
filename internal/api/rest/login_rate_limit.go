// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"net/http"
	"sync"
	"time"
)

// loginRateLimiter provides a per-username sliding-window rate limit for
// local-auth login attempts. It complements the existing per-IP httprate
// middleware: per-IP limits protect against volumetric floods from a single
// source; this limiter protects against distributed brute-force attacks where
// an attacker rotates across many IPs to target a specific credential.
//
// CA-339 / CA-207 (X-M1): local-auth brute-force viable from distributed
// sources because the per-IP bucket is the only gate and each IP gets its own
// fresh window. This adds a second, IP-transparent gate keyed on the username
// (user-visible identifier used to key the credential, i.e. the account).
//
// For OSS local-auth, the "username" is always the single admin account
// ("admin@localhost"). Using a fixed key means the limiter fires after the
// configured N failed attempts regardless of which IP sent them — effectively
// a lock-out that resets after the window expires.
//
// Timing-safety: the limiter is called on EVERY login attempt (valid and
// invalid password alike) so the response-time difference between a locked-out
// account and an unknown account is not observable by an attacker. The caller
// in handleLogin / handleDesktopLocalLogin must call loginLimiter.Allow()
// BEFORE the bcrypt comparison to keep the fast-path constant-time.
//
// Bucket lifecycle: entries are allocated on first access and pruned lazily
// (on each Allow call). A process restart resets all buckets. No persistence
// is needed — the bcrypt cost (~100ms) already limits brute-force throughput;
// this limiter is the guard against parallel distributed requests.
type loginRateLimiter struct {
	mu      sync.Map // key: string → *loginBucket
	limit   int
	window  time.Duration
}

// newLoginRateLimiter creates a limiter that allows at most limit attempts
// per username per window. limit <= 0 disables the limiter (always allows).
func newLoginRateLimiter(limit int, window time.Duration) *loginRateLimiter {
	return &loginRateLimiter{limit: limit, window: window}
}

// Allow records an attempt for username and returns true if the request
// should be allowed, false if the per-username budget is exceeded.
// When limit <= 0 the limiter is disabled and always returns true.
func (l *loginRateLimiter) Allow(username string) bool {
	if l.limit <= 0 {
		return true
	}
	raw, _ := l.mu.LoadOrStore(username, &loginBucket{})
	b := raw.(*loginBucket)
	return b.allow(l.limit, l.window)
}

// WriteRejection writes a 429 Too Many Requests response with a Retry-After
// header. The delay is the configured window in seconds rounded to the nearest
// whole second (conservative: tells the client the maximum wait, not the
// minimum).
func (l *loginRateLimiter) WriteRejection(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", secondsString(l.window))
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte(`{"error":"too_many_requests"}`))
}

// loginBucket holds the sliding-window timestamps for a single username.
type loginBucket struct {
	mu       sync.Mutex
	attempts []time.Time // sorted ascending; pruned on each allow() call
}

// allow records the current attempt, prunes entries outside the window, and
// returns whether the total count within the window is ≤ limit.
func (b *loginBucket) allow(limit int, window time.Duration) bool {
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()

	// Prune expired entries (older than now - window).
	cutoff := now.Add(-window)
	i := 0
	for i < len(b.attempts) && b.attempts[i].Before(cutoff) {
		i++
	}
	b.attempts = append(b.attempts[i:], now) // prune + record this attempt

	return len(b.attempts) <= limit
}

// secondsString returns the duration rounded to whole seconds as a decimal
// string suitable for the Retry-After HTTP header.
func secondsString(d time.Duration) string {
	secs := int(d.Seconds())
	if secs < 1 {
		secs = 1
	}
	// Avoid importing strconv — hand-roll the int → string conversion.
	if secs == 0 {
		return "0"
	}
	buf := make([]byte, 0, 10)
	n := secs
	for n > 0 {
		buf = append(buf, byte('0'+n%10))
		n /= 10
	}
	// reverse
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}
