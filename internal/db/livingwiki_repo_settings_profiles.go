// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"
	"fmt"

	"github.com/surrealdb/surrealdb.go"
)

// EnsureProfilesSchemaExtensions adds the `profile_id` field to the
// `living_wiki_llm_override` nested object on `lw_repo_settings` rows.
// When non-empty, the resolver uses the picked profile instead of the
// inline override fields. Slice 3 wires the read/write path; the
// schema-ensure lands in slice 1 so the field is always defined before
// any reader reaches it (codex-H4).
//
// Idempotent: re-running on a hot DB is a no-op.
func (s *LivingWikiRepoSettingsStore) EnsureProfilesSchemaExtensions(ctx context.Context) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	// DEFAULT '' so existing override rows that lack the field decode
	// cleanly without rejecting on TYPE string — same reasoning as
	// the active_profile_id field on ca_llm_config.
	_, err := surrealdb.Query[interface{}](ctx, db, `
		DEFINE FIELD IF NOT EXISTS living_wiki_llm_override.profile_id ON lw_repo_settings TYPE string DEFAULT '';
	`, map[string]any{})
	if err != nil {
		return fmt.Errorf("ensure lw_repo_settings profile-extensions schema: %w", err)
	}
	return nil
}
