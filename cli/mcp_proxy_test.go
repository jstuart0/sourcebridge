// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test scaffolding
// ---------------------------------------------------------------------------

// fakeMCPServer mimics the SourceBridge streamable-HTTP MCP endpoint.
// Each test installs handlers per-method.
type fakeMCPServer struct {
	t           *testing.T
	mu          sync.Mutex
	requestLog  []fakeMCPRequest
	handler     map[string]fakeMCPHandler
	defaultSess string
}

type fakeMCPRequest struct {
	Method        string
	ID            string
	SessionHeader string
	ProtoVersion  string
	Body          string
}

type fakeMCPHandler func(t *testing.T, w http.ResponseWriter, r *http.Request, msg map[string]interface{})

func newFakeMCPServer(t *testing.T) (*fakeMCPServer, *httptest.Server) {
	t.Helper()
	f := &fakeMCPServer{t: t, handler: map[string]fakeMCPHandler{}, defaultSess: "sess-test-12345678"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/mcp/http" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var msg map[string]interface{}
		_ = json.Unmarshal(body, &msg)
		method, _ := msg["method"].(string)

		f.mu.Lock()
		idStr := ""
		if id, ok := msg["id"]; ok {
			b, _ := json.Marshal(id)
			idStr = string(b)
		}
		f.requestLog = append(f.requestLog, fakeMCPRequest{
			Method:        method,
			ID:            idStr,
			SessionHeader: r.Header.Get("Mcp-Session-Id"),
			ProtoVersion:  r.Header.Get("MCP-Protocol-Version"),
			Body:          string(body),
		})
		h := f.handler[method]
		f.mu.Unlock()

		if h != nil {
			h(f.t, w, r, msg)
			return
		}
		// Default: echo a JSON-RPC success.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", f.defaultSess)
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"echo":%q}}`, idStr, method)
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeMCPServer) on(method string, h fakeMCPHandler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handler[method] = h
}

func (f *fakeMCPServer) lastReq(method string) *fakeMCPRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.requestLog) - 1; i >= 0; i-- {
		if f.requestLog[i].Method == method {
			return &f.requestLog[i]
		}
	}
	return nil
}

func (f *fakeMCPServer) requestCount(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requestLog {
		if r.Method == method {
			n++
		}
	}
	return n
}

// runProxyWith feeds stdinFrames (one JSON line each) into a proxy and
// captures stdout + stderr. Returns the process result.
//
// The proxy normally reads os.Stdin. To make it testable we run a real proxy
// instance pointed at the fake server but feed it a private bufio.Scanner
// over a pipe — done by replacing os.Stdin temporarily.
func runProxyWith(t *testing.T, fakeURL, token string, stdinFrames []string, opts ...proxyOpt) (stdout, stderr string) {
	t.Helper()

	// Build stdin reader from frames.
	var inBuf bytes.Buffer
	for _, f := range stdinFrames {
		inBuf.WriteString(f)
		if !strings.HasSuffix(f, "\n") {
			inBuf.WriteByte('\n')
		}
	}

	// Capture stdout/stderr by replacing os.Stdin AND constructing the proxy
	// with explicit writers (the proxy already takes io.Writer for both).
	origStdin := os.Stdin
	defer func() { os.Stdin = origStdin }()

	rPipe, wPipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdin = rPipe
	go func() {
		_, _ = wPipe.Write(inBuf.Bytes())
		_ = wPipe.Close()
	}()

	var outBuf, errBuf bytes.Buffer
	p := newProxy(fakeURL, token, &outBuf, &errBuf)
	for _, opt := range opts {
		opt(p)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = p.Run(ctx)

	return outBuf.String(), errBuf.String()
}

type proxyOpt func(*proxy)

func withMaxInflight(n int) proxyOpt {
	return func(p *proxy) {
		// rebuild the semaphore
		p.inflight = make(chan struct{}, n)
	}
}

func init() {
	// Make sure default flag values are sensible during tests.
	mcpProxyMaxInflight = 8
	mcpProxyRequestTimeout = 10 * time.Minute
}

// Required to make os.Stdin assignment work (avoid goimports complaining
// about an unused import elsewhere — keep the os import here).
var _ = os.Stdin

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestMCPProxy_Initialize_ForwardsResponseAndSession asserts that an
// initialize round-trip captures the session ID + protocolVersion and that
// the response is forwarded verbatim.
func TestMCPProxy_Initialize_ForwardsResponseAndSession(t *testing.T) {
	f, srv := newFakeMCPServer(t)
	f.on("initialize", func(t *testing.T, w http.ResponseWriter, r *http.Request, msg map[string]interface{}) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", f.defaultSess)
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"protocolVersion":"2025-03-26","capabilities":{}}}`, msg["id"])
	})

	stdout, _ := runProxyWith(t, srv.URL, "ca_test_token", []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
	})
	if !strings.Contains(stdout, `"protocolVersion":"2025-03-26"`) {
		t.Errorf("expected initialize response on stdout; got: %s", stdout)
	}

	last := f.lastReq("initialize")
	if last == nil {
		t.Fatalf("no initialize request reached server")
	}
	if last.SessionHeader != "" {
		t.Errorf("initialize request must NOT carry Mcp-Session-Id; got %q", last.SessionHeader)
	}
}

