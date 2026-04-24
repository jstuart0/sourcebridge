// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

// Streaming response support for the MCP streamable-HTTP transport, and
// progress notifications for long-running tool calls.
//
// Why this exists
// ---------------
// Tools like `explain_code` route through the worker and an LLM, which can
// take 60–120 s on modest hardware. Default MCP-client tool timeouts are
// shorter than that (Claude Code's is ~60 s), so a synchronous response
// gets cancelled client-side even when the server eventually answers.
//
// The MCP spec resolves this two ways:
//   1. Per §6.2.1 (streamable HTTP), a server may respond with a
//      text/event-stream body instead of application/json. The client
//      reads JSON-RPC messages framed as SSE `data:` events until the
//      final response arrives. This keeps the HTTP connection alive and
//      lets us push intermediate messages.
//   2. Per §12.1 (progress), a client that sent a `_meta.progressToken`
//      with a request expects zero-or-more `notifications/progress`
//      messages before the final response. Receiving one resets the
//      client's tool timeout.
//
// Together, these turn a blocking 90-second tool call into a stream of
// keepalive progress messages followed by the real response. Clients that
// don't opt in (no Accept header for SSE, no progress token) fall back to
// the existing synchronous JSON behaviour, with a bounded server-side
// timeout so a dead worker can't pin a connection forever.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// toolCallDeadline bounds how long a single tool call may run before we
// return an error. 5 minutes is long enough for a local LLM to finish
// `explain_code` on a slow setup but short enough that a truly stuck
// worker doesn't monopolize a server slot.
const toolCallDeadline = 5 * time.Minute

// progressHeartbeat is the interval between `notifications/progress`
// messages sent to a client that provided a progressToken. 15 seconds is
// comfortably inside both Claude Code's and Codex's default tool timeouts.
const progressHeartbeat = 15 * time.Second

// slowToolNames is the set of tools that should run under the progress +
// streaming machinery. Fast tools (sub-second) don't benefit and would
// just burn a streaming HTTP connection.
var slowToolNames = map[string]bool{
	"explain_code":    true,
	"get_cliff_notes": true,
	"ask_question":    true,
}

// requestWantsSSE reports whether the client opted into streamed SSE
// responses via its Accept header.
func requestWantsSSE(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/event-stream")
}

// toolCallShouldStream reports whether the request is a tools/call for a
// slow tool AND the client accepts SSE. Both conditions must hold — we
// don't waste an SSE response on fast tools, and we don't impose SSE on
// clients that only asked for JSON.
func toolCallShouldStream(r *http.Request, msg jsonRPCRequest) bool {
	if !requestWantsSSE(r) {
		return false
	}
	if msg.Method != "tools/call" {
		return false
	}
	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return false
	}
	return slowToolNames[params.Name]
}

