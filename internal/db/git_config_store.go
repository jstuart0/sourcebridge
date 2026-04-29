// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/surrealdb/surrealdb.go"

	"github.com/sourcebridge/sourcebridge/internal/secretcipher"
)

// SurrealGitConfigStore persists git configuration (default PAT, SSH key
// path) in SurrealDB using a well-known record ID. R3 slice 2:
//
//   - default_token is encrypted at rest under the sbenc:v1 envelope via
//     a secretcipher.Cipher, mirroring SurrealLLMConfigStore.
//   - LoadGitConfig / LoadGitConfigVersion / SaveGitConfig take ctx so a
//     cancelled request bypasses the DB rather than completing on
//     context.Background.
//   - SaveGitConfig bumps a monotonic version cell so the resolver on a
//     peer replica detects cross-replica saves on the very next Resolve
//     (without polling).
//
// The on-disk byte format of default_token is unchanged when written via
// the post-R3 code path: sbenc:v1:<base64>. Existing legacy plaintext
// rows are read transparently with a one-time WARN; new saves always
// write the envelope (or refuse the save when no encryption key is
// configured, unless the OSS escape hatch is on — see ErrGitTokenEncryptionKeyRequired).
type SurrealGitConfigStore struct {
	client *SurrealDB
	cipher secretcipher.Cipher
	// Held for backward-compatible options chaining: NewSurrealGitConfigStore
	// still accepts WithGitConfigEncryptionKey + WithGitConfigAllowUnencrypted
	// and constructs the cipher from those values. Live callers (cli/serve.go)
	// inject a pre-built cipher via WithGitConfigCipher; tests use either form.
	encryptionKeyForBootstrap    string
	allowUnencryptedForBootstrap bool
}

// GitConfigStoreOption configures optional behavior.
type GitConfigStoreOption func(*SurrealGitConfigStore)

// WithGitConfigEncryptionKey supplies the at-rest encryption passphrase
// (typically SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY). Empty disables
// encryption — non-empty saves then require WithGitConfigAllowUnencrypted
// to be true (OSS dev escape hatch); otherwise SaveGitConfig refuses.
func WithGitConfigEncryptionKey(key string) GitConfigStoreOption {
	return func(s *SurrealGitConfigStore) {
		s.encryptionKeyForBootstrap = key
	}
}

// WithGitConfigAllowUnencrypted toggles the OSS escape hatch. When true
// AND no encryption key is configured, the store saves default_token as
// plaintext + emits a one-time WARN. Production deployments leave this
// off; OSS dev sets SOURCEBRIDGE_ALLOW_UNENCRYPTED_GIT_TOKEN=true.
func WithGitConfigAllowUnencrypted(allow bool) GitConfigStoreOption {
	return func(s *SurrealGitConfigStore) {
		s.allowUnencryptedForBootstrap = allow
	}
}

// WithGitConfigCipher injects a pre-built secretcipher.Cipher. When set,
// it overrides any cipher constructed from WithGitConfigEncryptionKey /
// WithGitConfigAllowUnencrypted. Production wiring uses this so a single
// cipher is shared across the LLM and git stores.
func WithGitConfigCipher(c secretcipher.Cipher) GitConfigStoreOption {
	return func(s *SurrealGitConfigStore) {
		s.cipher = c
	}
}

