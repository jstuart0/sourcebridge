// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package auth

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/surrealdb/surrealdb.go"
	"github.com/surrealdb/surrealdb.go/pkg/models"
)

// dbProvider gives access to the current SurrealDB connection handle,
// allowing the persister to pick up reconnected connections automatically.
type dbProvider interface {
	DB() *surrealdb.DB
}

// SurrealPersister stores the local admin user in SurrealDB.
// It uses a well-known record ID (ca_local_auth:admin) so the same record
// is loaded and updated across restarts.
type SurrealPersister struct {
	dbp dbProvider
}

// NewSurrealPersister creates a new SurrealDB auth persister.
// Accepts any type with a DB() method (typically *db.SurrealDB).
func NewSurrealPersister(dbp dbProvider) *SurrealPersister {
	return &SurrealPersister{dbp: dbp}
}

type surrealLocalUser struct {
	ID           *models.RecordID `json:"id,omitempty"`
	UserID       string           `json:"user_id"`
	Email        string           `json:"email"`
	Name         string           `json:"name"`
	PasswordHash string           `json:"password_hash"`
}

func (s *SurrealPersister) LoadUser() (*LocalUser, error) {
	db := s.dbp.DB()
	if db == nil {
		return nil, nil
	}
	ctx := context.Background()
	raw, err := surrealdb.Query[[]surrealLocalUser](ctx, db, "SELECT * FROM ca_local_auth WHERE id = type::thing('ca_local_auth', 'admin') LIMIT 1", map[string]any{})
	if err != nil {
		slog.Debug("surreal auth load query failed", "error", err)
		return nil, nil // Treat as no user; table may not exist yet
	}

	if raw == nil || len(*raw) == 0 {
		return nil, nil
	}
	qr := (*raw)[0]
	if qr.Error != nil || len(qr.Result) == 0 {
		return nil, nil
	}

	u := qr.Result[0]
	return &LocalUser{
		ID:           u.UserID,
		Email:        u.Email,
		Name:         u.Name,
		PasswordHash: u.PasswordHash,
	}, nil
}

func (s *SurrealPersister) SaveUser(user *LocalUser) error {
	if user == nil {
		return fmt.Errorf("nil user")
	}

	db := s.dbp.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}

	ctx := context.Background()

	// Ensure the table exists (idempotent)
	_, err := surrealdb.Query[interface{}](ctx, db, `
		DEFINE TABLE IF NOT EXISTS ca_local_auth SCHEMAFULL;
		DEFINE FIELD IF NOT EXISTS user_id ON ca_local_auth TYPE string;
		DEFINE FIELD IF NOT EXISTS email ON ca_local_auth TYPE string;
		DEFINE FIELD IF NOT EXISTS name ON ca_local_auth TYPE string;
		DEFINE FIELD IF NOT EXISTS password_hash ON ca_local_auth TYPE string;
	`, map[string]any{})
	if err != nil {
		slog.Warn("failed to ensure ca_local_auth table", "error", err)
		// Continue; the table might already exist
	}

	// Upsert with well-known ID
	_, err = surrealdb.Query[interface{}](ctx, db,
		"UPSERT type::thing('ca_local_auth', 'admin') SET user_id = $user_id, email = $email, name = $name, password_hash = $password_hash",
		map[string]any{
			"user_id":       user.ID,
			"email":         user.Email,
			"name":          user.Name,
			"password_hash": user.PasswordHash,
		},
	)
	if err != nil {
		return fmt.Errorf("persisting user: %w", err)
	}

	return nil
}
