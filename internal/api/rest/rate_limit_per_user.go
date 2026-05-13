// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"net/http"
	"time"

	"github.com/go-chi/httprate"

	"github.com/sourcebridge/sourcebridge/internal/auth"
)

// perUserRateLimit returns a middleware that caps requests per
// authenticated user per window. Must be mounted AFTER authMiddleware so
// claims are present in the request context.
//
// Keying:
//   - Authenticated requests key by "user:<UserID>".
//   - Unauthenticated requests (or any path where claims are absent for
//     some reason) fall back to per-IP keying so the limiter still
//     does *something* defensive instead of degrading to a single
//     shared bucket across the entire process.
//
// CA-221 (X-L6): the existing global httprate.LimitByIP layer fires
// pre-auth and protects against IP-level spam. It does NOT protect
// against an attacker with one valid token rotating across many IPs
// (Tor exit relays, NAT pools, residential proxies). This middleware
// adds the user-keyed defense-in-depth layer.
//
// requests<=0 disables the limiter entirely (returns a passthrough
// middleware) — operators can opt out via
// server.per_user_rate_limit_per_min=0.
func perUserRateLimit(requests int, window time.Duration) func(http.Handler) http.Handler {
	if requests <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	return httprate.Limit(
		requests, window,
		httprate.WithKeyFuncs(perUserRateLimitKey),
		httprate.WithLimitHandler(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			// Body shape mirrors the CSRF + auth error envelopes (lowercase-
			// underscore error code) so frontend retry logic can branch on
			// `error === "too_many_requests"` without a separate parser.
			_, _ = w.Write([]byte(`{"error":"too_many_requests"}`))
		}),
	)
}

// perUserRateLimitKey extracts the rate-limit bucket key from the
// request. Exported as a package-level identifier so unit tests can
// pin behavior without re-implementing the auth.GetClaims plumbing.
func perUserRateLimitKey(r *http.Request) (string, error) {
	if claims := auth.GetClaims(r.Context()); claims != nil && claims.UserID != "" {
		return "user:" + claims.UserID, nil
	}
	return httprate.KeyByIP(r)
}
