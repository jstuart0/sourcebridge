// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package rest_test exercises admin-role gating through the full HTTP router.
// Tests run in the external test package so they use only exported Server
// surface and prove that the middleware chain (auth → role) is wired correctly.
package rest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/api/rest"
	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/config"
)

// adminRoleTestServer builds a minimal Server with a live JWT manager so that
// requests carry real signed JWTs through the auth middleware. The token store
// is in-memory; no SurrealDB, no worker. Sufficient for all role-gate tests.
func adminRoleTestServer(t *testing.T) (*rest.Server, *auth.JWTManager, auth.APITokenStore) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Server.HTTPPort = 0
	cfg.Security.JWTSecret = "test-secret-32-bytes-long-x-pad!"
	cfg.Security.CSRFEnabled = false // do not require CSRF tokens in tests
	jwtMgr := auth.NewJWTManager(cfg.Security.JWTSecret, 60, "")
	localAuth := auth.NewLocalAuth(jwtMgr, nil)
	tokenStore := auth.NewAPITokenStore()
	s := rest.NewServer(cfg, localAuth, jwtMgr, nil, nil,
		rest.WithTokenStore(tokenStore),
	)
	return s, jwtMgr, tokenStore
}

// bearerToken mints a JWT for the given user and role and returns the raw
// Authorization header value ("Bearer <token>").
func bearerToken(t *testing.T, jwtMgr *auth.JWTManager, userID, role string) string {
	t.Helper()
	tok, err := jwtMgr.GenerateToken(userID, userID+"@test.example", "", role)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	return "Bearer " + tok
}