// extractProgressToken pulls `_meta.progressToken` from the request
// params. Returns nil if absent — the spec says servers MUST NOT send
// progress notifications without one.
func extractProgressToken(params json.RawMessage) json.RawMessage {
	var wrapper struct {
		Meta struct {
			ProgressToken json.RawMessage `json:"progressToken"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(params, &wrapper); err != nil {
		return nil
	}
	if len(wrapper.Meta.ProgressToken) == 0 {
		return nil
	}
	return wrapper.Meta.ProgressToken
}

// handleStreamingToolCall dispatches a slow tool call with SSE-framed
// responses and a progress heartbeat. It writes the full HTTP response
// itself — callers must not write anything else.
//
// The tool runs in a goroutine against a context bounded by
// toolCallDeadline. While it runs, we emit a progress notification at
// each `progressHeartbeat` tick (only if the client sent a token). When
// the tool returns, we emit the final JSON-RPC response as a single SSE
// event and close the stream.
func (h *mcpHandler) handleStreamingToolCall(
	w http.ResponseWriter,
	r *http.Request,
	sess *mcpSession,
	msg jsonRPCRequest,
	sessionID string,
) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// No flusher means we can't really stream — fall back to synchronous.
		// This is exceedingly rare in production (chi + net/http both flush).
		resp := h.safeDispatch(sess, msg)
		w.Header().Set("Content-Type", "application/json")
		if sessionID != "" {
			w.Header().Set("Mcp-Session-Id", sessionID)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if sessionID != "" {
		w.Header().Set("Mcp-Session-Id", sessionID)
	}
	w.WriteHeader(http.StatusOK)

	progressToken := extractProgressToken(msg.Params)

	// Bound the tool call so we never stream forever.
	ctx, cancel := context.WithTimeout(r.Context(), toolCallDeadline)
	defer cancel()

	// A content emitter lets tools (specifically explain_code today)
	// push per-token deltas up to this goroutine for real-time SSE
	// streaming. Tools that don't use it are unaffected — the channel
	// simply stays empty and the select falls through to the heartbeat
	// ticker like before.
	emitter := newContentEmitter()
	ctx = WithContentEmitter(ctx, emitter)

	// Run the dispatch in a goroutine; the main goroutine owns the HTTP
	// response and ticks progress messages.
	type dispatchResult struct {
		resp jsonRPCResponse
	}
	resultCh := make(chan dispatchResult, 1)
	go func() {
		resultCh <- dispatchResult{resp: h.safeDispatchCtx(ctx, sess, msg)}
	}()

	ticker := time.NewTicker(progressHeartbeat)
	defer ticker.Stop()

	start := time.Now()
	for {
		select {
		case <-ctx.Done():
			// Either the client disconnected or we hit the deadline.
			// Emit a JSON-RPC error and close. safeDispatch may still be
			// running — we leak that goroutine briefly (bounded by the
			// worker's own timeouts), but we don't block the HTTP handler.
			errResp := errorResponse(msg.ID, -32603, fmt.Sprintf("tool call cancelled after %s", time.Since(start).Round(time.Second)))
			writeSSEMessage(w, flusher, errResp)
			slog.Warn("mcp streaming tool call cancelled", "session_id", sess.id, "elapsed", time.Since(start))
			return

		case delta := <-emitter.Chan():
			// A tool pushed a content chunk. Only forward it when the
			// client opted into progress notifications; otherwise drop
			// (the final response still contains the full content).
			if progressToken != nil {
				writeSSEContentDelta(w, flusher, progressToken, time.Since(start), delta)
			}

		case <-ticker.C:
			// Only send a "still working" heartbeat if the client opted in.
			if progressToken != nil {
				writeSSEProgress(w, flusher, progressToken, time.Since(start))
			} else {
				// Fall back to an SSE comment keepalive so intermediaries
				// (traefik, nginx, CDN) don't idle-close the connection.
				_, _ = fmt.Fprintf(w, ": keepalive\n\n")
				flusher.Flush()
			}

		case res := <-resultCh:
			// Drain any remaining deltas the tool pushed just before
			// returning so the client doesn't miss the last few tokens.
			for drained := true; drained; {
				select {
				case delta := <-emitter.Chan():
					if progressToken != nil {
						writeSSEContentDelta(w, flusher, progressToken, time.Since(start), delta)
					}
				default:
					drained = false
				}
			}
			writeSSEMessage(w, flusher, res.resp)
			return
		}
	}
}

// writeSSEMessage frames a JSON-RPC message as a single SSE `message` event.
func writeSSEMessage(w http.ResponseWriter, flusher http.Flusher, resp jsonRPCResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		slog.Error("mcp failed to marshal streaming response", "error", err)
		return
	}
	_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
	flusher.Flush()
}

// writeSSEProgress frames a `notifications/progress` JSON-RPC notification.
// progress is reported as elapsed whole seconds — MCP clients treat the
// number as opaque; it just needs to advance monotonically.
func writeSSEProgress(w http.ResponseWriter, flusher http.Flusher, progressToken json.RawMessage, elapsed time.Duration) {
	notif := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/progress",
		"params": map[string]interface{}{
			"progressToken": json.RawMessage(progressToken),
			"progress":      int(elapsed.Seconds()),
			"message":       fmt.Sprintf("still working (%s elapsed)", elapsed.Round(time.Second)),
		},
	}
	data, err := json.Marshal(notif)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
	flusher.Flush()
}

// writeSSEContentDelta frames a progress notification that carries a
// chunk of visible tool output. The standard `message` field is kept
// for human-readable clients; the `delta` field is a non-standard
// extension our VS Code plugin treats as the answer-text fragment to
// append to the running exchange. Clients that don't understand it
// simply ignore the unknown field (MCP leaves extension fields open).
func writeSSEContentDelta(
	w http.ResponseWriter,
	flusher http.Flusher,
	progressToken json.RawMessage,
	elapsed time.Duration,
	delta string,
) {
	notif := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/progress",
		"params": map[string]interface{}{
			"progressToken": json.RawMessage(progressToken),
			"progress":      int(elapsed.Seconds()),
			"message":       "generating",
			"delta":         delta,
		},
	}
	data, err := json.Marshal(notif)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
	flusher.Flush()
}

// contextKey is a private type for context values added by this
// package so it never collides with other packages' keys.
type contextKey int

const contentEmitterKey contextKey = iota

// ContentEmitter lets a tool push visible-content deltas back up to
// the HTTP layer for streaming. Tools that don't care ignore it.
// Buffered channel keeps the producer (tool goroutine) from stalling
// if the consumer (HTTP goroutine) is briefly busy flushing a frame.
type ContentEmitter struct {
	ch chan string
}

func newContentEmitter() *ContentEmitter {
	return &ContentEmitter{ch: make(chan string, 64)}
}

// Emit pushes a delta. Non-blocking with a modest buffer — if the
// consumer falls far behind, older deltas are dropped rather than
// blocking the LLM stream. The final resolved answer in the terminal
// response still contains the full text, so dropped deltas only
// affect the live-stream UX.
func (e *ContentEmitter) Emit(delta string) {
	if e == nil || delta == "" {
		return
	}
	select {
	case e.ch <- delta:
	default:
	}
}

// Chan returns the read side. Used by the streaming handler's event
// loop; tools should not touch it.
func (e *ContentEmitter) Chan() <-chan string {
	if e == nil {
		return nil
	}
	return e.ch
}

// WithContentEmitter attaches an emitter to ctx so tools can pull it
// out. Tools call `ContentEmitterFromContext(ctx)` and emit on it
// when they have streaming output.
func WithContentEmitter(ctx context.Context, e *ContentEmitter) context.Context {
	return context.WithValue(ctx, contentEmitterKey, e)
}

// ContentEmitterFromContext returns the emitter or nil if none was
// installed (unary dispatch path).
func ContentEmitterFromContext(ctx context.Context) *ContentEmitter {
	v, _ := ctx.Value(contentEmitterKey).(*ContentEmitter)
	return v
}
