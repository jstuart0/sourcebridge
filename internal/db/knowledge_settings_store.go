// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	surrealdb "github.com/surrealdb/surrealdb.go"
	"github.com/surrealdb/surrealdb.go/pkg/models"
)

// KnowledgeSettingsStore persists the operator-tunable knowledge-RPC
// safety-net timeout (CA-122). Single-row table; the singleton is
// keyed `ca_knowledge_settings:current`.
//
// The contract this store supports:
//
//   - Get(): returns the current row's repository_timeout_secs, or
//     ErrKnowledgeSettingsNotFound if no row has been seeded yet.
//     Callers (cli/serve.go's knowledgeTimeoutProvider closure) treat
//     "not found" as "use the boot-time env-default fallback."
//   - Put(secs, updatedBy): writes the new value. Validates the
//     range [1800, 86400] (30 min - 24 h) BEFORE writing -- the
//     write side rejects out-of-range; the read side defensively
//     clamps as a defense-in-depth guard. Codex r1 L1.
//
// Failure semantics: a Surreal outage causes both Get and Put to
// return an error. The provider closure in serve.go uses a 5s
// in-memory cache + last-known-good fallback so a transient outage
// does not prevent new RPC dispatch.
type KnowledgeSettingsStore struct {
	db *SurrealDB
}

// NewKnowledgeSettingsStore wires the store to a live SurrealDB.
func NewKnowledgeSettingsStore(db *SurrealDB) *KnowledgeSettingsStore {
	return &KnowledgeSettingsStore{db: db}
}

// ErrKnowledgeSettingsNotFound is returned by Get when the singleton
// row has never been seeded. Boot-time logic in serve.go seeds it on
// the first observation of "no row exists" and uses the env default
// (SOURCEBRIDGE_KNOWLEDGE_TIMEOUT_SECS_DEFAULT, default 14400 = 4h).
var ErrKnowledgeSettingsNotFound = errors.New("knowledge settings row not found")

// Range constraints. Codex r1 M3 / L1: write-side reject; read-side
// defensive clamp.
const (
	KnowledgeTimeoutMinSecs     = 1800  // 30 minutes
	KnowledgeTimeoutMaxSecs     = 86400 // 24 hours
	KnowledgeTimeoutDefaultSecs = 14400 // 4 hours
)

// ErrKnowledgeTimeoutOutOfRange is returned by Put when the supplied
// value is below the minimum (30 min) or above the maximum (24 h).
// The REST handler maps this to HTTP 400 with a structured error.
var ErrKnowledgeTimeoutOutOfRange = errors.New("knowledge timeout out of range")

// surrealKnowledgeSettings mirrors the migration's row schema.
type surrealKnowledgeSettings struct {
	ID                    *models.RecordID `json:"id,omitempty"`
	RepositoryTimeoutSecs int              `json:"repository_timeout_secs"`
	UpdatedAt             surrealTime      `json:"updated_at"`
	UpdatedBy             string           `json:"updated_by"`
}

// EnsureSchema creates the table + fields if they don't exist.
// Idempotent — safe to call on every boot.
func (s *KnowledgeSettingsStore) EnsureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("knowledge settings store: nil db")
	}
	stmts := []string{
		`DEFINE TABLE IF NOT EXISTS ca_knowledge_settings SCHEMAFULL;`,
		`DEFINE FIELD IF NOT EXISTS repository_timeout_secs ON ca_knowledge_settings TYPE int
		   ASSERT $value >= 1800 AND $value <= 86400;`,
		`DEFINE FIELD IF NOT EXISTS updated_at ON ca_knowledge_settings TYPE datetime
		   VALUE time::now();`,
		`DEFINE FIELD IF NOT EXISTS updated_by ON ca_knowledge_settings TYPE string
		   DEFAULT "";`,
	}
	for _, q := range stmts {
		if _, err := surrealdb.Query[any](ctx, s.db.DB(), q, nil); err != nil {
			return fmt.Errorf("knowledge settings ensure schema: %w", err)
		}
	}
	return nil
}

