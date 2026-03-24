// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/surrealdb/surrealdb.go"
)

// SurrealGitConfigStore persists git configuration (tokens, SSH paths) in
// SurrealDB using a well-known record ID, following the same pattern as
// auth.SurrealPersister.
type SurrealGitConfigStore struct {
	client *SurrealDB
}

// NewSurrealGitConfigStore creates a new git config store backed by SurrealDB.
func NewSurrealGitConfigStore(client *SurrealDB) *SurrealGitConfigStore {
	return &SurrealGitConfigStore{client: client}
}

func (s *SurrealGitConfigStore) LoadGitConfig() (string, string, error) {
	db := s.client.DB()
	if db == nil {
		return "", "", nil
	}

	ctx := context.Background()

	// Query using []map[string]interface{} — the SurrealDB Go SDK has issues
	// with json.RawMessage and typed struct deserialization for this pattern.
	raw, err := surrealdb.Query[[]map[string]interface{}](ctx, db,
		"SELECT default_token, ssh_key_path FROM ca_git_config WHERE id = type::thing('ca_git_config', 'default') LIMIT 1",
		map[string]any{})
	if err != nil {
		slog.Warn("surreal git config load query failed", "error", err)
		return "", "", nil
	}

	if raw == nil || len(*raw) == 0 {
		return "", "", nil
	}

	qr := (*raw)[0]
	if qr.Error != nil {
		slog.Warn("git config load: query error", "error", fmt.Sprintf("%v", qr.Error))
		return "", "", nil
	}

	if len(qr.Result) == 0 {
		return "", "", nil
	}

	row := qr.Result[0]
	token, _ := row["default_token"].(string)
	sshKey, _ := row["ssh_key_path"].(string)
	return token, sshKey, nil
}

func (s *SurrealGitConfigStore) SaveGitConfig(token, sshKeyPath string) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}

	ctx := context.Background()

	// Ensure table exists (idempotent)
	_, err := surrealdb.Query[interface{}](ctx, db, `
		DEFINE TABLE IF NOT EXISTS ca_git_config SCHEMAFULL;
		DEFINE FIELD IF NOT EXISTS default_token ON ca_git_config TYPE string;
		DEFINE FIELD IF NOT EXISTS ssh_key_path ON ca_git_config TYPE string;
	`, map[string]any{})
	if err != nil {
		slog.Warn("failed to ensure ca_git_config table", "error", err)
	}

	// Upsert with well-known ID
	// NOTE: $token is a reserved SurrealDB variable — use $default_token instead.
	_, err = surrealdb.Query[interface{}](ctx, db,
		"UPSERT type::thing('ca_git_config', 'default') SET default_token = $default_token, ssh_key_path = $ssh_key_path",
		map[string]any{
			"default_token": token,
			"ssh_key_path":  sshKeyPath,
		},
	)
	if err != nil {
		return err
	}

	slog.Info("git config persisted to database")
	return nil
}
