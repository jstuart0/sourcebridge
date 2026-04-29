// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package secretcipher provides at-rest encryption envelopes for secrets
// stored in the SurrealDB layer (LLM API keys, git tokens, future Vault
// adapters). Today there is one production envelope, sbenc:v1, implemented
// by AESGCMCipher; the interface exists so a future Vault-backed
// implementation can swap in without touching every caller.
//
// IMPORTANT: this package is not the place for the LivingWikiSettingsStore
// envelope (which uses a different, older bare-base64 shape). That store
// keeps its own helpers; migrating it to sbenc:v1 is a separate effort
// captured in the R3 followups doc.
package secretcipher

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
)

// EnvelopePrefix marks ciphertext stored under the v1 envelope. The
// stored form is "sbenc:v1:" + base64(nonce || ciphertext_with_tag).
// Absence of this prefix means the value is legacy plaintext (read-
// only — Encrypt never produces a plaintext output for non-empty input
// when a key is configured).
const EnvelopePrefix = "sbenc:v1:"

// ErrEncryptionKeyRequired is returned by Encrypt when the caller passes
// a non-empty plaintext but the cipher has no encryption key configured
// AND the OSS escape hatch is off. Callers should map this to a 422 with
// an admin-facing remediation message (e.g. "set
// SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY").
var ErrEncryptionKeyRequired = errors.New("secretcipher: encryption key is required to save a non-empty secret (set SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY or enable the OSS escape hatch)")

// ErrDecryptFailed is returned by Decrypt when a stored value carries
// the v1 envelope prefix but the ciphertext cannot be decoded or the
// authentication tag does not verify (corruption, key rotation without
// re-save, or a programming bug). Callers MUST surface this rather than
// silently falling back to any other source for the secret — fail-closed
// is the whole point of the envelope.
var ErrDecryptFailed = errors.New("secretcipher: stored value could not be decrypted; refusing to return a partial value")

// Cipher is the contract every at-rest encryption envelope obeys.
// Implementations must be safe for concurrent use.
type Cipher interface {
	// Encrypt converts plaintext to a stored form. Empty plaintext
	// returns empty stored (no envelope). Non-empty plaintext with no
	// key configured and no escape hatch returns ErrEncryptionKeyRequired.
	Encrypt(plaintext string) (stored string, err error)

	// Decrypt reverses Encrypt. Empty stored returns empty plaintext.
	// Envelope-prefixed stored that cannot be decrypted returns
	// ErrDecryptFailed. Unprefixed stored is treated as legacy plaintext
	// (returned as-is + one-time WARN).
	Decrypt(stored string) (plaintext string, err error)

	// IsEnvelopeEncrypted reports whether stored carries this cipher's
	// envelope prefix (true) or is legacy/empty (false).
	IsEnvelopeEncrypted(stored string) bool
}

// AESGCMCipher implements the sbenc:v1 envelope:
//
//	stored = "sbenc:v1:" + base64(nonce || ciphertext_with_tag)
//
// Empty plaintext encrypts to empty (no envelope). Empty stored decrypts
// to empty. Unprefixed stored reads as legacy plaintext (one-time WARN).
// Prefixed but undecryptable stored returns ErrDecryptFailed.
//
// Concurrency: the derived key and configuration fields are immutable
// post-construction; the cipher is safe for concurrent Encrypt/Decrypt
// calls.
type AESGCMCipher struct {
	key              []byte // 32 bytes (SHA-256 of passphrase); empty when no key
	allowUnencrypted bool   // OSS escape hatch
	// loadedLegacyOnce rate-limits the "this row is unencrypted; please
	// migrate" WARN to one log line per process lifetime.
	loadedLegacyOnce sync.Once
	// savedUnencryptedOnce rate-limits the "AllowUnencrypted is on and we
	// just saved plaintext" WARN to one log line per process lifetime.
	savedUnencryptedOnce sync.Once
}

