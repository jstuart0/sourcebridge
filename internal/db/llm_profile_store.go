// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/surrealdb/surrealdb.go"
	"github.com/surrealdb/surrealdb.go/pkg/models"

	"github.com/sourcebridge/sourcebridge/internal/secretcipher"
)

// SurrealLLMProfileStore persists named LLM provider profiles in
// SurrealDB. Each row is a single profile (name, provider, model fields,
// per-profile encrypted api_key). The active-profile pointer + version
// cell live on `ca_llm_config:default` (see SurrealLLMConfigStore).
//
// Slice 1 of the LLM provider profiles plan: the api_key column is
// encrypted at rest using the same sbenc:v1 envelope as ca_llm_config,
// via the shared secretcipher.Cipher. The cipher is constructed ONCE at
// boot in cli/serve.go and passed to both stores via WithLLMProfileCipher
// (librarian-M1).
//
// Concurrency: a SurrealDB UNIQUE INDEX on `name_key` enforces
// case-insensitive uniqueness across replicas (codex-M2). Concurrent
// CreateProfile calls with names that normalize to the same key are
// serialized by SurrealDB; the loser receives ErrDuplicateProfileName.
type SurrealLLMProfileStore struct {
	client                       *SurrealDB
	cipher                       secretcipher.Cipher
	encryptionKeyForBootstrap    string
	allowUnencryptedForBootstrap bool
}

// LLMProfileStoreOption configures optional behavior.
type LLMProfileStoreOption func(*SurrealLLMProfileStore)

// WithLLMProfileEncryptionKey sets the key used for at-rest encryption
// of the per-profile api_key column. Empty string means "no encryption"
// (matches OSS embedded mode).
func WithLLMProfileEncryptionKey(key string) LLMProfileStoreOption {
	return func(s *SurrealLLMProfileStore) {
		s.encryptionKeyForBootstrap = key
	}
}

// WithLLMProfileAllowUnencrypted is the OSS escape hatch.
func WithLLMProfileAllowUnencrypted(allow bool) LLMProfileStoreOption {
	return func(s *SurrealLLMProfileStore) {
		s.allowUnencryptedForBootstrap = allow
	}
}

// WithLLMProfileCipher injects a pre-built secretcipher.Cipher. When set,
// it overrides any cipher constructed from the encryption-key options
// above. This is the production-recommended path: cli/serve.go builds
// ONE cipher and passes it to both ca_llm_config and ca_llm_profile so
// both rows are encrypted under identical key material (librarian-M1).
func WithLLMProfileCipher(c secretcipher.Cipher) LLMProfileStoreOption {
	return func(s *SurrealLLMProfileStore) {
		s.cipher = c
	}
}

// NewSurrealLLMProfileStore creates a new profile store backed by
// SurrealDB.
func NewSurrealLLMProfileStore(client *SurrealDB, opts ...LLMProfileStoreOption) *SurrealLLMProfileStore {
	s := &SurrealLLMProfileStore{client: client}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	if s.cipher == nil {
		s.cipher = secretcipher.NewAESGCMCipher(s.encryptionKeyForBootstrap, s.allowUnencryptedForBootstrap)
	}
	return s
}

// Profile is the persisted profile record. APIKey is plaintext at this
// layer (encryption is handled transparently by the store). APIKeySet
// and APIKeyHint are populated by the store on Load for caller-facing
// shapes that must NEVER include the api_key (REST list endpoint).
type Profile struct {
	ID                       string    // SurrealDB record id, e.g. "ca_llm_profile:default-migrated"
	Name                     string    // displayed verbatim (preserves user casing/whitespace)
	NameKey                  string    // normalized uniqueness key: lowercase(trim(name))
	Provider                 string
	BaseURL                  string
	APIKey                   string // decrypted; never logged
	SummaryModel             string
	ReviewModel              string
	AskModel                 string
	KnowledgeModel           string
	ArchitectureDiagramModel string
	ReportModel              string
	DraftModel               string
	TimeoutSecs              int
	AdvancedMode             bool
	CreatedAt                time.Time
	UpdatedAt                time.Time
	// LastLegacyVersionConsumed is the rolling-deploy reconciliation
	// watermark (codex-H2 / r1b). New-code writes set this to
	// ca_llm_config:default.version after the bump. Old pods do not
	// touch this field. The resolver compares
	// workspace.version > active.LastLegacyVersionConsumed to detect
	// old-pod legacy writes.
	LastLegacyVersionConsumed uint64
}

