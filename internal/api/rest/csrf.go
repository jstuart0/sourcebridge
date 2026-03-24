// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/sourcebridge/sourcebridge/internal/auth"
)

const (
	csrfTokenHeader = "X-CSRF-Token"
	csrfTokenLength = 32
)

// csrfProtectionWithName returns CSRF middleware that uses the given cookie name.
func csrfProtectionWithName(cookieName string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Safe methods don't need CSRF protection
			if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
				ensureCSRFCookie(w, r, cookieName)
				next.ServeHTTP(w, r)
				return
			}

			// If the request uses a Bearer token (not a cookie), skip CSRF check
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
				next.ServeHTTP(w, r)
				return
			}

			// Cookie-authenticated requests must include a matching CSRF token
			cookie, err := r.Cookie(cookieName)
			if err != nil {
				http.Error(w, `{"error":"CSRF token missing"}`, http.StatusForbidden)
				return
			}

			headerToken := r.Header.Get(csrfTokenHeader)
			if headerToken == "" || headerToken != cookie.Value {
				http.Error(w, `{"error":"CSRF token mismatch"}`, http.StatusForbidden)
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

// CSRFTokenEndpoint returns a fresh CSRF token for the authenticated user.
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

	writeJSON(w, http.StatusOK, map[string]string{"csrf_token": token})
}
