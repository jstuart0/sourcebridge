// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"crypto/subtle"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/sourcebridge/sourcebridge/internal/auth"
)

// newTestJWTMgr returns a JWTManager with test credentials.
// Cookie names are derived from it — never use magic strings in tests.
func newTestJWTMgr(t *testing.T) *auth.JWTManager {
	t.Helper()
	// 64 hex chars = 32 raw bytes, passes the JWT Validate() ≥32-byte gate.
	const testSecret = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	return auth.NewJWTManager(testSecret, 60, "")
}

// okHandler is a trivial 200 handler used as the downstream in middleware tests.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
})

// ─────────────────────────────────────────────────────────────────────────────
// Existing tests — updated to three-param signature.
// ─────────────────────────────────────────────────────────────────────────────

// TestCSRFTokenCompareIsConstantTime verifies that the CSRF middleware uses
// crypto/subtle.ConstantTimeCompare for token validation rather than a
// plain string equality check. The test operates at the function level —
// it does not attempt to measure real timing (timing tests are flaky in CI).
func TestCSRFTokenCompareIsConstantTime(t *testing.T) {
	t.Run("mismatched tokens of same length return 0", func(t *testing.T) {
		a := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		b := []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
		if subtle.ConstantTimeCompare(a, b) != 0 {
			t.Error("expected ConstantTimeCompare to return 0 for unequal tokens")
		}
	})

	t.Run("identical tokens return 1", func(t *testing.T) {
		tok := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		if subtle.ConstantTimeCompare(tok, tok) != 1 {
			t.Error("expected ConstantTimeCompare to return 1 for equal tokens")
		}
	})
}

// TestCSRFMiddlewareRejectsMismatch verifies the middleware rejects a request
// whose CSRF header token does not match the cookie token.
func TestCSRFMiddlewareRejectsMismatch(t *testing.T) {
	jwtMgr := newTestJWTMgr(t)
	mw := csrfProtectionWithName(jwtMgr.CSRFCookieName(), jwtMgr.SessionCookieName(), true)
	handler := mw(okHandler)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/something", nil)
	req.AddCookie(&http.Cookie{Name: jwtMgr.CSRFCookieName(), Value: "correct-token-value"})
	req.Header.Set(csrfTokenHeader, "wrong-token-value")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for mismatched CSRF token, got %d", rr.Code)
	}
	wantBody := `{"error":"csrf_token_mismatch"}`
	if got := rr.Body.String(); got != wantBody {
		t.Errorf("expected body %q, got %q", wantBody, got)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}
}

// TestCSRFMiddlewareAcceptsMatch verifies the middleware passes a request
// whose CSRF header token matches the cookie token exactly.
func TestCSRFMiddlewareAcceptsMatch(t *testing.T) {
	jwtMgr := newTestJWTMgr(t)
	mw := csrfProtectionWithName(jwtMgr.CSRFCookieName(), jwtMgr.SessionCookieName(), true)
	handler := mw(okHandler)

	token := "matching-csrf-token-value"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/something", nil)
	req.AddCookie(&http.Cookie{Name: jwtMgr.CSRFCookieName(), Value: token})
	req.Header.Set(csrfTokenHeader, token)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK for matching CSRF token, got %d", rr.Code)
	}
}

// TestCSRFMiddlewareRejectsMissingHeader verifies the middleware rejects a
// request that has a valid cookie but no CSRF header.
func TestCSRFMiddlewareRejectsMissingHeader(t *testing.T) {
	jwtMgr := newTestJWTMgr(t)
	mw := csrfProtectionWithName(jwtMgr.CSRFCookieName(), jwtMgr.SessionCookieName(), true)
	handler := mw(okHandler)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/something", nil)
	req.AddCookie(&http.Cookie{Name: jwtMgr.CSRFCookieName(), Value: "some-token"})
	// deliberately no X-CSRF-Token header

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for missing CSRF header, got %d", rr.Code)
	}
	wantBody := `{"error":"csrf_token_mismatch"}`
	if got := rr.Body.String(); got != wantBody {
		t.Errorf("expected body %q, got %q", wantBody, got)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}
}

// TestCSRFMiddlewareSkipsBearerAuth is DELETED — the one-arg
// csrfProtectionWithName(cookieName) form no longer exists.
// Its coverage is replaced by TestCSRFFlagOff_PreservesTodayBehavior
// and TestCSRFBearerWithoutSessionCookieSkipsCheck below.

// ─────────────────────────────────────────────────────────────────────────────
// New tests — CA-198 + CA-201 matrix.
// ─────────────────────────────────────────────────────────────────────────────

// TestCSRFFlagOff_PreservesTodayBehavior pins the kill-switch invariant:
// when fullCoverage=false, any Bearer header bypasses CSRF exactly as before
// (even if a session cookie is also present).
func TestCSRFFlagOff_PreservesTodayBehavior(t *testing.T) {
	jwtMgr := newTestJWTMgr(t)
	mw := csrfProtectionWithName(jwtMgr.CSRFCookieName(), jwtMgr.SessionCookieName(), false)
	handler := mw(okHandler)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/something", nil)
	req.Header.Set("Authorization", "Bearer some-api-token")
	// session cookie present — would block under fullCoverage=true, but not here.
	req.AddCookie(&http.Cookie{Name: jwtMgr.SessionCookieName(), Value: "sess-value"})
	// deliberately no CSRF header

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("fullCoverage=false: expected 200 for Bearer (today's behavior), got %d", rr.Code)
	}
}

