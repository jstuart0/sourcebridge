// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/auth"
)

// newTokenTestServer builds a minimal Server with only the tokenStore wired,
// sufficient for handleCreateToken tests.
func newTokenTestServer(t *testing.T) *Server {
	t.Helper()
	store := auth.NewAPITokenStore()
	return &Server{
		tokenStore: store,
	}
}

// withClaims injects auth.Claims into the request context, simulating what
// MiddlewareWithTokens does for an authenticated caller.
func withClaims(r *http.Request, claims *auth.Claims) *http.Request {
	ctx := context.WithValue(r.Context(), auth.ClaimsKey, claims)
	return r.WithContext(ctx)
}

// postCreateToken builds and dispatches a POST /api/v1/tokens request against
// handleCreateToken, returning the recorded response.
func postCreateToken(t *testing.T, s *Server, claims *auth.Claims, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tokens", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if claims != nil {
		req = withClaims(req, claims)
	}
	rec := httptest.NewRecorder()
	s.handleCreateToken(rec, req)
	return rec
}

// TestHandleCreateToken_NoRoleDefaultsToUser verifies that omitting the role
// field creates a token with role "user" (least privilege).
func TestHandleCreateToken_NoRoleDefaultsToUser(t *testing.T) {
	s := newTokenTestServer(t)
	userClaims := &auth.Claims{UserID: "uid-1", Role: auth.RoleUser}

	rec := postCreateToken(t, s, userClaims, map[string]string{"name": "my-token"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if role, _ := resp["role"].(string); role != auth.RoleUser {
		t.Fatalf("expected role %q in response, got %q", auth.RoleUser, role)
	}
}

// TestHandleCreateToken_UserRoleByNonAdmin verifies that a non-admin caller
// can explicitly request role "user" and the request succeeds.
func TestHandleCreateToken_UserRoleByNonAdmin(t *testing.T) {
	s := newTokenTestServer(t)
	userClaims := &auth.Claims{UserID: "uid-1", Role: auth.RoleUser}

	rec := postCreateToken(t, s, userClaims, map[string]string{"name": "my-token", "role": "user"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if role, _ := resp["role"].(string); role != auth.RoleUser {
		t.Fatalf("expected role %q in response, got %q", auth.RoleUser, role)
	}
}

// TestHandleCreateToken_AdminRoleByNonAdmin verifies that a non-admin caller
// requesting role "admin" receives 403.
func TestHandleCreateToken_AdminRoleByNonAdmin(t *testing.T) {
	s := newTokenTestServer(t)
	userClaims := &auth.Claims{UserID: "uid-1", Role: auth.RoleUser}

	rec := postCreateToken(t, s, userClaims, map[string]string{"name": "my-token", "role": "admin"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleCreateToken_AdminRoleByAdmin verifies that an admin caller can
// mint a token with role "admin" and receives 201.
func TestHandleCreateToken_AdminRoleByAdmin(t *testing.T) {
	s := newTokenTestServer(t)
	adminClaims := &auth.Claims{UserID: "uid-admin", Role: auth.RoleAdmin}

	rec := postCreateToken(t, s, adminClaims, map[string]string{"name": "admin-token", "role": "admin"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if role, _ := resp["role"].(string); role != auth.RoleAdmin {
		t.Fatalf("expected role %q in response, got %q", auth.RoleAdmin, role)
	}
}

// TestHandleCreateToken_NilClaimsNoRole verifies that a request with no claims
// (legacy/anonymous path) and no role field defaults to "user".
func TestHandleCreateToken_NilClaimsNoRole(t *testing.T) {
	s := newTokenTestServer(t)

	// Pass nil claims (no auth.ClaimsKey in context).
	rec := postCreateToken(t, s, nil, map[string]string{"name": "anon-token"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if role, _ := resp["role"].(string); role != auth.RoleUser {
		t.Fatalf("expected role %q in response, got %q", auth.RoleUser, role)
	}
}

// TestHandleCreateToken_AdminRoleByNilClaims verifies that a request with no
// claims attempting to set role "admin" receives 403 (nil claims ≠ admin).
func TestHandleCreateToken_AdminRoleByNilClaims(t *testing.T) {
	s := newTokenTestServer(t)

	rec := postCreateToken(t, s, nil, map[string]string{"name": "anon-token", "role": "admin"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleCreateToken_NameWhitespaceStripped verifies that leading/trailing
// whitespace is stripped from the name before storage (CA-320 B1).
// The response name must equal the trimmed value, not the raw input.
func TestHandleCreateToken_NameWhitespaceStripped(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"  my token  ", "my token"},
		{"\t my token \t", "my token"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			s := newTokenTestServer(t)
			rec := postCreateToken(t, s, nil, map[string]string{"name": tc.input})
			if rec.Code != http.StatusCreated {
				t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
			}
			var resp map[string]interface{}
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			got, _ := resp["name"].(string)
			if got != tc.want {
				t.Errorf("name: got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestHandleCreateToken_MissingName verifies that a request with no name
// receives 400.
func TestHandleCreateToken_MissingName(t *testing.T) {
	s := newTokenTestServer(t)
	adminClaims := &auth.Claims{UserID: "uid-admin", Role: auth.RoleAdmin}

	rec := postCreateToken(t, s, adminClaims, map[string]string{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}
