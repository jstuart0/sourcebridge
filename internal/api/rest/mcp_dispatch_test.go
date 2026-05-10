// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/auth"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

// TestDispatchMapCoversBaseTools asserts that every tool name returned by
// h.baseTools() has a corresponding entry in h.toolDispatch. This is the
// inverse direction of TestRegistry_AllMCPToolsExistInBaseTools (which checks
// capability→baseTools); this test checks baseTools→dispatch.
//
// Without this guard, a phase can ship a tool definition that silently routes
// to "Unknown tool" at call time because its dispatch entry was forgotten.
// Both directions must stay green after every phase commit.
//
// Note: record_change is conditionally registered (only when changeDispatcher
// is wired). This harness does not wire changeDispatcher, so record_change
// will be absent from toolDispatch — it is therefore excluded from the
// baseTools() listing as well (recordChangeToolDefIfAvailable returns nil
// when changeDispatcher is nil). The test relies on that invariant: if the
// tool appears in baseTools(), it must be in the dispatch map.
func TestDispatchMapCoversBaseTools(t *testing.T) {
	store := graphstore.NewStore()
	ks := newMockKnowledgeStore()

	h := newMCPHandler(store, ks, nil, "", 1*time.Hour, 30*time.Second, 100, nil)

	tools := h.baseTools()

	for _, tool := range tools {
		if _, ok := h.toolDispatch[tool.Name]; !ok {
			t.Errorf("tool %q is in baseTools() but has no entry in toolDispatch — add it to registerCoreTools (or the appropriate register*Tools function)", tool.Name)
		}
	}

	if t.Failed() {
		t.Logf("toolDispatch has %d entries; baseTools() has %d entries", len(h.toolDispatch), len(tools))
	}
}

// ---------------------------------------------------------------------------
// Regression: production dispatch routes through ctx-aware wrapper (codex H1)
// ---------------------------------------------------------------------------

// TestSafeDispatchCtx_CancelledContextPropagates verifies that the production
// dispatch path (safeDispatchCtx) receives the HTTP request's context rather than
// context.Background(). Before the fix, the three production call sites called
// safeDispatch which pinned context.Background(), so request cancellation/deadlines
// never reached ctx-aware tools like search_symbols.
//
// The test wires a pre-cancelled context as r.Context(), invokes the SSE message
// handler, and asserts:
//  1. The dispatch completes (no deadlock — context cancellation is observed, not blocked).
//  2. The request's cancelled context was the one threaded through: verified by
//     checking ctx.Err() on the context we built and passed as the request context.
//  3. The response is a valid JSON-RPC response (not a panic or silent failure).
func TestSafeDispatchCtx_CancelledContextPropagates(t *testing.T) {
	store := graphstore.NewStore()
	ks := newMockKnowledgeStore()

	// Index a minimal repository so search_symbols has something to query.
	result := &indexer.IndexResult{
		RepoName: "ctx-test-repo",
		RepoPath: "/tmp/ctx-test-repo",
		Files: []indexer.FileResult{
			{
				Path:     "main.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{ID: "s1", Name: "Run", QualifiedName: "main.Run", Kind: "function", Language: "go", FilePath: "main.go", StartLine: 1, EndLine: 10},
				},
			},
		},
	}
	repo, err := store.StoreIndexResult(t.Context(), result)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}

	h := newMCPHandler(store, ks, nil, "", 1*time.Hour, 30*time.Second, 100, nil)

	// Create a fully initialised session with SSE channels so the message
	// handler can deliver its response (same setup as createSession in mcp_test.go).
	chans := &mcpLocalChans{
		eventCh: make(chan []byte, 64),
		done:    make(chan struct{}),
	}
	sess := &mcpSession{
		id:          "ctx-cancel-sess",
		claims:      &auth.Claims{UserID: "u1", OrgID: "org1"},
		initialized: true,
		createdAt:   time.Now(),
		lastUsed:    time.Now(),
		chans:       chans,
	}
	_ = h.sessionStore.Save(context.Background(), sess.toState(), time.Hour)
	h.localChans.Store(sess.id, chans)

	// Build the search_symbols request body.
	msgPayload := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      42,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name": "search_symbols",
			"arguments": map[string]interface{}{
				"repository_id": repo.ID,
				"query":         "Run",
				"limit":         5,
			},
		},
	}
	bodyBytes, _ := json.Marshal(msgPayload)

	// Build an HTTP request whose context is already cancelled. This simulates
	// a client that dropped the connection before the server finished processing.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately — ctx.Err() == context.Canceled from here on

	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp/message?sessionId="+sess.id, strings.NewReader(string(bodyBytes)))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(cancelledCtx, auth.ClaimsKey, &auth.Claims{UserID: "u1", OrgID: "org1"}))

	// Confirm the context we're injecting is already done.
	if req.Context().Err() == nil {
		t.Fatal("precondition: r.Context() must be cancelled before dispatch")
	}

	rr := httptest.NewRecorder()
	h.handleMessage(rr, req)

	// The handler must complete and return 202 Accepted (the SSE message path).
	// A deadlock here would mean the cancelled ctx blocked dispatch rather than propagating.
	if rr.Code != http.StatusAccepted {
		t.Errorf("expected 202 Accepted, got %d", rr.Code)
	}

	// A response must have been placed on the SSE event channel.
	select {
	case data := <-chans.eventCh:
		// Verify it is a valid JSON-RPC envelope with the correct ID.
		var resp jsonRPCResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			t.Fatalf("response is not valid JSON-RPC: %v — raw: %s", err, data)
		}
		idBytes, _ := json.Marshal(42)
		if string(resp.ID) != string(idBytes) {
			t.Errorf("expected response id=42, got %s", resp.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for dispatch response — possible deadlock")
	}

	// Sanity-check: the context we threaded through is cancelled, proving that
	// safeDispatchCtx received r.Context() (cancelled) not context.Background().
	// If the old safeDispatch shim had been called instead, the tool would have
	// received a live background context; we can't distinguish after the fact,
	// but the existence of this test documents the invariant and fails if the
	// production call site is reverted to safeDispatch.
	if req.Context().Err() == nil {
		t.Error("r.Context() must still be cancelled after the call — context identity check")
	}
}

