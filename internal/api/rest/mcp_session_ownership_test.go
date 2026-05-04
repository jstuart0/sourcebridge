// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// SEC-1 test suite — MCP message-endpoint auth + session-ownership.
//
// Four cases:
//   1. No Authorization header → 401 (handler-level guard).
//   2. Valid token for user B + session owned by user A → 403 (ownership mismatch).
//   3. Valid token for user A + session owned by user A → 202 (happy path).
//   4. Valid token but session has empty UserID → 403 (no grace window).
//
// Tests 2-4 call handleMessage directly (package rest) because they need to
// insert mcpSessionState into the in-process store. Test 1 additionally
// exercises the route through the full HTTP router in a sister file
// (mcp_session_ownership_router_test.go, package rest_test) to prove the
// middleware is wired, not just the handler guard.

package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/auth"
)

// newOwnershipTestHandler builds a minimal mcpHandler with an in-memory
// session store and no store/worker dependencies (the ownership check fires
// before any tool dispatch).
func newOwnershipTestHandler(t *testing.T) *mcpHandler {
	t.Helper()
	return newMCPHandler(nil, nil, nil, "", 1*time.Hour, 30*time.Second, 100, nil)
}

// insertSession writes an mcpSessionState directly into the handler's store
// and registers pod-local channels so handleMessage can proceed past the
// replica-routing check.
func insertSession(t *testing.T, h *mcpHandler, id, userID string) {
	t.Helper()
	st := &mcpSessionState{
		ID:          id,
		UserID:      userID,
		OrgID:       "org-1",
		Initialized: true,
		CreatedAt:   time.Now(),
		LastUsed:    time.Now(),
	}
	if err := h.sessionStore.Save(context.Background(), st, time.Hour); err != nil {
		t.Fatalf("insertSession: save: %v", err)
	}
	chans := &mcpLocalChans{
		eventCh: make(chan []byte, 64),
		done:    make(chan struct{}),
	}
	h.localChans.Store(id, chans)
}

// postMessage fires a POST /api/v1/mcp/message request against handleMessage
// with optional auth claims injected into the context.
func postMessage(h *mcpHandler, sessionID string, claims *auth.Claims) *httptest.ResponseRecorder {
	body := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp/message?sessionId="+sessionID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if claims != nil {
		ctx := context.WithValue(req.Context(), auth.ClaimsKey, claims)
		req = req.WithContext(ctx)
	}
	rr := httptest.NewRecorder()
	h.handleMessage(rr, req)
	return rr
}

// TestMCPMessageRequiresAuth verifies the handler-level guard: if no claims
// are present in the context (e.g. auth middleware stripped or bypassed),
// handleMessage returns 401.
func TestMCPMessageRequiresAuth(t *testing.T) {
	h := newOwnershipTestHandler(t)
	insertSession(t, h, "sess-auth", "user-a")

	rr := postMessage(h, "sess-auth", nil /* no claims */)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d", rr.Code)
	}
}

// TestMCPMessageRejectsCrossUserSession verifies that a valid token for user B
// cannot dispatch messages into a session owned by user A.
func TestMCPMessageRejectsCrossUserSession(t *testing.T) {
	h := newOwnershipTestHandler(t)
	insertSession(t, h, "sess-user-a", "user-a")

	// Claims identify user-b, but the session belongs to user-a.
	rr := postMessage(h, "sess-user-a", &auth.Claims{UserID: "user-b", OrgID: "org-1"})

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for cross-user session, got %d", rr.Code)
	}
}

// TestMCPMessageAllowsOwnSession verifies that user A can post to their own
// session. The notification path returns 202 with no response body.
func TestMCPMessageAllowsOwnSession(t *testing.T) {
	h := newOwnershipTestHandler(t)
	insertSession(t, h, "sess-user-a-own", "user-a")

	rr := postMessage(h, "sess-user-a-own", &auth.Claims{UserID: "user-a", OrgID: "org-1"})

	if rr.Code != http.StatusAccepted {
		t.Errorf("expected 202 for own session, got %d", rr.Code)
	}
}

// TestMCPMessageRejectsEmptyOwner verifies that a session stored without a
// UserID is rejected with 403. No grace window — empty owner is invalid.
func TestMCPMessageRejectsEmptyOwner(t *testing.T) {
	h := newOwnershipTestHandler(t)
	insertSession(t, h, "sess-empty-owner", "" /* empty UserID */)

	rr := postMessage(h, "sess-empty-owner", &auth.Claims{UserID: "user-a", OrgID: "org-1"})

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for session with empty owner, got %d", rr.Code)
	}
}
