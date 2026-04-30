// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"strings"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/secretcipher"
)

// These tests cover the profile-store building blocks that don't need a
// running SurrealDB:
//   - name normalization and validation
//   - api_key encryption round-trip via the cipher (slice 1 invariant)
//   - cipher reuse guarantee (librarian-M1: store + cipher option chaining)
//   - unique-index error detection
//   - record-id splitting helper
//
// Integration coverage of CreateProfile / UpdateProfile / DeleteProfile
// against a real SurrealDB lives in llm_profile_store_integration_test.go
// behind the integration build tag (matches the existing pattern in this
// package — see testutil_integration_test.go).

func TestNormalizeProfileName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Default", "default"},
		{"  default  ", "default"},
		{"DEFAULT", "default"},
		{"Production-A", "production-a"},
		{"  ", ""},
		{"", ""},
		{"Mix Case  Spaces", "mix case  spaces"},
	}
	for _, c := range cases {
		got := NormalizeProfileName(c.in)
		if got != c.want {
			t.Errorf("NormalizeProfileName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestValidateProfileName(t *testing.T) {
	cases := []struct {
		in        string
		wantTrim  string
		wantKey   string
		wantErr   error
		wantEmpty bool
	}{
		{"Default", "Default", "default", nil, false},
		{"  spaced  ", "spaced", "spaced", nil, false},
		{"", "", "", ErrProfileNameRequired, true},
		{"   ", "", "", ErrProfileNameRequired, true},
		{strings.Repeat("a", 65), "", "", ErrProfileNameTooLong, true},
		{strings.Repeat("a", 64), strings.Repeat("a", 64), strings.Repeat("a", 64), nil, false},
	}
	for _, c := range cases {
		gotTrim, gotKey, err := validateProfileName(c.in)
		if c.wantEmpty {
			if err == nil {
				t.Errorf("validateProfileName(%q): expected error, got nil", c.in)
			} else if c.wantErr != nil && err != c.wantErr {
				t.Errorf("validateProfileName(%q): got %v, want %v", c.in, err, c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("validateProfileName(%q): unexpected err %v", c.in, err)
			continue
		}
		if gotTrim != c.wantTrim {
			t.Errorf("validateProfileName(%q): trim got %q, want %q", c.in, gotTrim, c.wantTrim)
		}
		if gotKey != c.wantKey {
			t.Errorf("validateProfileName(%q): key got %q, want %q", c.in, gotKey, c.wantKey)
		}
	}
}

func TestProfileStoreCipherRoundTrip(t *testing.T) {
	// Construct the store via the public option chain (librarian-M1:
	// the cipher is built from With…EncryptionKey when no explicit
	// cipher is injected). This mirrors how cli/serve.go boots in
	// embedded-test mode.
	s := NewSurrealLLMProfileStore(nil,
		WithLLMProfileEncryptionKey("test-key"),
		WithLLMProfileAllowUnencrypted(false),
	)
	plain := "sk-test-1234567890"
	sealed, err := s.EncryptedAPIKey(plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !strings.HasPrefix(sealed, secretcipher.EnvelopePrefix) {
		t.Errorf("sealed missing envelope prefix: %q", sealed)
	}
	if !s.IsEnvelopeEncrypted(sealed) {
		t.Errorf("IsEnvelopeEncrypted: false on sealed bytes")
	}
	plainOut, err := s.cipher.Decrypt(sealed)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if plainOut != plain {
		t.Errorf("round trip: got %q, want %q", plainOut, plain)
	}
}

func TestProfileStoreCipherFailsWithoutKey(t *testing.T) {
	s := NewSurrealLLMProfileStore(nil,
		WithLLMProfileEncryptionKey(""),
		WithLLMProfileAllowUnencrypted(false),
	)
	_, err := s.EncryptedAPIKey("non-empty")
	if err == nil {
		t.Fatal("expected error when encrypting without key + no escape hatch")
	}
	// The store wraps secretcipher.ErrEncryptionKeyRequired in
	// db.ErrEncryptionKeyRequired.
	if err.Error() == "" {
		t.Errorf("error message empty")
	}
}

func TestProfileStoreCipherEscapeHatchPreservesPlaintext(t *testing.T) {
	s := NewSurrealLLMProfileStore(nil,
		WithLLMProfileEncryptionKey(""),
		WithLLMProfileAllowUnencrypted(true),
	)
	got, err := s.EncryptedAPIKey("plain-key")
	if err != nil {
		t.Fatalf("encrypt with escape hatch: %v", err)
	}
	if got != "plain-key" {
		t.Errorf("escape hatch: got %q, want plain bytes preserved", got)
	}
}

func TestProfileStoreSharedCipher(t *testing.T) {
	// librarian-M1: cli/serve.go builds ONE cipher and passes it to
	// both the LLM config store and the profile store. Verify the
	// option chaining wires the same cipher instance.
	cipher := secretcipher.NewAESGCMCipher("shared", false)
	lcs := NewSurrealLLMConfigStore(nil, WithLLMConfigCipher(cipher))
	lps := NewSurrealLLMProfileStore(nil, WithLLMProfileCipher(cipher))

	// Both stores accept the same cipher pointer.
	if lcs.cipher != cipher {
		t.Error("config store: WithLLMConfigCipher did not install the shared cipher")
	}
	if lps.cipher != cipher {
		t.Error("profile store: WithLLMProfileCipher did not install the shared cipher")
	}

	// Same plaintext, same cipher, same envelope produces compatible
	// ciphertext (each store can decrypt the other's output).
	sealed, err := lcs.EncryptedAPIKey("shared-key")
	if err != nil {
		t.Fatalf("lcs encrypt: %v", err)
	}
	plain, err := cipher.Decrypt(sealed)
	if err != nil {
		t.Fatalf("cipher decrypt of lcs output: %v", err)
	}
	if plain != "shared-key" {
		t.Errorf("decrypt: got %q, want shared-key", plain)
	}
}

func TestSplitRecordID(t *testing.T) {
	cases := []struct {
		in        string
		wantTable string
		wantID    string
		wantOK    bool
	}{
		{"ca_llm_profile:default-migrated", "ca_llm_profile", "default-migrated", true},
		{"ca_llm_profile:abc123", "ca_llm_profile", "abc123", true},
		{"abc123", "", "abc123", true},
		{"", "", "", false},
		{":bad", "", ":bad", true}, // best-effort: leading colon means "no table prefix"
		{"trailing:", "", "trailing:", true},
	}
	for _, c := range cases {
		gotTable, gotID, gotOK := splitRecordID(c.in)
		if gotTable != c.wantTable || gotID != c.wantID || gotOK != c.wantOK {
			t.Errorf("splitRecordID(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.in, gotTable, gotID, gotOK, c.wantTable, c.wantID, c.wantOK)
		}
	}
}

func TestIsUniqueIndexViolation(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"Database index `ca_llm_profile_name_key_unique` already contains 'default'", true},
		{"some other random error", false},
		{"already contains, name_key conflict", true},
		{"duplicate row name_key value", true},
		{"", false},
	}
	for _, c := range cases {
		got := isUniqueIndexViolationString(c.msg)
		if got != c.want {
			t.Errorf("isUniqueIndexViolationString(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}