// ProfileCreate is the input shape for CreateProfile. APIKey is plaintext
// (encrypted by the store). All fields are values; missing fields are
// stored as empty values.
type ProfileCreate struct {
	Name                     string
	Provider                 string
	BaseURL                  string
	APIKey                   string
	SummaryModel             string
	ReviewModel              string
	AskModel                 string
	KnowledgeModel           string
	ArchitectureDiagramModel string
	ReportModel              string
	DraftModel               string
	TimeoutSecs              int
	AdvancedMode             bool
}

// ProfileUpdate uses pointer-patch semantics (matches the existing
// livingwiki override pattern in this codebase, codex-M3):
//   - nil pointer → preserve existing value
//   - pointer to ""  → CLEAR (writes empty string; for api_key this means zero the ciphertext)
//   - pointer to non-empty → set; for api_key, encrypt+save
//
// Name has special handling: nil = preserve; *"" is REJECTED (422; name
// is non-nullable per ruby UX §4.5); non-empty = rename (re-validates
// name_key uniqueness via the UNIQUE INDEX).
type ProfileUpdate struct {
	Name                     *string
	Provider                 *string
	BaseURL                  *string
	APIKey                   *string
	ClearAPIKey              bool // optional convenience flag; equivalent to APIKey:&"" (matches existing clearAPIKey precedent in livingwiki override)
	SummaryModel             *string
	ReviewModel              *string
	AskModel                 *string
	KnowledgeModel           *string
	ArchitectureDiagramModel *string
	ReportModel              *string
	DraftModel               *string
	TimeoutSecs              *int
	AdvancedMode             *bool
}

// ─────────────────────────────────────────────────────────────────────────
// Sentinel errors
// ─────────────────────────────────────────────────────────────────────────

// ErrProfileNotFound is returned by LoadProfile / UpdateProfile /
// DeleteProfile / SetActiveProfile when the supplied id does not exist
// or has been deleted.
var ErrProfileNotFound = errors.New("llm profile not found")

// ErrDuplicateProfileName is returned by CreateProfile / UpdateProfile
// when the normalized name_key collides with an existing row's
// name_key. Mapped to 409 Conflict at the REST layer.
var ErrDuplicateProfileName = errors.New("llm profile with this name already exists")

// ErrProfileNameRequired is returned when an UpdateProfile patch points
// at an empty Name string (clearing the name is not allowed; profiles
// must always have a non-empty name).
var ErrProfileNameRequired = errors.New("llm profile name cannot be empty")

// ErrProfileNameTooLong is returned when the proposed Name (after
// trim) exceeds 64 chars (per ruby UX §4.5).
var ErrProfileNameTooLong = errors.New("llm profile name exceeds 64 characters")

// ─────────────────────────────────────────────────────────────────────────
// Schema-ensure (codex-H4)
// ─────────────────────────────────────────────────────────────────────────

