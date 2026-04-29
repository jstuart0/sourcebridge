// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/surrealdb/surrealdb.go"

	"github.com/sourcebridge/sourcebridge/internal/secretcipher"
)

// SurrealLLMConfigStore persists LLM configuration in SurrealDB using a
// well-known record ID, following the same pattern as SurrealGitConfigStore.
//
// Slice 3 of the workspace-LLM-source-of-truth plan: the api_key column
// is now encrypted at rest using a versioned AES-GCM envelope ("sbenc:v1:"
// prefix). Existing legacy plaintext rows are read transparently; new
// saves always write ciphertext (or refuse the save when no encryption
// key is configured, unless the OSS escape hatch is on).
//
// R3 slice 1: encryption is delegated to a secretcipher.Cipher. The default
// implementation is secretcipher.AESGCMCipher (the same sbenc:v1 envelope
// the store has always used); a future Vault-backed Cipher swaps in here
// without touching any callsite.
type SurrealLLMConfigStore struct {
	client *SurrealDB
	cipher secretcipher.Cipher
	// Held for backward-compatible options chaining: NewSurrealLLMConfigStore
	// still accepts WithLLMConfigEncryptionKey + WithLLMConfigAllowUnencrypted
	// and constructs the cipher from those values. Tests / live callers don't
	// need to know the cipher exists.
	encryptionKeyForBootstrap string
	allowUnencryptedForBootstrap bool
}

// LLMConfigStoreOption configures optional behavior.
type LLMConfigStoreOption func(*SurrealLLMConfigStore)

// WithLLMConfigEncryptionKey sets the key used for at-rest encryption of
// the api_key column. Empty string means "no encryption" (matches OSS
// embedded mode). The key is hashed to 32 bytes via SHA-256, so any
// non-empty value works; production deployments should set 32+ random
// bytes via SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY.
func WithLLMConfigEncryptionKey(key string) LLMConfigStoreOption {
	return func(s *SurrealLLMConfigStore) {
		s.encryptionKeyForBootstrap = key
	}
}

// WithLLMConfigAllowUnencrypted is the OSS escape hatch. When true,
// SaveLLMConfig with a non-empty API key is permitted even when
// encryptionKey == "". Logs a one-time warning per process. Production
// deployments leave this false; opt in via
// SOURCEBRIDGE_ALLOW_UNENCRYPTED_LLM_KEY=true.
func WithLLMConfigAllowUnencrypted(allow bool) LLMConfigStoreOption {
	return func(s *SurrealLLMConfigStore) {
		s.allowUnencryptedForBootstrap = allow
	}
}

// WithLLMConfigCipher injects a pre-built secretcipher.Cipher. When set,
// it overrides any cipher constructed from WithLLMConfigEncryptionKey /
// WithLLMConfigAllowUnencrypted. Reserved for tests and future Vault
// integration; production code uses the convenience options above.
func WithLLMConfigCipher(c secretcipher.Cipher) LLMConfigStoreOption {
	return func(s *SurrealLLMConfigStore) {
		s.cipher = c
	}
}