// TestCSRFBearerWithSessionCookieRequiresToken verifies that when
// fullCoverage=true, Bearer + session cookie + no X-CSRF-Token → 403.
func TestCSRFBearerWithSessionCookieRequiresToken(t *testing.T) {
	jwtMgr := newTestJWTMgr(t)
	mw := csrfProtectionWithName(jwtMgr.CSRFCookieName(), jwtMgr.SessionCookieName(), true)
	handler := mw(okHandler)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/llm/server-drain", nil)
	req.Header.Set("Authorization", "Bearer some-api-token")
	req.AddCookie(&http.Cookie{Name: jwtMgr.SessionCookieName(), Value: "sess-value"})
	// no X-CSRF-Token header

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("fullCoverage=true, Bearer+session, no CSRF header: expected 403, got %d", rr.Code)
	}
}

// TestCSRFBearerWithoutSessionCookieSkipsCheck verifies that when
// fullCoverage=true, Bearer + no session cookie + no CSRF header → 200
// (genuine API-client path: CLI, MCP, VS Code extension).
func TestCSRFBearerWithoutSessionCookieSkipsCheck(t *testing.T) {
	jwtMgr := newTestJWTMgr(t)
	mw := csrfProtectionWithName(jwtMgr.CSRFCookieName(), jwtMgr.SessionCookieName(), true)
	handler := mw(okHandler)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/llm/server-drain", nil)
	req.Header.Set("Authorization", "Bearer some-api-token")
	// no session cookie, no CSRF header

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("fullCoverage=true, Bearer only (no session): expected 200, got %d", rr.Code)
	}
}

// TestCSRFBearerWithSessionCookieAndMismatchedToken verifies that when
// fullCoverage=true, Bearer + session cookie + mismatched CSRF header → 403.
// This closes the fall-through regression where Bearer might short-circuit
// the token comparison.
func TestCSRFBearerWithSessionCookieAndMismatchedToken(t *testing.T) {
	jwtMgr := newTestJWTMgr(t)
	mw := csrfProtectionWithName(jwtMgr.CSRFCookieName(), jwtMgr.SessionCookieName(), true)
	handler := mw(okHandler)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/llm/server-drain", nil)
	req.Header.Set("Authorization", "Bearer some-api-token")
	req.AddCookie(&http.Cookie{Name: jwtMgr.SessionCookieName(), Value: "sess-value"})
	req.AddCookie(&http.Cookie{Name: jwtMgr.CSRFCookieName(), Value: "correct-csrf-token"})
	req.Header.Set(csrfTokenHeader, "wrong-csrf-token")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("fullCoverage=true, Bearer+session+mismatched CSRF: expected 403, got %d", rr.Code)
	}
}

// TestCSRFCookieNamesAreDistinctByName pins the cookie-name distinction invariant
// using real JWTManager cookie names (not magic strings).
//
//   - Sub-test A: Bearer + CSRF cookie only (no session cookie), no header → 200
//     (API-client shaped; bypass fires because the named session cookie is absent).
//   - Sub-test B: Bearer + session cookie only, no header → 403
//     (browser shaped; bypass blocked because the session cookie is present).
func TestCSRFCookieNamesAreDistinctByName(t *testing.T) {
	jwtMgr := newTestJWTMgr(t)
	mw := csrfProtectionWithName(jwtMgr.CSRFCookieName(), jwtMgr.SessionCookieName(), true)
	handler := mw(okHandler)

	t.Run("CSRF cookie only → bypass fires (API client)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/something", nil)
		req.Header.Set("Authorization", "Bearer api-token")
		// CSRF cookie present but that is NOT the session cookie.
		req.AddCookie(&http.Cookie{Name: jwtMgr.CSRFCookieName(), Value: "csrf-val"})
		// no session cookie, no X-CSRF-Token header

		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("expected 200 (no session cookie → API-client bypass), got %d", rr.Code)
		}
	})

	t.Run("session cookie only → bypass blocked (browser shaped)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/something", nil)
		req.Header.Set("Authorization", "Bearer api-token")
		// session cookie present — this is the browser fingerprint.
		req.AddCookie(&http.Cookie{Name: jwtMgr.SessionCookieName(), Value: "session-val"})
		// no CSRF cookie, no X-CSRF-Token header

		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("expected 403 (session cookie → browser-shaped, blocked), got %d", rr.Code)
		}
	})
}