// EnsureSchema defines the ca_llm_profile table, fields, and the UNIQUE
// INDEX on name_key. Idempotent: re-running on a hot DB is a no-op.
//
// Boot order in cli/serve.go:
//  1. lps.EnsureSchema(ctx)                                — this method
//  2. lcs.EnsureProfilesSchemaExtensions(ctx)              — adds active_profile_id + updated_at
//  3. lwRepoSettingsStore.EnsureProfilesSchemaExtensions() — adds living_wiki_llm_override.profile_id
//  4. db.MigrateToProfiles(...)                            — seeds Default profile
//  5. resolver mount + REST router registration            — handlers can serve writes
func (s *SurrealLLMProfileStore) EnsureSchema(ctx context.Context) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	_, err := surrealdb.Query[interface{}](ctx, db, `
		DEFINE TABLE IF NOT EXISTS ca_llm_profile SCHEMAFULL;
		DEFINE FIELD IF NOT EXISTS name ON ca_llm_profile TYPE string;
		DEFINE FIELD IF NOT EXISTS name_key ON ca_llm_profile TYPE string;
		DEFINE FIELD IF NOT EXISTS provider ON ca_llm_profile TYPE string;
		DEFINE FIELD IF NOT EXISTS base_url ON ca_llm_profile TYPE string;
		DEFINE FIELD IF NOT EXISTS api_key ON ca_llm_profile TYPE string;
		DEFINE FIELD IF NOT EXISTS summary_model ON ca_llm_profile TYPE string;
		DEFINE FIELD IF NOT EXISTS review_model ON ca_llm_profile TYPE string;
		DEFINE FIELD IF NOT EXISTS ask_model ON ca_llm_profile TYPE string;
		DEFINE FIELD IF NOT EXISTS knowledge_model ON ca_llm_profile TYPE string;
		DEFINE FIELD IF NOT EXISTS architecture_diagram_model ON ca_llm_profile TYPE string;
		DEFINE FIELD IF NOT EXISTS report_model ON ca_llm_profile TYPE string;
		DEFINE FIELD IF NOT EXISTS draft_model ON ca_llm_profile TYPE string;
		DEFINE FIELD IF NOT EXISTS timeout_secs ON ca_llm_profile TYPE int;
		DEFINE FIELD IF NOT EXISTS advanced_mode ON ca_llm_profile TYPE bool;
		DEFINE FIELD IF NOT EXISTS created_at ON ca_llm_profile TYPE datetime;
		DEFINE FIELD IF NOT EXISTS updated_at ON ca_llm_profile TYPE datetime;
		DEFINE FIELD IF NOT EXISTS last_legacy_version_consumed ON ca_llm_profile TYPE int DEFAULT 0;
		DEFINE INDEX IF NOT EXISTS ca_llm_profile_name_key_unique ON ca_llm_profile FIELDS name_key UNIQUE;
	`, map[string]any{})
	if err != nil {
		return fmt.Errorf("ensure ca_llm_profile schema: %w", err)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────
// Helpers (name normalization, validation, DB row → Profile)
// ─────────────────────────────────────────────────────────────────────────

// NormalizeProfileName produces the case-insensitive uniqueness key
// (codex-M2). Whitespace is trimmed and the remaining string is
// lowercased. Auto-applied by every CreateProfile / UpdateProfile path.
func NormalizeProfileName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func validateProfileName(name string) (string, string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", "", ErrProfileNameRequired
	}
	if len(trimmed) > 64 {
		return "", "", ErrProfileNameTooLong
	}
	return trimmed, strings.ToLower(trimmed), nil
}

// rowToProfile converts a SurrealDB row map into a *Profile, decrypting
// the api_key in the process. Returns ErrAPIKeyDecryptFailed if the
// stored ciphertext is corrupt.
func (s *SurrealLLMProfileStore) rowToProfile(row map[string]interface{}) (*Profile, error) {
	p := &Profile{
		ID:                       extractRecordIDString(row, "id"),
		Name:                     strVal(row, "name"),
		NameKey:                  strVal(row, "name_key"),
		Provider:                 strVal(row, "provider"),
		BaseURL:                  strVal(row, "base_url"),
		SummaryModel:             strVal(row, "summary_model"),
		ReviewModel:              strVal(row, "review_model"),
		AskModel:                 strVal(row, "ask_model"),
		KnowledgeModel:           strVal(row, "knowledge_model"),
		ArchitectureDiagramModel: strVal(row, "architecture_diagram_model"),
		ReportModel:              strVal(row, "report_model"),
		DraftModel:               strVal(row, "draft_model"),
	}
	storedKey := strVal(row, "api_key")
	plaintext, err := s.cipher.Decrypt(storedKey)
	if err != nil {
		if errors.Is(err, secretcipher.ErrDecryptFailed) {
			return nil, ErrAPIKeyDecryptFailed
		}
		return nil, fmt.Errorf("llm profile: decrypt api_key: %w", err)
	}
	p.APIKey = plaintext

	if v, ok := row["timeout_secs"]; ok {
		switch t := v.(type) {
		case float64:
			p.TimeoutSecs = int(t)
		case uint64:
			p.TimeoutSecs = int(t)
		case int64:
			p.TimeoutSecs = int(t)
		case int:
			p.TimeoutSecs = t
		}
	}
	if v, ok := row["advanced_mode"]; ok {
		if b, ok := v.(bool); ok {
			p.AdvancedMode = b
		}
	}
	if v, ok := row["last_legacy_version_consumed"]; ok {
		switch t := v.(type) {
		case float64:
			p.LastLegacyVersionConsumed = uint64(t)
		case uint64:
			p.LastLegacyVersionConsumed = t
		case int64:
			if t >= 0 {
				p.LastLegacyVersionConsumed = uint64(t)
			}
		case int:
			if t >= 0 {
				p.LastLegacyVersionConsumed = uint64(t)
			}
		}
	}
	if t := extractTime(row, "created_at"); !t.IsZero() {
		p.CreatedAt = t
	}
	if t := extractTime(row, "updated_at"); !t.IsZero() {
		p.UpdatedAt = t
	}
	return p, nil
}

