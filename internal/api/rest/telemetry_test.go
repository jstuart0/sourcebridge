// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/featureflags"
	"github.com/sourcebridge/sourcebridge/internal/funnel"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// mockFunnelStore is a thread-safe in-memory FunnelStore for tests.
type mockFunnelStore struct {
	mu     sync.Mutex
	events []funnel.FunnelEvent
}

func (m *mockFunnelStore) RecordEvent(_ context.Context, ev funnel.FunnelEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, ev)
	return nil
}

func (m *mockFunnelStore) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.events)
}

func (m *mockFunnelStore) last() (funnel.FunnelEvent, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.events) == 0 {
		return funnel.FunnelEvent{}, false
	}
	return m.events[len(m.events)-1], true
}

// recordingHandler is a slog.Handler that records whether Handle was called
// and captures the messages. Used to assert that the handler is NOT called
// when telemetry is disabled.
type recordingHandler struct {
	mu       sync.Mutex
	records  []slog.Record
	delegate slog.Handler
}

func newRecordingHandler(delegate slog.Handler) *recordingHandler {
	return &recordingHandler{delegate: delegate}
}

func (h *recordingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.delegate.Enabled(ctx, level)
}

func (h *recordingHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r)
	h.mu.Unlock()
	return h.delegate.Handle(ctx, r)
}

func (h *recordingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &recordingHandler{delegate: h.delegate.WithAttrs(attrs)}
}

func (h *recordingHandler) WithGroup(name string) slog.Handler {
	return &recordingHandler{delegate: h.delegate.WithGroup(name)}
}

func (h *recordingHandler) hasMsg(msg string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Message == msg {
			return true
		}
	}
	return false
}

// withAuthContext injects a JWT Claims context simulating an authenticated user.
func withAuthContext(req *http.Request, userID string) *http.Request {
	ctx := context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{UserID: userID})
	return req.WithContext(ctx)
}

// newTelemetryServer builds a minimal *Server for telemetry handler tests.
func newTelemetryServer(flags featureflags.Flags, store funnel.FunnelStore) *Server {
	return &Server{
		flags:       flags,
		funnelStore: store,
	}
}

// postTelemetry fires handleTelemetryEvent directly and returns the recorder.
// If req is nil, a minimal default body is used.
func postTelemetry(s *Server, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/telemetry", strings.NewReader(body))
	req = withAuthContext(req, "user-1")
	w := httptest.NewRecorder()
	s.handleTelemetryEvent(w, req)
	return w
}

// waitForStore polls until the store has at least n events or the deadline
// elapses. Returns true if the condition was met.
func waitForStore(ms *mockFunnelStore, n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ms.count() >= n {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// ---------------------------------------------------------------------------
// Test 1: FunnelTelemetry=false — no log, no RecordEvent, 202
// ---------------------------------------------------------------------------

func TestTelemetryHandler_OptOut_NoLogNoStore(t *testing.T) {
	// Install a recording slog handler so we can assert no log line is emitted.
	buf := &bytes.Buffer{}
	rec := newRecordingHandler(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	defer slog.SetDefault(prev)

	store := &mockFunnelStore{}
	s := newTelemetryServer(featureflags.Flags{FunnelTelemetry: false}, store)

	w := postTelemetry(s, `{"event":"funnel.setup.completed","repositoryId":"repo-1"}`)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d", w.Code)
	}
	if store.count() != 0 {
		t.Fatalf("expected 0 RecordEvent calls, got %d", store.count())
	}
	if rec.hasMsg("product telemetry") {
		t.Fatal("expected no 'product telemetry' log line when telemetry is disabled")
	}
}

// ---------------------------------------------------------------------------
// Test 2: oversized body (>64KB) → 400 / 413
// ---------------------------------------------------------------------------

func TestTelemetryHandler_OversizedBody(t *testing.T) {
	s := newTelemetryServer(featureflags.Flags{FunnelTelemetry: true}, &mockFunnelStore{})

	// Build a body larger than 64KB.
	big := strings.Repeat("x", 65*1024)
	body := `{"event":"funnel.setup.completed","repositoryId":"repo-1","metadata":{"k":"` + big + `"}}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/telemetry", strings.NewReader(body))
	req = withAuthContext(req, "user-1")
	w := httptest.NewRecorder()
	s.handleTelemetryEvent(w, req)

	if w.Code != http.StatusBadRequest && w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 400 or 413 for oversized body, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Test 3: unknown event name → 400
// ---------------------------------------------------------------------------

func TestTelemetryHandler_UnknownEvent(t *testing.T) {
	s := newTelemetryServer(featureflags.Flags{FunnelTelemetry: true}, &mockFunnelStore{})

	w := postTelemetry(s, `{"event":"totally_made_up_event","repositoryId":"repo-1"}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown event name, got %d", w.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode error body: %v", err)
	}
	if resp["error"] != "unknown event name" {
		t.Fatalf("unexpected error message: %q", resp["error"])
	}
}

// ---------------------------------------------------------------------------
// Test 4: canonical event + valid metadata → 202 + 1 RecordEvent within 3s
// ---------------------------------------------------------------------------

