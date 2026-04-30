// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/surrealdb/surrealdb.go"
)

// EnsureProfilesSchemaExtensions adds the columns needed for the LLM
// provider-profiles plan to ca_llm_config:
//   - active_profile_id: pointer to ca_llm_profile:<id> (the workspace's
//     currently-active profile). Empty when no profile is active.
//   - updated_at: informational timestamp; not used for reconciliation
//     (the version watermark is the load-bearing signal — see
//     internal/llm/resolution/profile_aware_adapter.go).
//
// Idempotent: re-running on a hot DB is a no-op. Boot order in
// cli/serve.go: lps.EnsureSchema → THIS METHOD → lwRepoSettings.EnsureProfilesSchemaExtensions
// → MigrateToProfiles. (codex-H4)
func (s *SurrealLLMConfigStore) EnsureProfilesSchemaExtensions(ctx context.Context) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	// DEFAULT '' / now is critical: the legacy SaveLLMConfig is still
	// invoked by old pods during a rolling deploy (they run pre-profile
	// code that writes only the legacy fields). Without DEFAULTs the
	// SurrealDB schema rejects the UPSERT with "Found NONE for field
	// `active_profile_id` ... but expected a string" on a fresh row.
	// (The slice-4 cleanup removed the in-process boot-race fallback in
	// the cli adapter, but old-pod legacy writes during rollout remain.)
	_, err := surrealdb.Query[interface{}](ctx, db, `
		DEFINE FIELD IF NOT EXISTS active_profile_id ON ca_llm_config TYPE string DEFAULT '';
		DEFINE FIELD IF NOT EXISTS updated_at ON ca_llm_config TYPE datetime DEFAULT time::now();
	`, map[string]any{})
	if err != nil {
		return fmt.Errorf("ensure ca_llm_config profile-extensions schema: %w", err)
	}
	return nil
}

// LLMConfigSnapshot is the combined read shape used by the
// profile-aware resolver adapter. It contains the workspace pointer
// (active_profile_id), the workspace version cell, and the legacy
// fields (provider, api_key, model fields) that the dual-read fallback
// uses during the rolling-deploy transition (D8). The legacy fields
// MAY be empty on fresh installs; the resolver only consults them
// when active_profile_id is empty.
type LLMConfigSnapshot struct {
	ActiveProfileID string
	Version         uint64
	UpdatedAt       time.Time

	// Legacy fields (kept on ca_llm_config:default for the rolling
	// deploy window per D8). NEW code dual-writes these alongside the
	// active profile so an old pod that doesn't know about profiles
	// reads the right values. After the rollout is verified on thor,
	// a follow-up cleanup plan drops these columns.
	LegacyProvider                 string
	LegacyBaseURL                  string
	LegacyAPIKey                   string // already-decrypted plaintext (or empty)
	LegacySummaryModel             string
	LegacyReviewModel              string
	LegacyAskModel                 string
	LegacyKnowledgeModel           string
	LegacyArchitectureDiagramModel string
	LegacyReportModel              string
	LegacyDraftModel               string
	LegacyTimeoutSecs              int
	LegacyAdvancedMode             bool
}

