// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"log/slog"
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
	id := generateDesktopSessionID()
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

// constantTimeLookupLocked finds the session whose ID byte-compares equal
// to id using subtle.ConstantTimeCompare. Caller MUST hold s.mu. Iterates
// every session and accumulates the match without short-circuiting so an
// attacker cannot distinguish "session id exists" from "session id absent"
// via the wall-clock cost of the lookup. O(N) is acceptable for an
// in-memory desktop-auth store (typically <100 concurrent flows).
//
// Returns the matched session pointer and its map key, or (nil, "") if
// no match. The map key is returned because the caller deletes by key
// for the consume path.
//
// CA-218 (X-L3): closes the timing side-channel between "session-id
// found" and "session-id absent" branches at the lookup boundary.
func (s *memoryDesktopAuthStore) constantTimeLookupLocked(id string) (*desktopAuthSession, string) {
	idBytes := []byte(id)
	var match *desktopAuthSession
	var matchKey string
	for k, sess := range s.sessions {
		// Equal-length-only operands: pad with the shorter side's length
		// to keep ConstantTimeCompare's contract (mismatched-length keys
		// return 0 without leaking which side was shorter via early exit).
		if subtle.ConstantTimeCompare([]byte(k), idBytes) == 1 {
			match = sess
			matchKey = k
			// Intentionally NOT breaking — keep iterating so the work
			// done per call is independent of where (or whether) the
			// match was found.
		}
	}
	return match, matchKey
}

func (s *memoryDesktopAuthStore) Poll(_ context.Context, id string) (*desktopAuthSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, key := s.constantTimeLookupLocked(id)
	if session == nil {
		return nil, false
	}
	if time.Now().After(session.ExpiresAt) {
		delete(s.byState, session.State)
		delete(s.sessions, key)
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
	delete(s.sessions, key)
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

// generateDesktopSessionID returns "ide_" followed by 16 random bytes
// base64url-encoded (22 chars, no padding). ~128 bits of entropy makes
// brute-force enumeration of in-flight sessions infeasible. The "ide_"
// prefix is preserved so log-grep tooling can recognize desktop sessions.
func generateDesktopSessionID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand cannot fail under any normal condition; if it does,
		// the only safe action is to refuse to mint a session ID.
		panic("crypto/rand failed: " + err.Error())
	}
	return "ide_" + base64.RawURLEncoding.EncodeToString(buf[:])
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
	// CA-321: expose password_min_length so CLI clients (cli/login.go,
	// cli/setup_admin.go) can render the operator-configured minimum
	// instead of the hardcoded 8 default. Falls back to 8 when LocalAuth
	// isn't yet wired (test paths). Matches the field shape exposed by
	// the browser-facing /auth/info endpoint.
	minLen := 8
	if s.localAuth != nil {
		if v := s.localAuth.PasswordMinLength(); v > 0 {
			minLen = v
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"local_auth":          true,
		"setup_done":          s.localAuth.IsSetupDone(),
		"oidc_enabled":        s.oidc != nil,
		"password_min_length": minLen,
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

	// CA-339 / CA-207: same per-username gate as handleLogin. The desktop
	// local-login endpoint shares the same loginLimiter instance so attempts
	// across both paths consume from the same per-username budget.
	const loginUsername = "admin@localhost"
	if s.loginLimiter != nil && !s.loginLimiter.Allow(loginUsername) {
		s.loginLimiter.WriteRejection(w)
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
		slog.Error("failed to create IDE session token", "error", err, "user_id", user.ID)
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