// NewSurrealLLMConfigStore creates a new LLM config store backed by SurrealDB.
//
// If WithLLMConfigCipher is not supplied, a secretcipher.AESGCMCipher is
// constructed from the encryption-key and allow-unencrypted options.
func NewSurrealLLMConfigStore(client *SurrealDB, opts ...LLMConfigStoreOption) *SurrealLLMConfigStore {
	s := &SurrealLLMConfigStore{client: client}
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

// LLMConfigRecord is the persisted LLM configuration. APIKey is always
// plaintext at this layer; encryption / decryption is handled
// transparently by Save / Load.
type LLMConfigRecord struct {
	Provider                 string `json:"provider"`
	BaseURL                  string `json:"base_url"`
	APIKey                   string `json:"api_key"`
	SummaryModel             string `json:"summary_model"`
	ReviewModel              string `json:"review_model"`
	AskModel                 string `json:"ask_model"`
	KnowledgeModel           string `json:"knowledge_model"`
	ArchitectureDiagramModel string `json:"architecture_diagram_model"`
	ReportModel              string `json:"report_model"`
	DraftModel               string `json:"draft_model"`
	TimeoutSecs              int    `json:"timeout_secs"`
	AdvancedMode             bool   `json:"advanced_mode"`
	// Version is bumped on every Save so resolvers on other replicas can
	// detect that the workspace settings changed without polling. Zero
	// means the record has never been saved (or pre-dates the version
	// field). The resolver treats version=0 as "no workspace layer".
	Version uint64 `json:"version"`
}

// ErrEncryptionKeyRequired is returned by SaveLLMConfig when the caller
// supplies a non-empty API key but the store has no encryption key
// configured AND the OSS escape hatch is off. The REST handler maps this
// to a 422 Unprocessable Entity with a clear admin-facing message.
//
// R3: this is now a wrapper around secretcipher.ErrEncryptionKeyRequired
// so callers can match on either error via errors.Is.
var ErrEncryptionKeyRequired = fmt.Errorf("llm api key cannot be saved without an encryption key (set SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY or enable SOURCEBRIDGE_ALLOW_UNENCRYPTED_LLM_KEY for OSS development): %w", secretcipher.ErrEncryptionKeyRequired)

// ErrAPIKeyDecryptFailed is returned by LoadLLMConfig when a stored
// ciphertext blob cannot be decrypted (corruption, key rotation, or a
// programming bug). Fail-closed: callers must NOT silently fall back to
// any other source for the api_key.
//
// R3: now wraps secretcipher.ErrDecryptFailed for unified errors.Is matching.
var ErrAPIKeyDecryptFailed = fmt.Errorf("llm api key decrypt failed; refusing to return a partial config: %w", secretcipher.ErrDecryptFailed)

// envelopePrefix marks ciphertext stored under the v1 envelope. The
// stored form is "sbenc:v1:" + base64(nonce || ciphertext_with_tag).
// Absence of this prefix means the value is legacy plaintext (read-
// only — Save never writes plaintext under this prefix).
//
// R3: kept as a constant for the compile-time-stable test surface
// (test files use envelopePrefix directly).
const envelopePrefix = secretcipher.EnvelopePrefix

// LoadLLMConfig reads the workspace LLM config record. The api_key field
// is decrypted before return: prefixed values go through GCM-Open;
// unprefixed values are treated as legacy plaintext (with a one-time
// migration warning in the log).
//
// Decrypt failures (corruption, wrong key, key rotated without
// re-saving) return ErrAPIKeyDecryptFailed — caller MUST surface this
// rather than silently falling back to env or builtin defaults.
func (s *SurrealLLMConfigStore) LoadLLMConfig() (*LLMConfigRecord, error) {
	db := s.client.DB()
	if db == nil {
		return nil, nil
	}

	ctx := context.Background()

	raw, err := surrealdb.Query[[]map[string]interface{}](ctx, db,
		"SELECT provider, base_url, api_key, summary_model, review_model, ask_model, knowledge_model, architecture_diagram_model, report_model, draft_model, timeout_secs, advanced_mode, version FROM ca_llm_config WHERE id = type::thing('ca_llm_config', 'default') LIMIT 1",
		map[string]any{})
	if err != nil {
		slog.Warn("surreal llm config load query failed", "error", err)
		return nil, nil
	}

	if raw == nil || len(*raw) == 0 {
		return nil, nil
	}

	qr := (*raw)[0]
	if qr.Error != nil {
		slog.Warn("llm config load: query error", "error", fmt.Sprintf("%v", qr.Error))
		return nil, nil
	}

	if len(qr.Result) == 0 {
		return nil, nil
	}

	row := qr.Result[0]
	rec := &LLMConfigRecord{
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

	// API key path: fail-closed on decrypt failure.
	storedKey := strVal(row, "api_key")
	plaintext, err := s.decryptAPIKey(storedKey)
	if err != nil {
		slog.Error("llm config load: api_key decrypt failed",
			"error", err,
			"hint", "key rotated or ciphertext corrupted; re-save via /admin/llm or run `sourcebridge migrate llm-secrets`")
		return nil, ErrAPIKeyDecryptFailed
	}
	rec.APIKey = plaintext

	if v, ok := row["timeout_secs"]; ok {
		switch t := v.(type) {
		case float64:
			rec.TimeoutSecs = int(t)
		case uint64:
			rec.TimeoutSecs = int(t)
		case int:
			rec.TimeoutSecs = t
		}
	}
	if v, ok := row["advanced_mode"]; ok {
		if b, ok := v.(bool); ok {
			rec.AdvancedMode = b
		}
	}
	if v, ok := row["version"]; ok {
		switch t := v.(type) {
		case float64:
			rec.Version = uint64(t)
		case uint64:
			rec.Version = t
		case int64:
			if t >= 0 {
				rec.Version = uint64(t)
			}
		case int:
			if t >= 0 {
				rec.Version = uint64(t)
			}
		}
	}
	return rec, nil
}

// LoadLLMConfigVersion fetches just the version cell. The resolver calls
// this on every Resolve to detect cross-replica workspace saves; keeping
// it as a single-field SELECT keeps the per-resolve cost in the sub-
// millisecond range against a healthy SurrealDB.
//
// Returns 0 when the row doesn't exist yet (no workspace settings saved).
// Errors propagate so the resolver can fall back to its cached snapshot
// and stamp Stale=true.
func (s *SurrealLLMConfigStore) LoadLLMConfigVersion() (uint64, error) {
	db := s.client.DB()
	if db == nil {
		return 0, fmt.Errorf("database not connected")
	}
	ctx := context.Background()
	raw, err := surrealdb.Query[[]map[string]interface{}](ctx, db,
		"SELECT version FROM ca_llm_config WHERE id = type::thing('ca_llm_config', 'default') LIMIT 1",
		map[string]any{})
	if err != nil {
		return 0, err
	}
	if raw == nil || len(*raw) == 0 {
		return 0, nil
	}
	qr := (*raw)[0]
	if qr.Error != nil {
		return 0, fmt.Errorf("%v", qr.Error)
	}
	if len(qr.Result) == 0 {
		return 0, nil
	}
	row := qr.Result[0]
	v, ok := row["version"]
	if !ok {
		return 0, nil
	}
	switch t := v.(type) {
	case float64:
		return uint64(t), nil
	case uint64:
		return t, nil
	case int64:
		if t < 0 {
			return 0, nil
		}
		return uint64(t), nil
	case int:
		if t < 0 {
			return 0, nil
		}
		return uint64(t), nil
	}
	return 0, nil
}

func (s *SurrealLLMConfigStore) SaveLLMConfig(rec *LLMConfigRecord) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}

	// Encrypt the api_key (or refuse to save when no key is available
	// and we're not in OSS-escape-hatch mode).
	storedAPIKey, err := s.encryptAPIKey(rec.APIKey)
	if err != nil {
		return err
	}

	ctx := context.Background()

	// Ensure table exists (idempotent)
	_, err = surrealdb.Query[interface{}](ctx, db, `
		DEFINE TABLE IF NOT EXISTS ca_llm_config SCHEMAFULL;
		DEFINE FIELD IF NOT EXISTS provider ON ca_llm_config TYPE string;
		DEFINE FIELD IF NOT EXISTS base_url ON ca_llm_config TYPE string;
		DEFINE FIELD IF NOT EXISTS api_key ON ca_llm_config TYPE string;
		DEFINE FIELD IF NOT EXISTS summary_model ON ca_llm_config TYPE string;
		DEFINE FIELD IF NOT EXISTS review_model ON ca_llm_config TYPE string;
		DEFINE FIELD IF NOT EXISTS ask_model ON ca_llm_config TYPE string;
		DEFINE FIELD IF NOT EXISTS knowledge_model ON ca_llm_config TYPE string;
		DEFINE FIELD IF NOT EXISTS architecture_diagram_model ON ca_llm_config TYPE string;
		DEFINE FIELD IF NOT EXISTS report_model ON ca_llm_config TYPE string;
		DEFINE FIELD IF NOT EXISTS draft_model ON ca_llm_config TYPE string;
		DEFINE FIELD IF NOT EXISTS timeout_secs ON ca_llm_config TYPE int;
		DEFINE FIELD IF NOT EXISTS advanced_mode ON ca_llm_config TYPE bool;
		DEFINE FIELD IF NOT EXISTS version ON ca_llm_config TYPE int DEFAULT 0;
	`, map[string]any{})
	if err != nil {
		slog.Warn("failed to ensure ca_llm_config table", "error", err)
	}

	// version = (version OR 0) + 1: monotonically bumps on every save so
	// resolvers on other replicas can detect changes via the lightweight
	// LoadLLMConfigVersion probe. Atomic at the row level — concurrent
	// saves UPSERT serially in SurrealDB and each one bumps once.
	_, err = surrealdb.Query[interface{}](ctx, db,
		`UPSERT type::thing('ca_llm_config', 'default') SET
			provider = $provider,
			base_url = $base_url,
			api_key = $api_key,
			summary_model = $summary_model,
				review_model = $review_model,
				ask_model = $ask_model,
				knowledge_model = $knowledge_model,
				architecture_diagram_model = $architecture_diagram_model,
				report_model = $report_model,
				draft_model = $draft_model,
			timeout_secs = $timeout_secs,
			advanced_mode = $advanced_mode,
			version = (IF version != NONE THEN version ELSE 0 END) + 1`,
		map[string]any{
			"provider":                   rec.Provider,
			"base_url":                   rec.BaseURL,
			"api_key":                    storedAPIKey,
			"summary_model":              rec.SummaryModel,
			"review_model":               rec.ReviewModel,
			"ask_model":                  rec.AskModel,
			"knowledge_model":            rec.KnowledgeModel,
			"architecture_diagram_model": rec.ArchitectureDiagramModel,
			"report_model":               rec.ReportModel,
			"draft_model":                rec.DraftModel,
			"timeout_secs":               rec.TimeoutSecs,
			"advanced_mode":              rec.AdvancedMode,
		},
	)
	if err != nil {
		return err
	}

	// Slog only the provider name, never the api key. The api_key_set
	// boolean lets operators verify a key landed without leaking the
	// value into logs.
	slog.Info("llm config persisted to database",
		"provider", rec.Provider,
		"api_key_set", rec.APIKey != "",
		"encrypted_at_rest", strings.HasPrefix(storedAPIKey, envelopePrefix))
	return nil
}

