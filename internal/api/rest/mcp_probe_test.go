// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// CA-314: MCP HEAD probe flag tests.
//
// Pins two contracts:
//   - default (PublicProbe=true): unauthenticated HEAD returns 204.
//   - flag-off (PublicProbe=false): unauthenticated HEAD returns 404;
//     authenticated HEAD still returns 204.

package rest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/config"
)

const mcpProbeURL = "/api/v1/mcp/http"

// mcpProbeTestServer builds a minimal Server with MCP enabled so the HEAD
// probe route is registered. JWTSecret is a known-length test value.
func mcpProbeTestServer(t *testing.T, publicProbe bool) (*Server, *auth.JWTManager) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Server.HTTPPort = 0
	cfg.Security.JWTSecret = "test-secret-32-bytes-long-x-pad!"
	cfg.Security.CSRFEnabled = false
	cfg.MCP.Enabled = true
	cfg.MCP.PublicProbe = publicProbe
	jwtMgr := auth.NewJWTManager(cfg.Security.JWTSecret, 60, "")
	localAuth := auth.NewLocalAuth(jwtMgr, nil)
	return NewServer(cfg, localAuth, jwtMgr, nil, nil), jwtMgr
}

func TestMCPProbe_DefaultPublicProbeTrue_Returns204(t *testing.T) {
	srv, _ := mcpProbeTestServer(t, true)
	req := httptest.NewRequest(http.MethodHead, mcpProbeURL, nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Errorf("PublicProbe=true: expected 204, got %d", rr.Code)
	}
}

func TestMCPProbe_PublicProbeFalse_UnauthReturns404(t *testing.T) {
	srv, _ := mcpProbeTestServer(t, false)
	req := httptest.NewRequest(http.MethodHead, mcpProbeURL, nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("PublicProbe=false unauthenticated: expected 404, got %d", rr.Code)
	}
}

func TestMCPProbe_PublicProbeFalse_AuthedReturns204(t *testing.T) {
	srv, jwtMgr := mcpProbeTestServer(t, false)
	tok, err := jwtMgr.GenerateToken("user-1", "user-1@test.example", "", auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	req := httptest.NewRequest(http.MethodHead, mcpProbeURL, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Errorf("PublicProbe=false authenticated: expected 204, got %d", rr.Code)
	}
}
