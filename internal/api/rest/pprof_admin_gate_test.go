// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// CA-204 + codex r2 Medium: smoke tests for the pprof admin gate. The
// route is conditionally mounted on SOURCEBRIDGE_PPROF_ENABLED=true and
// must require admin role (goroutine / heap / profile dumps can leak
// in-flight tokens and environment values).
//
// Uses /debug/pprof/ (the index page) rather than /profile to avoid the
// 30-second profile capture in test runtime.

package rest_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/api/rest"
	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/config"
)

func pprofTestServer(t *testing.T, pprofEnabled bool) (*rest.Server, *auth.JWTManager) {
	t.Helper()
	if pprofEnabled {
		t.Setenv("SOURCEBRIDGE_PPROF_ENABLED", "true")
	} else {
		t.Setenv("SOURCEBRIDGE_PPROF_ENABLED", "false")
	}
	cfg := config.Defaults()
	cfg.Server.HTTPPort = 0
	cfg.Security.JWTSecret = "test-secret-32-bytes-long-x-pad!"
	cfg.Security.CSRFEnabled = false
	jwtMgr := auth.NewJWTManager(cfg.Security.JWTSecret, 60, "")
	localAuth := auth.NewLocalAuth(jwtMgr, nil)
	s := rest.NewServer(cfg, localAuth, jwtMgr, nil, nil)
	return s, jwtMgr
}

func TestPprofAdminGate_UnauthenticatedReturns401(t *testing.T) {
	s, _ := pprofTestServer(t, true)
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("want 401 for unauthenticated /debug/pprof/, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPprofAdminGate_NonAdminReturns403(t *testing.T) {
	s, jwtMgr := pprofTestServer(t, true)
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req.Header.Set("Authorization", bearerToken(t, jwtMgr, "user-1", "user"))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("want 403 for non-admin /debug/pprof/, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPprofAdminGate_AdminReturns200(t *testing.T) {
	s, jwtMgr := pprofTestServer(t, true)
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req.Header.Set("Authorization", bearerToken(t, jwtMgr, "admin-1", string(auth.RoleAdmin)))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("want 200 for admin /debug/pprof/, got %d: %s", rec.Code, rec.Body.String())
	}
}

// CA-204 anchor: pprof off (env unset / false) → 404. Don't relax this
// without re-evaluating the security model.
func TestPprofAdminGate_DisabledReturns404(t *testing.T) {
	s, jwtMgr := pprofTestServer(t, false)
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req.Header.Set("Authorization", bearerToken(t, jwtMgr, "admin-1", string(auth.RoleAdmin)))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("want 404 when pprof disabled, got %d: %s", rec.Code, rec.Body.String())
	}
}