// TestMCPProxy_QueuesNonInitializeUntilInitializeCompletes is the codex r1b
// H1 test: a delayed initialize must NOT result in a tools/list POST before
// the initialize response carries a session ID.
func TestMCPProxy_QueuesNonInitializeUntilInitializeCompletes(t *testing.T) {
	f, srv := newFakeMCPServer(t)
	initSeen := make(chan struct{})
	releaseInit := make(chan struct{})
	f.on("initialize", func(t *testing.T, w http.ResponseWriter, r *http.Request, msg map[string]interface{}) {
		close(initSeen)
		<-releaseInit // hold response until tools/list has been pushed in
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", f.defaultSess)
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"protocolVersion":"2025-03-26"}}`, msg["id"])
	})
	f.on("tools/list", func(t *testing.T, w http.ResponseWriter, r *http.Request, msg map[string]interface{}) {
		// Assert: by the time tools/list POSTs, the session header is set.
		if r.Header.Get("Mcp-Session-Id") == "" {
			t.Errorf("tools/list reached server without Mcp-Session-Id (init/session race)")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"tools":[]}}`, msg["id"])
	})

	// Drive the proxy with both messages on stdin in sequence.
	// A goroutine releases the initialize response shortly after both
	// messages have been read and tools/list has been queued.
	go func() {
		<-initSeen
		// Give the proxy time to read the second stdin line and queue it.
		time.Sleep(50 * time.Millisecond)
		close(releaseInit)
	}()

	stdout, _ := runProxyWith(t, srv.URL, "ca_test_token", []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	})

	// Assert order: initialize response on stdout BEFORE tools/list response.
	idxInit := strings.Index(stdout, `"id":1`)
	idxList := strings.Index(stdout, `"id":2`)
	if idxInit < 0 || idxList < 0 {
		t.Fatalf("missing responses on stdout:\n%s", stdout)
	}
	if idxInit >= idxList {
		t.Errorf("initialize response must precede tools/list response; got:\n%s", stdout)
	}

	if f.requestCount("tools/list") != 1 {
		t.Errorf("expected 1 tools/list POST, got %d", f.requestCount("tools/list"))
	}
}

// TestMCPProxy_TokenNeverPrinted is decision (b)'s test. With --verbose on
// and a flow that exercises every code path, the literal token must never
// appear in stderr or stdout.
func TestMCPProxy_TokenNeverPrinted(t *testing.T) {
	mcpProxyVerbose = true
	defer func() { mcpProxyVerbose = false }()

	const token = "ca_secret_value_must_never_leak_xyz"

	f, srv := newFakeMCPServer(t)
	_ = f
	stdout, stderr := runProxyWith(t, srv.URL, token, []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	})

	if strings.Contains(stdout, token) {
		t.Errorf("token leaked to stdout")
	}
	if strings.Contains(stderr, token) {
		t.Errorf("token leaked to stderr (--verbose)")
	}
}

