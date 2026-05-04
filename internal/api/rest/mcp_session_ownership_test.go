// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// SEC-1 test suite — MCP session-ownership for all transport paths.
//
// Original four cases (SSE / handleMessage):
//   1. No claims → 401 (handler-level guard).
//   2. Valid token for user B + session owned by user A → 403.
//   3. Valid token for user A + session owned by user A → 202 (happy path).
//   4. Session with empty UserID → 403 (no grace window).
//
// Twelve new cases covering the streamable HTTP transport paths that
// codex r2 identified as unprotected (C-1):
//
// Notification branch (handleStreamableHTTP, msg with no ID):
//   5.  Cross-user → 403.
//   6.  Empty owner → 403.
//   7.  Own session → 202.
//
// POST branch (handleStreamableHTTP, non-initialize msg with session):
//   8.  Cross-user → 403.
//   9.  Empty owner → 403.
//   10. Own session → 200 (JSON-RPC response).
//
// Streaming tool-call branch (handleStreamableHTTP → handleStreamingToolCall):
//   11. Cross-user → 403.
//   12. Empty owner → 403.
//   13. Own session → 200 (SSE response).
//
// DELETE branch (handleStreamableHTTPDelete):
//   14. Cross-user → 403.
//   15. Empty owner → 403.
//   16. Own session → 200.

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

// ---------------------------------------------------------------------------
// Streamable HTTP transport helpers
// ---------------------------------------------------------------------------

// insertStreamableSession writes an mcpSessionState for the streamable-HTTP
// transport. Unlike insertSession, it does NOT register pod-local channels
// because the streamable transport is request/response without an SSE
// delivery channel.
func insertStreamableSession(t *testing.T, h *mcpHandler, id, userID string) {
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
		t.Fatalf("insertStreamableSession: save: %v", err)
	}
}

// postStreamableNotification fires a POST with a JSON-RPC notification (no
// ID) to handleStreamableHTTP.  Returns the response recorder.
func postStreamableNotification(h *mcpHandler, sessionID string, claims *auth.Claims) *httptest.ResponseRecorder {
	body := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp/http", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", sessionID)
	if claims != nil {
		ctx := context.WithValue(req.Context(), auth.ClaimsKey, claims)
		req = req.WithContext(ctx)
	}
	rr := httptest.NewRecorder()
	h.handleStreamableHTTP(rr, req)
	return rr
}

// postStreamableMethod fires a POST with a JSON-RPC request (with ID) to
// handleStreamableHTTP using an existing session.
func postStreamableMethod(h *mcpHandler, sessionID string, claims *auth.Claims) *httptest.ResponseRecorder {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp/http", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", sessionID)
	if claims != nil {
		ctx := context.WithValue(req.Context(), auth.ClaimsKey, claims)
		req = req.WithContext(ctx)
	}
	rr := httptest.NewRecorder()
	h.handleStreamableHTTP(rr, req)
	return rr
}

// postStreamableToolCall fires a POST with a tools/call request to
// handleStreamableHTTP. The Accept header requests SSE so it exercises
// handleStreamingToolCall.
func postStreamableToolCall(h *mcpHandler, sessionID string, claims *auth.Claims) *httptest.ResponseRecorder {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_repositories","arguments":{}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp/http", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Session-Id", sessionID)
	if claims != nil {
		ctx := context.WithValue(req.Context(), auth.ClaimsKey, claims)
		req = req.WithContext(ctx)
	}
	rr := httptest.NewRecorder()
	h.handleStreamableHTTP(rr, req)
	return rr
}

// deleteStreamableSession fires a DELETE to handleStreamableHTTPDelete.
func deleteStreamableSession(h *mcpHandler, sessionID string, claims *auth.Claims) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/mcp/http", nil)
	req.Header.Set("Mcp-Session-Id", sessionID)
	if claims != nil {
		ctx := context.WithValue(req.Context(), auth.ClaimsKey, claims)
		req = req.WithContext(ctx)
	}
	rr := httptest.NewRecorder()
	h.handleStreamableHTTPDelete(rr, req)
	return rr
}

// ---------------------------------------------------------------------------
// Notification branch (cases 5-7)
// ---------------------------------------------------------------------------

// TestStreamableNotificationRejectsCrossUser verifies user B cannot send a
// notification into user A's streamable session.
func TestStreamableNotificationRejectsCrossUser(t *testing.T) {
	h := newOwnershipTestHandler(t)
	insertStreamableSession(t, h, "st-notif-cross", "user-a")

	rr := postStreamableNotification(h, "st-notif-cross", &auth.Claims{UserID: "user-b", OrgID: "org-1"})

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for cross-user notification, got %d", rr.Code)
	}
}

// TestStreamableNotificationRejectsEmptyOwner verifies a session with empty
// UserID rejects even the authenticated caller.
func TestStreamableNotificationRejectsEmptyOwner(t *testing.T) {
	h := newOwnershipTestHandler(t)
	insertStreamableSession(t, h, "st-notif-empty", "" /* empty UserID */)

	rr := postStreamableNotification(h, "st-notif-empty", &auth.Claims{UserID: "user-a", OrgID: "org-1"})

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for notification into session with empty owner, got %d", rr.Code)
	}
}

