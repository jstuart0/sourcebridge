// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/auth"
)

// CA-221 (X-L6): perUserRateLimitKey returns "user:<id>" when claims are
// present in the context, and falls back to per-IP keying when they're
// not. The fallback ensures the limiter still does *something* defensive
// instead of degrading to a single shared bucket if the limiter is ever
// mounted on a route that didn't go through authMiddleware first.
func TestPerUserRateLimitKey_UsesClaimsUserID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/foo", nil)
	req = withClaims(req, &auth.Claims{UserID: "user-123", Role: auth.RoleUser})

	key, err := perUserRateLimitKey(req)
	if err != nil {
		t.Fatalf("perUserRateLimitKey() error: %v", err)
	}
	if key != "user:user-123" {
		t.Fatalf("key=%q want user:user-123", key)
	}
}

func TestPerUserRateLimitKey_FallsBackToIPWhenNoClaims(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/foo", nil)
	req.RemoteAddr = "203.0.113.42:51234"
	// No claims attached — context has no UserID.

	key, err := perUserRateLimitKey(req)
	if err != nil {
		t.Fatalf("perUserRateLimitKey() error: %v", err)
	}
	// httprate.KeyByIP canonicalizes IPv4 by stripping the port.
	if key == "user:" || key == "" {
		t.Fatalf("expected IP-derived key, got %q", key)
	}
	if key == "user:user-123" {
		t.Fatalf("must not return a fake user key when claims are absent: %q", key)
	}
}

func TestPerUserRateLimitKey_EmptyUserIDFallsBackToIP(t *testing.T) {
	// Pin: claims present but UserID empty (legacy token migration window)
	// must fall back to IP, NOT key as "user:" with empty suffix.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/foo", nil)
	req.RemoteAddr = "203.0.113.99:12345"
	req = withClaims(req, &auth.Claims{UserID: ""})

	key, err := perUserRateLimitKey(req)
	if err != nil {
		t.Fatalf("perUserRateLimitKey() error: %v", err)
	}
	if key == "user:" {
		t.Fatal("must NOT key on empty user-id — that produces a single shared bucket")
	}
}

func TestPerUserRateLimit_ZeroRequestsIsPassthrough(t *testing.T) {
	// Pin: requests<=0 → return a passthrough middleware. This is the
	// operator opt-out path (server.per_user_rate_limit_per_min=0).
	mw := perUserRateLimit(0, time.Minute)

	handlerCalls := 0
	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalls++
		w.WriteHeader(http.StatusOK)
	})

	wrapped := mw(final)

	// Hammer it well beyond any reasonable limit; every call must pass through.
	for i := 0; i < 50; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/foo", nil)
		req = withClaims(req, &auth.Claims{UserID: "u1"})
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("opt-out path returned %d on call %d; expected always 200", rec.Code, i)
		}
	}
	if handlerCalls != 50 {
		t.Fatalf("opt-out passthrough must invoke handler every time; got %d of 50", handlerCalls)
	}
}

func TestPerUserRateLimit_EnforcesLimitPerUser(t *testing.T) {
	// 5 requests per minute keyed by user ID. 6th request must be 429.
	mw := perUserRateLimit(5, time.Minute)

	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	wrapped := mw(final)

	makeReq := func(userID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/foo", nil)
		req = withClaims(req, &auth.Claims{UserID: userID})
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, req)
		return rec
	}

	// First 5 must succeed.
	for i := 0; i < 5; i++ {
		rec := makeReq("user-A")
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d for user-A: code=%d want 200", i+1, rec.Code)
		}
	}
	// 6th must be limited.
	rec := makeReq("user-A")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("6th request for user-A: code=%d want 429", rec.Code)
	}
	if got := rec.Body.String(); got != `{"error":"too_many_requests"}` {
		t.Fatalf("429 body=%q want JSON envelope", got)
	}

	// user-B must NOT be limited — buckets are independent.
	for i := 0; i < 5; i++ {
		rec := makeReq("user-B")
		if rec.Code != http.StatusOK {
			t.Fatalf("user-B request %d should pass; got %d", i+1, rec.Code)
		}
	}
}

