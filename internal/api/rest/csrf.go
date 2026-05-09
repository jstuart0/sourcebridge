// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/auth"
)

const (
	csrfTokenHeader = "X-CSRF-Token"
	csrfTokenLength = 32
)

// csrfRejectionRateLimiter gates structured-log emission for CSRF rejections.
// At most 10 log lines per second; excess rejections increment the drop counter
// but are NOT logged. The drop counter is intentionally NOT exposed via
// /metrics (xander CSRF-5).
var (
	csrfLogCounter   atomic.Int64 // logged rejections in the current window
	csrfDropCounter  atomic.Int64 // dropped (rate-limited) rejections
	csrfRateLimitTkr = func() chan struct{} {
		ch := make(chan struct{}, 10)
		// Pre-fill with 10 tokens.
		for i := 0; i < 10; i++ {
			ch <- struct{}{}
		}
		go func() {
			ticker := time.NewTicker(100 * time.Millisecond) // refill 1 token per 100ms → 10/sec
			defer ticker.Stop()
			for range ticker.C {
				select {
				case ch <- struct{}{}:
				default: // bucket full
				}
			}
		}()
		return ch
	}()
)

// csrfReject writes a 403 and emits a rate-limited structured log line.
func csrfReject(w http.ResponseWriter, r *http.Request, reason string, bearerWithSession bool) {
	select {
	case <-csrfRateLimitTkr:
		csrfLogCounter.Add(1)
		slog.Warn("csrf rejection",
			"path", r.URL.Path,
			"method", r.Method,
			"reason", reason,
			"bearer_with_session_cookie", bearerWithSession,
		)
	default:
		csrfDropCounter.Add(1)
	}
	body := `{"error":"CSRF token missing"}`
	if reason == "csrf_token_mismatch" {
		body = `{"error":"CSRF token mismatch"}`
	}
	http.Error(w, body, http.StatusForbidden)
}

// csrfProtectionWithName returns CSRF middleware parameterised by cookie names
// and a coverage flag.
//
// Parameters:
//   - csrfCookieName:    the CSRF double-submit cookie name (e.g. "sourcebridge_csrf").
//   - sessionCookieName: the session cookie name (e.g. "sourcebridge_session").
//   - fullCoverage:      when false, any Bearer header bypasses CSRF (today's behaviour).
//     When true, the bypass only fires when Bearer is present AND no session
//     cookie is attached — i.e. the request looks like a genuine API client
//     (CLI, MCP, VS Code extension) rather than a browser.
//
// Security invariant on the bypass site: skip CSRF only when the request looks
// like an API client (Bearer present, no browser session cookie). Browsers
// attaching both Bearer and session cookie are subject to CSRF protection
// because the frontend's authFetch wrapper sends both; a request with both is
// indistinguishable from a browser-originated request.
func csrfProtectionWithName(csrfCookieName, sessionCookieName string, fullCoverage bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Safe methods don't need CSRF protection; opportunistically refresh the cookie.
			if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
				ensureCSRFCookie(w, r, csrfCookieName)
				next.ServeHTTP(w, r)
				return
			}

			// CA-201: determine whether this request is API-client-shaped or
			// browser-shaped.
			//
			// fullCoverage=false (default): any Bearer header bypasses CSRF, matching
			// pre-CA-201 behaviour exactly. This is the kill-switch path; flipping
			// CSRFFullCoverageEnabled back to false at runtime restores today's bypass
			// posture without a redeploy.
			//
			// fullCoverage=true: bypass requires Bearer AND absence of the session
			// cookie. Browsers attach the session cookie automatically on same-origin
			// requests; if the session cookie is present alongside a Bearer header the
			// request is browser-shaped and must carry a CSRF token regardless.
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
				if !fullCoverage {
					// Pre-CA-201 behaviour: any Bearer skips CSRF.
					next.ServeHTTP(w, r)
					return
				}
				_, sessionErr := r.Cookie(sessionCookieName)
				if sessionErr != nil {
					// No session cookie → genuine API-client request (CLI, MCP, VS Code);
					// CSRF doesn't apply.
					next.ServeHTTP(w, r)
					return
				}
				// fullCoverage on + session cookie present → browser-shaped; fall through
				// to CSRF token check below.
			}

			// Cookie-authenticated (or browser-shaped Bearer) requests must include a
			// matching CSRF token.
			cookie, err := r.Cookie(csrfCookieName)
			if err != nil {
				_, hasSess := r.Cookie(sessionCookieName)
				csrfReject(w, r, "csrf_token_missing", hasSess == nil && authHeader != "")
				return
			}

			headerToken := r.Header.Get(csrfTokenHeader)
			// The empty-string check below is load-bearing: without it,
			// ConstantTimeCompare([]byte(""), []byte("")) == 1 would spuriously
			// accept a request that sends neither the header nor a cookie value.
			if headerToken == "" || subtle.ConstantTimeCompare([]byte(headerToken), []byte(cookie.Value)) != 1 {
				_, hasSess := r.Cookie(sessionCookieName)
				csrfReject(w, r, "csrf_token_mismatch", hasSess == nil && authHeader != "")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// ensureCSRFCookie sets a CSRF cookie if one doesn't already exist.
func ensureCSRFCookie(w http.ResponseWriter, r *http.Request, cookieName string) {
	if _, err := r.Cookie(cookieName); err == nil {
		return // Cookie already exists
	}

	token := generateCSRFToken()
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: false, // JS needs to read this to send in header
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func generateCSRFToken() string {
	b := make([]byte, csrfTokenLength)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// handleCSRFToken returns a fresh CSRF token for the authenticated user.
func (s *Server) handleCSRFToken(w http.ResponseWriter, r *http.Request) {
	claims := auth.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	token := generateCSRFToken()
	http.SetCookie(w, &http.Cookie{
		Name:     s.jwtMgr.CSRFCookieName(),
		Value:    token,
		Path:     "/",
		HttpOnly: false,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})

	// Cache-Control: no-store prevents proxies and browsers from caching the token
	// response. Stale tokens from a cached response would cause spurious 403s (tessa M2).
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"csrf_token": token})
}
