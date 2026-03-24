// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/surrealdb/surrealdb.go"
	"github.com/surrealdb/surrealdb.go/pkg/models"
)

type OIDCSurrealStateStore struct {
	dbp tokenDBProvider
}

type surrealOIDCState struct {
	ID         *models.RecordID `json:"id,omitempty"`
	State      string           `json:"state"`
	ExpiresAt  time.Time        `json:"expires_at"`
	ConsumedAt *time.Time       `json:"consumed_at,omitempty"`
}

func NewSurrealOIDCStateStore(dbp tokenDBProvider) *OIDCSurrealStateStore {
	return &OIDCSurrealStateStore{dbp: dbp}
}

func (s *OIDCSurrealStateStore) SaveState(ctx context.Context, state string, expiresAt time.Time) error {
	db := s.dbp.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	_, err := surrealdb.Query[interface{}](ctx, db, `
		CREATE type::thing('ca_oidc_state', $state) CONTENT {
			state: $state,
			expires_at: $expires_at,
			consumed_at: NONE
		}
	`, map[string]any{
		"state":      state,
		"expires_at": expiresAt,
	})
	if err != nil {
		return fmt.Errorf("saving oidc state: %w", err)
	}
	return nil
}

func (s *OIDCSurrealStateStore) ConsumeState(ctx context.Context, state string) (bool, error) {
	db := s.dbp.DB()
	if db == nil {
		return false, fmt.Errorf("database not connected")
	}
	now := time.Now()
	raw, err := surrealdb.Query[[]surrealOIDCState](ctx, db, `
		UPDATE type::thing('ca_oidc_state', $state)
		SET consumed_at = $consumed_at
		WHERE consumed_at = NONE AND expires_at >= $now
		RETURN AFTER
	`, map[string]any{
		"state":       state,
		"consumed_at": now,
		"now":         now,
	})
	if err != nil {
		return false, fmt.Errorf("consuming oidc state: %w", err)
	}
	if raw == nil || len(*raw) == 0 || (*raw)[0].Error != nil || len((*raw)[0].Result) == 0 {
		return false, nil
	}
	return true, nil
}

func (s *OIDCSurrealStateStore) Cleanup(ctx context.Context) (int, error) {
	db := s.dbp.DB()
	if db == nil {
		return 0, fmt.Errorf("database not connected")
	}
	now := time.Now()
	raw, err := surrealdb.Query[[]map[string]any](ctx, db, `
		DELETE ca_oidc_state
		WHERE consumed_at != NONE OR expires_at < $now
		RETURN BEFORE
	`, map[string]any{
		"now": now,
	})
	if err != nil {
		return 0, fmt.Errorf("cleaning oidc states: %w", err)
	}
	if raw == nil || len(*raw) == 0 || (*raw)[0].Error != nil {
		return 0, nil
	}
	return len((*raw)[0].Result), nil
}
