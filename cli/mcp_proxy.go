// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/sourcebridge/sourcebridge/internal/config"
)

// mcpProxyCmd bridges Claude Code's stdio MCP transport to a SourceBridge
// server's streamable-HTTP MCP endpoint. It exists so .mcp.json can specify
//   {"command":"sourcebridge","args":["mcp-proxy","--server",<url>]}
// without depending on a SOURCEBRIDGE_API_TOKEN environment variable being
// set in Claude Code's launch context.
//
// The proxy is intentionally thin: stdin → POST → stdout. It supports SSE
// progress streams, HTTP 202 ack-only notifications, cancellation via
// notifications/cancelled, and concurrent dispatch after the initialize
// handshake completes. Stdout is reserved exclusively for the MCP protocol
// channel — every diagnostic goes to stderr, and the token never appears in
// any log path.
var mcpProxyCmd = &cobra.Command{
	Use:   "mcp-proxy",
	Short: "Bridge stdio MCP to a SourceBridge server. Used by Claude Code via .mcp.json.",
	Long: `Reads MCP JSON-RPC messages from stdin, forwards them to the
SourceBridge streamable-HTTP MCP endpoint, and writes responses back to
stdout. The token is read from ~/.sourcebridge/token (or
SOURCEBRIDGE_API_TOKEN if set). The server URL is read from --server,
SOURCEBRIDGE_URL, ~/.sourcebridge/server, or config.toml in that order.

This command is intended to be invoked by Claude Code (or another MCP
client) as the value of "command" in .mcp.json. It is not designed for
interactive use.

Supported:
  - Concurrent dispatch (multiple in-flight requests during slow tool calls)
  - notifications/cancelled (aborts the matching in-flight request)
  - text/event-stream responses (SSE-framed progress + final result)
  - HTTP 202 ack for notifications (no stdout output)
  - Server-initiated notifications during a tool call (forwarded verbatim)

Limitations:
  - Does not currently long-poll for server-initiated notifications outside a
    tool call. SourceBridge does not emit those today.`,
	RunE: runMCPProxy,
}

// mcpProxy* flags. Defined as package vars (cobra convention) so they can
// be reset between tests via the mcpProxyResetFlags helper.
var (
	mcpProxyServer         string
	mcpProxyVerbose        bool
	mcpProxyMaxInflight    int
	mcpProxyRequestTimeout time.Duration
	// mcpProxyDrainTimeout bounds how long Run() waits for in-flight requests
	// to finish after stdin EOFs. After this, drain cancels all registered
	// entries and waits a short grace window before returning. Tunable via
	// the package var so tests can shrink it without exposing a flag.
	mcpProxyDrainTimeout = 30 * time.Second
)

func init() {
	mcpProxyCmd.Flags().StringVar(&mcpProxyServer, "server", "", "SourceBridge server URL (overrides config and SOURCEBRIDGE_URL)")
	mcpProxyCmd.Flags().BoolVar(&mcpProxyVerbose, "verbose", false, "Log per-request diagnostics to stderr (never includes token/headers/bodies)")
	mcpProxyCmd.Flags().IntVar(&mcpProxyMaxInflight, "max-inflight", 8, "Maximum concurrent in-flight requests")
	mcpProxyCmd.Flags().DurationVar(&mcpProxyRequestTimeout, "request-timeout", 10*time.Minute, "Per-request HTTP timeout (slow deep_repo_qa tool calls need ~5-10m)")
}

func runMCPProxy(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	serverURL := resolveProxyServerURL(cfg)
	if serverURL == "" {
		fmt.Fprintln(os.Stderr,
			"sourcebridge mcp-proxy: no server URL configured.\n"+
				"Pass --server <url>, set SOURCEBRIDGE_URL, or run `sourcebridge login --server <url>` first.")
		os.Exit(1)
	}

	// Validate URL shape before doing anything network-y.
	u, parseErr := url.Parse(serverURL)
	if parseErr != nil || (u.Scheme != "http" && u.Scheme != "https") {
		fmt.Fprintf(os.Stderr, "sourcebridge mcp-proxy: invalid server URL %q (must be http or https)\n", serverURL)
		os.Exit(1)
	}

	token := readAPIToken()
	if token == "" {
		fmt.Fprintf(os.Stderr,
			"sourcebridge mcp-proxy: no token found at ~/.sourcebridge/token. "+
				"Run \"sourcebridge login --server %s\" first.\n", serverURL)
		os.Exit(1)
	}

	p := newProxy(serverURL, token, cmd.OutOrStdout(), cmd.ErrOrStderr())
	return p.Run(cmd.Context())
}

