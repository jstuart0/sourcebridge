// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"time"

	surrealdb "github.com/surrealdb/surrealdb.go"

	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

// LivingWikiSettingsStore persists living-wiki configuration in SurrealDB.
// Secret fields are encrypted with AES-256-GCM before writing and decrypted
// after reading. The encryption key is derived from config.Security.EncryptionKey
// via SHA-256 so any non-empty string can serve as the passphrase.
//
// When encryptionKey is empty, secrets are stored in plaintext. A startup
// warning is emitted by the caller (server main) in that case.
type LivingWikiSettingsStore struct {
	client        *SurrealDB
	encryptionKey string // raw passphrase; key bytes derived via SHA-256
}

// NewLivingWikiSettingsStore creates a store. encryptionKey is the raw
// passphrase from config.Security.EncryptionKey; pass "" to disable encryption
// (development only).
func NewLivingWikiSettingsStore(client *SurrealDB, encryptionKey string) *LivingWikiSettingsStore {
	return &LivingWikiSettingsStore{client: client, encryptionKey: encryptionKey}
}

// Compile-time interface check.
var _ livingwiki.Store = (*LivingWikiSettingsStore)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// SurrealDB DTO
// ─────────────────────────────────────────────────────────────────────────────

type surrealLivingWikiSettings struct {
	// Orchestration
	EnabledSet   bool   `json:"enabled_set"`
	Enabled      bool   `json:"enabled"`
	WorkerCount  int    `json:"worker_count"`
	EventTimeout string `json:"event_timeout"`

	// Plain (non-secret) config
	ConfluenceSite string `json:"confluence_site"`

	// Encrypted secret fields. Stored as base64(nonce+ciphertext).
	GitHubToken             string `json:"github_token"`
	GitLabToken             string `json:"gitlab_token"`
	ConfluenceEmail         string `json:"confluence_email"`
	ConfluenceToken         string `json:"confluence_token"`
	NotionToken             string `json:"notion_token"`
	ConfluenceWebhookSecret string `json:"confluence_webhook_secret"`
	NotionWebhookSecret     string `json:"notion_webhook_secret"`

	UpdatedAt surrealTime `json:"updated_at"`
	UpdatedBy string      `json:"updated_by"`
}

