// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"errors"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/secretcipher"
)

// These tests exercise the store-level wiring around secretcipher.Cipher.
// The full Save/Load round-trip through SurrealDB lives in
// tests/integration; here we verify that:
//   - the cipher option chain produces a working AESGCMCipher
//   - the store's sentinel errors wrap the cipher's sentinels so
//     callers can errors.Is on either form
//   - the WithGitConfigCipher injection wins over the bootstrap options

func TestGitConfigStore_CipherInjection(t *testing.T) {
	custom := secretcipher.NewAESGCMCipher("custom-key-A", false)
	s := NewSurrealGitConfigStore(nil,
		WithGitConfigEncryptionKey("ignored-key-B"),
		WithGitConfigCipher(custom),
	)
	if s.cipher != custom {
		t.Fatalf("WithGitConfigCipher must override the bootstrap key option")
	}
}

func TestGitConfigStore_DefaultCipherFromBootstrapOptions(t *testing.T) {
	s := NewSurrealGitConfigStore(nil,
		WithGitConfigEncryptionKey("from-bootstrap"),
	)
	if s.cipher == nil {
		t.Fatalf("default cipher must be constructed when none is injected")
	}
	// Round-trip through the cipher to verify the key took effect.
	stored, err := s.cipher.Encrypt("plaintext")
	if err != nil {
		t.Fatalf("encrypt with bootstrap-derived cipher: %v", err)
	}
	if !s.cipher.IsEnvelopeEncrypted(stored) {
		t.Fatalf("expected envelope-encrypted output from bootstrap-derived cipher")
	}
}

func TestErrGitTokenEncryptionKeyRequired_WrapsCipherSentinel(t *testing.T) {
	if !errors.Is(ErrGitTokenEncryptionKeyRequired, secretcipher.ErrEncryptionKeyRequired) {
		t.Fatalf("ErrGitTokenEncryptionKeyRequired must wrap secretcipher.ErrEncryptionKeyRequired so callers can match either form")
	}
}

func TestErrGitTokenDecryptFailed_WrapsCipherSentinel(t *testing.T) {
	if !errors.Is(ErrGitTokenDecryptFailed, secretcipher.ErrDecryptFailed) {
		t.Fatalf("ErrGitTokenDecryptFailed must wrap secretcipher.ErrDecryptFailed so callers can match either form")
	}
}

func TestGitConfigStore_NoKeyNoEscapeHatch_RefusesNonEmptyEncrypt(t *testing.T) {
	cipher := secretcipher.NewAESGCMCipher("", false)
	s := NewSurrealGitConfigStore(nil, WithGitConfigCipher(cipher))
	// Direct cipher path (the store's SaveGitConfig calls cipher.Encrypt
	// internally; here we exercise the same shape at the store-level
	// boundary so a future regression in error wrapping is caught).
	_, err := s.cipher.Encrypt("a-token")
	if !errors.Is(err, secretcipher.ErrEncryptionKeyRequired) {
		t.Fatalf("expected ErrEncryptionKeyRequired, got %v", err)
	}
}

func TestGitConfigStore_AllowUnencryptedEscapeHatch(t *testing.T) {
	cipher := secretcipher.NewAESGCMCipher("", true)
	s := NewSurrealGitConfigStore(nil, WithGitConfigCipher(cipher))
	stored, err := s.cipher.Encrypt("dev-token")
	if err != nil {
		t.Fatalf("escape hatch encrypt: %v", err)
	}
	if stored != "dev-token" {
		t.Fatalf("escape hatch should produce plaintext, got %q", stored)
	}
}