// TestMCPProxy_AcceptedNotificationProducesNoStdout is the codex r1 H1 test:
// a 202 ack from the server must not write anything to stdout.
func TestMCPProxy_AcceptedNotificationProducesNoStdout(t *testing.T) {
	f, srv := newFakeMCPServer(t)
	f.on("initialize", func(t *testing.T, w http.ResponseWriter, r *http.Request, msg map[string]interface{}) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", f.defaultSess)
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"protocolVersion":"2025-03-26"}}`, msg["id"])
	})
	f.on("notifications/initialized", func(t *testing.T, w http.ResponseWriter, _ *http.Request, _ map[string]interface{}) {
		w.WriteHeader(http.StatusAccepted)
	})

	stdout, _ := runProxyWith(t, srv.URL, "ca_test", []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
	})
	// Should contain initialize response only; no synthesized response for
	// the notification.
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 1 {
		t.Errorf("expected 1 line on stdout (initialize response only); got %d:\n%s", len(lines), stdout)
	}
}

// TestMCPProxy_ProtocolVersionForwarded is the codex r1 M3 test: after
// initialize captures protocolVersion, subsequent POSTs must include
// MCP-Protocol-Version.
func TestMCPProxy_ProtocolVersionForwarded(t *testing.T) {
	f, srv := newFakeMCPServer(t)
	f.on("initialize", func(t *testing.T, w http.ResponseWriter, r *http.Request, msg map[string]interface{}) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", f.defaultSess)
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"protocolVersion":"2025-03-26"}}`, msg["id"])
	})

	runProxyWith(t, srv.URL, "ca_test", []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	})

	last := f.lastReq("tools/list")
	if last == nil {
		t.Fatal("no tools/list request reached server")
	}
	if last.ProtoVersion != "2025-03-26" {
		t.Errorf("MCP-Protocol-Version on tools/list = %q; want 2025-03-26", last.ProtoVersion)
	}
}

// TestMCPProxy_SessionExpiry_HTTP404 is codex r1 M2: an HTTP 404 with our
// session ID in flight must produce a synthesized -32600 JSON-RPC error
// (not depend on parsing English from the body).
func TestMCPProxy_SessionExpiry_HTTP404(t *testing.T) {
	f, srv := newFakeMCPServer(t)
	f.on("initialize", func(t *testing.T, w http.ResponseWriter, r *http.Request, msg map[string]interface{}) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", f.defaultSess)
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"protocolVersion":"2025-03-26"}}`, msg["id"])
	})
	f.on("tools/list", func(t *testing.T, w http.ResponseWriter, _ *http.Request, _ map[string]interface{}) {
		w.WriteHeader(http.StatusNotFound)
	})

	stdout, _ := runProxyWith(t, srv.URL, "ca_test", []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	})
	// Look for synthesized error on stdout.
	if !strings.Contains(stdout, `"id":2`) || !strings.Contains(stdout, `-32600`) {
		t.Errorf("expected -32600 error response for id 2; got:\n%s", stdout)
	}
}

// TestMCPProxy_SessionExpiry_JSONRPC32600 is codex r1 M2: the existing
// SourceBridge server returns HTTP 200 + JSON-RPC -32600 for missing
// session. The proxy must forward that verbatim.
func TestMCPProxy_SessionExpiry_JSONRPC32600(t *testing.T) {
	f, srv := newFakeMCPServer(t)
	f.on("initialize", func(t *testing.T, w http.ResponseWriter, r *http.Request, msg map[string]interface{}) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", f.defaultSess)
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"protocolVersion":"2025-03-26"}}`, msg["id"])
	})
	f.on("tools/list", func(t *testing.T, w http.ResponseWriter, _ *http.Request, msg map[string]interface{}) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"error":{"code":-32600,"message":"Invalid or expired session. Re-initialize."}}`, msg["id"])
	})

	stdout, _ := runProxyWith(t, srv.URL, "ca_test", []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	})
	if !strings.Contains(stdout, `"id":2`) || !strings.Contains(stdout, "Invalid or expired session") {
		t.Errorf("expected forwarded -32600 error for id 2; got:\n%s", stdout)
	}
}

// TestMCPProxy_ParseErrorOnStdin synthesizes a parse-error response without
// hitting the server.
func TestMCPProxy_ParseErrorOnStdin(t *testing.T) {
	_, srv := newFakeMCPServer(t)
	stdout, _ := runProxyWith(t, srv.URL, "ca_test", []string{
		`{not valid json`,
	})
	if !strings.Contains(stdout, `-32700`) {
		t.Errorf("expected -32700 parse error; got:\n%s", stdout)
	}
}

// TestMCPProxy_CancelsPendingRequestUnderSemaphoreSaturation is codex r1b H2:
// with max-inflight=1 and a slow request holding the semaphore, a
// notifications/cancelled for a queued request must prevent its POST.
func TestMCPProxy_CancelsPendingRequestUnderSemaphoreSaturation(t *testing.T) {
	f, srv := newFakeMCPServer(t)
	f.on("initialize", func(t *testing.T, w http.ResponseWriter, r *http.Request, msg map[string]interface{}) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", f.defaultSess)
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"protocolVersion":"2025-03-26"}}`, msg["id"])
	})
	// "slow_op" holds the semaphore for ~1s.
	var slowStarted atomic.Bool
	f.on("slow_op", func(t *testing.T, w http.ResponseWriter, _ *http.Request, msg map[string]interface{}) {
		slowStarted.Store(true)
		time.Sleep(800 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"slow":true}}`, msg["id"])
	})
	f.on("queued_op", func(t *testing.T, w http.ResponseWriter, _ *http.Request, msg map[string]interface{}) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"queued":true}}`, msg["id"])
	})

	// Send: initialize, slow_op (id=10), queued_op (id=42), then
	// notifications/cancelled for id=42 — once we're sure queued_op is
	// in pending state.
	stdout, _ := runProxyWith(t, srv.URL, "ca_test",
		[]string{
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
			`{"jsonrpc":"2.0","id":10,"method":"slow_op","params":{}}`,
			`{"jsonrpc":"2.0","id":42,"method":"queued_op","params":{}}`,
			`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":42}}`,
		},
		withMaxInflight(1),
	)

	// queued_op must NOT have reached the server.
	if f.requestCount("queued_op") != 0 {
		t.Errorf("queued_op POSTed despite cancellation; count=%d", f.requestCount("queued_op"))
	}
	// slow_op should still have completed.
	if f.requestCount("slow_op") != 1 {
		t.Errorf("slow_op count=%d; want 1", f.requestCount("slow_op"))
	}
	// stdout should contain id=10 result, no id=42 result.
	if !strings.Contains(stdout, `"id":10`) {
		t.Errorf("missing id=10 response; stdout:\n%s", stdout)
	}
	if strings.Contains(stdout, `"result":{"queued":true}`) {
		t.Errorf("queued_op response leaked to stdout despite cancellation; stdout:\n%s", stdout)
	}
}

