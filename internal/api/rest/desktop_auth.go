// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/auth"
)

type desktopAuthSession struct {
	ID          string
	State       string
	Token       string
	ExpiresAt   time.Time
	ConsumedAt  *time.Time
	CompletedAt *time.Time
}

type DesktopAuthSessionStore interface {
	Create(ctx context.Context, state string) (*desktopAuthSession, error)
	Complete(ctx context.Context, state, token string) bool
	Poll(ctx context.Context, id string) (*desktopAuthSession, bool)
	LookupByState(ctx context.Context, state string) (*desktopAuthSession, bool)
	TTL() time.Duration
}

type memoryDesktopAuthStore struct {
	mu         sync.Mutex
	sessions   map[string]*desktopAuthSession
	byState    map[string]string
	nextID     int64
	sessionTTL time.Duration
}

func NewMemoryDesktopAuthStore() DesktopAuthSessionStore {
	return &memoryDesktopAuthStore{
		sessions:   make(map[string]*desktopAuthSession),
		byState:    make(map[string]string),
		sessionTTL: 10 * time.Minute,
	}
}

func (s *memoryDesktopAuthStore) TTL() time.Duration {
	return s.sessionTTL
}

func (s *memoryDesktopAuthStore) Create(_ context.Context, state string) (*desktopAuthSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	id := generateDesktopSessionID(s.nextID)
	session := &desktopAuthSession{
		ID:        id,
		State:     state,
		ExpiresAt: time.Now().Add(s.sessionTTL),
	}
	s.sessions[id] = session
	s.byState[state] = id
	return session, nil
}

func (s *memoryDesktopAuthStore) Complete(_ context.Context, state, token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byState[state]
	if !ok {
		return false
	}
	session := s.sessions[id]
	if session == nil || time.Now().After(session.ExpiresAt) || session.ConsumedAt != nil {
		delete(s.byState, state)
		delete(s.sessions, id)
		return false
	}
	now := time.Now()
	session.Token = token
	session.CompletedAt = &now
	return true
}

func (s *memoryDesktopAuthStore) Poll(_ context.Context, id string) (*desktopAuthSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[id]
	if session == nil {
		return nil, false
	}
	if time.Now().After(session.ExpiresAt) {
		delete(s.byState, session.State)
		delete(s.sessions, id)
		return nil, false
	}
	if session.Token == "" {
		cp := *session
		return &cp, true
	}
	if session.ConsumedAt != nil {
		return nil, false
	}
	now := time.Now()
	session.ConsumedAt = &now
	cp := *session
	delete(s.byState, session.State)
	delete(s.sessions, id)
	return &cp, true
}

func (s *memoryDesktopAuthStore) LookupByState(_ context.Context, state string) (*desktopAuthSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byState[state]
	if !ok {
		return nil, false
	}
	session := s.sessions[id]
	if session == nil || time.Now().After(session.ExpiresAt) || session.ConsumedAt != nil {
		delete(s.byState, state)
		delete(s.sessions, id)
		return nil, false
	}
	cp := *session
	return &cp, true
}

func generateDesktopSessionID(seed int64) string {
	return "ide_" + time.Now().UTC().Format("20060102150405") + "_" + time.Unix(seed, 0).UTC().Format("405")
}

type desktopLocalLoginRequest struct {
	Password  string `json:"password"`
	TokenName string `json:"token_name"`
}

type desktopOIDCStartResponse struct {
	SessionID string `json:"session_id"`
	AuthURL   string `json:"auth_url"`
	ExpiresIn int    `json:"expires_in"`
}

type desktopAuthPollResponse struct {
	Status    string `json:"status"`
	Token     string `json:"token,omitempty"`
	ExpiresIn int    `json:"expires_in,omitempty"`
}

func (s *Server) handleDesktopAuthInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"local_auth":   true,
		"setup_done":   s.localAuth.IsSetupDone(),
		"oidc_enabled": s.oidc != nil,
	})
}

func (s *Server) handleDesktopLocalLogin(w http.ResponseWriter, r *http.Request) {
	var req desktopLocalLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.Password == "" {
		http.Error(w, `{"error":"password is required"}`, http.StatusBadRequest)
		return
	}
	if _, err := s.localAuth.Login(req.Password); err != nil {
		if !s.localAuth.IsSetupDone() {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not set up"})
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}

	tokenName := req.TokenName
	if tokenName == "" {
		tokenName = "IDE Session"
	}
	user := s.localAuth.GetUser()
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not set up"})
		return
	}
	tokenStr, _, err := s.tokenStore.CreateToken(r.Context(), auth.CreateTokenInput{
		Name:        tokenName,
		UserID:      user.ID,
		Kind:        auth.TokenKindIDESession,
		ClientType:  "desktop_ide",
		AuthMethod:  auth.AuthMethodLocalPassword,
		DeviceLabel: r.UserAgent(),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create IDE session"})
		return
	}
	writeJSON(w, http.StatusOK, tokenResponse{
		Token:     tokenStr,
		ExpiresIn: 0,
	})
}

func (s *Server) handleDesktopOIDCStart(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "OIDC not configured"})
		return
	}
	authURL, state, err := s.oidc.AuthorizationURL(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate auth URL"})
		return
	}
	session, err := s.desktopAuth.Create(r.Context(), state)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create desktop auth session"})
		return
	}
	writeJSON(w, http.StatusOK, desktopOIDCStartResponse{
		SessionID: session.ID,
		AuthURL:   authURL,
		ExpiresIn: int(s.desktopAuth.TTL().Seconds()),
	})
}

func (s *Server) handleDesktopOIDCPoll(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("session_id")
	if id == "" {
		http.Error(w, `{"error":"session_id is required"}`, http.StatusBadRequest)
		return
	}
	session, ok := s.desktopAuth.Poll(r.Context(), id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found or expired"})
		return
	}
	if session.Token == "" {
		writeJSON(w, http.StatusOK, desktopAuthPollResponse{Status: "pending"})
		return
	}
	writeJSON(w, http.StatusOK, desktopAuthPollResponse{
		Status:    "complete",
		Token:     session.Token,
		ExpiresIn: 0,
	})
}