// resolveProxyServerURL applies the resolution chain: --server flag →
// SOURCEBRIDGE_URL env → ~/.sourcebridge/server → config.toml.
func resolveProxyServerURL(cfg *config.Config) string {
	if mcpProxyServer != "" {
		return strings.TrimRight(mcpProxyServer, "/")
	}
	if env := os.Getenv("SOURCEBRIDGE_URL"); env != "" {
		return strings.TrimRight(env, "/")
	}
	if saved := readServerURL(); saved != "" {
		return strings.TrimRight(saved, "/")
	}
	return strings.TrimRight(cfg.Server.PublicBaseURL, "/")
}

// proxy holds the runtime state for one mcp-proxy invocation.
//
// Lifecycle (codex r1b H1):
//
//	uninitialized → initializing → ready  → (loop)
//	             ↘             ↘ failed (terminal)
//
// While uninitialized: only "initialize" is dispatched. Every other request
// or notification is queued in pending. Once the server's initialize response
// is forwarded and Mcp-Session-Id + protocolVersion are captured, the queue
// is drained under the in-flight semaphore.
//
// Cancellation registry (codex r1b H2): every accepted request gets a
// requestEntry with state pending → running → done|cancelled. Cancellation
// can fire at any state; pending requests are dropped without a POST,
// running requests have their HTTP context cancelled.
type proxy struct {
	serverURL string
	mcpURL    string
	token     string

	out io.Writer // stdout, protocol channel — guarded by outMu
	err io.Writer // stderr, diagnostics — guarded by errMu (verbosef path)

	outMu sync.Mutex
	errMu sync.Mutex

	hc *http.Client

	// session state, guarded by sessMu.
	sessMu          sync.Mutex
	sessionState    proxySessionState
	sessionID       string
	protocolVersion string
	pendingQueue    []*pendingMessage // queued while uninitialized/initializing

	// cancellation registry, guarded by regMu.
	regMu sync.Mutex
	reg   map[string]*requestEntry // key: stringified id

	// concurrency.
	inflight chan struct{} // semaphore (buffered to maxInflight)

	// shutdown coordination.
	wg sync.WaitGroup
}

type proxySessionState int

const (
	stateUninit proxySessionState = iota
	stateInitializing
	stateReady
	stateFailed
)

type requestState int

const (
	statePending requestState = iota
	stateRunning
	stateDone
	stateCancelled
)

type requestEntry struct {
	id     string
	state  requestState
	cancel context.CancelFunc
}

// pendingMessage is a JSON-RPC frame queued while the proxy is initializing.
type pendingMessage struct {
	raw    []byte
	id     json.RawMessage // nil for notifications
	method string
}

func newProxy(serverURL, token string, out, errOut io.Writer) *proxy {
	maxInflight := mcpProxyMaxInflight
	if maxInflight <= 0 {
		maxInflight = 8
	}
	timeout := mcpProxyRequestTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	return &proxy{
		serverURL: serverURL,
		mcpURL:    serverURL + "/api/v1/mcp/http",
		token:     token,
		out:       out,
		err:       errOut,
		// Per-request timeout is enforced via context, not the client's
		// global timeout (we want to cancel a single request without
		// killing concurrent requests on the same client).
		hc:       &http.Client{},
		reg:      make(map[string]*requestEntry),
		inflight: make(chan struct{}, maxInflight),
	}
}

// Run reads stdin, dispatches messages, and exits when stdin EOFs or ctx is
// cancelled.
func (p *proxy) Run(ctx context.Context) error {
	ctx, cancelAll := context.WithCancel(ctx)
	defer cancelAll()

	scanner := bufio.NewScanner(os.Stdin)
	// Allow large messages — initialize results carry full capability sets.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			break
		default:
		}
		line := scanner.Bytes()
		// Skip blank lines (some clients send them as keep-alives).
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		// Copy: scanner reuses its buffer between Scan calls.
		buf := make([]byte, len(line))
		copy(buf, line)
		p.handleStdinMessage(ctx, buf)
	}

	// Stdin closed. Drain in-flight (with deadline), then DELETE session.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), mcpProxyDrainTimeout)
	defer drainCancel()
	p.drain(drainCtx)
	cancelAll()

	p.deleteSession(context.Background())

	if err := scanner.Err(); err != nil {
		// stdin scan error is unusual but not fatal for our caller — the
		// MCP client lost the channel either way. Return so callers can
		// log if they like.
		return err
	}
	return nil
}