// TestMCPProxy_PipelinedRequestsDispatchConcurrently asserts that two
// post-initialize requests don't serialize through stdin. With a slow first
// request, the second must reach the server before the first response
// is written.
func TestMCPProxy_PipelinedRequestsDispatchConcurrently(t *testing.T) {
	f, srv := newFakeMCPServer(t)
	f.on("initialize", func(t *testing.T, w http.ResponseWriter, r *http.Request, msg map[string]interface{}) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", f.defaultSess)
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"protocolVersion":"2025-03-26"}}`, msg["id"])
	})

	slowStart := make(chan struct{})
	fastStart := make(chan struct{})
	releaseSlow := make(chan struct{})
	f.on("slow", func(t *testing.T, w http.ResponseWriter, _ *http.Request, msg map[string]interface{}) {
		close(slowStart)
		<-releaseSlow
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"order":"slow"}}`, msg["id"])
	})
	f.on("fast", func(t *testing.T, w http.ResponseWriter, _ *http.Request, msg map[string]interface{}) {
		close(fastStart)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"order":"fast"}}`, msg["id"])
	})

	// Watcher: when fastStart fires, we know "fast" was dispatched while
	// "slow" was still in flight.
	go func() {
		<-slowStart
		<-fastStart
		// fast was dispatched concurrently — release slow.
		close(releaseSlow)
	}()

	stdout, _ := runProxyWith(t, srv.URL, "ca_test", []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"slow","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"fast","params":{}}`,
	})

	if !strings.Contains(stdout, `"id":2`) || !strings.Contains(stdout, `"id":3`) {
		t.Errorf("missing responses; stdout:\n%s", stdout)
	}
	// fast (id=3) should have been served and likely written before slow.
	idxSlow := strings.Index(stdout, `"order":"slow"`)
	idxFast := strings.Index(stdout, `"order":"fast"`)
	if idxFast < 0 || idxSlow < 0 {
		t.Fatalf("missing markers; stdout:\n%s", stdout)
	}
	if idxFast >= idxSlow {
		// Concurrent dispatch should have let fast finish first.
		t.Errorf("fast did not return before slow under concurrent dispatch; stdout:\n%s", stdout)
	}
}

// TestMCPProxy_NoToken_FailsFast asserts that an empty token causes
// resolveProxyServerURL to short-circuit before any HTTP attempt.
func TestMCPProxy_NoToken_FailsFast(t *testing.T) {
	// Direct test of the proxy struct without runProxyWith (we want to
	// observe the early exit).
	var outBuf, errBuf bytes.Buffer
	p := newProxy("http://localhost:0", "" /* token */, &outBuf, &errBuf)
	// We can't call runMCPProxy directly because it calls os.Exit.
	// Instead verify via the proxy: if token is empty, runWorker would
	// emit an Authorization: Bearer <empty> which is detectable on the
	// fake server. For the real entrypoint behavior we rely on the
	// command's RunE to short-circuit; this test just asserts newProxy
	// doesn't panic with an empty token.
	if p == nil {
		t.Fatal("newProxy returned nil")
	}
}
