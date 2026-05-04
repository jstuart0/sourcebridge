// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// makeTestMiddleware builds a MiddlewareWithTokensAndLegacyAdmin handler around
// a trivial "claims captured" endpoint so tests can inspect what role landed in
// context.
func makeTestMiddleware(t *testing.T, store APITokenStore, legacyAdminDefault bool) (http.Handler, *Claims) {
	t.Helper()
	var captured *Claims
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = GetClaims(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	jwtMgr := NewJWTManager("test-secret-min-32-chars-padding!", 60, "oss")
	mw := MiddlewareWithTokensAndLegacyAdmin(jwtMgr, store, legacyAdminDefault)
	return mw(inner), captured
}

// issueTokenAndRun creates a token with the given role, issues the request, and
// returns the claims captured by the inner handler.
func issueTokenAndRun(t *testing.T, inputRole string, legacyAdminDefault bool) *Claims {
	t.Helper()
	store := NewAPITokenStore()
	rawToken, _, err := store.CreateToken(context.Background(), CreateTokenInput{
		Name:   "test",
		UserID: "uid-1",
		Kind:   TokenKindAdminAPI,
		Role:   inputRole,
	})
	if err != nil {
		t.Fatalf("CreateToken() error: %v", err)
	}

	var captured *Claims
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = GetClaims(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	jwtMgr := NewJWTManager("test-secret-min-32-chars-padding!", 60, "oss")
	mw := MiddlewareWithTokensAndLegacyAdmin(jwtMgr, store, legacyAdminDefault)
	handler := mw(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	return captured
}

// TestMiddlewareAdminRoleProducesClaims: token with role "admin" → claims Role = RoleAdmin.
func TestMiddlewareAdminRoleProducesClaims(t *testing.T) {
	claims := issueTokenAndRun(t, RoleAdmin, false)
	if claims == nil {
		t.Fatal("expected non-nil claims")
	}
	if claims.Role != RoleAdmin {
		t.Fatalf("expected role %q, got %q", RoleAdmin, claims.Role)
	}
}

// TestMiddlewareUserRoleProducesClaims: token with role "user" → claims Role = RoleUser.
func TestMiddlewareUserRoleProducesClaims(t *testing.T) {
	claims := issueTokenAndRun(t, RoleUser, false)
	if claims == nil {
		t.Fatal("expected non-nil claims")
	}
	if claims.Role != RoleUser {
		t.Fatalf("expected role %q, got %q", RoleUser, claims.Role)
	}
}

// TestMiddlewareEmptyRoleLegacyFalse: token with empty role and legacyAdminDefault=false
// must produce claims with RoleUser (least privilege / secure default).
func TestMiddlewareEmptyRoleLegacyFalse(t *testing.T) {
	// Bypass CreateToken defaulting logic by directly inserting a token with
	// empty role into a MemoryAPITokenStore.
	store := &MemoryAPITokenStore{
		tokens: make(map[string]*APIToken),
		byHash: make(map[string]string),
	}
	const rawToken = "ca_" + "deadbeef00000000deadbeef00000000deadbeef00000000deadbeef00000000"
	hash := hashToken(rawToken)
	store.tokens["0001"] = &APIToken{
		ID:        "0001",
		Name:      "legacy",
		UserID:    "uid-legacy",
		Role:      "", // explicitly empty — simulates pre-migration row
		TokenHash: hash,
	}
	store.byHash[hash] = "0001"

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
		t.Fatalf("expected role %q (least privilege), got %q", RoleUser, captured.Role)
	}
}

// TestMiddlewareEmptyRoleLegacyTrue: token with empty role and legacyAdminDefault=true
// must produce claims with RoleAdmin (escape hatch behaviour for migration window).
func TestMiddlewareEmptyRoleLegacyTrue(t *testing.T) {
	store := &MemoryAPITokenStore{
		tokens: make(map[string]*APIToken),
		byHash: make(map[string]string),
	}
	const rawToken = "ca_" + "aabbccdd00000000aabbccdd00000000aabbccdd00000000aabbccdd00000000"
	hash := hashToken(rawToken)
	store.tokens["0001"] = &APIToken{
		ID:        "0001",
		Name:      "legacy-admin",
		UserID:    "uid-legacy",
		Role:      "", // explicitly empty
		TokenHash: hash,
	}
	store.byHash[hash] = "0001"

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
		t.Fatalf("expected role %q (legacy escape hatch), got %q", RoleAdmin, captured.Role)
	}
}

// TestRolesFromAPITokenHelper exercises the helper function directly.
func TestRolesFromAPITokenHelper(t *testing.T) {
	tests := []struct {
		name               string
		token              *APIToken
		legacyAdminDefault bool
		wantRole           string
	}{
		{"nil token", nil, false, RoleUser},
		{"nil token legacy true", nil, true, RoleUser},
		{"admin role", &APIToken{Role: RoleAdmin}, false, RoleAdmin},
		{"admin role legacy true", &APIToken{Role: RoleAdmin}, true, RoleAdmin},
		{"user role", &APIToken{Role: RoleUser}, false, RoleUser},
		{"user role legacy true", &APIToken{Role: RoleUser}, true, RoleUser},
		{"empty role legacy false", &APIToken{Role: ""}, false, RoleUser},
		{"empty role legacy true", &APIToken{Role: ""}, true, RoleAdmin},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rolesFromAPIToken(tt.token, tt.legacyAdminDefault)
			if got != tt.wantRole {
				t.Errorf("rolesFromAPIToken(%v, %v) = %q, want %q", tt.token, tt.legacyAdminDefault, got, tt.wantRole)
			}
		})
	}
}