func (r *surrealLivingWikiSettings) toSettings(decrypt func(string) (string, error)) (*livingwiki.Settings, error) {
	s := &livingwiki.Settings{
		WorkerCount:    r.WorkerCount,
		EventTimeout:   r.EventTimeout,
		ConfluenceSite: r.ConfluenceSite,
		UpdatedAt:      r.UpdatedAt.Time,
		UpdatedBy:      r.UpdatedBy,
	}
	if r.EnabledSet {
		b := r.Enabled
		s.Enabled = &b
	}

	var err error
	if s.GitHubToken, err = decrypt(r.GitHubToken); err != nil {
		return nil, fmt.Errorf("decrypt github_token: %w", err)
	}
	if s.GitLabToken, err = decrypt(r.GitLabToken); err != nil {
		return nil, fmt.Errorf("decrypt gitlab_token: %w", err)
	}
	if s.ConfluenceEmail, err = decrypt(r.ConfluenceEmail); err != nil {
		return nil, fmt.Errorf("decrypt confluence_email: %w", err)
	}
	if s.ConfluenceToken, err = decrypt(r.ConfluenceToken); err != nil {
		return nil, fmt.Errorf("decrypt confluence_token: %w", err)
	}
	if s.NotionToken, err = decrypt(r.NotionToken); err != nil {
		return nil, fmt.Errorf("decrypt notion_token: %w", err)
	}
	if s.ConfluenceWebhookSecret, err = decrypt(r.ConfluenceWebhookSecret); err != nil {
		return nil, fmt.Errorf("decrypt confluence_webhook_secret: %w", err)
	}
	if s.NotionWebhookSecret, err = decrypt(r.NotionWebhookSecret); err != nil {
		return nil, fmt.Errorf("decrypt notion_webhook_secret: %w", err)
	}
	return s, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Public interface
// ─────────────────────────────────────────────────────────────────────────────

func (s *LivingWikiSettingsStore) Get(ctx context.Context) (*livingwiki.Settings, error) {
	db := s.client.DB()
	if db == nil {
		return &livingwiki.Settings{}, nil
	}

	sql := `SELECT * FROM lw_settings WHERE id = type::thing('lw_settings', 'default') LIMIT 1`
	result, err := queryOne[[]surrealLivingWikiSettings](ctx, db, sql, nil)
	if err != nil || len(result) == 0 {
		return &livingwiki.Settings{}, nil
	}

	return result[0].toSettings(s.decrypt)
}

func (s *LivingWikiSettingsStore) Set(ctx context.Context, settings *livingwiki.Settings) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}

	enabledSet := settings.Enabled != nil
	enabled := false
	if settings.Enabled != nil {
		enabled = *settings.Enabled
	}

	githubEnc, err := s.encrypt(settings.GitHubToken)
	if err != nil {
		return fmt.Errorf("encrypt github_token: %w", err)
	}
	gitlabEnc, err := s.encrypt(settings.GitLabToken)
	if err != nil {
		return fmt.Errorf("encrypt gitlab_token: %w", err)
	}
	confEmailEnc, err := s.encrypt(settings.ConfluenceEmail)
	if err != nil {
		return fmt.Errorf("encrypt confluence_email: %w", err)
	}
	confTokenEnc, err := s.encrypt(settings.ConfluenceToken)
	if err != nil {
		return fmt.Errorf("encrypt confluence_token: %w", err)
	}
	notionEnc, err := s.encrypt(settings.NotionToken)
	if err != nil {
		return fmt.Errorf("encrypt notion_token: %w", err)
	}
	confWebhookEnc, err := s.encrypt(settings.ConfluenceWebhookSecret)
	if err != nil {
		return fmt.Errorf("encrypt confluence_webhook_secret: %w", err)
	}
	notionWebhookEnc, err := s.encrypt(settings.NotionWebhookSecret)
	if err != nil {
		return fmt.Errorf("encrypt notion_webhook_secret: %w", err)
	}

	sql := `
		UPSERT type::thing('lw_settings', 'default') SET
			tenant_id                    = 'default',
			enabled_set                  = $enabled_set,
			enabled                      = $enabled,
			worker_count                 = $worker_count,
			event_timeout                = $event_timeout,
			confluence_site              = $confluence_site,
			github_token                 = $github_token,
			gitlab_token                 = $gitlab_token,
			confluence_email             = $confluence_email,
			confluence_token             = $confluence_token,
			notion_token                 = $notion_token,
			confluence_webhook_secret    = $confluence_webhook_secret,
			notion_webhook_secret        = $notion_webhook_secret,
			updated_by                   = $updated_by,
			updated_at                   = time::now()
	`

	_, err = surrealdb.Query[interface{}](ctx, db, sql, map[string]any{
		"enabled_set":               enabledSet,
		"enabled":                   enabled,
		"worker_count":              settings.WorkerCount,
		"event_timeout":             settings.EventTimeout,
		"confluence_site":           settings.ConfluenceSite,
		"github_token":              githubEnc,
		"gitlab_token":              gitlabEnc,
		"confluence_email":          confEmailEnc,
		"confluence_token":          confTokenEnc,
		"notion_token":              notionEnc,
		"confluence_webhook_secret": confWebhookEnc,
		"notion_webhook_secret":     notionWebhookEnc,
		"updated_by":                settings.UpdatedBy,
	})
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// AES-256-GCM encryption helpers
// ─────────────────────────────────────────────────────────────────────────────

// deriveKey produces a 32-byte AES key from an arbitrary passphrase.
func (s *LivingWikiSettingsStore) deriveKey() []byte {
	h := sha256.Sum256([]byte(s.encryptionKey))
	return h[:]
}

// encrypt returns base64(nonce + ciphertext) for plaintext.
// Empty plaintext is returned as-is (no encryption, no overhead).
func (s *LivingWikiSettingsStore) encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	if s.encryptionKey == "" {
		return plaintext, nil // dev mode: no encryption
	}

	block, err := aes.NewCipher(s.deriveKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decrypt reverses [encrypt]. Returns "" for empty input.
func (s *LivingWikiSettingsStore) decrypt(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	if s.encryptionKey == "" {
		return encoded, nil // dev mode: no encryption
	}

	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		// Stored before encryption was enabled: treat as plaintext.
		return encoded, nil
	}

	block, err := aes.NewCipher(s.deriveKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		// Corruption or wrong key — fail closed.
		return "", fmt.Errorf("decryption failed: %w", err)
	}
	return string(plaintext), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Encryption-at-rest test helper (exported for tests)
// ─────────────────────────────────────────────────────────────────────────────

// EncryptForTest exposes the encrypt helper so integration tests can assert
// that raw DB values are ciphertext, not plaintext.
func (s *LivingWikiSettingsStore) EncryptForTest(plaintext string) (string, error) {
	return s.encrypt(plaintext)
}

// DecryptForTest exposes the decrypt helper for test round-trip assertions.
func (s *LivingWikiSettingsStore) DecryptForTest(encoded string) (string, error) {
	return s.decrypt(encoded)
}

// LivingWikiSettingsUpdatedAt is a lightweight helper that just reads the
// updated_at timestamp from the stored row. Used by health checks.
func LivingWikiSettingsUpdatedAt(ctx context.Context, store livingwiki.Store) (time.Time, error) {
	s, err := store.Get(ctx)
	if err != nil {
		return time.Time{}, err
	}
	return s.UpdatedAt, nil
}
