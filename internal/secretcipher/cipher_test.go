// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package secretcipher

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"
)

const testPassphrase = "test-passphrase-do-not-use-in-prod"

func TestAESGCMCipher_RoundTrip(t *testing.T) {
	c := MustNewAESGCMCipher(testPassphrase, DeriveInstallationSaltFromKey(testPassphrase), false)

	plaintext := "sk-anthropic-1234567890"
	stored, err := c.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	if !strings.HasPrefix(stored, EnvelopePrefix) {
		t.Fatalf("stored should be envelope-prefixed, got: %q", stored)
	}
	if !c.IsEnvelopeEncrypted(stored) {
		t.Fatalf("IsEnvelopeEncrypted should be true for envelope-prefixed stored")
	}

	decoded, err := c.Decrypt(stored)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	if decoded != plaintext {
		t.Fatalf("round-trip mismatch: got %q, want %q", decoded, plaintext)
	}
}

func TestAESGCMCipher_EmptyPlaintext(t *testing.T) {
	c := MustNewAESGCMCipher(testPassphrase, DeriveInstallationSaltFromKey(testPassphrase), false)

	stored, err := c.Encrypt("")
	if err != nil {
		t.Fatalf("encrypt empty: %v", err)
	}
	if stored != "" {
		t.Fatalf("encrypt empty should produce empty stored, got: %q", stored)
	}

	out, err := c.Decrypt("")
	if err != nil {
		t.Fatalf("decrypt empty: %v", err)
	}
	if out != "" {
		t.Fatalf("decrypt empty should produce empty plaintext, got: %q", out)
	}
}

func TestAESGCMCipher_LegacyPlaintext(t *testing.T) {
	c := MustNewAESGCMCipher(testPassphrase, DeriveInstallationSaltFromKey(testPassphrase), false)

	// Stored without the envelope prefix should be returned as-is.
	out, err := c.Decrypt("legacy-plaintext-token")
	if err != nil {
		t.Fatalf("decrypt legacy: %v", err)
	}
	if out != "legacy-plaintext-token" {
		t.Fatalf("legacy plaintext should pass through, got: %q", out)
	}
	if c.IsEnvelopeEncrypted("legacy-plaintext-token") {
		t.Fatalf("IsEnvelopeEncrypted should be false for unprefixed value")
	}
}

func TestAESGCMCipher_NoKeyNoEscapeHatch_RefusesEncrypt(t *testing.T) {
	c := MustNewAESGCMCipher("", DeriveInstallationSaltFromKey(""), false)

	// Empty plaintext is fine.
	if _, err := c.Encrypt(""); err != nil {
		t.Fatalf("encrypt empty with no key should succeed: %v", err)
	}

	// Non-empty plaintext refused.
	_, err := c.Encrypt("some-secret")
	if !errors.Is(err, ErrEncryptionKeyRequired) {
		t.Fatalf("expected ErrEncryptionKeyRequired, got: %v", err)
	}
}

func TestAESGCMCipher_NoKeyWithEscapeHatch_StoresPlaintext(t *testing.T) {
	c := MustNewAESGCMCipher("", DeriveInstallationSaltFromKey(""), true)

	stored, err := c.Encrypt("dev-secret")
	if err != nil {
		t.Fatalf("encrypt with escape hatch: %v", err)
	}
	if stored != "dev-secret" {
		t.Fatalf("with escape hatch + no key, stored should equal plaintext, got: %q", stored)
	}
	if c.IsEnvelopeEncrypted(stored) {
		t.Fatalf("escape-hatch storage should not be envelope-encrypted")
	}
}

func TestAESGCMCipher_DecryptCorruptedReturnsErrDecryptFailed(t *testing.T) {
	c := MustNewAESGCMCipher(testPassphrase, DeriveInstallationSaltFromKey(testPassphrase), false)

	// Garbage after the prefix.
	corrupt := EnvelopePrefix + base64.StdEncoding.EncodeToString([]byte("not-a-real-ciphertext"))
	_, err := c.Decrypt(corrupt)
	if !errors.Is(err, ErrDecryptFailed) {
		t.Fatalf("expected ErrDecryptFailed, got: %v", err)
	}
}

func TestAESGCMCipher_DecryptNotBase64ReturnsErrDecryptFailed(t *testing.T) {
	c := MustNewAESGCMCipher(testPassphrase, DeriveInstallationSaltFromKey(testPassphrase), false)

	// Prefix + non-base64 garbage.
	corrupt := EnvelopePrefix + "!!!not-base64!!!"
	_, err := c.Decrypt(corrupt)
	if !errors.Is(err, ErrDecryptFailed) {
		t.Fatalf("expected ErrDecryptFailed, got: %v", err)
	}
}

func TestAESGCMCipher_DecryptShortCiphertextReturnsErrDecryptFailed(t *testing.T) {
	c := MustNewAESGCMCipher(testPassphrase, DeriveInstallationSaltFromKey(testPassphrase), false)

	// Prefix + base64 of bytes shorter than the GCM nonce.
	tooShort := EnvelopePrefix + base64.StdEncoding.EncodeToString([]byte{1, 2, 3})
	_, err := c.Decrypt(tooShort)
	if !errors.Is(err, ErrDecryptFailed) {
		t.Fatalf("expected ErrDecryptFailed, got: %v", err)
	}
}

