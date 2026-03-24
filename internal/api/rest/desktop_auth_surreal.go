// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	surrealdb "github.com/surrealdb/surrealdb.go"
	"github.com/surrealdb/surrealdb.go/pkg/models"
)

type surrealDBProvider interface {
	DB() *surrealdb.DB
}

type SurrealDesktopAuthStore struct {
	dbp        surrealDBProvider
	sessionTTL time.Duration
}

type surrealDesktopAuthSession struct {
	ID          *models.RecordID `json:"id,omitempty"`
	State       string           `json:"state"`
	Token       *string          `json:"token,omitempty"`
	ExpiresAt   time.Time        `json:"expires_at"`
	CompletedAt *time.Time       `json:"completed_at,omitempty"`
	ConsumedAt  *time.Time       `json:"consumed_at,omitempty"`
}

func NewSurrealDesktopAuthStore(dbp surrealDBProvider) *SurrealDesktopAuthStore {
	return &SurrealDesktopAuthStore{
		dbp:        dbp,
		sessionTTL: 10 * time.Minute,
	}
}

func (s *SurrealDesktopAuthStore) TTL() time.Duration {
	return s.sessionTTL
}

func (s *SurrealDesktopAuthStore) Create(ctx context.Context, state string) (*desktopAuthSession, error) {
	db := s.dbp.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}
	id, err := generateDesktopAuthRecordID()
	if err != nil {
		return nil, err
	}
	expiresAt := time.Now().Add(s.sessionTTL)
	_, err = surrealdb.Query[interface{}](ctx, db, `
		CREATE type::thing('ca_desktop_auth_session', $id) CONTENT {
			state: $state,
			token: NONE,
			expires_at: $expires_at,
			completed_at: NONE,
			consumed_at: NONE
		}
	`, map[string]any{
		"id":         id,
		"state":      state,
		"expires_at": expiresAt,
	})
	if err != nil {
		return nil, fmt.Errorf("creating desktop auth session: %w", err)
	}
	return &desktopAuthSession{ID: id, State: state, ExpiresAt: expiresAt}, nil
}

func generateDesktopAuthRecordID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *SurrealDesktopAuthStore) Complete(ctx context.Context, state, token string) bool {
	db := s.dbp.DB()
	if db == nil {
		return false
	}
	now := time.Now()
	raw, err := surrealdb.Query[[]surrealDesktopAuthSession](ctx, db, `
		UPDATE ca_desktop_auth_session
		SET token = $token, completed_at = $completed_at
		WHERE state = $state AND consumed_at = NONE AND expires_at >= $now AND token = NONE
		RETURN AFTER
	`, map[string]any{
		"state":        state,
		"token":        token,
		"completed_at": now,
		"now":          now,
	})
	if err != nil || raw == nil || len(*raw) == 0 || (*raw)[0].Error != nil || len((*raw)[0].Result) == 0 {
		return false
	}
	return true
}

func (s *SurrealDesktopAuthStore) Poll(ctx context.Context, id string) (*desktopAuthSession, bool) {
	db := s.dbp.DB()
	if db == nil {
		return nil, false
	}
	now := time.Now()
	ready, err := surrealdb.Query[[]surrealDesktopAuthSession](ctx, db, `
		UPDATE type::thing('ca_desktop_auth_session', $id)
		SET consumed_at = $consumed_at
		WHERE consumed_at = NONE AND expires_at >= $now AND token != NONE
		RETURN AFTER
	`, map[string]any{
		"id":          id,
		"consumed_at": now,
		"now":         now,
	})
	if err == nil && ready != nil && len(*ready) > 0 && (*ready)[0].Error == nil && len((*ready)[0].Result) > 0 {
		return desktopAuthSessionFromSurreal((*ready)[0].Result[0]), true
	}

	raw, err := surrealdb.Query[[]surrealDesktopAuthSession](ctx, db, `
		SELECT * FROM type::thing('ca_desktop_auth_session', $id)
		WHERE expires_at >= $now AND consumed_at = NONE
		LIMIT 1
	`, map[string]any{
		"id":  id,
		"now": now,
	})
	if err != nil || raw == nil || len(*raw) == 0 || (*raw)[0].Error != nil || len((*raw)[0].Result) == 0 {
		return nil, false
	}
	return desktopAuthSessionFromSurreal((*raw)[0].Result[0]), true
}

func (s *SurrealDesktopAuthStore) LookupByState(ctx context.Context, state string) (*desktopAuthSession, bool) {
	db := s.dbp.DB()
	if db == nil {
		return nil, false
	}
	now := time.Now()
	raw, err := surrealdb.Query[[]surrealDesktopAuthSession](ctx, db, `
		SELECT * FROM ca_desktop_auth_session
		WHERE state = $state AND expires_at >= $now AND consumed_at = NONE
		LIMIT 1
	`, map[string]any{
		"state": state,
		"now":   now,
	})
	if err != nil || raw == nil || len(*raw) == 0 || (*raw)[0].Error != nil || len((*raw)[0].Result) == 0 {
		return nil, false
	}
	return desktopAuthSessionFromSurreal((*raw)[0].Result[0]), true
}

func (s *SurrealDesktopAuthStore) Cleanup(ctx context.Context) (int, error) {
	db := s.dbp.DB()
	if db == nil {
		return 0, fmt.Errorf("database not connected")
	}
	now := time.Now()
	raw, err := surrealdb.Query[[]map[string]any](ctx, db, `
		DELETE ca_desktop_auth_session
		WHERE consumed_at != NONE OR expires_at < $now
		RETURN BEFORE
	`, map[string]any{
		"now": now,
	})
	if err != nil {
		return 0, fmt.Errorf("cleaning desktop auth sessions: %w", err)
	}
	if raw == nil || len(*raw) == 0 || (*raw)[0].Error != nil {
		return 0, nil
	}
	return len((*raw)[0].Result), nil
}

func desktopAuthSessionFromSurreal(record surrealDesktopAuthSession) *desktopAuthSession {
	if record.ID == nil {
		return nil
	}
	session := &desktopAuthSession{
		ID:          fmt.Sprint(record.ID.ID),
		State:       record.State,
		ExpiresAt:   record.ExpiresAt,
		CompletedAt: record.CompletedAt,
		ConsumedAt:  record.ConsumedAt,
	}
	if record.Token != nil {
		session.Token = *record.Token
	}
	return session
}
