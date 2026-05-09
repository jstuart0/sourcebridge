// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// CA-205 + codex r2 M1 + ian H1: SSE handler auth/filter matrix.
//
// Test coverage for:
//   - sseRepoIDFromEvent key fallback (both repo_id and repository_id accepted)
//   - handleSSE OSS pass-through (all events flow)
//   - handleSSE multi-tenant allow/drop by allowed repo set
//   - handleSSE drops events with no repo identifier on multi-tenant connections
//   - handleSSE admin role still scoped to tenant's repos (Decision 6 / CA-205)
//   - handleSSE subscription cleanup on connection close (goroutine-leak check)
//
// Multi-tenant tests run in the internal package so they can inject tenantID
// directly into the request context (bypassing TenantMiddleware, which is an
// enterprise extension wired only in enterprise_routes.go). OSS pass-through
// and goroutine-leak tests run in the external package through the full router.

package rest

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	apimiddleware "github.com/sourcebridge/sourcebridge/internal/api/middleware"
	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/events"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// stubRepoCheckerSSE is a minimal RepoAccessChecker returning a fixed allowed set.
type stubRepoCheckerSSE struct {
	tenantRepos map[string][]string // tenantID → []repoID
}

func (s *stubRepoCheckerSSE) GetTenantRepos(tenantID string) ([]string, error) {
	return s.tenantRepos[tenantID], nil
}

// sseInternalTestServer builds a minimal Server wired with a pre-built event
// bus and optional repo checker. It also returns the JWT manager so tests can
// mint authenticated tokens. Used by internal-package tests only.
func sseInternalTestServer(t *testing.T, bus *events.Bus, checker apimiddleware.RepoAccessChecker) (*Server, *auth.JWTManager) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Server.HTTPPort = 0
	cfg.Security.JWTSecret = "test-secret-32-bytes-long-x-pad!"
	cfg.Security.CSRFEnabled = false
	jwtMgr := auth.NewJWTManager(cfg.Security.JWTSecret, 60, "")
	localAuth := auth.NewLocalAuth(jwtMgr, nil)
	opts := []ServerOption{
		WithEventBus(bus),
	}
	if checker != nil {
		opts = append(opts, WithRepoChecker(checker))
	}
	return NewServer(cfg, localAuth, jwtMgr, nil, nil, opts...), jwtMgr
}

// ssePipeWriter is a minimal http.ResponseWriter + http.Flusher backed by an
// io.Pipe so the SSE stream can be read from a concurrent goroutine.
type ssePipeWriter struct {
	pw     *io.PipeWriter
	header http.Header
	code   int
}

func (w *ssePipeWriter) Header() http.Header        { return w.header }
func (w *ssePipeWriter) WriteHeader(code int)        { w.code = code }
func (w *ssePipeWriter) Write(b []byte) (int, error) { return w.pw.Write(b) }
func (w *ssePipeWriter) Flush()                      {}
func (w *ssePipeWriter) Close()                      { w.pw.Close() }

func newSSEPipe() (*io.PipeReader, *ssePipeWriter) {
	pr, pw := io.Pipe()
	return pr, &ssePipeWriter{
		pw:     pw,
		header: make(http.Header),
	}
}

// callHandleSSEDirect invokes handleSSE directly, bypassing the full HTTP
// router. This lets internal tests inject an arbitrary tenantID into the
// request context (multi-tenant filtering is an enterprise feature not
// exercisable through the OSS HTTP stack).
func callHandleSSEDirect(ctx context.Context, t *testing.T, s *Server, jwtMgr *auth.JWTManager, tenantID, role string) <-chan string {
	t.Helper()
	lines := make(chan string, 64)

	tok, err := jwtMgr.GenerateToken("user-1", "user-1@test.example", "", role)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+tok)

	// Inject tenant context so handleSSE sees the correct tenantID.
	reqCtx := context.WithValue(req.Context(), apimiddleware.TenantIDKey, tenantID)
	req = req.WithContext(reqCtx)

	pr, pw := newSSEPipe()
	go func() {
		defer pw.Close()
		s.handleSSE(pw, req)
	}()
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "data:") {
				lines <- strings.TrimPrefix(line, "data:")
			}
		}
	}()
	return lines
}

// collectLines reads from a channel for up to dur, returns all lines seen.
func collectLines(ch <-chan string, dur time.Duration) []string {
	var got []string
	deadline := time.After(dur)
	for {
		select {
		case line, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, line)
		case <-deadline:
			return got
		}
	}
}

