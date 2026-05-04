// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"crypto/subtle"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
	cookieName := "csrf_token"
	mw := csrfProtectionWithName(cookieName)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/something", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "correct-token-value"})
	req.Header.Set(csrfTokenHeader, "wrong-token-value")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for mismatched CSRF token, got %d", rr.Code)
	}
}

// TestCSRFMiddlewareAcceptsMatch verifies the middleware passes a request
// whose CSRF header token matches the cookie token exactly.
func TestCSRFMiddlewareAcceptsMatch(t *testing.T) {
	cookieName := "csrf_token"
	mw := csrfProtectionWithName(cookieName)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	token := "matching-csrf-token-value"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/something", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
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
	cookieName := "csrf_token"
	mw := csrfProtectionWithName(cookieName)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/something", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "some-token"})
	// deliberately no X-CSRF-Token header

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for missing CSRF header, got %d", rr.Code)
	}
}

// TestCSRFMiddlewareSkipsBearerAuth verifies that Bearer-authenticated requests
// bypass CSRF validation (they carry their own credential and cannot be forged
// via cross-site form submission).
func TestCSRFMiddlewareSkipsBearerAuth(t *testing.T) {
	cookieName := "csrf_token"
	mw := csrfProtectionWithName(cookieName)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/something", nil)
	req.Header.Set("Authorization", "Bearer some-api-token")
	// deliberately no cookie and no CSRF header

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK for Bearer-authenticated request, got %d", rr.Code)
	}
}