// LoadConfigSnapshot reads ca_llm_config:default in a single round trip,
// returning the combined pointer + version + legacy fields needed by
// the profile-aware resolver adapter (codex-H2 / codex-H3).
//
// When the row does not exist (fresh install before any save), returns
// a zero-value snapshot with no error — caller treats it as
// "pre-migration / no profiles yet."
func (s *SurrealLLMConfigStore) LoadConfigSnapshot(ctx context.Context) (*LLMConfigSnapshot, error) {
	db := s.client.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}
	raw, err := surrealdb.Query[[]map[string]interface{}](ctx, db,
		`SELECT active_profile_id, version, updated_at,
			provider, base_url, api_key, summary_model, review_model,
			ask_model, knowledge_model, architecture_diagram_model,
			report_model, draft_model, timeout_secs, advanced_mode
		 FROM ca_llm_config WHERE id = type::thing('ca_llm_config', 'default') LIMIT 1`,
		map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("load llm config snapshot: %w", err)
	}
	if raw == nil || len(*raw) == 0 {
		return &LLMConfigSnapshot{}, nil
	}
	qr := (*raw)[0]
	if qr.Error != nil {
		return nil, fmt.Errorf("load llm config snapshot: %v", qr.Error)
	}
	if len(qr.Result) == 0 {
		return &LLMConfigSnapshot{}, nil
	}
	row := qr.Result[0]
	snap := &LLMConfigSnapshot{
		ActiveProfileID:                strVal(row, "active_profile_id"),
		LegacyProvider:                 strVal(row, "provider"),
		LegacyBaseURL:                  strVal(row, "base_url"),
		LegacySummaryModel:             strVal(row, "summary_model"),
		LegacyReviewModel:              strVal(row, "review_model"),
		LegacyAskModel:                 strVal(row, "ask_model"),
		LegacyKnowledgeModel:           strVal(row, "knowledge_model"),
		LegacyArchitectureDiagramModel: strVal(row, "architecture_diagram_model"),
		LegacyReportModel:              strVal(row, "report_model"),
		LegacyDraftModel:               strVal(row, "draft_model"),
	}
	// Decrypt the legacy api_key so the dual-read fallback path
	// returns plaintext. When the cipher is unable to decrypt
	// (corruption, key rotation), surface the wrapped error — fail-
	// closed is the rule for at-rest secrets (codex-H5).
	if storedKey := strVal(row, "api_key"); storedKey != "" {
		plaintext, decErr := s.decryptAPIKey(storedKey)
		if decErr != nil {
			if errors.Is(decErr, ErrAPIKeyDecryptFailed) {
				return nil, decErr
			}
			return nil, fmt.Errorf("legacy api_key decrypt: %w", decErr)
		}
		snap.LegacyAPIKey = plaintext
	}
	if v, ok := row["timeout_secs"]; ok {
		switch t := v.(type) {
		case float64:
			snap.LegacyTimeoutSecs = int(t)
		case int64:
			snap.LegacyTimeoutSecs = int(t)
		case int:
			snap.LegacyTimeoutSecs = t
		case uint64:
			snap.LegacyTimeoutSecs = int(t)
		}
	}
	if v, ok := row["advanced_mode"]; ok {
		if b, ok := v.(bool); ok {
			snap.LegacyAdvancedMode = b
		}
	}
	if v, ok := row["version"]; ok {
		switch t := v.(type) {
		case float64:
			snap.Version = uint64(t)
		case uint64:
			snap.Version = t
		case int64:
			if t >= 0 {
				snap.Version = uint64(t)
			}
		case int:
			if t >= 0 {
				snap.Version = uint64(t)
			}
		}
	}
	if t := extractTime(row, "updated_at"); !t.IsZero() {
		snap.UpdatedAt = t
	}
	return snap, nil
}

// LoadActiveProfileIDAndVersion is the cheap probe used by the
// migration's step 1 fast-exit and by the resolver's snapshot read.
// Returns "" / 0 when the row does not exist (fresh install).
func (s *SurrealLLMConfigStore) LoadActiveProfileIDAndVersion(ctx context.Context) (string, uint64, error) {
	db := s.client.DB()
	if db == nil {
		return "", 0, fmt.Errorf("database not connected")
	}
	raw, err := surrealdb.Query[[]map[string]interface{}](ctx, db,
		`SELECT active_profile_id, version FROM ca_llm_config WHERE id = type::thing('ca_llm_config', 'default') LIMIT 1`,
		map[string]any{})
	if err != nil {
		return "", 0, fmt.Errorf("load active profile id/version: %w", err)
	}
	if raw == nil || len(*raw) == 0 {
		return "", 0, nil
	}
	qr := (*raw)[0]
	if qr.Error != nil {
		return "", 0, fmt.Errorf("load active profile id/version: %v", qr.Error)
	}
	if len(qr.Result) == 0 {
		return "", 0, nil
	}
	row := qr.Result[0]
	id := strVal(row, "active_profile_id")
	var version uint64
	if v, ok := row["version"]; ok {
		switch t := v.(type) {
		case float64:
			version = uint64(t)
		case uint64:
			version = t
		case int64:
			if t >= 0 {
				version = uint64(t)
			}
		case int:
			if t >= 0 {
				version = uint64(t)
			}
		}
	}
	return id, version, nil
}