// handleStdinMessage classifies and dispatches one inbound JSON-RPC frame.
func (p *proxy) handleStdinMessage(ctx context.Context, raw []byte) {
	var hdr struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(raw, &hdr); err != nil {
		// Synthesize a parse-error JSON-RPC response (id null per spec).
		p.writeErrorResponse(nil, -32700, "Parse error: "+err.Error())
		p.verbosef("parse error on stdin frame (%d bytes)", len(raw))
		return
	}
	if hdr.JSONRPC != "2.0" {
		p.writeErrorResponse(hdr.ID, -32600, "Invalid Request: jsonrpc must be '2.0'")
		return
	}

	// Cancellation handling (codex r1b H2).
	if hdr.Method == "notifications/cancelled" {
		p.handleCancellation(hdr.Params)
		return
	}

	if hdr.Method == "initialize" {
		p.dispatchInitialize(ctx, raw, hdr.ID)
		return
	}

	// Non-initialize traffic: route based on session state.
	//
	// IMPORTANT: register the request entry in p.reg BEFORE checking the
	// session state, so a notifications/cancelled that arrives while this
	// request is still in pendingQueue can find it. Without this, a
	// cancellation for a queued message lands in handleCancellation with
	// no registry hit and is silently dropped — the message then drains
	// and POSTs anyway.
	idKey := stringifyID(hdr.ID)
	if idKey != "" {
		p.regMu.Lock()
		if _, exists := p.reg[idKey]; !exists {
			p.reg[idKey] = &requestEntry{id: idKey, state: statePending}
		}
		p.regMu.Unlock()
	}

	p.sessMu.Lock()
	switch p.sessionState {
	case stateUninit, stateInitializing:
		// Queue for drain after initialize completes (codex r1b H1).
		p.pendingQueue = append(p.pendingQueue, &pendingMessage{
			raw:    raw,
			id:     hdr.ID,
			method: hdr.Method,
		})
		p.sessMu.Unlock()
		p.verbosef("queued %s while session=initializing", hdr.Method)
		return
	case stateFailed:
		p.sessMu.Unlock()
		p.unregister(idKey)
		p.writeErrorResponse(hdr.ID, -32603, "initialize failed; client must re-issue initialize")
		return
	case stateReady:
		p.sessMu.Unlock()
		p.dispatchRequest(ctx, raw, hdr.ID, hdr.Method)
	}
}