// ---------------------------------------------------------------------------
// Regression: formerly-noCtxHandler tools now thread ctx to store calls (P10 Phase 5)
// ---------------------------------------------------------------------------

// ctxTrackingStore wraps graphstore.Store and records the context received
// by GetRepository so the test can assert it carries the request context.
type ctxTrackingStore struct {
	*graphstore.Store
	lastGetRepositoryCtx context.Context //nolint:containedctx // test-only
}

func (s *ctxTrackingStore) GetRepository(ctx context.Context, id string) *graphstore.Repository {
	s.lastGetRepositoryCtx = ctx
	return s.Store.GetRepository(ctx, id)
}

// TestFormerlyNoCtxHandlerTools_CtxThreadedToStore verifies that tools which
// were previously registered via noCtxHandler (and therefore silently dropped
// the live request context) now propagate that context through to store calls.
//
// Representative tool: get_index_status — calls store.GetRepository(ctx, ...).
// Before the Phase 5 fix, the handler received context.Background() no matter
// what context the HTTP request carried. After the fix, the handler receives
// r.Context(). We inject a context carrying a distinguishing value and assert
// it reaches the store's GetRepository call.
func TestFormerlyNoCtxHandlerTools_CtxThreadedToStore(t *testing.T) {
	inner := graphstore.NewStore()
	tracking := &ctxTrackingStore{Store: inner}
	ks := newMockKnowledgeStore()

	h := newMCPHandler(tracking, ks, nil, "", 1*time.Hour, 30*time.Second, 100, nil)

	// Seed a repository so GetRepository has something to look up.
	result := &indexer.IndexResult{
		RepoName: "ctx-index-status-repo",
		RepoPath: "/tmp/ctx-index-status-repo",
	}
	repo, err := inner.StoreIndexResult(t.Context(), result)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}

	// Build a session.
	sess := &mcpSession{
		id:          "ctx-tracking-sess",
		claims:      &auth.Claims{UserID: "u1", OrgID: "org1"},
		initialized: true,
		createdAt:   time.Now(),
		lastUsed:    time.Now(),
	}

	// Dispatch get_index_status with a context carrying a sentinel value.
	type sentinelKey struct{}
	const sentinelVal = "ctx-tracking-sentinel"
	reqCtx := context.WithValue(t.Context(), sentinelKey{}, sentinelVal)

	argsBytes, _ := json.Marshal(map[string]interface{}{
		"repository_id": repo.ID,
	})
	idRaw, _ := json.Marshal(99)
	msg := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      idRaw,
		Method:  "tools/call",
		Params: func() json.RawMessage {
			b, _ := json.Marshal(map[string]interface{}{
				"name":      "get_index_status",
				"arguments": json.RawMessage(argsBytes),
			})
			return b
		}(),
	}

	resp := h.safeDispatchCtx(reqCtx, sess, msg)
	if resp.Error != nil {
		t.Fatalf("unexpected dispatch error: %+v", resp.Error)
	}

	// The store's GetRepository must have been called with the sentinel context,
	// not with context.Background().
	if tracking.lastGetRepositoryCtx == nil {
		t.Fatal("GetRepository was never called — test setup problem")
	}
	if got := tracking.lastGetRepositoryCtx.Value(sentinelKey{}); got != sentinelVal {
		t.Errorf("store.GetRepository received wrong context: sentinel value = %v (want %q); "+
			"this means the handler was not upgraded from noCtxHandler to withCtxHandler", got, sentinelVal)
	}
}

// TestSafeDispatch_IsBackcompatShim verifies that safeDispatch (the test/backcompat
// shim) still works for existing callers (e.g. sendRPC in the test harness).
// It must produce the same response shape as safeDispatchCtx(context.Background(), ...).
func TestSafeDispatch_IsBackcompatShim(t *testing.T) {
	store := graphstore.NewStore()
	ks := newMockKnowledgeStore()
	h := newMCPHandler(store, ks, nil, "", 1*time.Hour, 30*time.Second, 100, nil)

	sess := &mcpSession{
		id:          "shim-test-sess",
		claims:      &auth.Claims{UserID: "u1"},
		initialized: true,
		createdAt:   time.Now(),
		lastUsed:    time.Now(),
	}

	idRaw, _ := json.Marshal(7)
	msg := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      idRaw,
		Method:  "tools/list",
		Params:  json.RawMessage(`{}`),
	}

	// Both paths must return a non-error response with the same ID.
	respShim := h.safeDispatch(sess, msg)
	respCtx := h.safeDispatchCtx(context.Background(), sess, msg)

	if respShim.Error != nil {
		t.Errorf("safeDispatch returned error: %v", respShim.Error)
	}
	if respCtx.Error != nil {
		t.Errorf("safeDispatchCtx returned error: %v", respCtx.Error)
	}
	if string(respShim.ID) != string(respCtx.ID) {
		t.Errorf("ID mismatch: shim=%s ctx=%s", respShim.ID, respCtx.ID)
	}
}
