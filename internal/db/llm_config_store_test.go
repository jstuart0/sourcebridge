// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

// These tests cover the at-rest encryption logic in isolation — the
// encrypt/decrypt helpers are deterministic given a key, so we can
// exercise round-trips, fail-closed behavior, legacy plaintext
// pass-through, and the OSS escape hatch without standing up a
// SurrealDB connection. Integration coverage of the full Save+Load
// cycle through SurrealDB lives in tests/integration.

func newStoreForCryptoTest(key string, allowUnenc bool) *SurrealLLMConfigStore {
	s := &SurrealLLMConfigStore{
		encryptionKey:    key,
		allowUnencrypted: allowUnenc,
	}
	return s
}

func TestEncryptAPIKey_RoundTrip(t *testing.T) {
	s := newStoreForCryptoTest("super-secret-test-key", false)
	plaintext := "sk-ant-api03-abcdefghijklmnop"

	encrypted, err := s.encryptAPIKey(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !strings.HasPrefix(encrypted, envelopePrefix) {
		t.Errorf("encrypted form missing prefix: got %q", encrypted)
	}
	if encrypted == plaintext {
		t.Errorf("encrypted form should not equal plaintext")
	}

	decrypted, err := s.decryptAPIKey(encrypted)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if decrypted != plaintext {
		t.Errorf("round trip: got %q, want %q", decrypted, plaintext)
	}
}

func TestEncryptAPIKey_EmptyInputReturnsEmpty(t *testing.T) {
	s := newStoreForCryptoTest("any-key", false)
	got, err := s.encryptAPIKey("")
	if err != nil {
		t.Fatalf("encrypt empty: %v", err)
	}
	if got != "" {
		t.Errorf("encrypt empty: got %q, want empty", got)
	}
}

func TestEncryptAPIKey_NoKeyAndNoEscapeHatchFailsClosed(t *testing.T) {
	s := newStoreForCryptoTest("", false)
	_, err := s.encryptAPIKey("sk-secret")
	if err == nil {
		t.Fatal("expected error when no encryption key and no escape hatch")
	}
	if !errors.Is(err, ErrEncryptionKeyRequired) {
		t.Errorf("expected ErrEncryptionKeyRequired, got %v", err)
	}
}

func TestEncryptAPIKey_NoKeyWithEscapeHatchPassesThrough(t *testing.T) {
	s := newStoreForCryptoTest("", true)
	got, err := s.encryptAPIKey("plain-key-os-dev")
	if err != nil {
		t.Fatalf("encrypt with escape hatch: %v", err)
	}
	if got != "plain-key-os-dev" {
		t.Errorf("escape-hatch path should pass through plaintext, got %q", got)
	}
	if strings.HasPrefix(got, envelopePrefix) {
		t.Errorf("escape-hatch path must not add envelope prefix, got %q", got)
	}
}

func TestDecryptAPIKey_LegacyPlaintextPassesThrough(t *testing.T) {
	s := newStoreForCryptoTest("any-key", false)
	got, err := s.decryptAPIKey("legacy-plain-token")
	if err != nil {
		t.Fatalf("decrypt legacy: %v", err)
	}
	if got != "legacy-plain-token" {
		t.Errorf("legacy plaintext: got %q, want legacy-plain-token", got)
	}
}

func TestDecryptAPIKey_FailClosedOnCorruption(t *testing.T) {
	s := newStoreForCryptoTest("test-key", false)
	// Encrypt a key, then mangle the ciphertext to simulate corruption.
	enc, err := s.encryptAPIKey("real-key")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	corrupted := enc + "AAAA" // appends garbage onto base64 → GCM Open will fail
	_, err = s.decryptAPIKey(corrupted)
	if err == nil {
		t.Fatal("expected error on corrupted ciphertext")
	}
}

func TestDecryptAPIKey_FailClosedOnWrongKey(t *testing.T) {
	enc, err := newStoreForCryptoTest("key-A", false).encryptAPIKey("secret")
	if err != nil {
		t.Fatalf("encrypt with key A: %v", err)
	}
	// Try to decrypt with a different key.
	_, err = newStoreForCryptoTest("key-B-different", false).decryptAPIKey(enc)
	if err == nil {
		t.Fatal("expected error when decrypting with wrong key")
	}
}

func TestDecryptAPIKey_PrefixedButNoKeyFailsClosed(t *testing.T) {
	enc, err := newStoreForCryptoTest("the-key", false).encryptAPIKey("secret")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	// Now decrypt with a store that has no key configured.
	_, err = newStoreForCryptoTest("", false).decryptAPIKey(enc)
	if err == nil {
		t.Fatal("expected error when key is absent and stored value is encrypted")
	}
}

func TestEncryptAPIKey_DistinctNonces(t *testing.T) {
	s := newStoreForCryptoTest("nonce-test", false)
	a, _ := s.encryptAPIKey("same-input")
	b, _ := s.encryptAPIKey("same-input")
	if a == b {
		t.Error("two encryptions of the same plaintext should differ (nonces are random)")
	}
}

func TestEncryptAPIKey_ConcurrentSafe(t *testing.T) {
	// 100 goroutines each encrypt and decrypt distinct values; the
	// helpers are stateless beyond the encryption key, so this is
	// trivially safe — but we assert it explicitly so a future
	// refactor that adds shared state on the hot path can't quietly
	// regress.
	s := newStoreForCryptoTest("concurrent-test", false)
	var wg sync.WaitGroup
	const N = 100
	wg.Add(N)
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			plaintext := "secret-" + intToStrTest(idx)
			enc, err := s.encryptAPIKey(plaintext)
			if err != nil {
				errs <- err
				return
			}
			dec, err := s.decryptAPIKey(enc)
			if err != nil {
				errs <- err
				return
			}
			if dec != plaintext {
				errs <- errors.New("round-trip mismatch")
				return
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent error: %v", err)
	}
}

func TestIsEncrypted(t *testing.T) {
	if IsEncrypted("") {
		t.Error("empty string should not be encrypted")
	}
	if IsEncrypted("plain") {
		t.Error("plain string should not be encrypted")
	}
	if !IsEncrypted("sbenc:v1:abc") {
		t.Error("prefixed string should be flagged encrypted")
	}
	if IsEncrypted("sbenc:v0:abc") {
		t.Error("v0 prefix is not the v1 envelope")
	}
}

// intToStrTest is a tiny helper so the concurrent test file doesn't pull
// strconv in.
func intToStrTest(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