// TestStreamableNotificationAllowsOwner verifies the session owner can send
// notifications to their own session (returns 202 on success).
func TestStreamableNotificationAllowsOwner(t *testing.T) {
	h := newOwnershipTestHandler(t)
	insertStreamableSession(t, h, "st-notif-own", "user-a")

	rr := postStreamableNotification(h, "st-notif-own", &auth.Claims{UserID: "user-a", OrgID: "org-1"})

	if rr.Code != http.StatusAccepted {
		t.Errorf("expected 202 for owner notification, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// POST (non-initialize) branch (cases 8-10)
// ---------------------------------------------------------------------------

// TestStreamablePOSTRejectsCrossUser verifies user B cannot dispatch methods
// in user A's streamable session.
func TestStreamablePOSTRejectsCrossUser(t *testing.T) {
	h := newOwnershipTestHandler(t)
	insertStreamableSession(t, h, "st-post-cross", "user-a")

	rr := postStreamableMethod(h, "st-post-cross", &auth.Claims{UserID: "user-b", OrgID: "org-1"})

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for cross-user POST, got %d", rr.Code)
	}
}

// TestStreamablePOSTRejectsEmptyOwner verifies a session with empty UserID
// blocks method dispatch.
func TestStreamablePOSTRejectsEmptyOwner(t *testing.T) {
	h := newOwnershipTestHandler(t)
	insertStreamableSession(t, h, "st-post-empty", "" /* empty UserID */)

	rr := postStreamableMethod(h, "st-post-empty", &auth.Claims{UserID: "user-a", OrgID: "org-1"})

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for POST into session with empty owner, got %d", rr.Code)
	}
}

// TestStreamablePOSTAllowsOwner verifies the session owner can dispatch
// methods. tools/list on a nil store returns a JSON-RPC error response
// (code 200) — not a 403 — proving the ownership gate passes.
func TestStreamablePOSTAllowsOwner(t *testing.T) {
	h := newOwnershipTestHandler(t)
	insertStreamableSession(t, h, "st-post-own", "user-a")

	rr := postStreamableMethod(h, "st-post-own", &auth.Claims{UserID: "user-a", OrgID: "org-1"})

	// 200 means the ownership gate passed; any 403 means it was blocked.
	if rr.Code == http.StatusForbidden {
		t.Errorf("expected ownership gate to pass for owner, got 403")
	}
}

// ---------------------------------------------------------------------------
// Streaming tool-call branch (cases 11-13)
// ---------------------------------------------------------------------------

// TestStreamableToolCallRejectsCrossUser verifies the streaming path (SSE
// Accept header) also enforces ownership before dispatching.
func TestStreamableToolCallRejectsCrossUser(t *testing.T) {
	h := newOwnershipTestHandler(t)
	insertStreamableSession(t, h, "st-stream-cross", "user-a")

	rr := postStreamableToolCall(h, "st-stream-cross", &auth.Claims{UserID: "user-b", OrgID: "org-1"})

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for cross-user streaming tool call, got %d", rr.Code)
	}
}

// TestStreamableToolCallRejectsEmptyOwner verifies that a session with no
// UserID blocks the streaming path.
func TestStreamableToolCallRejectsEmptyOwner(t *testing.T) {
	h := newOwnershipTestHandler(t)
	insertStreamableSession(t, h, "st-stream-empty", "" /* empty UserID */)

	rr := postStreamableToolCall(h, "st-stream-empty", &auth.Claims{UserID: "user-a", OrgID: "org-1"})

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for streaming tool call into session with empty owner, got %d", rr.Code)
	}
}

// TestStreamableToolCallAllowsOwner verifies the session owner can reach the
// streaming branch. On a nil store the dispatch returns an error JSON-RPC
// response (still 200 from the SSE path) — the ownership gate passes.
func TestStreamableToolCallAllowsOwner(t *testing.T) {
	h := newOwnershipTestHandler(t)
	insertStreamableSession(t, h, "st-stream-own", "user-a")

	rr := postStreamableToolCall(h, "st-stream-own", &auth.Claims{UserID: "user-a", OrgID: "org-1"})

	if rr.Code == http.StatusForbidden {
		t.Errorf("expected ownership gate to pass for owner on streaming path, got 403")
	}
}

// ---------------------------------------------------------------------------
// DELETE branch (cases 14-16)
// ---------------------------------------------------------------------------

// TestStreamableDELETERejectsCrossUser verifies user B cannot terminate
// user A's streamable session.
func TestStreamableDELETERejectsCrossUser(t *testing.T) {
	h := newOwnershipTestHandler(t)
	insertStreamableSession(t, h, "st-del-cross", "user-a")

	rr := deleteStreamableSession(h, "st-del-cross", &auth.Claims{UserID: "user-b", OrgID: "org-1"})

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for cross-user DELETE, got %d", rr.Code)
	}
}

// TestStreamableDELETERejectsEmptyOwner verifies a session with empty UserID
// cannot be deleted by an arbitrary authenticated user.
func TestStreamableDELETERejectsEmptyOwner(t *testing.T) {
	h := newOwnershipTestHandler(t)
	insertStreamableSession(t, h, "st-del-empty", "" /* empty UserID */)

	rr := deleteStreamableSession(h, "st-del-empty", &auth.Claims{UserID: "user-a", OrgID: "org-1"})

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for DELETE on session with empty owner, got %d", rr.Code)
	}
}

// TestStreamableDELETEAllowsOwner verifies the session owner can terminate
// their own session.
func TestStreamableDELETEAllowsOwner(t *testing.T) {
	h := newOwnershipTestHandler(t)
	insertStreamableSession(t, h, "st-del-own", "user-a")

	rr := deleteStreamableSession(h, "st-del-own", &auth.Claims{UserID: "user-a", OrgID: "org-1"})

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 for owner DELETE, got %d", rr.Code)
	}
}
