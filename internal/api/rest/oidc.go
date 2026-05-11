// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/sourcebridge/sourcebridge/internal/auth"
)

func (s *Server) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "OIDC not configured"})
		return
	}

	url, _, err := s.oidc.AuthorizationURL(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate auth URL"})
		return
	}

	http.Redirect(w, r, url, http.StatusFound)
}

func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "OIDC not configured"})
		return
	}

	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		// CA-208: scrub IdP-supplied error from the browser response to avoid leaking
		// internal error strings or encoded IdP details. Log the full message server-side
		// with a correlation ID so operators can trace the failure without exposing it.
		desc := r.URL.Query().Get("error_description")
		corrID := uuid.New().String()
		slog.Warn("OIDC callback: IdP returned error", "correlation_id", corrID, "error", errMsg, "description", desc)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "authentication_failed", "correlation_id": corrID})
		return
	}

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	if code == "" || state == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing code or state"})
		return
	}

	token, err := s.oidc.Exchange(r.Context(), state, code)
	if err != nil {
		// CA-208: scrub the exchange error — may contain IdP internals (token endpoint URLs,
		// partial credentials, server-side error messages). Log full detail server-side.
		corrID := uuid.New().String()
		slog.Warn("OIDC callback: token exchange failed", "correlation_id", corrID, "error", err.Error())
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication_failed", "correlation_id": corrID})
		return
	}

	setSessionCookie(w, s.jwtMgr.SessionCookieName(), token, s.cfg.Security.JWTTTLMinutes)

	if s.desktopAuth != nil {
		if session, ok := s.desktopAuth.LookupByState(r.Context(), state); ok && session != nil {
			claims, err := s.jwtMgr.ValidateToken(token)
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid session token"})
				return
			}
			apiToken, _, err := s.tokenStore.CreateToken(r.Context(), auth.CreateTokenInput{
				Name:        "IDE OIDC Session",
				UserID:      claims.UserID,
				TenantID:    claims.OrgID,
				Kind:        auth.TokenKindIDESession,
				ClientType:  "desktop_ide",
				AuthMethod:  auth.AuthMethodOIDC,
				DeviceLabel: r.UserAgent(),
			})
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create IDE session"})
				return
			}
			s.desktopAuth.Complete(r.Context(), state, apiToken)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<html><body style="font-family: sans-serif; padding: 24px;"><h1>SourceBridge sign-in complete</h1><p>You can return to your IDE.</p></body></html>`))
			return
		}
	}

	// Redirect to the web UI after successful login
	http.Redirect(w, r, "/", http.StatusFound)
}