// extractRecordIDString reads a field that may be a SurrealDB RecordID
// (the SDK decodes record-id CBOR tags as models.RecordID values when the
// target is interface{}) or a string literal. Returns the canonical
// "table:id" form, or empty string when missing.
func extractRecordIDString(row map[string]interface{}, key string) string {
	v, ok := row[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case models.RecordID:
		return t.String()
	case *models.RecordID:
		if t == nil {
			return ""
		}
		return t.String()
	}
	// Defensive: fall back to fmt %v for any future SDK shape that
	// stringifies cleanly.
	return fmt.Sprintf("%v", v)
}

// extractTime reads a datetime field from a SurrealDB row. The SDK
// decodes Tag-12 CustomDateTime CBOR values into models.CustomDateTime
// (which embeds time.Time) when the target is interface{}; ISO-8601
// strings (Tag 0) come through as time.Time directly. Both shapes are
// tolerated.
func extractTime(row map[string]interface{}, key string) time.Time {
	v, ok := row[key]
	if !ok || v == nil {
		return time.Time{}
	}
	switch t := v.(type) {
	case time.Time:
		return t
	case models.CustomDateTime:
		return t.Time
	case *models.CustomDateTime:
		if t == nil {
			return time.Time{}
		}
		return t.Time
	case string:
		if parsed, err := time.Parse(time.RFC3339Nano, t); err == nil {
			return parsed
		}
		if parsed, err := time.Parse(time.RFC3339, t); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// ─────────────────────────────────────────────────────────────────────────
// Read API
// ─────────────────────────────────────────────────────────────────────────

// ListProfiles returns every profile in name order. The api_key field
// is DECRYPTED on each profile — callers that surface this list to the
// REST layer must replace APIKey with APIKeySet:bool before sending it
// across the wire. The store returns plaintext because some callers
// (the migration / reconciler) do need the cleartext.
func (s *SurrealLLMProfileStore) ListProfiles(ctx context.Context) ([]Profile, error) {
	db := s.client.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}
	raw, err := surrealdb.Query[[]map[string]interface{}](ctx, db,
		`SELECT id, name, name_key, provider, base_url, api_key,
			summary_model, review_model, ask_model, knowledge_model,
			architecture_diagram_model, report_model, draft_model,
			timeout_secs, advanced_mode, created_at, updated_at,
			last_legacy_version_consumed
		 FROM ca_llm_profile ORDER BY name_key ASC`,
		map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("list profiles query: %w", err)
	}
	if raw == nil || len(*raw) == 0 {
		return nil, nil
	}
	qr := (*raw)[0]
	if qr.Error != nil {
		return nil, fmt.Errorf("list profiles: %v", qr.Error)
	}
	out := make([]Profile, 0, len(qr.Result))
	for _, row := range qr.Result {
		p, perr := s.rowToProfile(row)
		if perr != nil {
			return nil, perr
		}
		out = append(out, *p)
	}
	return out, nil
}

// LoadProfile fetches a single profile by id. Returns ErrProfileNotFound
// when the id does not exist.
func (s *SurrealLLMProfileStore) LoadProfile(ctx context.Context, id string) (*Profile, error) {
	db := s.client.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}
	if id == "" {
		return nil, ErrProfileNotFound
	}
	tableName, recordID, ok := splitRecordID(id)
	if !ok {
		return nil, ErrProfileNotFound
	}
	if tableName != "ca_llm_profile" {
		return nil, ErrProfileNotFound
	}
	raw, err := surrealdb.Query[[]map[string]interface{}](ctx, db,
		`SELECT id, name, name_key, provider, base_url, api_key,
			summary_model, review_model, ask_model, knowledge_model,
			architecture_diagram_model, report_model, draft_model,
			timeout_secs, advanced_mode, created_at, updated_at,
			last_legacy_version_consumed
		 FROM ca_llm_profile WHERE id = type::thing('ca_llm_profile', $rid) LIMIT 1`,
		map[string]any{"rid": recordID})
	if err != nil {
		return nil, fmt.Errorf("load profile query: %w", err)
	}
	if raw == nil || len(*raw) == 0 {
		return nil, ErrProfileNotFound
	}
	qr := (*raw)[0]
	if qr.Error != nil {
		return nil, fmt.Errorf("load profile: %v", qr.Error)
	}
	if len(qr.Result) == 0 {
		return nil, ErrProfileNotFound
	}
	return s.rowToProfile(qr.Result[0])
}