// dispatchInitialize handles the initialize handshake. Called under no lock;
// transitions sessionState while holding sessMu.
func (p *proxy) dispatchInitialize(ctx context.Context, raw []byte, id json.RawMessage) {
	p.sessMu.Lock()
	if p.sessionState == stateInitializing {
		// A second initialize while we're still talking to the server.
		// Spec doesn't define this case; reject with a clear error.
		p.sessMu.Unlock()
		p.writeErrorResponse(id, -32600, "initialize already in progress")
		return
	}
	// Re-initialize from stateReady or stateFailed: cancel everything and
	// restart the handshake.
	if p.sessionState == stateReady {
		p.regMu.Lock()
		for _, e := range p.reg {
			if e.cancel != nil {
				e.cancel()
			}
			e.state = stateCancelled
		}
		p.regMu.Unlock()
	}
	p.sessionState = stateInitializing
	p.sessionID = ""
	p.protocolVersion = ""
	p.sessMu.Unlock()

	// Register an initialize cancellation entry so shutdown drain and
	// notifications/cancelled can target it (codex r2 H4). The id key is
	// the initialize request's id; if absent (no id on initialize, which
	// is non-spec but defensive) we use a sentinel key.
	idKey := stringifyID(id)
	if idKey == "" {
		idKey = "__initialize__"
	}
	initCtx, initCancel := context.WithTimeout(ctx, mcpProxyRequestTimeout)
	p.regMu.Lock()
	p.reg[idKey] = &requestEntry{id: idKey, state: stateRunning, cancel: initCancel}
	p.regMu.Unlock()

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer initCancel()
		defer p.unregister(idKey)

		respBody, sessionID, _, status, sse, err := p.do(initCtx, raw, "")
		if err != nil {
			p.failInitialize(id, "transport error: "+sanitizeForJSON(err.Error()))
			return
		}
		// Initialize responses must be application/json per spec; if the
		// server emitted SSE for some reason, drain it and use the last
		// JSON-RPC payload as the initialize response. Defensive — current
		// SourceBridge server always replies JSON for initialize.
		if sse {
			p.failInitialize(id, "initialize returned SSE; expected application/json")
			return
		}

		// Validate the body is a JSON-RPC message before doing anything
		// else with it. A non-JSON-RPC HTTP error body (e.g. {"error":
		// "unauthorized"} from middleware) must not corrupt the protocol
		// channel (codex r2 H2).
		validBody, parsed := validateJSONRPC(respBody, id)
		if status >= 400 || !parsed {
			// Either an HTTP error or an unparseable body — synthesize a
			// JSON-RPC error rather than leaking raw HTTP error JSON to
			// stdout. If the body parsed AS JSON-RPC even with non-2xx,
			// forward it; the server's chosen error wording is more
			// actionable than our synthesized one.
			if parsed {
				p.writeRaw(validBody)
			} else {
				p.writeErrorResponse(id, -32603,
					fmt.Sprintf("initialize failed: HTTP %d", status))
			}
			p.failInitialize(id, "server rejected initialize")
			return
		}

		// Parse protocolVersion from the result for forwarding on
		// subsequent calls (codex r1 M3).
		var initResp struct {
			Result struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"result"`
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(validBody, &initResp)
		if initResp.Error != nil {
			p.writeRaw(validBody)
			p.failInitialize(id, "server returned JSON-RPC error on initialize")
			return
		}

		// CRITICAL ordering (codex r2 H3): write the initialize response
		// to stdout BEFORE flipping sessionState to Ready. Otherwise a
		// new stdin message can take the ready path and write its
		// response ahead of the initialize response. The pendingQueue
		// is held for drain — new arrivals continue to land there until
		// we explicitly drain.
		p.writeRaw(validBody)
		p.verbosef("initialized session=%s protocolVersion=%s", redactSessionID(sessionID), initResp.Result.ProtocolVersion)

		// Now flip state and grab the queue under sessMu.
		p.sessMu.Lock()
		p.sessionID = sessionID
		p.protocolVersion = initResp.Result.ProtocolVersion
		p.sessionState = stateReady
		queue := p.pendingQueue
		p.pendingQueue = nil
		p.sessMu.Unlock()

		// Drain queue.
		for _, m := range queue {
			p.dispatchRequest(ctx, m.raw, m.id, m.method)
		}
	}()
}

// failInitialize marks the session failed and drains any queued requests
// with synthesized errors.
func (p *proxy) failInitialize(initID json.RawMessage, reason string) {
	p.sessMu.Lock()
	p.sessionState = stateFailed
	queue := p.pendingQueue
	p.pendingQueue = nil
	p.sessMu.Unlock()

	for _, m := range queue {
		p.writeErrorResponse(m.id, -32603, "initialize failed: "+reason)
	}
	p.verbosef("initialize failed: %s; drained %d queued message(s)", reason, len(queue))
	_ = initID
}

// dispatchRequest is the post-initialize, gated dispatch path. Reuses the
// request entry created by handleStdinMessage (so cancellation that arrived
// during pendingQueue residency is honored).
func (p *proxy) dispatchRequest(parent context.Context, raw []byte, id json.RawMessage, method string) {
	idKey := stringifyID(id)

	// Look up existing entry (created in handleStdinMessage). If absent
	// (e.g. id=null notification), no registry entry is needed.
	var entry *requestEntry
	if idKey != "" {
		p.regMu.Lock()
		if existing, ok := p.reg[idKey]; ok {
			entry = existing
		} else {
			// Race-safe fallback: create one. Only happens if a request was
			// dispatched without going through handleStdinMessage (not the
			// normal path; defensive).
			entry = &requestEntry{id: idKey, state: statePending}
			p.reg[idKey] = entry
		}
		// Early cancellation check — if cancellation marked this entry
		// cancelled while it was queued, drop it now (before we even spawn
		// the worker goroutine).
		if entry.state == stateCancelled {
			p.regMu.Unlock()
			p.unregister(idKey)
			p.verbosef("dropped cancelled %s id=%s before dispatch", method, idKey)
			return
		}
		p.regMu.Unlock()
	}

	// For no-id notifications, register a synthetic registry entry BEFORE
	// the goroutine spawns so the drain path can cancel the worker at any
	// point — including while it waits on the semaphore (codex r2b L). The
	// per-request context is created here too; the cancel is stored on the
	// registry entry from the start. Nanosecond keys keep synthetic entries
	// unique within a single proxy lifetime.
	var (
		notifKey  string
		notifCtx  context.Context
		notifCanc context.CancelFunc
	)
	if entry == nil {
		notifKey = fmt.Sprintf("__notif_%d__", time.Now().UnixNano())
		notifCtx, notifCanc = context.WithTimeout(parent, mcpProxyRequestTimeout)
		p.regMu.Lock()
		p.reg[notifKey] = &requestEntry{id: notifKey, state: stateRunning, cancel: notifCanc}
		p.regMu.Unlock()
	}

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()

		// Wait for a semaphore slot. Cancellation while we wait must
		// short-circuit BEFORE the POST. We watch:
		//   - parent.Done() — outer proxy context cancel
		//   - notifCtx.Done() — drain or per-request timeout for notifs
		//     (only meaningful for the entry == nil path)
		// For id-bearing requests, drain triggers entry.cancel() which is
		// either the placeholder context this goroutine builds post-acquire
		// OR the per-request context once it's swapped in. For the
		// pre-acquire window of an id-bearing request, the registry entry's
		// cancel may be nil (we haven't built its reqCtx yet); we rely on
		// the entry.state == stateCancelled check after acquiring the slot
		// to drop the request before the POST.
		var notifDone <-chan struct{}
		if entry == nil {
			notifDone = notifCtx.Done()
		}
		select {
		case p.inflight <- struct{}{}:
			// got the slot
		case <-parent.Done():
			if entry == nil {
				notifCanc()
				p.unregister(notifKey)
			} else {
				p.unregister(idKey)
			}
			return
		case <-notifDone:
			// Drain (or timeout) cancelled this notification before it
			// reached the semaphore. notifCanc has already fired via
			// drain; just clean up the registry and exit.
			notifCanc()
			p.unregister(notifKey)
			p.verbosef("dropped cancelled notification %s while waiting on semaphore", method)
			return
		}
		defer func() { <-p.inflight }()

		// Check cancellation just before POST.
		if entry != nil {
			p.regMu.Lock()
			if entry.state == stateCancelled {
				p.regMu.Unlock()
				p.unregister(idKey)
				p.verbosef("dropped cancelled %s id=%s before POST", method, idKey)
				return
			}
			// Build per-request context with cancel + deadline.
			reqCtx, cancel := context.WithTimeout(parent, mcpProxyRequestTimeout)
			entry.state = stateRunning
			entry.cancel = cancel
			p.regMu.Unlock()

			defer cancel()
			defer p.unregister(idKey)
			p.runWorker(reqCtx, raw, id, method)
			return
		}

		// No id (notification) — registry entry was already created before
		// the goroutine spawned (see above). If drain marked us cancelled
		// while we waited on the semaphore, short-circuit before the POST.
		defer notifCanc()
		defer p.unregister(notifKey)
		if notifCtx.Err() != nil {
			p.verbosef("dropped cancelled notification %s before POST", method)
			return
		}
		p.runWorker(notifCtx, raw, id, method)
	}()
}

// unregister removes the request entry and marks it done.
func (p *proxy) unregister(idKey string) {
	if idKey == "" {
		return
	}
	p.regMu.Lock()
	if e, ok := p.reg[idKey]; ok {
		e.state = stateDone
		delete(p.reg, idKey)
	}
	p.regMu.Unlock()
}

// runWorker performs the POST and forwards the response. For SSE responses
// the body is parsed event-by-event as it arrives so progress notifications
// reach the client during long tool calls (codex r2 H1). Non-JSON-RPC
// response bodies are converted to synthesized JSON-RPC errors instead of
// being written verbatim — corrupting the protocol channel with raw HTTP
// error JSON would be a silent client-breaking bug (codex r2 H2).
func (p *proxy) runWorker(ctx context.Context, raw []byte, id json.RawMessage, method string) {
	p.sessMu.Lock()
	sessionID := p.sessionID
	p.sessMu.Unlock()

	resp, err := p.openResponse(ctx, raw, sessionID)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			p.verbosef("worker for id=%s cancelled mid-flight", stringifyID(id))
			return
		}
		p.writeErrorResponse(id, -32603, "transport error: "+sanitizeForJSON(err.Error()))
		return
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	status := resp.StatusCode
	sse := strings.Contains(ct, "text/event-stream")

	// Session lifecycle errors (codex r1 M2):
	// - HTTP 400 / 404 with our session ID → synthesize -32600.
	// - HTTP 200 + JSON-RPC -32600 → forward verbatim (handled below).
	if (status == http.StatusBadRequest || status == http.StatusNotFound) && sessionID != "" {
		p.writeErrorResponse(id, -32600, "Invalid or expired session. Re-initialize.")
		return
	}

	// Notification ack: 202 with no body — write nothing.
	if status == http.StatusAccepted {
		p.verbosef("ack %s", method)
		return
	}

	if sse {
		// Stream-parse SSE events as they arrive. Each event's data is
		// validated as JSON-RPC; non-JSON-RPC payloads (rare) are dropped
		// with a verbose log entry. Completion is detected by matching
		// the originating id with a result/error envelope.
		p.streamSSE(ctx, resp.Body, id, method)
		return
	}

	// Non-SSE path: read the full body and validate it's JSON-RPC.
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		p.writeErrorResponse(id, -32603, "transport error reading body: "+sanitizeForJSON(readErr.Error()))
		return
	}
	if validBody, parsed := validateJSONRPC(body, id); parsed {
		p.writeRaw(validBody)
		return
	}
	// Not JSON-RPC. Convert to a synthesized error rather than leaking
	// raw HTTP error JSON to the protocol channel (codex r2 H2).
	if status == http.StatusOK && len(body) == 0 {
		p.verbosef("empty 200 for %s; treating as ack", method)
		return
	}
	p.writeErrorResponse(id, -32603,
		fmt.Sprintf("server returned non-JSON-RPC response (HTTP %d)", status))
}

// streamSSE reads SSE events as they arrive on r and writes each well-formed
// JSON-RPC payload to stdout. Stops after the originating id receives a
// result/error envelope, or when the stream closes.
func (p *proxy) streamSSE(ctx context.Context, r io.Reader, originID json.RawMessage, method string) {
	originIDKey := stringifyID(originID)
	completed := false
	for ev := range parseSSE(r) {
		if completed {
			// Drain remaining events but do not forward — server may emit a
			// final keep-alive or trailing notification we don't need.
			continue
		}
		if ev.Data == "" {
			continue
		}
		validBody, ok := validateJSONRPCAny([]byte(ev.Data))
		if !ok {
			p.verbosef("dropped non-JSON-RPC SSE event for %s", method)
			continue
		}
		p.writeRaw(validBody)
		// Detect completion: a JSON-RPC response carrying our id with
		// either result or error.
		if originIDKey != "" && sseFrameIsTerminal(validBody, originIDKey) {
			completed = true
		}
		_ = ctx
	}
}

// handleCancellation processes notifications/cancelled.
func (p *proxy) handleCancellation(params json.RawMessage) {
	var c struct {
		RequestID json.RawMessage `json:"requestId"`
		Reason    string          `json:"reason"`
	}
	if err := json.Unmarshal(params, &c); err != nil {
		p.verbosef("cancellation: malformed params: %v", err)
		return
	}
	idKey := stringifyID(c.RequestID)
	if idKey == "" {
		return
	}
	p.regMu.Lock()
	defer p.regMu.Unlock()
	e, ok := p.reg[idKey]
	if !ok {
		p.verbosef("cancellation: no in-flight request for id=%s", idKey)
		return
	}
	switch e.state {
	case statePending:
		e.state = stateCancelled
		p.verbosef("cancellation: marked pending id=%s cancelled", idKey)
	case stateRunning:
		if e.cancel != nil {
			e.cancel()
		}
		e.state = stateCancelled
		p.verbosef("cancellation: aborted running id=%s", idKey)
	default:
		// Already done / cancelled — nothing to do.
	}
}

// openResponse performs one POST and returns the open *http.Response. The
// caller must close the body. SSE responses are streamed by the caller; JSON
// responses are read into a buffer by the caller. This split (vs the prior
// `do` that always io.ReadAll'd) is what lets streamSSE forward events as
// they arrive rather than buffering until EOF (codex r2 H1).
func (p *proxy) openResponse(ctx context.Context, body []byte, sessionID string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.mcpURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	if pv := p.snapProtocolVersion(); pv != "" {
		req.Header.Set("MCP-Protocol-Version", pv)
	}
	return p.hc.Do(req)
}

// do is a JSON-only convenience used by dispatchInitialize (which expects
// application/json, not SSE). Buffers the body to EOF.
//
// Returns: respBody, sessionID-from-response, content-type, http-status,
// sseFlag, transport-or-context error.
func (p *proxy) do(ctx context.Context, body []byte, sessionID string) ([]byte, string, string, int, bool, error) {
	resp, err := p.openResponse(ctx, body, sessionID)
	if err != nil {
		return nil, "", "", 0, false, err
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	status := resp.StatusCode
	respSession := resp.Header.Get("Mcp-Session-Id")
	sse := strings.Contains(ct, "text/event-stream")

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, "", ct, status, sse, readErr
	}
	return respBody, respSession, ct, status, sse, nil
}

// validateJSONRPC parses body as a JSON-RPC 2.0 RESPONSE for the given
// expectedID. Strict shape (codex r2b M):
//
//   - jsonrpc == "2.0"
//   - id matches expectedID (when expectedID is non-empty)
//   - exactly one of result or error (not both, not neither)
//   - method is absent (responses don't carry method)
//   - if error is present, it's a JSON-RPC error object (has code + message)
//
// Returns the original bytes if valid, false otherwise.
//
// expectedID may be nil/empty to skip the id check — used by tests and the
// notification path. Production code paths always pass the originating id.
func validateJSONRPC(body []byte, expectedID json.RawMessage) ([]byte, bool) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, false
	}
	var rpc struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
		Method  json.RawMessage `json:"method"`
	}
	if err := json.Unmarshal(body, &rpc); err != nil {
		return nil, false
	}
	if rpc.JSONRPC != "2.0" {
		return nil, false
	}
	// Responses don't carry method.
	if len(rpc.Method) > 0 {
		return nil, false
	}
	// Exactly one of result/error must be present.
	hasResult := len(rpc.Result) > 0
	hasError := len(rpc.Error) > 0
	if hasResult == hasError {
		return nil, false
	}
	// If error is present, validate the inner shape.
	if hasError {
		var inner struct {
			Code    *int   `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(rpc.Error, &inner); err != nil {
			return nil, false
		}
		if inner.Code == nil || inner.Message == "" {
			return nil, false
		}
	}
	// id check: when expectedID is non-empty, matched ids are required.
	if len(bytes.TrimSpace(expectedID)) > 0 {
		gotKey := stringifyID(rpc.ID)
		wantKey := stringifyID(expectedID)
		if gotKey != wantKey {
			return nil, false
		}
	}
	return body, true
}