func TestTelemetryHandler_CanonicalEvent_PersistsToStore(t *testing.T) {
	store := &mockFunnelStore{}
	s := newTelemetryServer(featureflags.Flags{FunnelTelemetry: true}, store)

	body := `{"event":"funnel.repo.added","repositoryId":"repo-42","metadata":{"language":"go"}}`
	w := postTelemetry(s, body)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	if !waitForStore(store, 1, 3*time.Second) {
		t.Fatal("RecordEvent was not called within 3s for canonical event")
	}

	ev, _ := store.last()
	if ev.Event != "funnel.repo.added" {
		t.Errorf("expected event funnel.repo.added, got %q", ev.Event)
	}
	if ev.RepoID != "repo-42" {
		t.Errorf("expected RepoID repo-42, got %q", ev.RepoID)
	}
	if ev.Source != "browser" {
		t.Errorf("expected source 'browser', got %q", ev.Source)
	}
	if ev.UserID == nil || *ev.UserID == "" {
		t.Error("expected non-empty UserID")
	}
}

// ---------------------------------------------------------------------------
// Test 5: legacy snake_case event with filePath in metadata → 202 + filePath
// STRIPPED from stored metadata
// ---------------------------------------------------------------------------

func TestTelemetryHandler_LegacyEvent_PIIScrubbed(t *testing.T) {
	store := &mockFunnelStore{}
	s := newTelemetryServer(featureflags.Flags{FunnelTelemetry: true}, store)

	body := `{"event":"review_code_used","repositoryId":"repo-1","metadata":{"template":"standard","filePath":"/src/secret.go","scopePath":"/internal/sensitive"}}`
	w := postTelemetry(s, body)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	if !waitForStore(store, 1, 3*time.Second) {
		t.Fatal("RecordEvent was not called within 3s for legacy event")
	}

	ev, _ := store.last()
	if ev.Event != "review_code_used" {
		t.Errorf("expected event review_code_used, got %q", ev.Event)
	}
	if _, hasFilePath := ev.Metadata["filePath"]; hasFilePath {
		t.Error("filePath should be stripped from legacy event metadata")
	}
	if _, hasScopePath := ev.Metadata["scopePath"]; hasScopePath {
		t.Error("scopePath should be stripped from legacy event metadata")
	}
	// Non-PII field must be retained.
	if ev.Metadata["template"] != "standard" {
		t.Errorf("expected metadata.template=standard, got %v", ev.Metadata["template"])
	}
}

// ---------------------------------------------------------------------------
// Test 6: both auth paths (Bearer JWT and no-auth fallback) reach the handler
// ---------------------------------------------------------------------------

// TestTelemetryHandler_BearerAuth verifies that a request carrying a JWT
// claims context (simulating Bearer token auth) is handled correctly.
func TestTelemetryHandler_BearerAuth(t *testing.T) {
	store := &mockFunnelStore{}
	s := newTelemetryServer(featureflags.Flags{FunnelTelemetry: true}, store)

	// Request with explicit JWT claims context (Bearer path).
	req := httptest.NewRequest(http.MethodPost, "/api/v1/telemetry",
		strings.NewReader(`{"event":"funnel.setup.completed","repositoryId":"r1"}`))
	ctx := context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{
		UserID: "bearer-user",
		OrgID:  "tenant-99",
	})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	s.handleTelemetryEvent(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 with Bearer auth, got %d", w.Code)
	}
	if !waitForStore(store, 1, 3*time.Second) {
		t.Fatal("RecordEvent not called within 3s for Bearer-authenticated request")
	}

	ev, _ := store.last()
	if ev.UserID == nil || *ev.UserID != "bearer-user" {
		t.Errorf("expected UserID=bearer-user, got %v", ev.UserID)
	}
}

// TestTelemetryHandler_NoAuthFallback verifies that a request without explicit
// auth context (anonymous path, falls back to "admin" sentinel) still reaches
// the handler and persists.
func TestTelemetryHandler_NoAuthFallback(t *testing.T) {
	store := &mockFunnelStore{}
	s := newTelemetryServer(featureflags.Flags{FunnelTelemetry: true}, store)

	// No auth context injected.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/telemetry",
		strings.NewReader(`{"event":"funnel.setup.completed","repositoryId":"r2"}`))
	w := httptest.NewRecorder()
	s.handleTelemetryEvent(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 with no-auth fallback, got %d", w.Code)
	}
	if !waitForStore(store, 1, 3*time.Second) {
		t.Fatal("RecordEvent not called within 3s for no-auth request")
	}
}

// ---------------------------------------------------------------------------
// Test 7: slog metadata scrub — handler must NOT include metadata in log attrs
// ---------------------------------------------------------------------------

func TestTelemetryHandler_SlogDoesNotLogMetadata(t *testing.T) {
	buf := &bytes.Buffer{}
	rec := newRecordingHandler(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	defer slog.SetDefault(prev)

	store := &mockFunnelStore{}
	s := newTelemetryServer(featureflags.Flags{FunnelTelemetry: true}, store)

	body := `{"event":"funnel.repo.added","repositoryId":"repo-1","metadata":{"secret":"do-not-log"}}`
	postTelemetry(s, body)

	// Wait briefly for the goroutine to run (it doesn't affect log emission,
	// which is synchronous, but avoids test pollution from parallel runs).
	time.Sleep(20 * time.Millisecond)

	logOutput := buf.String()
	if strings.Contains(logOutput, "do-not-log") {
		t.Fatalf("metadata value leaked into slog output: %s", logOutput)
	}
	if strings.Contains(logOutput, "metadata") {
		t.Fatalf("'metadata' key should not appear in slog output: %s", logOutput)
	}
}