// LoadAllProfileIDs returns every profile's record id. Used by the
// resolver / diagnostics path; not on the hot resolver path.
// Implements ProfileLookupStore.LoadAllProfileIDs (codex-M5).
func (s *SurrealLLMProfileStore) LoadAllProfileIDs(ctx context.Context) ([]string, error) {
	db := s.client.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}
	raw, err := surrealdb.Query[[]map[string]interface{}](ctx, db,
		`SELECT id FROM ca_llm_profile`, map[string]any{})
	if err != nil {
		return nil, err
	}
	if raw == nil || len(*raw) == 0 {
		return nil, nil
	}
	qr := (*raw)[0]
	if qr.Error != nil {
		return nil, fmt.Errorf("%v", qr.Error)
	}
	ids := make([]string, 0, len(qr.Result))
	for _, row := range qr.Result {
		if id := extractRecordIDString(row, "id"); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// splitRecordID splits a SurrealDB record id of the form "table:id"
// into ("table", "id", true). When the input does not contain a colon
// it is treated as the record id alone (returning the empty table).
func splitRecordID(full string) (table, recordID string, ok bool) {
	idx := strings.IndexByte(full, ':')
	if idx <= 0 || idx >= len(full)-1 {
		return "", full, full != ""
	}
	return full[:idx], full[idx+1:], true
}

// ─────────────────────────────────────────────────────────────────────────
// Write API
// ─────────────────────────────────────────────────────────────────────────

// CreateProfile inserts a new profile row, encrypting the api_key. The
// SurrealDB record id is auto-generated (rand id). Returns the new id.
//
// On UNIQUE-index violation (name_key collision) returns
// ErrDuplicateProfileName.
//
// Important: this method does NOT bump workspace.version. It only
// performs the row insert. Callers that need the version bump (admin
// CRUD handlers) wrap this call in writeNonActiveProfileWithWatermarkBump
// or equivalent, or call BumpVersion explicitly. Tests that exercise
// the store directly often skip the version bump — they're verifying
// the store contract, not the workspace cache invariant.
func (s *SurrealLLMProfileStore) CreateProfile(ctx context.Context, p ProfileCreate) (string, error) {
	db := s.client.DB()
	if db == nil {
		return "", fmt.Errorf("database not connected")
	}
	name, nameKey, err := validateProfileName(p.Name)
	if err != nil {
		return "", err
	}
	encryptedKey, err := s.encryptKey(p.APIKey)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	raw, err := surrealdb.Query[[]map[string]interface{}](ctx, db,
		`CREATE ca_llm_profile SET
			name                         = $name,
			name_key                     = $name_key,
			provider                     = $provider,
			base_url                     = $base_url,
			api_key                      = $api_key,
			summary_model                = $summary_model,
			review_model                 = $review_model,
			ask_model                    = $ask_model,
			knowledge_model              = $knowledge_model,
			architecture_diagram_model   = $architecture_diagram_model,
			report_model                 = $report_model,
			draft_model                  = $draft_model,
			timeout_secs                 = $timeout_secs,
			advanced_mode                = $advanced_mode,
			created_at                   = type::datetime($now),
			updated_at                   = type::datetime($now),
			last_legacy_version_consumed = 0
		 RETURN id`,
		map[string]any{
			"name":                       name,
			"name_key":                   nameKey,
			"provider":                   p.Provider,
			"base_url":                   p.BaseURL,
			"api_key":                    encryptedKey,
			"summary_model":              p.SummaryModel,
			"review_model":               p.ReviewModel,
			"ask_model":                  p.AskModel,
			"knowledge_model":            p.KnowledgeModel,
			"architecture_diagram_model": p.ArchitectureDiagramModel,
			"report_model":               p.ReportModel,
			"draft_model":                p.DraftModel,
			"timeout_secs":               p.TimeoutSecs,
			"advanced_mode":              p.AdvancedMode,
			"now":                        now,
		})
	if err != nil {
		if isUniqueIndexViolation(err) {
			return "", ErrDuplicateProfileName
		}
		return "", fmt.Errorf("create profile: %w", err)
	}
	if raw == nil || len(*raw) == 0 {
		return "", fmt.Errorf("create profile: empty response")
	}
	qr := (*raw)[0]
	if qr.Error != nil {
		errStr := fmt.Sprintf("%v", qr.Error)
		if isUniqueIndexViolationString(errStr) {
			return "", ErrDuplicateProfileName
		}
		return "", fmt.Errorf("create profile: %s", errStr)
	}
	if len(qr.Result) == 0 {
		return "", fmt.Errorf("create profile: empty result")
	}
	id := extractRecordIDString(qr.Result[0], "id")
	if id == "" {
		return "", fmt.Errorf("create profile: missing id in response")
	}

	// Mask api_key in slog: api_key_set:bool only (xander-L1).
	slog.Info("llm profile created",
		"id", id,
		"name", name,
		"provider", p.Provider,
		"api_key_set", p.APIKey != "",
		"encrypted_at_rest", strings.HasPrefix(encryptedKey, envelopePrefix))
	return id, nil
}

// UpdateProfile applies a pointer-patch ProfileUpdate to an existing
// row. Pointer-patch semantics:
//   - nil pointer field → preserve existing value
//   - pointer to non-empty → set the field
//   - pointer to "" (empty string) → clear the field; for api_key this
//     means zero the ciphertext (xander-M1)
//
// Name has special handling: nil = preserve; *"" is rejected as
// ErrProfileNameRequired.
//
// On UNIQUE-index violation (rename collides with an existing name_key)
// returns ErrDuplicateProfileName. On unknown id returns
// ErrProfileNotFound. Like CreateProfile, this method does NOT bump
// workspace.version on its own — callers wrap it in the appropriate
// helper (writeActiveProfileWithLegacyMirror, etc.).
func (s *SurrealLLMProfileStore) UpdateProfile(ctx context.Context, id string, patch ProfileUpdate) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	tableName, recordID, ok := splitRecordID(id)
	if !ok || tableName != "ca_llm_profile" {
		return ErrProfileNotFound
	}

	// Preflight: ensure the row exists. SurrealDB's UPDATE on a missing
	// row is silently a no-op — we want a concrete ErrProfileNotFound.
	preCheck, err := surrealdb.Query[[]map[string]interface{}](ctx, db,
		`SELECT id FROM ca_llm_profile WHERE id = type::thing('ca_llm_profile', $rid) LIMIT 1`,
		map[string]any{"rid": recordID})
	if err != nil {
		return fmt.Errorf("update profile pre-check: %w", err)
	}
	if preCheck == nil || len(*preCheck) == 0 {
		return ErrProfileNotFound
	}
	if pq := (*preCheck)[0]; pq.Error != nil {
		return fmt.Errorf("update profile pre-check: %v", pq.Error)
	} else if len(pq.Result) == 0 {
		return ErrProfileNotFound
	}

	setClauses := []string{}
	vars := map[string]any{
		"rid": recordID,
		"now": time.Now().UTC().Format(time.RFC3339Nano),
	}

	// Name + name_key (codex-M2: name_key recomputed on every rename).
	if patch.Name != nil {
		name, nameKey, vErr := validateProfileName(*patch.Name)
		if vErr != nil {
			return vErr
		}
		setClauses = append(setClauses, "name = $name", "name_key = $name_key")
		vars["name"] = name
		vars["name_key"] = nameKey
	}

	// API key: ClearAPIKey takes precedence; then APIKey pointer.
	switch {
	case patch.ClearAPIKey:
		setClauses = append(setClauses, "api_key = ''")
	case patch.APIKey != nil:
		encryptedKey, encErr := s.encryptKey(*patch.APIKey)
		if encErr != nil {
			return encErr
		}
		setClauses = append(setClauses, "api_key = $api_key")
		vars["api_key"] = encryptedKey
	}

	// Other string fields: pointer-patch semantics.
	stringFields := []struct {
		col string
		val *string
	}{
		{"provider", patch.Provider},
		{"base_url", patch.BaseURL},
		{"summary_model", patch.SummaryModel},
		{"review_model", patch.ReviewModel},
		{"ask_model", patch.AskModel},
		{"knowledge_model", patch.KnowledgeModel},
		{"architecture_diagram_model", patch.ArchitectureDiagramModel},
		{"report_model", patch.ReportModel},
		{"draft_model", patch.DraftModel},
	}
	for _, f := range stringFields {
		if f.val == nil {
			continue
		}
		varKey := f.col
		setClauses = append(setClauses, fmt.Sprintf("%s = $%s", f.col, varKey))
		vars[varKey] = *f.val
	}

	if patch.TimeoutSecs != nil {
		setClauses = append(setClauses, "timeout_secs = $timeout_secs")
		vars["timeout_secs"] = *patch.TimeoutSecs
	}
	if patch.AdvancedMode != nil {
		setClauses = append(setClauses, "advanced_mode = $advanced_mode")
		vars["advanced_mode"] = *patch.AdvancedMode
	}

	if len(setClauses) == 0 {
		// Nothing to update — short-circuit.
		return nil
	}

	setClauses = append(setClauses, "updated_at = type::datetime($now)")

	sql := fmt.Sprintf(
		`UPDATE type::thing('ca_llm_profile', $rid) SET %s`,
		strings.Join(setClauses, ", "),
	)
	_, err = surrealdb.Query[interface{}](ctx, db, sql, vars)
	if err != nil {
		if isUniqueIndexViolation(err) {
			return ErrDuplicateProfileName
		}
		return fmt.Errorf("update profile: %w", err)
	}

	// Track api_key_set without leaking the value.
	apiKeySet := patch.APIKey != nil && *patch.APIKey != "" && !patch.ClearAPIKey
	slog.Info("llm profile updated",
		"id", id,
		"name_renamed", patch.Name != nil,
		"api_key_changed", patch.APIKey != nil || patch.ClearAPIKey,
		"api_key_set", apiKeySet)
	return nil
}