// LegacyFields is the migration's snapshot of the ca_llm_config:default
// row's legacy provider/api_key/model fields, plus the version observed
// at read time. The version is captured so the migration's BEGIN/COMMIT
// batch can CAS-guard against an old-pod legacy SaveLLMConfig that
// commits in between (codex-r1d-NEW).
//
// APIKey here is the RAW STORED FORM (still ciphertext-or-plaintext as
// it appears in the DB). The migration does not decrypt before it
// chooses the per-profile api_key strategy in
// chooseAPIKeyForMigratedProfile (which inspects whether the bytes
// already carry the sbenc:v1 envelope).
type LegacyFields struct {
	Provider                 string
	BaseURL                  string
	APIKey                   string // raw stored bytes (not decrypted)
	SummaryModel             string
	ReviewModel              string
	AskModel                 string
	KnowledgeModel           string
	ArchitectureDiagramModel string
	ReportModel              string
	DraftModel               string
	TimeoutSecs              int
	AdvancedMode             bool
	Version                  uint64
}

// LoadLegacyFieldsRaw is used by MigrateToProfiles to capture the
// pre-migration legacy row contents along with its version watermark.
// Returns hasRow=false when the row does not exist (fresh install).
//
// Unlike LoadConfigSnapshot, this method does NOT decrypt the api_key —
// the migration needs the raw stored bytes so it can decide whether to
// copy them as-is (sbenc:v1 already) or decrypt+re-encrypt (legacy
// plaintext with a key newly available).
func (s *SurrealLLMConfigStore) LoadLegacyFieldsRaw(ctx context.Context) (LegacyFields, bool, error) {
	db := s.client.DB()
	if db == nil {
		return LegacyFields{}, false, fmt.Errorf("database not connected")
	}
	raw, err := surrealdb.Query[[]map[string]interface{}](ctx, db,
		`SELECT provider, base_url, api_key, summary_model, review_model,
			ask_model, knowledge_model, architecture_diagram_model, report_model,
			draft_model, timeout_secs, advanced_mode, version
		 FROM ca_llm_config WHERE id = type::thing('ca_llm_config', 'default') LIMIT 1`,
		map[string]any{})
	if err != nil {
		return LegacyFields{}, false, fmt.Errorf("load legacy fields: %w", err)
	}
	if raw == nil || len(*raw) == 0 {
		return LegacyFields{}, false, nil
	}
	qr := (*raw)[0]
	if qr.Error != nil {
		return LegacyFields{}, false, fmt.Errorf("load legacy fields: %v", qr.Error)
	}
	if len(qr.Result) == 0 {
		return LegacyFields{}, false, nil
	}
	row := qr.Result[0]
	lf := LegacyFields{
		Provider:                 strVal(row, "provider"),
		BaseURL:                  strVal(row, "base_url"),
		APIKey:                   strVal(row, "api_key"), // RAW
		SummaryModel:             strVal(row, "summary_model"),
		ReviewModel:              strVal(row, "review_model"),
		AskModel:                 strVal(row, "ask_model"),
		KnowledgeModel:           strVal(row, "knowledge_model"),
		ArchitectureDiagramModel: strVal(row, "architecture_diagram_model"),
		ReportModel:              strVal(row, "report_model"),
		DraftModel:               strVal(row, "draft_model"),
	}
	if v, ok := row["timeout_secs"]; ok {
		switch t := v.(type) {
		case float64:
			lf.TimeoutSecs = int(t)
		case int64:
			lf.TimeoutSecs = int(t)
		case int:
			lf.TimeoutSecs = t
		case uint64:
			lf.TimeoutSecs = int(t)
		}
	}
	if v, ok := row["advanced_mode"]; ok {
		if b, ok := v.(bool); ok {
			lf.AdvancedMode = b
		}
	}
	if v, ok := row["version"]; ok {
		switch t := v.(type) {
		case float64:
			lf.Version = uint64(t)
		case uint64:
			lf.Version = t
		case int64:
			if t >= 0 {
				lf.Version = uint64(t)
			}
		case int:
			if t >= 0 {
				lf.Version = uint64(t)
			}
		}
	}
	return lf, true, nil
}
