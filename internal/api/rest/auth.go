// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/events"
)

type setupRequest struct {
	Password string `json:"password"`
}

type loginRequest struct {
	Password string `json:"password"`
}

type changePasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

type tokenResponse struct {
	Token     string `json:"token"`
	ExpiresIn int    `json:"expires_in"`
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	if _, err := s.localAuth.Setup(req.Password); err != nil {
		status := http.StatusBadRequest
		if s.localAuth.IsSetupDone() {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	// Auto-login after setup
	token, err := s.localAuth.Login(req.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "setup succeeded but login failed"})
		return
	}

	setSessionCookie(w, s.jwtMgr.SessionCookieName(), token, s.cfg.Security.JWTTTLMinutes)
	writeJSON(w, http.StatusOK, tokenResponse{
		Token:     token,
		ExpiresIn: s.cfg.Security.JWTTTLMinutes * 60,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	token, err := s.localAuth.Login(req.Password)
	if err != nil {
		if !s.localAuth.IsSetupDone() {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not set up"})
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}

	setSessionCookie(w, s.jwtMgr.SessionCookieName(), token, s.cfg.Security.JWTTTLMinutes)
	writeJSON(w, http.StatusOK, tokenResponse{
		Token:     token,
		ExpiresIn: s.cfg.Security.JWTTTLMinutes * 60,
	})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req changePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	if err := s.localAuth.ChangePassword(req.OldPassword, req.NewPassword); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "password changed"})
}

func setSessionCookie(w http.ResponseWriter, cookieName, token string, ttlMinutes int) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   ttlMinutes * 60,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Clear the session cookie
	http.SetCookie(w, &http.Cookie{
		Name:     s.jwtMgr.SessionCookieName(),
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

func (s *Server) handleRevokeCurrentToken(w http.ResponseWriter, r *http.Request) {
	apiToken := auth.GetAPIToken(r.Context())
	if apiToken == nil || apiToken.ID == "" {
		http.Error(w, `{"error":"current request is not authenticated with an API token"}`, http.StatusBadRequest)
		return
	}
	ok, err := s.tokenStore.RevokeToken(r.Context(), apiToken.ID)
	if err != nil {
		http.Error(w, `{"error":"failed to revoke current token"}`, http.StatusInternalServerError)
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]string{"status": "already revoked"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "revoked",
		"token_id":   apiToken.ID,
		"user_id":    apiToken.UserID,
		"tenant_id":  apiToken.TenantID,
		"token_kind": apiToken.Kind,
	})
}

func (s *Server) handleCurrentToken(w http.ResponseWriter, r *http.Request) {
	apiToken := auth.GetAPIToken(r.Context())
	if apiToken == nil || apiToken.ID == "" {
		http.Error(w, `{"error":"current request is not authenticated with an API token"}`, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, apiToken)
}

func (s *Server) handleAuthInfo(w http.ResponseWriter, r *http.Request) {
	info := map[string]interface{}{
		"local_auth":   true,
		"setup_done":   s.localAuth.IsSetupDone(),
		"oidc_enabled": s.oidc != nil,
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Send initial connected event
	w.Write([]byte("event: connected\ndata: {\"status\":\"connected\"}\n\n"))
	flusher.Flush()

	// Subscribe to all events via wildcard
	ch := make(chan events.Event, 100)
	s.eventBus.Subscribe("*", func(e events.Event) {
		select {
		case ch <- e:
		default: // drop if buffer full
		}
	})

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case event := <-ch:
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, data)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
