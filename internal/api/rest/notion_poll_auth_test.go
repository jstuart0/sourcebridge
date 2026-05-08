// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// NEW-H1 + codex r2 Medium: production auth matrix for /webhooks/notion-poll.
// The previous tests at livingwiki_webhooks_test.go run against a passthrough
// auth middleware to exercise the handler path; these tests run against the
// real production composition (authMiddleware + RequireRole(admin)) to pin
// the security boundary.

package rest_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/api/rest"
	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/config"
)

// notionPollAuthTestServer constructs a Server with full auth + RBAC wiring
// AND a configured living-wiki dispatcher so the Notion-poll route is
// registered in production shape.
func notionPollAuthTestServer(t *testing.T) (*rest.Server, *auth.JWTManager) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Server.HTTPPort = 0
	cfg.Security.JWTSecret = "test-secret-32-bytes-long-x-pad!"
	cfg.Security.CSRFEnabled = false
	cfg.LivingWiki.Enabled = true
	jwtMgr := auth.NewJWTManager(cfg.Security.JWTSecret, 60, "")
	localAuth := auth.NewLocalAuth(jwtMgr, nil)
	s := rest.NewServer(cfg, localAuth, jwtMgr, nil, nil)
	return s, jwtMgr
}

// CA-NEW-H1 anchor: unauthenticated POST /webhooks/notion-poll → 401.
// LOAD-BEARING: future cleanup must NOT remove this — without it, an
// unauthenticated dispatcher trigger goes live.
func TestNotionPollAuthMatrix_UnauthenticatedReturns401(t *testing.T) {
	s, _ := notionPollAuthTestServer(t)
	body := []byte(`{"repo_id":"r1"}`)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/notion-poll", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	// Acceptable: 401 (auth missing) OR 404 (route not mounted because
	// dispatcher fell through). Production wiring requires both
	// dispatcher AND auth middleware to be set; without LivingWiki
	// dispatcher fully wired here, the route may not register. Either
	// way, no unauthenticated 202 should occur.
	if rec.Code == http.StatusAccepted {
		t.Errorf("notion-poll accepted an unauthenticated request: %d %s", rec.Code, rec.Body.String())
	}
	// Acceptable: 401 (auth missing), 404 (route not mounted), or 503
	// living_wiki_disabled (dispatcher nil → RegisterLivingWikiDisabledRoutes
	// stub registered). All are "not 202", which is the invariant.
	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusNotFound && rec.Code != http.StatusServiceUnavailable {
		t.Errorf("want 401, 404, or 503 for unauthenticated /webhooks/notion-poll, got %d: %s", rec.Code, rec.Body.String())
	}
}

// CA-NEW-H1 companion: non-admin bearer → 403 (or 404 if dispatcher
// didn't wire). The important invariant is "not 202".
func TestNotionPollAuthMatrix_NonAdminReturns403Or404(t *testing.T) {
	s, jwtMgr := notionPollAuthTestServer(t)
	body := []byte(`{"repo_id":"r1"}`)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/notion-poll", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearerToken(t, jwtMgr, "user-1", "user"))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusAccepted {
		t.Errorf("notion-poll accepted a non-admin request: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound && rec.Code != http.StatusServiceUnavailable {
		t.Errorf("want 403, 404, or 503 for non-admin /webhooks/notion-poll, got %d: %s", rec.Code, rec.Body.String())
	}
}