// NewAESGCMCipher constructs a cipher under the configured passphrase.
// An empty passphrase disables encryption; in that mode Encrypt of
// non-empty plaintext returns ErrEncryptionKeyRequired unless
// allowUnencrypted is true (OSS dev escape hatch).
func NewAESGCMCipher(passphrase string, allowUnencrypted bool) *AESGCMCipher {
	c := &AESGCMCipher{allowUnencrypted: allowUnencrypted}
	if passphrase != "" {
		h := sha256.Sum256([]byte(passphrase))
		c.key = h[:]
	}
	return c
}

// IsEnvelopeEncrypted reports whether stored is a v1-envelope-prefixed
// value (true) versus empty/legacy (false).
func (c *AESGCMCipher) IsEnvelopeEncrypted(stored string) bool {
	return strings.HasPrefix(stored, EnvelopePrefix)
}

// Encrypt returns the form to store in the DB:
//   - empty input → empty (no encryption needed for an absent secret).
//   - encryption disabled (no key, no escape hatch, non-empty input) →
//     ErrEncryptionKeyRequired.
//   - encryption disabled with escape hatch on → store plaintext +
//     emit a one-time WARN.
//   - encryption enabled → "sbenc:v1:" + base64(nonce || ciphertext).
func (c *AESGCMCipher) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	if len(c.key) == 0 {
		if !c.allowUnencrypted {
			return "", ErrEncryptionKeyRequired
		}
		c.savedUnencryptedOnce.Do(func() {
			slog.Warn("secretcipher: AllowUnencrypted is on; saving secret as plaintext (NOT recommended for production)")
		})
		return plaintext, nil
	}

	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", fmt.Errorf("secretcipher encrypt: aes new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("secretcipher encrypt: gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("secretcipher encrypt: nonce: %w", err)
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return EnvelopePrefix + base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt reverses Encrypt:
//   - empty input → empty plaintext.
//   - prefixed → AES-GCM decrypt; failure returns ErrDecryptFailed.
//   - unprefixed → legacy plaintext, returned as-is + one-time WARN.
//
// Encryption-disabled (no key) decoding of a prefixed value returns an
// error — we cannot decrypt without the key, and silently returning the
// envelope string or "" would be worse than failing the load.
func (c *AESGCMCipher) Decrypt(stored string) (string, error) {
	if stored == "" {
		return "", nil
	}
	if !strings.HasPrefix(stored, EnvelopePrefix) {
		// Legacy plaintext path. Warn once per process; never auto-
		// migrate — the migration command is the only place that
		// re-saves under encryption. Auto-migrating from Decrypt
		// would race concurrent Encrypts.
		c.loadedLegacyOnce.Do(func() {
			slog.Warn("secretcipher: stored secret is unencrypted on disk; run the appropriate migrate command (or re-save via the admin UI) to migrate to the v1 envelope")
		})
		return stored, nil
	}
	if len(c.key) == 0 {
		return "", fmt.Errorf("%w: stored value is envelope-encrypted but no encryption key is configured", ErrDecryptFailed)
	}
	encoded := strings.TrimPrefix(stored, EnvelopePrefix)
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("%w: base64 decode: %v", ErrDecryptFailed, err)
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", fmt.Errorf("%w: aes new cipher: %v", ErrDecryptFailed, err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("%w: gcm: %v", ErrDecryptFailed, err)
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("%w: ciphertext too short", ErrDecryptFailed)
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("%w: gcm open: %v", ErrDecryptFailed, err)
	}
	return string(plaintext), nil
}

// HasKey reports whether the cipher has an encryption key configured.
// Used by callers that need to surface a startup WARN when encryption
// is disabled in production.
func (c *AESGCMCipher) HasKey() bool {
	return len(c.key) > 0
}

// AllowsUnencrypted reports whether the OSS escape hatch is enabled.
// Used by callers that log a startup WARN.
func (c *AESGCMCipher) AllowsUnencrypted() bool {
	return c.allowUnencrypted
}