// DeleteProfile removes a profile, zeroing the api_key ciphertext
// before the row removal (xander-M1). The two writes are NOT atomic
// against an arbitrary observer — but the helpful invariant is that
// any backup snapshot taken AFTER the zero-then-delete sees no
// ciphertext for this profile id. Race-window observers that catch
// the zero-write still see an empty api_key.
//
// Returns ErrProfileNotFound when id does not exist. Does NOT enforce
// the "cannot delete active" guard — that lives at the helper /
// handler layer (deleteNonActiveProfile in llm_profile_helpers.go and
// the REST handler).
func (s *SurrealLLMProfileStore) DeleteProfile(ctx context.Context, id string) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	tableName, recordID, ok := splitRecordID(id)
	if !ok || tableName != "ca_llm_profile" {
		return ErrProfileNotFound
	}
	// Pre-check existence so we return ErrProfileNotFound instead of a
	// silent no-op.
	preCheck, err := surrealdb.Query[[]map[string]interface{}](ctx, db,
		`SELECT id FROM ca_llm_profile WHERE id = type::thing('ca_llm_profile', $rid) LIMIT 1`,
		map[string]any{"rid": recordID})
	if err != nil {
		return fmt.Errorf("delete profile pre-check: %w", err)
	}
	if preCheck == nil || len(*preCheck) == 0 {
		return ErrProfileNotFound
	}
	if pq := (*preCheck)[0]; pq.Error != nil {
		return fmt.Errorf("delete profile pre-check: %v", pq.Error)
	} else if len(pq.Result) == 0 {
		return ErrProfileNotFound
	}

	// Step 1: zero the ciphertext. Defense in depth for backup snapshots
	// that might capture an in-flight DELETE.
	_, err = surrealdb.Query[interface{}](ctx, db,
		`UPDATE type::thing('ca_llm_profile', $rid) SET api_key = ''`,
		map[string]any{"rid": recordID})
	if err != nil {
		return fmt.Errorf("delete profile (zero key): %w", err)
	}
	// Step 2: delete the row.
	_, err = surrealdb.Query[interface{}](ctx, db,
		`DELETE type::thing('ca_llm_profile', $rid)`,
		map[string]any{"rid": recordID})
	if err != nil {
		return fmt.Errorf("delete profile (remove row): %w", err)
	}
	slog.Info("llm profile deleted", "id", id)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────
