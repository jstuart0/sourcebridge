// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package auth

import (
	"context"
	"strings"
	"testing"
	"time"
)

// CA-220 (X-L5): hmacHashToken with an empty key returns the legacy
// bare-SHA-256 format (no "hmac:" prefix). This preserves backward
// compatibility for OSS installs that don't have an encryption key
// configured yet.
func TestHmacHashToken_EmptyKeyFallsBackToLegacy(t *testing.T) {
	got := hmacHashToken("ca_test_token", nil)
	if strings.HasPrefix(got, hmacTokenHashPrefix) {
		t.Fatalf("empty key must NOT produce hmac-prefixed hash; got %q", got)
	}
	if got != legacyHashToken("ca_test_token") {
		t.Fatalf("empty key must produce identical output to legacyHashToken")
	}
}

func TestHmacHashToken_NonEmptyKeyPrefixed(t *testing.T) {
	got := hmacHashToken("ca_test_token", []byte("my-installation-key"))
	if !strings.HasPrefix(got, hmacTokenHashPrefix) {
		t.Fatalf("non-empty key must produce hmac-prefixed hash; got %q", got)
	}
	// 32-byte HMAC-SHA256 → 64 hex chars + "hmac:" prefix = 69 chars total.
	if len(got) != len(hmacTokenHashPrefix)+64 {
		t.Fatalf("hmac hash length=%d want %d", len(got), len(hmacTokenHashPrefix)+64)
	}
}

func TestHmacHashToken_KeySeparation(t *testing.T) {
	// Pin: different keys produce different hashes. Confirms key actually
	// participates in the HMAC (rather than being silently dropped).
	h1 := hmacHashToken("ca_test_token", []byte("key-A"))
	h2 := hmacHashToken("ca_test_token", []byte("key-B"))
	if h1 == h2 {
		t.Fatalf("different keys produced identical hash; HMAC keying broken")
	}
}

func TestIsLegacyTokenHash(t *testing.T) {
	if !isLegacyTokenHash(legacyHashToken("ca_test")) {
		t.Fatal("legacy hash must be recognized as legacy")
	}
	if isLegacyTokenHash(hmacHashToken("ca_test", []byte("key"))) {
		t.Fatal("HMAC hash must NOT be recognized as legacy")
	}
}

// CA-220: a token created with HMAC validates correctly.
func TestMemoryStore_CreateValidate_HMAC(t *testing.T) {
	store := NewMemoryAPITokenStoreWithKey([]byte("installation-key"))
	rawToken, record, err := store.CreateToken(context.Background(), CreateTokenInput{
		Name:   "ca-220-test",
		UserID: "u1",
	})
	if err != nil {
		t.Fatalf("CreateToken() error: %v", err)
	}
	if !strings.HasPrefix(record.TokenHash, hmacTokenHashPrefix) {
		t.Fatalf("CreateToken with key must store hmac-prefixed hash; got %q", record.TokenHash)
	}

	validated, err := store.ValidateToken(context.Background(), rawToken)
	if err != nil {
		t.Fatalf("ValidateToken() error: %v", err)
	}
	if validated == nil || validated.ID != record.ID {
		t.Fatalf("ValidateToken() returned %v; want id=%s", validated, record.ID)
	}
}

// CA-220 read-back compat: a legacy SHA-256 row inserted "out of band"
// (representing a row created before this change) must still validate.
// On validation, the row's index entry should opportunistically migrate
// to the HMAC format so subsequent lookups are single-step.
func TestMemoryStore_ValidateLegacyHash_OpportunisticMigration(t *testing.T) {
	store := NewMemoryAPITokenStoreWithKey([]byte("installation-key"))
	rawToken := "ca_legacyrow0000000000000000000000000000000000000000000000000000000"
	legacyHash := legacyHashToken(rawToken)
	now := time.Now()
	store.mu.Lock()
	store.nextID++
	id := "legacy-row"
	store.tokens[id] = &APIToken{
		ID:        id,
		Name:      "legacy-row",
		Prefix:    rawToken[:11],
		TokenHash: legacyHash,
		UserID:    "u1",
		Role:      tokenRoleDefault,
		CreatedAt: now,
	}
	store.byHash[legacyHash] = id
	store.mu.Unlock()

	// First validation: hits the legacy fallback.
	got, err := store.ValidateToken(context.Background(), rawToken)
	if err != nil {
		t.Fatalf("legacy-fallback ValidateToken() error: %v", err)
	}
	if got == nil || got.ID != id {
		t.Fatalf("legacy ValidateToken returned %v; want id=%s", got, id)
	}

	// After validation, the byHash index should point at the HMAC hash
	// instead of the legacy hash (opportunistic migration).
	expectedHmac := hmacHashToken(rawToken, []byte("installation-key"))
	store.mu.RLock()
	_, legacyStillIndexed := store.byHash[legacyHash]
	_, hmacIndexed := store.byHash[expectedHmac]
	store.mu.RUnlock()
	if legacyStillIndexed {
		t.Fatal("legacy hash should no longer be in byHash after migration")
	}
	if !hmacIndexed {
		t.Fatal("hmac hash should be in byHash after migration")
	}

	// Second validation must succeed via the active (HMAC) path.
	got2, err := store.ValidateToken(context.Background(), rawToken)
	if err != nil || got2 == nil || got2.ID != id {
		t.Fatalf("post-migration ValidateToken() error=%v got=%v", err, got2)
	}
}

// CA-220 opt-out: with no key configured, the store falls back to
// legacy-only behavior — new tokens get bare-SHA-256 hashes and
// validation never tries an HMAC lookup (no key means no HMAC to try).
func TestMemoryStore_NoKey_LegacyOnly(t *testing.T) {
	store := NewMemoryAPITokenStoreWithKey(nil)
	rawToken, record, err := store.CreateToken(context.Background(), CreateTokenInput{
		Name:   "no-key-test",
		UserID: "u1",
	})
	if err != nil {
		t.Fatalf("CreateToken() error: %v", err)
	}
	if strings.HasPrefix(record.TokenHash, hmacTokenHashPrefix) {
		t.Fatalf("no-key store must NOT produce hmac-prefixed hash; got %q", record.TokenHash)
	}
	if record.TokenHash != legacyHashToken(rawToken) {
		t.Fatalf("no-key hash mismatch")
	}

	got, err := store.ValidateToken(context.Background(), rawToken)
	if err != nil || got == nil || got.ID != record.ID {
		t.Fatalf("no-key ValidateToken() err=%v got=%v", err, got)
	}
}

func TestConstantTimeHashEqual(t *testing.T) {
	a := hmacHashToken("ca_test", []byte("key"))
	b := hmacHashToken("ca_test", []byte("key"))
	if !constantTimeHashEqual(a, b) {
		t.Fatal("identical hashes must compare equal")
	}
	c := hmacHashToken("ca_test", []byte("different-key"))
	if constantTimeHashEqual(a, c) {
		t.Fatal("different hashes must NOT compare equal")
	}
	// Differing lengths must early-return false.
	if constantTimeHashEqual(a, a[:len(a)-1]) {
		t.Fatal("differing-length strings must NOT compare equal")
	}
}