// ─────────────────────────────────────────────────────────────────────────
// Encryption-at-rest (delegated to secretcipher.Cipher)
// ─────────────────────────────────────────────────────────────────────────

// encryptAPIKey delegates to the cipher. Translates the cipher's sentinel
// errors to the store's wrapped errors so callers can use either form
// with errors.Is.
func (s *SurrealLLMConfigStore) encryptAPIKey(plaintext string) (string, error) {
	stored, err := s.cipher.Encrypt(plaintext)
	if err != nil {
		if errors.Is(err, secretcipher.ErrEncryptionKeyRequired) {
			return "", ErrEncryptionKeyRequired
		}
		return "", fmt.Errorf("llm api key encrypt: %w", err)
	}
	return stored, nil
}

// decryptAPIKey delegates to the cipher. A decryption failure is
// returned as ErrAPIKeyDecryptFailed (which wraps secretcipher.ErrDecryptFailed).
func (s *SurrealLLMConfigStore) decryptAPIKey(stored string) (string, error) {
	plaintext, err := s.cipher.Decrypt(stored)
	if err != nil {
		if errors.Is(err, secretcipher.ErrDecryptFailed) {
			return "", ErrAPIKeyDecryptFailed
		}
		return "", fmt.Errorf("llm api key decrypt: %w", err)
	}
	return plaintext, nil
}

// strVal safely extracts a string value from a map.
func strVal(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return v
}

// EncryptedAPIKey is exposed for the migration command and the test
// suite so they can verify the on-disk form. NOT used in normal Load /
// Save paths.
func (s *SurrealLLMConfigStore) EncryptedAPIKey(plaintext string) (string, error) {
	return s.encryptAPIKey(plaintext)
}

// DecryptedAPIKey is the test/migration counterpart of EncryptedAPIKey.
func (s *SurrealLLMConfigStore) DecryptedAPIKey(stored string) (string, error) {
	return s.decryptAPIKey(stored)
}

// IsEncrypted returns true when stored is a v1-envelope-prefixed value.
// Used by the migration command to skip already-migrated rows.
func IsEncrypted(stored string) bool {
	return strings.HasPrefix(stored, envelopePrefix)
}