// Encryption helpers (delegate to the shared cipher)
// ─────────────────────────────────────────────────────────────────────────

func (s *SurrealLLMProfileStore) encryptKey(plaintext string) (string, error) {
	stored, err := s.cipher.Encrypt(plaintext)
	if err != nil {
		if errors.Is(err, secretcipher.ErrEncryptionKeyRequired) {
			return "", ErrEncryptionKeyRequired
		}
		return "", fmt.Errorf("llm profile: encrypt api_key: %w", err)
	}
	return stored, nil
}

// EncryptedAPIKey is exposed for tests / migration to verify the on-disk form.
func (s *SurrealLLMProfileStore) EncryptedAPIKey(plaintext string) (string, error) {
	return s.encryptKey(plaintext)
}

// IsEnvelopeEncrypted reports whether stored carries the v1 envelope
// prefix. Used by the migration to decide whether to copy bytes
// as-is (sbenc:v1 already) vs decrypt+re-encrypt (legacy plaintext).
func (s *SurrealLLMProfileStore) IsEnvelopeEncrypted(stored string) bool {
	return s.cipher.IsEnvelopeEncrypted(stored)
}

// ─────────────────────────────────────────────────────────────────────────
// Unique-index violation detection
// ─────────────────────────────────────────────────────────────────────────

// isUniqueIndexViolation inspects an error from SurrealDB for the
// canonical UNIQUE INDEX violation marker. SurrealDB reports these as
// "Database index `ca_llm_profile_name_key_unique` already contains
// '<value>'" or similar; we match on stable substrings to avoid
// brittle exact-string compares.
func isUniqueIndexViolation(err error) bool {
	if err == nil {
		return false
	}
	return isUniqueIndexViolationString(err.Error())
}

func isUniqueIndexViolationString(msg string) bool {
	if msg == "" {
		return false
	}
	low := strings.ToLower(msg)
	if strings.Contains(low, "ca_llm_profile_name_key_unique") {
		return true
	}
	// Generic SurrealDB unique-index error shapes.
	if strings.Contains(low, "already contains") && strings.Contains(low, "name_key") {
		return true
	}
	if strings.Contains(low, "duplicate") && strings.Contains(low, "name_key") {
		return true
	}
	return false
}