// validateJSONRPCAny accepts any well-formed JSON-RPC frame for SSE streaming:
// either a response (id + result/error, no method) or a notification (method,
// no id). Each frame still must carry jsonrpc:"2.0".
//
// This is intentionally more permissive than validateJSONRPC because progress
// notifications during a tool call legitimately have method and no id, while
// the final response has the originating id and no method.
func validateJSONRPCAny(body []byte) ([]byte, bool) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, false
	}
	var rpc struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
		Method  json.RawMessage `json:"method"`
	}
	if err := json.Unmarshal(body, &rpc); err != nil {
		return nil, false
	}
	if rpc.JSONRPC != "2.0" {
		return nil, false
	}
	hasResult := len(rpc.Result) > 0
	hasError := len(rpc.Error) > 0
	hasMethod := len(rpc.Method) > 0
	hasID := len(bytes.TrimSpace(rpc.ID)) > 0 && string(bytes.TrimSpace(rpc.ID)) != "null"
	switch {
	case hasMethod && !hasResult && !hasError && !hasID:
		// Notification: method present, no id, no result/error. We
		// require id absent (or null) to distinguish from a JSON-RPC
		// request frame (which the proxy never expects to receive on
		// this server→client path).
		return body, true
	case !hasMethod && hasID && (hasResult != hasError):
		// Response: id + exactly one of result/error.
		if hasError {
			// validate inner error shape
			var inner struct {
				Code    *int   `json:"code"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(rpc.Error, &inner); err != nil {
				return nil, false
			}
			if inner.Code == nil || inner.Message == "" {
				return nil, false
			}
		}
		return body, true
	default:
		return nil, false
	}
}

// sseFrameIsTerminal returns true iff body is a JSON-RPC response (has
// result or error) carrying the originating id.
func sseFrameIsTerminal(body []byte, originIDKey string) bool {
	var rpc struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &rpc); err != nil {
		return false
	}
	if len(rpc.Result) == 0 && len(rpc.Error) == 0 {
		return false
	}
	return stringifyID(rpc.ID) == originIDKey
}

// snapProtocolVersion returns the negotiated protocolVersion under sessMu.
func (p *proxy) snapProtocolVersion() string {
	p.sessMu.Lock()
	defer p.sessMu.Unlock()
	return p.protocolVersion
}

// drain waits up to ctx's deadline for in-flight workers to exit, then
// cancels everything in the registry (initialize + running + pending +
// notification workers — codex r2 H4) and waits for them. After the
// deadline-then-cancel, an additional grace window (5s) is allowed so
// cancelled HTTP requests can return; if any worker is still alive after
// that, drain returns to avoid hanging shutdown indefinitely.
func (p *proxy) drain(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return
	case <-ctx.Done():
		// Cancel all registered workers (covers initialize, running,
		// pending, and notification entries thanks to the H4 fixes).
		p.regMu.Lock()
		for _, e := range p.reg {
			if e.cancel != nil {
				e.cancel()
			}
			e.state = stateCancelled
		}
		p.regMu.Unlock()
	}
	// Wait for cancelled workers to exit, but bound by a hard grace
	// window so a stuck DNS lookup or kernel-level network stall doesn't
	// hang the proxy shutdown forever.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		p.verbosef("drain: hard timeout — some workers did not exit")
	}
}

// deleteSession sends a best-effort DELETE to terminate the session.
func (p *proxy) deleteSession(ctx context.Context) {
	p.sessMu.Lock()
	sid := p.sessionID
	p.sessMu.Unlock()
	if sid == "" {
		return
	}
	delCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(delCtx, http.MethodDelete, p.mcpURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Mcp-Session-Id", sid)
	resp, err := p.hc.Do(req)
	if err != nil {
		p.verbosef("DELETE session error: %v", err)
		return
	}
	resp.Body.Close()
	p.verbosef("DELETE session=%s → %d", redactSessionID(sid), resp.StatusCode)
}

// writeRaw writes a JSON-RPC message + newline to stdout under outMu.
func (p *proxy) writeRaw(b []byte) {
	p.outMu.Lock()
	defer p.outMu.Unlock()
	// Make sure the body ends with exactly one newline.
	trimmed := bytes.TrimRight(b, "\r\n")
	_, _ = p.out.Write(trimmed)
	_, _ = p.out.Write([]byte{'\n'})
	if f, ok := p.out.(interface{ Flush() error }); ok {
		_ = f.Flush()
	}
}

// writeErrorResponse synthesizes and writes a JSON-RPC error.
func (p *proxy) writeErrorResponse(id json.RawMessage, code int, msg string) {
	type rpcErr struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	r := rpcErr{JSONRPC: "2.0", ID: id}
	if id == nil {
		r.ID = json.RawMessage("null")
	}
	r.Error.Code = code
	r.Error.Message = msg
	b, _ := json.Marshal(r)
	p.writeRaw(b)
}

// verbosef writes one line to stderr if --verbose is on. Never includes the
// token or full body content (decision b).
func (p *proxy) verbosef(format string, args ...interface{}) {
	if !mcpProxyVerbose {
		return
	}
	// Hold errMu so concurrent goroutines don't interleave bytes within a
	// single line — and so test buffers (bytes.Buffer is not goroutine-safe)
	// stay race-free under -race. os.Stderr is line-safe at the OS level
	// but test stderr writers are not.
	p.errMu.Lock()
	defer p.errMu.Unlock()
	fmt.Fprintf(p.err, "mcp-proxy: "+format+"\n", args...)
}

// stringifyID converts a JSON-RPC id RawMessage to a stable string key for
// the cancellation registry. Returns "" for nil/null/empty (notifications).
func stringifyID(id json.RawMessage) string {
	if len(id) == 0 {
		return ""
	}
	s := string(bytes.TrimSpace(id))
	if s == "" || s == "null" {
		return ""
	}
	return s
}

// redactSessionID truncates a UUID-shaped session ID for safe logging.
func redactSessionID(sid string) string {
	if len(sid) < 8 {
		return "***"
	}
	return sid[:8] + "…"
}

// sanitizeForJSON replaces characters that would break JSON-string embedding
// in a synthesized error message.
func sanitizeForJSON(s string) string {
	if len(s) > 256 {
		s = s[:256] + "…"
	}
	// Strip control bytes other than tab/newline/CR.
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			b.WriteByte('?')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