// ---------------------------------------------------------------------------
// sseRepoIDFromEvent — unit tests
// ---------------------------------------------------------------------------

// TestSSERepoIDFromEvent_FallbackToRepositoryID verifies the key resolution:
// repo_id wins; falls back to repository_id; both absent → "".
func TestSSERepoIDFromEvent_FallbackToRepositoryID(t *testing.T) {
	cases := []struct {
		name    string
		data    map[string]interface{}
		wantID  string
	}{
		{
			name:   "repo_id set → returned",
			data:   map[string]interface{}{"repo_id": "r1", "repository_id": "r2"},
			wantID: "r1",
		},
		{
			name:   "only repository_id set → fallback used",
			data:   map[string]interface{}{"repository_id": "r2"},
			wantID: "r2",
		},
		{
			name:   "neither key → empty string",
			data:   map[string]interface{}{"msg": "no id"},
			wantID: "",
		},
		{
			name:   "repo_id empty string → falls back to repository_id",
			data:   map[string]interface{}{"repo_id": "", "repository_id": "r3"},
			wantID: "r3",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := events.NewEvent("test.event", tc.data)
			got := sseRepoIDFromEvent(e)
			if got != tc.wantID {
				t.Errorf("sseRepoIDFromEvent() = %q, want %q", got, tc.wantID)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// handleSSE multi-tenant matrix (internal package — direct handler call)
// ---------------------------------------------------------------------------

// TestHandleSSE_OSSPassThrough verifies that "default" tenant receives all events.
func TestHandleSSE_OSSPassThrough(t *testing.T) {
	bus := events.NewBus()
	srv, jwtMgr := sseInternalTestServer(t, bus, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	received := callHandleSSEDirect(ctx, t, srv, jwtMgr, "default", "user")
	time.Sleep(50 * time.Millisecond)

	bus.Publish(events.NewEvent("repo.index.completed", map[string]interface{}{
		"repo_id": "any-repo",
		"msg":     "oss-event",
	}))

	got := collectLines(received, 500*time.Millisecond)
	cancel()

	for _, l := range got {
		if strings.Contains(l, "oss-event") {
			return
		}
	}
	t.Error("OSS default tenant should receive all events; oss-event not received")
}

// TestHandleSSE_MultiTenantAllowsTenantRepos verifies events for repos in the
// tenant's allowed set are delivered.
func TestHandleSSE_MultiTenantAllowsTenantRepos(t *testing.T) {
	bus := events.NewBus()
	checker := &stubRepoCheckerSSE{tenantRepos: map[string][]string{
		"tenantB": {"allowed-repo"},
	}}
	srv, jwtMgr := sseInternalTestServer(t, bus, checker)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	received := callHandleSSEDirect(ctx, t, srv, jwtMgr, "tenantB", "user")
	time.Sleep(50 * time.Millisecond)

	bus.Publish(events.NewEvent("repo.index.completed", map[string]interface{}{
		"repo_id": "allowed-repo",
		"msg":     "allowed-event",
	}))

	got := collectLines(received, 500*time.Millisecond)
	cancel()

	for _, l := range got {
		if strings.Contains(l, "allowed-event") {
			return
		}
	}
	t.Error("multi-tenant: event for allowed repo was not received")
}

// TestHandleSSE_MultiTenantDropsForeignRepos verifies events for repos NOT in
// the tenant's allowed set are not delivered.
func TestHandleSSE_MultiTenantDropsForeignRepos(t *testing.T) {
	bus := events.NewBus()
	checker := &stubRepoCheckerSSE{tenantRepos: map[string][]string{
		"tenantC": {"repo-c"},
	}}
	srv, jwtMgr := sseInternalTestServer(t, bus, checker)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	received := callHandleSSEDirect(ctx, t, srv, jwtMgr, "tenantC", "user")
	time.Sleep(50 * time.Millisecond)

	bus.Publish(events.NewEvent("repo.index.completed", map[string]interface{}{
		"repo_id": "foreign-repo",
		"msg":     "foreign-event",
	}))

	got := collectLines(received, 500*time.Millisecond)
	cancel()

	for _, l := range got {
		if strings.Contains(l, "foreign-event") {
			t.Error("multi-tenant: event for foreign repo must not be delivered")
		}
	}
}

// TestHandleSSE_DropsRepolessEventsOnMultiTenant verifies events with no repo
// identifier are dropped defensively on multi-tenant connections.
func TestHandleSSE_DropsRepolessEventsOnMultiTenant(t *testing.T) {
	bus := events.NewBus()
	checker := &stubRepoCheckerSSE{tenantRepos: map[string][]string{
		"tenantD": {"repo-d"},
	}}
	srv, jwtMgr := sseInternalTestServer(t, bus, checker)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	received := callHandleSSEDirect(ctx, t, srv, jwtMgr, "tenantD", "user")
	time.Sleep(50 * time.Millisecond)

	bus.Publish(events.NewEvent("repo.index.completed", map[string]interface{}{
		"msg": "repoless-event",
	}))

	got := collectLines(received, 500*time.Millisecond)
	cancel()

	for _, l := range got {
		if strings.Contains(l, "repoless-event") {
			t.Error("multi-tenant: event with no repo identifier must be dropped")
		}
	}
}

// TestHandleSSE_AdminIsTenantScoped verifies admin-role connections are still
// filtered to the tenant's repos (Decision 6 / CA-205).
func TestHandleSSE_AdminIsTenantScoped(t *testing.T) {
	bus := events.NewBus()
	checker := &stubRepoCheckerSSE{tenantRepos: map[string][]string{
		"tenantE": {"repo-e"},
	}}
	srv, jwtMgr := sseInternalTestServer(t, bus, checker)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Admin on tenantE — must NOT see repos outside tenantE's allowed set.
	received := callHandleSSEDirect(ctx, t, srv, jwtMgr, "tenantE", "admin")
	time.Sleep(50 * time.Millisecond)

	bus.Publish(events.NewEvent("repo.index.completed", map[string]interface{}{
		"repo_id": "repo-f",
		"msg":     "cross-tenant-event",
	}))
	bus.Publish(events.NewEvent("repo.index.completed", map[string]interface{}{
		"repo_id": "repo-e",
		"msg":     "own-tenant-event",
	}))

	got := collectLines(received, 500*time.Millisecond)
	cancel()

	foundOwn := false
	for _, l := range got {
		if strings.Contains(l, "cross-tenant-event") {
			t.Error("admin role must not grant cross-tenant visibility")
		}
		if strings.Contains(l, "own-tenant-event") {
			foundOwn = true
		}
	}
	if !foundOwn {
		t.Error("admin-scoped connection must still receive own-tenant events")
	}
}

// TestHandleSSE_AcceptsBothRepoIDAndRepositoryIDKeys verifies both "repo_id"
// and "repository_id" are recognised as valid repo identifier keys.
func TestHandleSSE_AcceptsBothRepoIDAndRepositoryIDKeys(t *testing.T) {
	bus := events.NewBus()
	checker := &stubRepoCheckerSSE{tenantRepos: map[string][]string{
		"tenantF": {"repo-x"},
	}}
	srv, jwtMgr := sseInternalTestServer(t, bus, checker)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	received := callHandleSSEDirect(ctx, t, srv, jwtMgr, "tenantF", "user")
	time.Sleep(50 * time.Millisecond)

	// Primary key.
	bus.Publish(events.NewEvent("repo.index.completed", map[string]interface{}{
		"repo_id": "repo-x",
		"msg":     "primary-key",
	}))
	// Fallback key (requirement events use this form).
	bus.Publish(events.NewEvent("requirement.created", map[string]interface{}{
		"repository_id": "repo-x",
		"msg":           "fallback-key",
	}))

	got := collectLines(received, 500*time.Millisecond)
	cancel()

	foundPrimary, foundFallback := false, false
	for _, l := range got {
		if strings.Contains(l, "primary-key") {
			foundPrimary = true
		}
		if strings.Contains(l, "fallback-key") {
			foundFallback = true
		}
	}
	if !foundPrimary {
		t.Error("event with repo_id key was not received")
	}
	if !foundFallback {
		t.Error("event with repository_id fallback key was not received")
	}
}

// TestHandleSSE_UnsubscribesOnConnectionClose verifies the SSE handler cleans
// up its subscription when the client disconnects (goroutine-leak check).
func TestHandleSSE_UnsubscribesOnConnectionClose(t *testing.T) {
	bus := events.NewBus()
	srv, jwtMgr := sseInternalTestServer(t, bus, nil)

	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	_ = callHandleSSEDirect(ctx, t, srv, jwtMgr, "default", "user")
	time.Sleep(100 * time.Millisecond)

	cancel()
	// Allow the handler goroutine and bus dispatcher to settle.
	time.Sleep(200 * time.Millisecond)

	after := runtime.NumGoroutine()
	const fuzz = 5
	if after > before+fuzz {
		t.Errorf("goroutine leak: before=%d, after=%d (delta=%d > fuzz=%d)", before, after, after-before, fuzz)
	}
}