func TestAESGCMCipher_DecryptEnvelopedWithoutKeyFails(t *testing.T) {
	c := MustNewAESGCMCipher(testPassphrase, DeriveInstallationSaltFromKey(testPassphrase), false)
	stored, err := c.Encrypt("secret")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	cNoKey := MustNewAESGCMCipher("", DeriveInstallationSaltFromKey(""), false)
	_, err = cNoKey.Decrypt(stored)
	if !errors.Is(err, ErrDecryptFailed) {
		t.Fatalf("expected ErrDecryptFailed when no key configured for prefixed value, got: %v", err)
	}
}

func TestAESGCMCipher_DerivedKeyDeterminism(t *testing.T) {
	// Two ciphers built from the same passphrase decrypt each other's output.
	a := MustNewAESGCMCipher(testPassphrase, DeriveInstallationSaltFromKey(testPassphrase), false)
	b := MustNewAESGCMCipher(testPassphrase, DeriveInstallationSaltFromKey(testPassphrase), false)

	stored, err := a.Encrypt("shared-plaintext")
	if err != nil {
		t.Fatalf("encrypt by a: %v", err)
	}
	out, err := b.Decrypt(stored)
	if err != nil {
		t.Fatalf("decrypt by b: %v", err)
	}
	if out != "shared-plaintext" {
		t.Fatalf("cross-cipher roundtrip mismatch: got %q", out)
	}
}

func TestAESGCMCipher_DifferentKeyCannotDecrypt(t *testing.T) {
	a := MustNewAESGCMCipher("alpha-passphrase", DeriveInstallationSaltFromKey("alpha-passphrase"), false)
	b := MustNewAESGCMCipher("beta-passphrase", DeriveInstallationSaltFromKey("beta-passphrase"), false)

	stored, _ := a.Encrypt("secret")
	_, err := b.Decrypt(stored)
	if !errors.Is(err, ErrDecryptFailed) {
		t.Fatalf("expected ErrDecryptFailed for mismatched key, got: %v", err)
	}
}

func TestAESGCMCipher_NonceUniqueness(t *testing.T) {
	// Same plaintext encrypted twice should produce different stored
	// values (random nonce per encrypt). Guards against accidental
	// removal of the random-nonce step.
	c := MustNewAESGCMCipher(testPassphrase, DeriveInstallationSaltFromKey(testPassphrase), false)
	a, _ := c.Encrypt("same-plaintext")
	b, _ := c.Encrypt("same-plaintext")
	if a == b {
		t.Fatalf("two encrypts of the same plaintext produced identical stored values; nonce reuse?")
	}
}

func TestAESGCMCipher_HasKey(t *testing.T) {
	if MustNewAESGCMCipher("k", DeriveInstallationSaltFromKey("k"), false).HasKey() != true {
		t.Fatalf("HasKey should be true when passphrase is set")
	}
	if MustNewAESGCMCipher("", DeriveInstallationSaltFromKey(""), false).HasKey() != false {
		t.Fatalf("HasKey should be false when passphrase is empty")
	}
}

func TestAESGCMCipher_AllowsUnencrypted(t *testing.T) {
	if !MustNewAESGCMCipher("k", DeriveInstallationSaltFromKey("k"), true).AllowsUnencrypted() {
		t.Fatalf("AllowsUnencrypted should be true when constructed with allowUnencrypted=true")
	}
	if MustNewAESGCMCipher("k", DeriveInstallationSaltFromKey("k"), false).AllowsUnencrypted() {
		t.Fatalf("AllowsUnencrypted should be false when constructed with allowUnencrypted=false")
	}
}

// Compile-time verify the implementation satisfies the interface.
var _ Cipher = (*AESGCMCipher)(nil)

// CA-200 transparent migration: legacy sbenc:v1 envelopes written by the
// previous cipher (SHA-256 KDF, no salt) must remain decryptable under
// the new cipher with the same passphrase. This test exists because the
// original CA-200 fail-closed posture broke the homelab deploy on
// 2026-05-08 — the master plan's "0 prod installs" assumption was wrong
// for installations with existing v1-encrypted data.
func TestDecryptV1EnvelopeIsTransparent(t *testing.T) {
	const passphrase = "homelab-passphrase-32-chars-long-x"

	v1Sum := sha256.Sum256([]byte(passphrase))
	block, err := aes.NewCipher(v1Sum[:])
	if err != nil {
		t.Fatalf("v1 setup: aes new cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("v1 setup: gcm: %v", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatalf("v1 setup: nonce: %v", err)
	}
	plaintext := "sk-test-secret-llm-api-key-12345"
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	v1Stored := EnvelopeV1Prefix + base64.StdEncoding.EncodeToString(ciphertext)

	c := MustNewAESGCMCipher(passphrase, DeriveInstallationSaltFromKey(passphrase), false)

	got, err := c.Decrypt(v1Stored)
	if err != nil {
		t.Fatalf("v1 decrypt should succeed transparently, got err: %v", err)
	}
	if got != plaintext {
		t.Errorf("v1 decrypt: got %q, want %q", got, plaintext)
	}

	v2Stored, err := c.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("re-encrypt: %v", err)
	}
	if !c.IsEnvelopeEncrypted(v2Stored) {
		t.Errorf("re-encrypted form should be a v2 envelope; got %q", v2Stored)
	}
	if !strings.HasPrefix(v2Stored, EnvelopeV2Prefix) {
		t.Errorf("expected v2 prefix, got %q", v2Stored[:min(20, len(v2Stored))])
	}
	roundTripped, err := c.Decrypt(v2Stored)
	if err != nil {
		t.Fatalf("v2 round-trip decrypt: %v", err)
	}
	if roundTripped != plaintext {
		t.Errorf("v2 round-trip: got %q, want %q", roundTripped, plaintext)
	}
}