// TestCSRFAdminRouteGroupGated verifies the middleware applied to a chi router
// correctly gates admin-group routes using real JWTManager cookie names.
func TestCSRFAdminRouteGroupGated(t *testing.T) {
	jwtMgr := newTestJWTMgr(t)

	r := chi.NewRouter()
	r.Use(csrfProtectionWithName(jwtMgr.CSRFCookieName(), jwtMgr.SessionCookieName(), true))
	r.Post("/api/v1/admin/llm/server-drain", okHandler.ServeHTTP)

	t.Run("Bearer+session+no CSRF → 403", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/llm/server-drain", nil)
		req.Header.Set("Authorization", "Bearer api-token")
		req.AddCookie(&http.Cookie{Name: jwtMgr.SessionCookieName(), Value: "sess"})

		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("expected 403 for admin route with no CSRF token, got %d", rr.Code)
		}
	})

	t.Run("Bearer+session+matching CSRF → 200", func(t *testing.T) {
		const tok = "valid-csrf-token"
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/llm/server-drain", nil)
		req.Header.Set("Authorization", "Bearer api-token")
		req.AddCookie(&http.Cookie{Name: jwtMgr.SessionCookieName(), Value: "sess"})
		req.AddCookie(&http.Cookie{Name: jwtMgr.CSRFCookieName(), Value: tok})
		req.Header.Set(csrfTokenHeader, tok)

		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("expected 200 for admin route with valid CSRF token, got %d", rr.Code)
		}
	})
}

// TestCSRFFlagOffSecondGroupNotGated pins the deploy-safety invariant:
// with fullCoverage=false (flag off), a route protected only by the second-group
// middleware should pass even when Bearer + session cookie + no CSRF header
// are all present. This proves that adding the Phase 1 CSRF header to frontend
// requests cannot break anything when the flag is still off.
func TestCSRFFlagOffSecondGroupNotGated(t *testing.T) {
	jwtMgr := newTestJWTMgr(t)

	// Simulate a "flag off" second-group route — no CSRF middleware at all.
	r := chi.NewRouter()
	// Flag is off → no r.Use(csrfProtection...) on this group.
	r.Post("/api/v1/admin/config", okHandler.ServeHTTP)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/config", nil)
	req.Header.Set("Authorization", "Bearer api-token")
	req.AddCookie(&http.Cookie{Name: jwtMgr.SessionCookieName(), Value: "sess"})
	// Phase 1 frontend sends this header on all requests; pin the contract that
	// it is harmless on ungated routes (flag off = no middleware on this group).
	req.Header.Set("X-CSRF-Token", "phase1-extra-header-must-be-harmless")

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("CSRFFullCoverageEnabled=false: second-group route should be ungated, got %d", rr.Code)
	}

	// Also verify: with fullCoverage=true middleware on the same route, the same
	// request gets 403 — this locks both directions of the flag.
	r2 := chi.NewRouter()
	r2.Use(csrfProtectionWithName(jwtMgr.CSRFCookieName(), jwtMgr.SessionCookieName(), true))
	r2.Post("/api/v1/admin/config", okHandler.ServeHTTP)

	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/admin/config", nil)
	req2.Header.Set("Authorization", "Bearer api-token")
	req2.AddCookie(&http.Cookie{Name: jwtMgr.SessionCookieName(), Value: "sess"})

	rr2 := httptest.NewRecorder()
	r2.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusForbidden {
		t.Errorf("CSRFFullCoverageEnabled=true: second-group route should be gated, got %d", rr2.Code)
	}
}

// TestCSRFCookieAuthRequiresToken verifies the non-Bearer path:
// cookie-only auth (no Authorization header) must include a valid CSRF token.
func TestCSRFCookieAuthRequiresToken(t *testing.T) {
	jwtMgr := newTestJWTMgr(t)
	mw := csrfProtectionWithName(jwtMgr.CSRFCookieName(), jwtMgr.SessionCookieName(), true)
	handler := mw(okHandler)

	const tok = "valid-csrf-token"

	t.Run("cookie auth + valid CSRF header → 200", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/something", nil)
		req.AddCookie(&http.Cookie{Name: jwtMgr.SessionCookieName(), Value: "sess"})
		req.AddCookie(&http.Cookie{Name: jwtMgr.CSRFCookieName(), Value: tok})
		req.Header.Set(csrfTokenHeader, tok)

		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("expected 200 for cookie auth + valid CSRF, got %d", rr.Code)
		}
	})

	t.Run("cookie auth + missing CSRF header → 403", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/something", nil)
		req.AddCookie(&http.Cookie{Name: jwtMgr.SessionCookieName(), Value: "sess"})
		req.AddCookie(&http.Cookie{Name: jwtMgr.CSRFCookieName(), Value: tok})

		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("expected 403 for cookie auth + missing CSRF header, got %d", rr.Code)
		}
	})

	t.Run("cookie auth + mismatched CSRF header → 403", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/something", nil)
		req.AddCookie(&http.Cookie{Name: jwtMgr.SessionCookieName(), Value: "sess"})
		req.AddCookie(&http.Cookie{Name: jwtMgr.CSRFCookieName(), Value: tok})
		req.Header.Set(csrfTokenHeader, "wrong-token")

		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("expected 403 for cookie auth + mismatched CSRF header, got %d", rr.Code)
		}
	})
}