// Get returns the current repository_timeout_secs setting. Returns
// ErrKnowledgeSettingsNotFound when no row has been seeded yet.
//
// Read-side clamping: if the stored value is somehow outside the
// valid range (e.g. via direct DB edit), Get clamps it to the
// nearest bound and surfaces a usable duration. Codex r1 M3.
func (s *KnowledgeSettingsStore) Get(ctx context.Context) (time.Duration, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("knowledge settings store: nil db")
	}
	const q = `SELECT * FROM ca_knowledge_settings:current LIMIT 1;`
	rows, err := surrealdb.Query[[]surrealKnowledgeSettings](ctx, s.db.DB(), q, nil)
	if err != nil {
		return 0, fmt.Errorf("knowledge settings get: %w", err)
	}
	if rows == nil || len(*rows) == 0 {
		return 0, ErrKnowledgeSettingsNotFound
	}
	first := (*rows)[0]
	if len(first.Result) == 0 {
		return 0, ErrKnowledgeSettingsNotFound
	}
	row := first.Result[0]
	secs := row.RepositoryTimeoutSecs
	// Defensive read-side clamp.
	if secs < KnowledgeTimeoutMinSecs {
		secs = KnowledgeTimeoutMinSecs
	}
	if secs > KnowledgeTimeoutMaxSecs {
		secs = KnowledgeTimeoutMaxSecs
	}
	return time.Duration(secs) * time.Second, nil
}

// Put writes the new value. Returns ErrKnowledgeTimeoutOutOfRange
// if the supplied value is outside [KnowledgeTimeoutMinSecs,
// KnowledgeTimeoutMaxSecs]. The REST handler maps this to HTTP 400.
func (s *KnowledgeSettingsStore) Put(ctx context.Context, secs int, updatedBy string) error {
	if s == nil || s.db == nil {
		return errors.New("knowledge settings store: nil db")
	}
	if secs < KnowledgeTimeoutMinSecs || secs > KnowledgeTimeoutMaxSecs {
		return ErrKnowledgeTimeoutOutOfRange
	}
	const q = `
		UPDATE ca_knowledge_settings:current SET
			repository_timeout_secs = $secs,
			updated_by = $updated_by;
	`
	_, err := surrealdb.Query[any](ctx, s.db.DB(), q, map[string]any{
		"secs":       secs,
		"updated_by": updatedBy,
	})
	if err != nil {
		return fmt.Errorf("knowledge settings put: %w", err)
	}
	return nil
}

// Seed writes the row only if it doesn't already exist. Used on boot
// in cli/serve.go to seed from the env default after EnsureSchema
// when no row has been operator-set yet.
func (s *KnowledgeSettingsStore) Seed(ctx context.Context, secs int) error {
	if s == nil || s.db == nil {
		return errors.New("knowledge settings store: nil db")
	}
	if secs < KnowledgeTimeoutMinSecs || secs > KnowledgeTimeoutMaxSecs {
		// Bootstrap fallbacks should never be out of range; clamp
		// rather than error to ensure the boot path always succeeds.
		if secs < KnowledgeTimeoutMinSecs {
			secs = KnowledgeTimeoutMinSecs
		}
		if secs > KnowledgeTimeoutMaxSecs {
			secs = KnowledgeTimeoutMaxSecs
		}
	}
	// CREATE … IF NOT EXISTS is the closest SurrealDB primitive to
	// "insert if missing." If a row already exists we want to leave
	// it alone -- operator changes win over boot defaults.
	const q = `
		CREATE ca_knowledge_settings:current SET
			repository_timeout_secs = $secs,
			updated_by = "system:boot-default"
		RETURN NONE;
	`
	if _, err := surrealdb.Query[any](ctx, s.db.DB(), q, map[string]any{"secs": secs}); err != nil {
		// Surreal's CREATE on an existing record raises an error; we
		// treat that as the no-op success case.
		if isAlreadyExistsErr(err) {
			return nil
		}
		return fmt.Errorf("knowledge settings seed: %w", err)
	}
	return nil
}

// isAlreadyExistsErr returns true for Surreal "record already exists"
// errors. Used by Seed to make the boot-time creation path a true
// no-op when a row is already present.
func isAlreadyExistsErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// Surreal's typical error string for create-on-existing-record:
	//   "Database record `ca_knowledge_settings:current` already exists"
	// We match loosely to absorb minor wording changes across versions.
	for _, marker := range []string{"already exists", "Record exists", "record exists"} {
		if containsCaseFold(msg, marker) {
			return true
		}
	}
	return false
}

func containsCaseFold(s, sub string) bool {
	// Tiny helper to avoid pulling strings.Contains+strings.ToLower
	// every time. Could be replaced by strings.Contains(strings.ToLower(...))
	// but that allocates; this version short-circuits.
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			a, b := s[i+j], sub[j]
			if a >= 'A' && a <= 'Z' {
				a += 32
			}
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
