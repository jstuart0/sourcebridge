// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Tests for tokenFromSurreal — the internal mapping function that converts a
// raw SurrealDB row to an APIToken.
//
// Codex r2 finding H-1: tokenFromSurreal used to normalise empty DB roles to
// tokenRoleDefault before returning.  That made the legacyAdminDefault flag
// in MiddlewareWithTokensAndLegacyAdmin ineffective for Surreal-backed tokens,
// because rolesFromAPIToken never saw an empty Role.
//
// Fix: the normalisation was removed from tokenFromSurreal; rolesFromAPIToken
// is the single conversion point.  These tests lock in the new behaviour.

package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestTokenFromSurrealPreservesEmptyRole verifies that a SurrealDB record
// with an empty role field arrives in the APIToken with Role == "".
// This is the regression guard for codex r2 H-1: the previous code silently
// normalised "" to tokenRoleDefault, breaking the legacy-admin escape hatch.
func TestTokenFromSurrealPreservesEmptyRole(t *testing.T) {
	record := surrealAPIToken{
		Name:      "pre-migration-token",
		Prefix:    "ca_",
		UserID:    "uid-legacy",
		TokenKind: "admin_api",
		Role:      "", // explicitly empty — simulates a row from before migration 056
		CreatedAt: time.Now(),
	}

	tok := tokenFromSurreal(record)

	if tok.Role != "" {
		t.Errorf("tokenFromSurreal must preserve empty role (got %q); normalisation must happen only in rolesFromAPIToken", tok.Role)
	}
}

// TestTokenFromSurrealPreservesNonEmptyRole verifies that a SurrealDB record
// with a non-empty role (the common case) is preserved unchanged.
func TestTokenFromSurrealPreservesNonEmptyRole(t *testing.T) {
	for _, role := range []string{RoleAdmin, RoleUser} {
		record := surrealAPIToken{
			Name:      "token-" + role,
			Prefix:    "ca_",
			UserID:    "uid-" + role,
			TokenKind: "admin_api",
			Role:      role,
			CreatedAt: time.Now(),
		}

		tok := tokenFromSurreal(record)

		if tok.Role != role {
			t.Errorf("tokenFromSurreal(%q) = %q, want %q", role, tok.Role, role)
		}
	}
}

// ---------------------------------------------------------------------------
// Middleware tests using tokenFromSurreal-constructed tokens
// ---------------------------------------------------------------------------
// These tests build a MemoryAPITokenStore that holds an APIToken whose Role
// field was set via tokenFromSurreal (simulating the Surreal store path) and
// then exercise MiddlewareWithTokensAndLegacyAdmin with both flag states.
//
// Pre-fix, tokenFromSurreal normalised "" to "user", so legacyAdminDefault=true
// had no effect.  Post-fix, the empty role reaches rolesFromAPIToken and the
// flag fires correctly.

// buildSurrealPathToken constructs an APIToken as it would arrive from the
// Surreal store (via tokenFromSurreal) for a pre-migration row with empty role.
// The token is placed in a MemoryAPITokenStore under a known raw token string.
func buildSurrealPathToken(t *testing.T) (rawToken string, store *MemoryAPITokenStore) {
	t.Helper()
	// Simulate what the Surreal store does: tokenFromSurreal returns Role "".
	surrealToken := tokenFromSurreal(surrealAPIToken{
		Name:      "legacy-surreal",
		Prefix:    "ca_",
		UserID:    "uid-surreal-legacy",
		TokenKind: "admin_api",
		Role:      "", // pre-migration row
		CreatedAt: time.Now(),
	})

	const raw = "ca_" + "11223344556677881122334455667788112233445566778811223344556677aa"
	surrealToken.ID = "surreal-001"
	surrealToken.TokenHash = legacyHashToken(raw)

	store = &MemoryAPITokenStore{
		tokens: make(map[string]*APIToken),
		byHash: make(map[string]string),
	}
	store.tokens[surrealToken.ID] = surrealToken
	store.byHash[surrealToken.TokenHash] = surrealToken.ID

	return raw, store
}

// TestSurrealPathEmptyRoleWithLegacyTrue verifies that a token built through
// the Surreal mapping path with an empty role and legacyAdminDefault=true
// results in RoleAdmin claims.  This is the operator escape-hatch that was
// broken before the H-1 fix.
func TestSurrealPathEmptyRoleWithLegacyTrue(t *testing.T) {
	rawToken, store := buildSurrealPathToken(t)

	var captured *Claims
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = GetClaims(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	jwtMgr := NewJWTManager("test-secret-min-32-chars-padding!", 60, "oss")
	mw := MiddlewareWithTokensAndLegacyAdmin(jwtMgr, store, true /* legacyAdminDefault */)
	handler := mw(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if captured == nil {
		t.Fatal("expected non-nil claims")
	}
	if captured.Role != RoleAdmin {
		t.Errorf("Surreal empty-role token + legacyAdminDefault=true: expected %q, got %q (escape hatch broken)", RoleAdmin, captured.Role)
	}
}

// TestSurrealPathEmptyRoleWithLegacyFalse verifies that a token built through
// the Surreal mapping path with an empty role and legacyAdminDefault=false
// results in RoleUser claims (least-privilege / secure default).
func TestSurrealPathEmptyRoleWithLegacyFalse(t *testing.T) {
	rawToken, store := buildSurrealPathToken(t)

	var captured *Claims
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = GetClaims(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	jwtMgr := NewJWTManager("test-secret-min-32-chars-padding!", 60, "oss")
	mw := MiddlewareWithTokensAndLegacyAdmin(jwtMgr, store, false /* legacyAdminDefault */)
	handler := mw(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if captured == nil {
		t.Fatal("expected non-nil claims")
	}
	if captured.Role != RoleUser {
		t.Errorf("Surreal empty-role token + legacyAdminDefault=false: expected %q (least privilege), got %q", RoleUser, captured.Role)
	}
}

// TestRolesFromAPITokenSurrealPath exercises rolesFromAPIToken directly with
// tokens as produced by tokenFromSurreal, confirming the conversion happens
// here and not in the store.
func TestRolesFromAPITokenSurrealPath(t *testing.T) {
	tests := []struct {
		name               string
		dbRole             string
		legacyAdminDefault bool
		wantRole           string
	}{
		{"empty db role + legacy true → admin", "", true, RoleAdmin},
		{"empty db role + legacy false → user", "", false, RoleUser},
		{"admin db role + legacy false → admin", RoleAdmin, false, RoleAdmin},
		{"user db role + legacy true → user (not elevated)", RoleUser, true, RoleUser},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tok := tokenFromSurreal(surrealAPIToken{
				Role:      tt.dbRole,
				UserID:    "uid-test",
				TokenKind: "admin_api",
				CreatedAt: time.Now(),
			})
			got := rolesFromAPIToken(tok, tt.legacyAdminDefault)
			if got != tt.wantRole {
				t.Errorf("rolesFromAPIToken(tokenFromSurreal(role=%q), %v) = %q, want %q",
					tt.dbRole, tt.legacyAdminDefault, got, tt.wantRole)
			}
		})
	}
}

// Ensure the Surreal store's tokenFromSurreal doesn't reference context —
// package-level function under test doesn't need DB access for unit testing.
var _ = context.Background
