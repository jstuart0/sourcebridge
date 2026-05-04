// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// SEC-1 router-level test — proves that POST /api/v1/mcp/message is wired
// behind the auth middleware in router.go. A request with no Authorization
// header must be rejected by the middleware (401) before handleMessage is
// ever invoked.
//
// This test is in package rest_test (external) so it exercises the full
// Server.Handler() chain, matching the pattern used in admin_role_test.go.

package rest_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/api/rest"
	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/config"
)

// mcpAuthTestServer builds a minimal Server with MCP enabled so that the MCP
// routes are registered. Config mirrors adminRoleTestServer from admin_role_test.go.
func mcpAuthTestServer(t *testing.T) (*rest.Server, *auth.JWTManager) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Server.HTTPPort = 0
	cfg.Security.JWTSecret = "test-secret-32-bytes-long-x-pad!"
	cfg.Security.CSRFEnabled = false
	cfg.MCP.Enabled = true
	jwtMgr := auth.NewJWTManager(cfg.Security.JWTSecret, 60, "")
	localAuth := auth.NewLocalAuth(jwtMgr, nil)
	tokenStore := auth.NewAPITokenStore()
	s := rest.NewServer(cfg, localAuth, jwtMgr, nil, nil,
		rest.WithTokenStore(tokenStore),
	)
	return s, jwtMgr
}

// TestMCPMessageRouteRequiresAuth_RouterLevel proves the middleware is wired
// at the router level: POST /api/v1/mcp/message with no Authorization header
// returns 401 from the middleware before the handler is reached.
func TestMCPMessageRouteRequiresAuth_RouterLevel(t *testing.T) {
	s, _ := mcpAuthTestServer(t)

	body := strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp/message?sessionId=any", body)
	req.Header.Set("Content-Type", "application/json")
	// Deliberately omit Authorization header.

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 from auth middleware for unauthenticated POST /api/v1/mcp/message, got %d (body: %s)",
			rec.Code, rec.Body.String())
	}
}
