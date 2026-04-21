// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

// Shared session storage for the MCP streamable-HTTP transport.
//
// Why this exists
// ---------------
// MCP's streamable-HTTP transport is request/response: `initialize` creates a
// session, the server returns `Mcp-Session-Id`, and subsequent tool calls
// present that header. Session state (claims, initialized flag, client info,
// last-used timestamp) was previously stored in a single-pod `sync.Map`, which
// broke HA: with 2+ API replicas behind a load balancer, `initialize` could
// land on pod A and `tools/call` on pod B — the second pod knows nothing about
// the session and returns "Invalid or expired session".
//
// The session store abstracts this. The memory implementation is the default
// and preserves the original single-pod behaviour with no external deps. The
// Redis implementation persists session state with a TTL so any replica can
// serve any streamable-HTTP request.
//
// Channel caveat
// --------------
// The legacy SSE transport owns a long-lived TCP connection on one pod and
// pushes events through `mcpSession.eventCh`. Channels can't cross pods, so
// SSE sessions still require pod-affinity (or sticky routing) even when Redis
// is configured. The session state stored here is channel-free; the
// in-process `localChans` map keeps the pod-local delivery channels for any
// SSE connection that happens to be anchored on this replica.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/db"
)

// mcpSessionState is the serializable slice of a session. Channels and
// sync.Once live in mcpSession, not here, because they're pod-local.
type mcpSessionState struct {
	ID            string    `json:"id"`
	UserID        string    `json:"uid"`
	OrgID         string    `json:"org,omitempty"`
	Email         string    `json:"email,omitempty"`
	Role          string    `json:"role,omitempty"`
	Initialized   bool      `json:"initialized"`
	ClientName    string    `json:"client_name,omitempty"`
	ClientVersion string    `json:"client_version,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	LastUsed      time.Time `json:"last_used"`
}

// mcpSessionStore persists session state, optionally across replicas.
//
// Save is create-or-update: callers compute the new state and push it.
// Get returns (nil, nil) on miss — no error — so callers can distinguish
// "missing" from "backend failure".
// Count is best-effort; Redis implementations may return 0 if counting
// would be expensive, and callers should treat the limit as a hint.
type mcpSessionStore interface {
	Save(ctx context.Context, s *mcpSessionState, ttl time.Duration) error
	Get(ctx context.Context, id string) (*mcpSessionState, error)
	Delete(ctx context.Context, id string) error
	Count(ctx context.Context) (int, error)
}

// ---------------------------------------------------------------------------
// Memory implementation
// ---------------------------------------------------------------------------

type memorySessionStore struct {
	mu       sync.RWMutex
	sessions map[string]memorySessionEntry
}

type memorySessionEntry struct {
	state     *mcpSessionState
	expiresAt time.Time
}

func newMemorySessionStore() *memorySessionStore {
	s := &memorySessionStore{sessions: make(map[string]memorySessionEntry)}
	// reapLoop prunes expired entries so Count stays accurate without a
	// read-path sweep. Runs for the lifetime of the process — acceptable
	// because the handler itself is process-scoped.
	go s.reapLoop()
	return s
}

func (s *memorySessionStore) Save(_ context.Context, st *mcpSessionState, ttl time.Duration) error {
	if st == nil || st.ID == "" {
		return errors.New("session state must have an ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var exp time.Time
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	s.sessions[st.ID] = memorySessionEntry{state: st, expiresAt: exp}
	return nil
}

func (s *memorySessionStore) Get(_ context.Context, id string) (*mcpSessionState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.sessions[id]
	if !ok {
		return nil, nil
	}
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		return nil, nil
	}
	// Return a copy so callers can mutate freely without data races.
	cp := *entry.state
	return &cp, nil
}

func (s *memorySessionStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
	return nil
}

func (s *memorySessionStore) Count(_ context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	n := 0
	for _, e := range s.sessions {
		if e.expiresAt.IsZero() || now.Before(e.expiresAt) {
			n++
		}
	}
	return n, nil
}

func (s *memorySessionStore) reapLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		s.mu.Lock()
		for id, e := range s.sessions {
			if !e.expiresAt.IsZero() && now.After(e.expiresAt) {
				delete(s.sessions, id)
			}
		}
		s.mu.Unlock()
	}
}

// ---------------------------------------------------------------------------
// Redis (cache-backed) implementation
// ---------------------------------------------------------------------------

const redisSessionPrefix = "mcp:session:"

// redisSessionStore uses db.Cache (backed by Redis) to persist session state
// with a TTL so any replica can resume a streamable-HTTP session.
//
// Count is best-effort and always returns 0 here — the cache interface is
// intentionally narrow (Get/Set/Delete only) and adding SCAN would leak a
// Redis-specific concept into every implementation. maxSessions enforcement
// is a soft limit anyway; use upstream rate-limiting for hard caps.
type redisSessionStore struct {
	cache db.Cache
}

func newRedisSessionStore(cache db.Cache) *redisSessionStore {
	return &redisSessionStore{cache: cache}
}

func (s *redisSessionStore) Save(ctx context.Context, st *mcpSessionState, ttl time.Duration) error {
	if st == nil || st.ID == "" {
		return errors.New("session state must have an ID")
	}
	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("marshal session state: %w", err)
	}
	return s.cache.Set(ctx, redisSessionPrefix+st.ID, string(data), ttl)
}

func (s *redisSessionStore) Get(ctx context.Context, id string) (*mcpSessionState, error) {
	val, err := s.cache.Get(ctx, redisSessionPrefix+id)
	if err != nil {
		return nil, err
	}
	if val == "" {
		return nil, nil
	}
	var st mcpSessionState
	if err := json.Unmarshal([]byte(val), &st); err != nil {
		return nil, fmt.Errorf("unmarshal session state: %w", err)
	}
	return &st, nil
}

func (s *redisSessionStore) Delete(ctx context.Context, id string) error {
	return s.cache.Delete(ctx, redisSessionPrefix+id)
}

func (s *redisSessionStore) Count(_ context.Context) (int, error) {
	return 0, nil
}