// do dispatches req through s.Handler() and returns the recorder.
func doRequest(s *rest.Server, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// seedToken creates an API token in the store via POST /api/v1/tokens using the
// provided bearer header. Returns the token ID from the response body.
func seedTokenViaAPI(t *testing.T, s *rest.Server, bearer, name string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tokens", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearer)
	rec := doRequest(s, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("seedTokenViaAPI: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("seedTokenViaAPI: decode: %v", err)
	}
	id, _ := resp["id"].(string)
	if id == "" {
		t.Fatal("seedTokenViaAPI: response missing id")
	}
	return id
}

// seedTokenDirect inserts a token directly into the store (bypassing the HTTP
// layer) so we can set up cross-user ownership without routing through the API.
func seedTokenDirect(t *testing.T, store auth.APITokenStore, userID, name string) string {
	t.Helper()
	_, rec, err := store.CreateToken(context.Background(), auth.CreateTokenInput{
		Name:       name,
		UserID:     userID,
		Kind:       auth.TokenKindAdminAPI,
		ClientType: "test",
		AuthMethod: auth.AuthMethodManual,
		Role:       auth.RoleUser,
	})
	if err != nil {
		t.Fatalf("seedTokenDirect: %v", err)
	}
	return rec.ID
}

// ─── Non-admin user, token self-service (cases 1–6) ──────────────────────────

// Case 1: POST /api/v1/tokens with no role field → 201, role "user".
func TestAdminRole_UserCreateToken_NoRole_Returns201(t *testing.T) {
	s, jwtMgr, _ := adminRoleTestServer(t)
	bearer := bearerToken(t, jwtMgr, "uid-user-1", auth.RoleUser)

	body, _ := json.Marshal(map[string]string{"name": "my-token"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tokens", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearer)

	rec := doRequest(s, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if role, _ := resp["role"].(string); role != auth.RoleUser {
		t.Fatalf("expected role %q, got %q", auth.RoleUser, role)
	}
}

// Case 2: GET /api/v1/tokens → 200, returns only this user's tokens (does NOT
// see another user's token).
func TestAdminRole_UserListTokens_SeesOnlyOwnTokens(t *testing.T) {
	s, jwtMgr, store := adminRoleTestServer(t)

	const userA = "uid-user-a"
	const userB = "uid-user-b"
	bearerA := bearerToken(t, jwtMgr, userA, auth.RoleUser)

	// Seed one token for userA (via API) and one for userB (directly in store).
	_ = seedTokenViaAPI(t, s, bearerA, "token-a")
	otherID := seedTokenDirect(t, store, userB, "token-b")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tokens", nil)
	req.Header.Set("Authorization", bearerA)
	rec := doRequest(s, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var tokens []map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&tokens); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, tok := range tokens {
		if id, _ := tok["id"].(string); id == otherID {
			t.Fatalf("user A's token list leaked user B's token (id=%s)", otherID)
		}
	}
}

// Case 3: DELETE /api/v1/tokens/{id} for own token → 200.
func TestAdminRole_UserRevokeOwnToken_Returns200(t *testing.T) {
	s, jwtMgr, _ := adminRoleTestServer(t)
	bearer := bearerToken(t, jwtMgr, "uid-user-3", auth.RoleUser)

	id := seedTokenViaAPI(t, s, bearer, "token-to-revoke")

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/tokens/"+id, nil)
	req.Header.Set("Authorization", bearer)
	rec := doRequest(s, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Case 4: DELETE /api/v1/tokens/{id} for another user's token → 403.
func TestAdminRole_UserRevokeOtherUserToken_Returns403(t *testing.T) {
	s, jwtMgr, store := adminRoleTestServer(t)
	bearerA := bearerToken(t, jwtMgr, "uid-user-4a", auth.RoleUser)

	// Token owned by a different user, seeded directly.
	otherID := seedTokenDirect(t, store, "uid-user-4b", "other-token")

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/tokens/"+otherID, nil)
	req.Header.Set("Authorization", bearerA)
	rec := doRequest(s, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Case 5: POST /api/v1/tokens/revoke-user (admin-only) called by non-admin → 403.
func TestAdminRole_UserRevokeUser_Returns403(t *testing.T) {
	s, jwtMgr, _ := adminRoleTestServer(t)
	bearer := bearerToken(t, jwtMgr, "uid-user-5", auth.RoleUser)

	body, _ := json.Marshal(map[string]string{"user_id": "uid-victim"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tokens/revoke-user", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearer)
	rec := doRequest(s, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Case 6: POST /api/v1/tokens with role:"admin" by non-admin → 403 (elevation
// attempt blocked by handleCreateToken's admin-only guard, Slice 4).
func TestAdminRole_UserCreateAdminRoleToken_Returns403(t *testing.T) {
	s, jwtMgr, _ := adminRoleTestServer(t)
	bearer := bearerToken(t, jwtMgr, "uid-user-6", auth.RoleUser)

	body, _ := json.Marshal(map[string]string{"name": "elev", "role": "admin"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tokens", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearer)
	rec := doRequest(s, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ─── Non-admin user, admin-only routes (cases 7–8) ──────────────────────────

// Case 7: PUT /api/v1/admin/llm-config by non-admin → 403.
func TestAdminRole_UserPutLLMConfig_Returns403(t *testing.T) {
	s, jwtMgr, _ := adminRoleTestServer(t)
	bearer := bearerToken(t, jwtMgr, "uid-user-7", auth.RoleUser)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/llm-config", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearer)
	rec := doRequest(s, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Case 8: POST /api/v1/admin/llm/server-drain by non-admin → 403.
func TestAdminRole_UserServerDrain_Returns403(t *testing.T) {
	s, jwtMgr, _ := adminRoleTestServer(t)
	bearer := bearerToken(t, jwtMgr, "uid-user-8", auth.RoleUser)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/llm/server-drain", nil)
	req.Header.Set("Authorization", bearer)
	rec := doRequest(s, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Case 9: GET /api/v1/csrf-token (non-admin protected, NOT in admin group) → 200.
func TestAdminRole_UserNonAdminProtectedRoute_Returns200(t *testing.T) {
	s, jwtMgr, _ := adminRoleTestServer(t)
	bearer := bearerToken(t, jwtMgr, "uid-user-9", auth.RoleUser)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/csrf-token", nil)
	req.Header.Set("Authorization", bearer)
	rec := doRequest(s, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Case 10: GET /healthz (public endpoint) → 200 regardless of auth.
func TestAdminRole_PublicHealthz_Returns200(t *testing.T) {
	s, _, _ := adminRoleTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := doRequest(s, req)

	// /healthz may return 200 or 503 (if DB is unavailable), but it must never
	// require auth. The important assertion is it is NOT 401 or 403.
	if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
		t.Fatalf("/healthz must be public, got %d", rec.Code)
	}
}

// ─── Admin user, mirror cases (cases 11–16) ──────────────────────────────────

// Case 11 (mirrors case 1): admin creates token → 201.
func TestAdminRole_AdminCreateToken_Returns201(t *testing.T) {
	s, jwtMgr, _ := adminRoleTestServer(t)
	bearer := bearerToken(t, jwtMgr, "uid-admin-11", auth.RoleAdmin)

	body, _ := json.Marshal(map[string]string{"name": "admin-token"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tokens", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearer)
	rec := doRequest(s, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Case 12 (mirrors case 2): admin lists tokens → 200.
func TestAdminRole_AdminListTokens_Returns200(t *testing.T) {
	s, jwtMgr, _ := adminRoleTestServer(t)
	bearer := bearerToken(t, jwtMgr, "uid-admin-12", auth.RoleAdmin)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tokens", nil)
	req.Header.Set("Authorization", bearer)
	rec := doRequest(s, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Case 13 (mirrors case 3): admin revokes own token → 200.
func TestAdminRole_AdminRevokeOwnToken_Returns200(t *testing.T) {
	s, jwtMgr, _ := adminRoleTestServer(t)
	bearer := bearerToken(t, jwtMgr, "uid-admin-13", auth.RoleAdmin)

	id := seedTokenViaAPI(t, s, bearer, "admin-own-token")

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/tokens/"+id, nil)
	req.Header.Set("Authorization", bearer)
	rec := doRequest(s, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Case 14 (mirrors case 5): POST /api/v1/tokens/revoke-user by admin → succeeds
// (2xx or 4xx from handler, but NOT 403 from the role gate).
func TestAdminRole_AdminRevokeUser_NotForbidden(t *testing.T) {
	s, jwtMgr, _ := adminRoleTestServer(t)
	bearer := bearerToken(t, jwtMgr, "uid-admin-14", auth.RoleAdmin)

	body, _ := json.Marshal(map[string]string{"user_id": "uid-nobody"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tokens/revoke-user", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearer)
	rec := doRequest(s, req)

	if rec.Code == http.StatusForbidden {
		t.Fatalf("admin must not receive 403 from role gate on revoke-user, got %d", rec.Code)
	}
}

// Case 15 (mirrors case 7): admin calls PUT /api/v1/admin/llm-config → passes
// the role gate (may return other errors from the handler, but NOT 403).
func TestAdminRole_AdminPutLLMConfig_NotForbiddenByRoleGate(t *testing.T) {
	s, jwtMgr, _ := adminRoleTestServer(t)
	bearer := bearerToken(t, jwtMgr, "uid-admin-15", auth.RoleAdmin)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/llm-config", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearer)
	rec := doRequest(s, req)

	if rec.Code == http.StatusForbidden {
		t.Fatalf("admin must not receive 403 from role gate on llm-config, got %d", rec.Code)
	}
}

// Case 16 (mirrors case 8): admin calls POST /api/v1/admin/llm/server-drain →
// passes the role gate (may return 200 or other non-403 status).
func TestAdminRole_AdminServerDrain_NotForbiddenByRoleGate(t *testing.T) {
	s, jwtMgr, _ := adminRoleTestServer(t)
	bearer := bearerToken(t, jwtMgr, "uid-admin-16", auth.RoleAdmin)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/llm/server-drain", nil)
	req.Header.Set("Authorization", bearer)
	rec := doRequest(s, req)

	if rec.Code == http.StatusForbidden {
		t.Fatalf("admin must not receive 403 from role gate on server-drain, got %d", rec.Code)
	}
}

// ─── CA-282: RBAC gate for test-worker and test-llm (cases 17–20) ─────────

// Case 17: POST /api/v1/admin/test-worker by non-admin → 403.
func TestAdminRole_UserTestWorker_Returns403(t *testing.T) {
	s, jwtMgr, _ := adminRoleTestServer(t)
	bearer := bearerToken(t, jwtMgr, "uid-user-17", auth.RoleUser)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/test-worker", nil)
	req.Header.Set("Authorization", bearer)
	rec := doRequest(s, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Case 18: POST /api/v1/admin/test-llm by non-admin → 403.
func TestAdminRole_UserTestLLM_Returns403(t *testing.T) {
	s, jwtMgr, _ := adminRoleTestServer(t)
	bearer := bearerToken(t, jwtMgr, "uid-user-18", auth.RoleUser)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/test-llm", nil)
	req.Header.Set("Authorization", bearer)
	rec := doRequest(s, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Case 19 (mirrors 17): admin calls POST /api/v1/admin/test-worker → passes
// the role gate (may return 200 with status field, not 403).
func TestAdminRole_AdminTestWorker_NotForbiddenByRoleGate(t *testing.T) {
	s, jwtMgr, _ := adminRoleTestServer(t)
	bearer := bearerToken(t, jwtMgr, "uid-admin-19", auth.RoleAdmin)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/test-worker", nil)
	req.Header.Set("Authorization", bearer)
	rec := doRequest(s, req)

	if rec.Code == http.StatusForbidden {
		t.Fatalf("admin must not receive 403 from role gate on test-worker, got %d", rec.Code)
	}
}

// Case 20 (mirrors 18): admin calls POST /api/v1/admin/test-llm → passes
// the role gate.
func TestAdminRole_AdminTestLLM_NotForbiddenByRoleGate(t *testing.T) {
	s, jwtMgr, _ := adminRoleTestServer(t)
	bearer := bearerToken(t, jwtMgr, "uid-admin-20", auth.RoleAdmin)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/test-llm", nil)
	req.Header.Set("Authorization", bearer)
	rec := doRequest(s, req)

	if rec.Code == http.StatusForbidden {
		t.Fatalf("admin must not receive 403 from role gate on test-llm, got %d", rec.Code)
	}
}