// NewSurrealGitConfigStore creates a new git config store backed by SurrealDB.
//
// If WithGitConfigCipher is not supplied, an AESGCMCipher is constructed
// from the encryption-key and allow-unencrypted options.
func NewSurrealGitConfigStore(client *SurrealDB, opts ...GitConfigStoreOption) *SurrealGitConfigStore {
	s := &SurrealGitConfigStore{client: client}
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

// ErrGitTokenEncryptionKeyRequired is returned by SaveGitConfig when the
// caller supplies a non-empty token but the store has no encryption key
// configured AND the OSS escape hatch is off. The REST handler maps this
// to a 422 with a clear admin-facing message. Wraps
// secretcipher.ErrEncryptionKeyRequired so callers can match either form
// via errors.Is.
var ErrGitTokenEncryptionKeyRequired = fmt.Errorf("git token cannot be saved without an encryption key (set SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY or enable SOURCEBRIDGE_ALLOW_UNENCRYPTED_GIT_TOKEN for OSS development): %w", secretcipher.ErrEncryptionKeyRequired)

// ErrGitTokenDecryptFailed is returned by LoadGitConfig when the stored
// default_token has the v1 envelope prefix but cannot be decrypted
// (corruption, key rotation, or a programming bug). Fail-closed: the
// resolver MUST surface this as a Snapshot.IntegrityError rather than
// silently falling back to env-bootstrap. Wraps secretcipher.ErrDecryptFailed.
var ErrGitTokenDecryptFailed = fmt.Errorf("git token decrypt failed; refusing to return a partial config: %w", secretcipher.ErrDecryptFailed)

// LoadGitConfig reads the workspace git config record. The default_token
// field is decrypted via the cipher; envelope-prefixed values that fail
// to decrypt return ErrGitTokenDecryptFailed (fail-closed). Empty stored
// values return empty plaintext. Unprefixed legacy values pass through
// with a one-time migration WARN.
//
// The returned version is the stored monotonic version cell, used by the
// resolver's version-keyed cache. A row that has never been saved
// returns empty values + version 0 + nil error.
//
// A nil DB connection (embedded mode without SurrealDB) returns empty
// values + version 0 + nil error so callers fall through to env-bootstrap
// without a hard failure.
func (s *SurrealGitConfigStore) LoadGitConfig(ctx context.Context) (token, sshKeyPath string, version uint64, err error) {
	d := s.client.DB()
	if d == nil {
		return "", "", 0, nil
	}

	raw, qerr := surrealdb.Query[[]map[string]interface{}](ctx, d,
		"SELECT default_token, ssh_key_path, version FROM ca_git_config WHERE id = type::thing('ca_git_config', 'default') LIMIT 1",
		map[string]any{})
	if qerr != nil {
		// Surface real errors to the resolver so it can serve cached
		// snapshot + Stale=true. The legacy code swallowed errors, which
		// concealed the multi-replica bug.
		return "", "", 0, fmt.Errorf("git config: load query: %w", qerr)
	}
	if raw == nil || len(*raw) == 0 {
		return "", "", 0, nil
	}
	qr := (*raw)[0]
	if qr.Error != nil {
		return "", "", 0, fmt.Errorf("git config: query error: %v", qr.Error)
	}
	if len(qr.Result) == 0 {
		return "", "", 0, nil
	}
	row := qr.Result[0]

	storedToken, _ := row["default_token"].(string)
	sshKeyPath, _ = row["ssh_key_path"].(string)
	version = uintVal(row, "version")

	plaintext, derr := s.cipher.Decrypt(storedToken)
	if derr != nil {
		if errors.Is(derr, secretcipher.ErrDecryptFailed) {
			return "", "", version, ErrGitTokenDecryptFailed
		}
		return "", "", version, fmt.Errorf("git config: decrypt: %w", derr)
	}
	return plaintext, sshKeyPath, version, nil
}

// LoadGitConfigVersion returns just the monotonic version cell. The
// resolver hits this on every Resolve to detect cross-replica saves; the
// full LoadGitConfig is only invoked on a cache miss. A nil DB returns
// 0 + nil so callers degrade to env-bootstrap.
func (s *SurrealGitConfigStore) LoadGitConfigVersion(ctx context.Context) (uint64, error) {
	d := s.client.DB()
	if d == nil {
		return 0, nil
	}
	raw, qerr := surrealdb.Query[[]map[string]interface{}](ctx, d,
		"SELECT version FROM ca_git_config WHERE id = type::thing('ca_git_config', 'default') LIMIT 1",
		map[string]any{})
	if qerr != nil {
		return 0, fmt.Errorf("git config: version query: %w", qerr)
	}
	if raw == nil || len(*raw) == 0 {
		return 0, nil
	}
	qr := (*raw)[0]
	if qr.Error != nil {
		return 0, fmt.Errorf("git config: version query error: %v", qr.Error)
	}
	if len(qr.Result) == 0 {
		return 0, nil
	}
	return uintVal(qr.Result[0], "version"), nil
}

// SaveGitConfig encrypts the token via the cipher, validates the SSH key
// path (callers must validate before calling — this method assumes a
// valid path), and upserts the row with an atomically-bumped version cell.
//
// Empty token saves empty (no envelope). Non-empty token with no key and
// no escape hatch returns ErrGitTokenEncryptionKeyRequired.
func (s *SurrealGitConfigStore) SaveGitConfig(ctx context.Context, token, sshKeyPath string) error {
	d := s.client.DB()
	if d == nil {
		return fmt.Errorf("database not connected")
	}

	stored, encErr := s.cipher.Encrypt(token)
	if encErr != nil {
		if errors.Is(encErr, secretcipher.ErrEncryptionKeyRequired) {
			return ErrGitTokenEncryptionKeyRequired
		}
		return fmt.Errorf("git config: encrypt: %w", encErr)
	}

	// Ensure table exists (idempotent — covers fresh deployments where
	// migrations haven't been run yet, e.g. first-boot OSS).
	_, _ = surrealdb.Query[interface{}](ctx, d, `
		DEFINE TABLE IF NOT EXISTS ca_git_config SCHEMAFULL;
		DEFINE FIELD IF NOT EXISTS default_token ON ca_git_config TYPE string;
		DEFINE FIELD IF NOT EXISTS ssh_key_path ON ca_git_config TYPE string;
		DEFINE FIELD IF NOT EXISTS version ON ca_git_config TYPE int DEFAULT 0;
	`, map[string]any{})

	// Upsert + atomic version bump. The +1 happens server-side so two
	// replicas saving simultaneously each get a strictly higher version
	// than they read.
	//
	// NOTE: $token is a reserved SurrealDB variable — use $default_token.
	_, qerr := surrealdb.Query[interface{}](ctx, d,
		"UPSERT type::thing('ca_git_config', 'default') SET default_token = $default_token, ssh_key_path = $ssh_key_path, version = (version OR 0) + 1",
		map[string]any{
			"default_token": stored,
			"ssh_key_path":  sshKeyPath,
		},
	)
	if qerr != nil {
		return fmt.Errorf("git config: save: %w", qerr)
	}
	slog.Info("git config persisted to database",
		"default_token_set", stored != "",
		"default_token_envelope", s.cipher.IsEnvelopeEncrypted(stored),
		"ssh_key_path_set", sshKeyPath != "",
	)
	return nil
}

// MigrateGitSecrets re-saves any legacy plaintext default_token row
// under the sbenc:v1 envelope. Idempotent: rows already prefixed are
// skipped. Refuses to run when the cipher has no key (no silent no-op).
func (s *SurrealGitConfigStore) MigrateGitSecrets(ctx context.Context) error {
	// Probe the cipher: if it would refuse to encrypt non-empty input,
	// we don't have a key — refuse to run.
	if _, err := s.cipher.Encrypt("probe"); err != nil {
		if errors.Is(err, secretcipher.ErrEncryptionKeyRequired) {
			return ErrGitTokenEncryptionKeyRequired
		}
		return fmt.Errorf("migrate git-secrets: cipher probe failed: %w", err)
	}

	d := s.client.DB()
	if d == nil {
		return fmt.Errorf("migrate git-secrets: database not connected")
	}

	// Read raw stored value (NOT through LoadGitConfig — we need the
	// envelope-or-plaintext form, not the decrypted plaintext).
	raw, qerr := surrealdb.Query[[]map[string]interface{}](ctx, d,
		"SELECT default_token, ssh_key_path FROM ca_git_config WHERE id = type::thing('ca_git_config', 'default') LIMIT 1",
		map[string]any{})
	if qerr != nil {
		return fmt.Errorf("migrate git-secrets: load: %w", qerr)
	}
	if raw == nil || len(*raw) == 0 || len((*raw)[0].Result) == 0 {
		// Nothing to migrate.
		return nil
	}
	row := (*raw)[0].Result[0]
	storedToken, _ := row["default_token"].(string)
	sshKeyPath, _ := row["ssh_key_path"].(string)

	if storedToken == "" {
		// Empty token; nothing to encrypt.
		return nil
	}
	if s.cipher.IsEnvelopeEncrypted(storedToken) {
		// Already migrated — idempotent skip.
		slog.Info("migrate git-secrets: row is already envelope-encrypted; skipping")
		return nil
	}

	// storedToken is legacy plaintext. Re-save through the normal Save
	// path so it gets the new envelope + a version bump.
	if err := s.SaveGitConfig(ctx, storedToken, sshKeyPath); err != nil {
		return fmt.Errorf("migrate git-secrets: re-save: %w", err)
	}
	slog.Info("migrate git-secrets: legacy plaintext token migrated to v1 envelope")
	return nil
}

// uintVal extracts a uint64 from a SurrealDB row regardless of how the
// driver decoded the integer (json.Number, float64, int64, uint64 all
// land here in practice).
func uintVal(m map[string]interface{}, key string) uint64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case int64:
		if n < 0 {
			return 0
		}
		return uint64(n)
	case uint64:
		return n
	case int:
		if n < 0 {
			return 0
		}
		return uint64(n)
	case float64:
		if n < 0 {
			return 0
		}
		return uint64(n)
	}
	return 0
}
