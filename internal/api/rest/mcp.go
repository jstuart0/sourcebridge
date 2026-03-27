// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/sourcebridge/sourcebridge/internal/auth"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/worker"

	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
)

// ---------------------------------------------------------------------------
// MCP protocol constants
// ---------------------------------------------------------------------------

const (
	mcpProtocolVersion = "2025-11-25"
	mcpServerName      = "sourcebridge"
	mcpServerVersion   = "1.0.0"
	mcpMaxBodySize     = 1 << 20 // 1MB
)

// ---------------------------------------------------------------------------
// JSON-RPC types
// ---------------------------------------------------------------------------

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // may be absent for notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *jsonRPCError `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// ---------------------------------------------------------------------------
// MCP content types
// ---------------------------------------------------------------------------

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type mcpToolResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

// ---------------------------------------------------------------------------
// MCP tool definition
// ---------------------------------------------------------------------------

type mcpToolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

// ---------------------------------------------------------------------------
// MCP resource definition
// ---------------------------------------------------------------------------

type mcpResourceDefinition struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ---------------------------------------------------------------------------
// Enterprise extension point interfaces
// ---------------------------------------------------------------------------

// MCPPermissionChecker validates whether a user can access a given repo via MCP.
type MCPPermissionChecker interface {
	CanAccessRepo(tenantID, userID, repoID string) bool
}

// MCPAuditLogger records MCP tool calls and resource reads for compliance.
type MCPAuditLogger interface {
	LogToolCall(tenantID, userID, toolName string, repoID string, durationMs int64, err error)
	LogResourceRead(tenantID, userID, resourceURI string, durationMs int64, err error)
}

// MCPToolExtender lets enterprise builds add extra MCP tools.
type MCPToolExtender interface {
	ExtraTools() []mcpToolDefinition
	CallTool(ctx context.Context, session *mcpSession, toolName string, args json.RawMessage) (interface{}, error)
}

// ---------------------------------------------------------------------------
// Worker interface (for testability)
// ---------------------------------------------------------------------------

// mcpWorkerCaller abstracts the worker methods used by MCP tools.
type mcpWorkerCaller interface {
	IsAvailable() bool
	AnswerQuestion(ctx context.Context, req *reasoningv1.AnswerQuestionRequest) (*reasoningv1.AnswerQuestionResponse, error)
}

// Verify that *worker.Client satisfies mcpWorkerCaller at compile time.
var _ mcpWorkerCaller = (*worker.Client)(nil)

// ---------------------------------------------------------------------------
// Session
// ---------------------------------------------------------------------------

type mcpSession struct {
	id          string
	claims      *auth.Claims
	initialized bool
	clientInfo  struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	createdAt time.Time
	lastUsed  time.Time
	eventCh   chan []byte // SSE events sent back to the client
	done      chan struct{}
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

type mcpHandler struct {
	store          graphstore.GraphStore
	knowledgeStore knowledge.KnowledgeStore
	worker         mcpWorkerCaller
	allowedRepos   map[string]bool // nil = all repos allowed
	sessionTTL     time.Duration
	keepalive      time.Duration
	maxSessions    int

	sessions sync.Map // map[string]*mcpSession

	// Enterprise extension points (nil in OSS)
	permChecker  MCPPermissionChecker
	auditLogger  MCPAuditLogger
	toolExtender MCPToolExtender
}

func newMCPHandler(store graphstore.GraphStore, ks knowledge.KnowledgeStore, w mcpWorkerCaller, repos string, sessionTTL, keepalive time.Duration, maxSessions int) *mcpHandler {
	h := &mcpHandler{
		store:          store,
		knowledgeStore: ks,
		worker:         w,
		sessionTTL:     sessionTTL,
		keepalive:      keepalive,
		maxSessions:    maxSessions,
	}
	if repos != "" {
		h.allowedRepos = make(map[string]bool)
		for _, r := range strings.Split(repos, ",") {
			r = strings.TrimSpace(r)
			if r != "" {
				h.allowedRepos[r] = true
			}
		}
		// Warn about repo IDs that don't exist in the store
		for repoID := range h.allowedRepos {
			if store.GetRepository(repoID) == nil {
				slog.Warn("mcp configured repo not found in store", "repo_id", repoID)
			}
		}
	}
	// Start session reaper
	go h.reapSessions()
	return h
}

// sessionCount returns the number of active sessions.
func (h *mcpHandler) sessionCount() int {
	count := 0
	h.sessions.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}

// reapSessions periodically removes expired sessions.
func (h *mcpHandler) reapSessions() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		h.sessions.Range(func(key, value interface{}) bool {
			sess := value.(*mcpSession)
			if now.Sub(sess.lastUsed) > h.sessionTTL {
				slog.Info("mcp session expired", "session_id", sess.id, "duration_seconds", int(now.Sub(sess.createdAt).Seconds()))
				h.sessions.Delete(key)
				close(sess.done)
			}
			return true
		})
	}
}

// ---------------------------------------------------------------------------
// SSE endpoint: GET /api/v1/mcp/sse
// ---------------------------------------------------------------------------

func (h *mcpHandler) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Enforce max sessions
	if h.maxSessions > 0 && h.sessionCount() >= h.maxSessions {
		slog.Warn("mcp max sessions reached", "current_sessions", h.sessionCount(), "max_sessions", h.maxSessions)
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many MCP sessions"})
		return
	}

	claims := auth.GetClaims(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	sess := &mcpSession{
		id:        uuid.New().String(),
		claims:    claims,
		createdAt: time.Now(),
		lastUsed:  time.Now(),
		eventCh:   make(chan []byte, 64),
		done:      make(chan struct{}),
	}
	h.sessions.Store(sess.id, sess)

	slog.Info("mcp session created", "session_id", sess.id, "user_id", claims.UserID)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Send the endpoint event so the client knows where to POST messages
	messageURL := fmt.Sprintf("/api/v1/mcp/message?sessionId=%s", sess.id)
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", messageURL)
	flusher.Flush()

	// Keepalive ticker
	keepaliveTicker := time.NewTicker(h.keepalive)
	defer keepaliveTicker.Stop()

	ctx := r.Context()
	defer func() {
		h.sessions.Delete(sess.id)
		slog.Info("mcp session closed", "session_id", sess.id, "duration_seconds", int(time.Since(sess.createdAt).Seconds()))
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-sess.done:
			return
		case data := <-sess.eventCh:
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
			flusher.Flush()
		case <-keepaliveTicker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
			slog.Debug("mcp keepalive sent", "session_id", sess.id)
		}
	}
}

// ---------------------------------------------------------------------------
// Message endpoint: POST /api/v1/mcp/message?sessionId=...
// ---------------------------------------------------------------------------

func (h *mcpHandler) handleMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid session"})
		return
	}

	val, ok := h.sessions.Load(sessionID)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid session"})
		return
	}
	sess := val.(*mcpSession)
	sess.lastUsed = time.Now()

	body, err := io.ReadAll(io.LimitReader(r.Body, mcpMaxBodySize+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
		return
	}
	if len(body) > mcpMaxBodySize {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body too large"})
		return
	}

	var msg jsonRPCRequest
	if err := json.Unmarshal(body, &msg); err != nil {
		h.sendResponse(sess, errorResponse(nil, -32700, "Parse error: "+err.Error()))
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if msg.JSONRPC != "2.0" {
		h.sendResponse(sess, errorResponse(msg.ID, -32600, "Invalid request: jsonrpc must be '2.0'"))
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Notifications (no ID) don't get responses
	if msg.ID == nil || string(msg.ID) == "" || string(msg.ID) == "null" {
		// Just acknowledge; notifications like "notifications/initialized" are no-ops
		w.WriteHeader(http.StatusAccepted)
		return
	}

	resp := h.safeDispatch(sess, msg)
	h.sendResponse(sess, resp)
	w.WriteHeader(http.StatusAccepted)
}

func (h *mcpHandler) sendResponse(sess *mcpSession, resp jsonRPCResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		slog.Error("mcp failed to marshal response", "error", err)
		return
	}
	select {
	case sess.eventCh <- data:
	default:
		slog.Warn("mcp session event buffer full, dropping response", "session_id", sess.id)
	}
}

// ---------------------------------------------------------------------------
// Panic-safe dispatch
// ---------------------------------------------------------------------------

func (h *mcpHandler) safeDispatch(session *mcpSession, msg jsonRPCRequest) (resp jsonRPCResponse) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("mcp handler panic", "method", msg.Method, "error", r)
			resp = errorResponse(msg.ID, -32603, "Internal error")
		}
	}()
	return h.dispatch(session, msg)
}

// ---------------------------------------------------------------------------
// Method dispatch
// ---------------------------------------------------------------------------

func (h *mcpHandler) dispatch(session *mcpSession, msg jsonRPCRequest) jsonRPCResponse {
	// initialize is always allowed (it's how you start)
	if msg.Method == "initialize" {
		return h.handleInitialize(session, msg)
	}

	// ping is always allowed
	if msg.Method == "ping" {
		return successResponse(msg.ID, struct{}{})
	}

	// All other methods require initialization
	if !session.initialized {
		slog.Warn("mcp pre-init method rejected", "session_id", session.id, "method", msg.Method)
		return errorResponse(msg.ID, -32600, "Session not initialized. Send 'initialize' first.")
	}

	switch msg.Method {
	case "tools/list":
		return h.handleToolsList(session, msg)
	case "tools/call":
		return h.handleToolsCall(session, msg)
	case "resources/list":
		return h.handleResourcesList(session, msg)
	case "resources/read":
		return h.handleResourcesRead(session, msg)
	default:
		slog.Warn("mcp method not found", "session_id", session.id, "method", msg.Method)
		return errorResponse(msg.ID, -32601, fmt.Sprintf("Method not found: %s", msg.Method))
	}
}

// ---------------------------------------------------------------------------
// initialize
// ---------------------------------------------------------------------------

func (h *mcpHandler) handleInitialize(session *mcpSession, msg jsonRPCRequest) jsonRPCResponse {
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct{} `json:"capabilities"`
		ClientInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}
	if msg.Params != nil {
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return errorResponse(msg.ID, -32602, "Invalid params: "+err.Error())
		}
	}

	// MCP spec: server responds with the version it supports; the client
	// decides whether it can work with it. We log a mismatch but don't reject,
	// because different clients (Claude Code, Codex, Cursor, etc.) ship
	// different protocol versions and the wire format is compatible.
	if params.ProtocolVersion != "" && params.ProtocolVersion != mcpProtocolVersion {
		slog.Info("mcp protocol version negotiation",
			"session_id", session.id,
			"client_version", params.ProtocolVersion,
			"server_version", mcpProtocolVersion,
		)
	}

	session.initialized = true
	session.clientInfo.Name = params.ClientInfo.Name
	session.clientInfo.Version = params.ClientInfo.Version

	slog.Info("mcp session initialized", "session_id", session.id, "client_name", params.ClientInfo.Name, "client_version", params.ClientInfo.Version)

	return successResponse(msg.ID, map[string]interface{}{
		"protocolVersion": mcpProtocolVersion,
		"capabilities": map[string]interface{}{
			"tools":     map[string]interface{}{},
			"resources": map[string]interface{}{},
		},
		"serverInfo": map[string]interface{}{
			"name":    mcpServerName,
			"version": mcpServerVersion,
		},
	})
}

// ---------------------------------------------------------------------------
// tools/list
// ---------------------------------------------------------------------------

func (h *mcpHandler) handleToolsList(_ *mcpSession, msg jsonRPCRequest) jsonRPCResponse {
	tools := h.baseTools()
	if h.toolExtender != nil {
		tools = append(tools, h.toolExtender.ExtraTools()...)
	}
	return successResponse(msg.ID, map[string]interface{}{"tools": tools})
}

func (h *mcpHandler) baseTools() []mcpToolDefinition {
	return []mcpToolDefinition{
		{
			Name:        "search_symbols",
			Description: "Search for code symbols (functions, classes, types, variables) in a repository.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{"type": "string", "description": "Repository ID to search"},
					"query":         map[string]interface{}{"type": "string", "description": "Search query (name or pattern)"},
					"kind":          map[string]interface{}{"type": "string", "description": "Filter by symbol kind (function, class, type, variable, etc.)"},
					"file_path":     map[string]interface{}{"type": "string", "description": "Filter to symbols in a specific file"},
					"limit":         map[string]interface{}{"type": "integer", "description": "Max results to return (default 50, max 500)"},
					"offset":        map[string]interface{}{"type": "integer", "description": "Offset for pagination"},
				},
				"required": []string{"repository_id", "query"},
			},
		},
		{
			Name:        "explain_code",
			Description: "Get an AI-generated explanation of code. Provide either inline code or a file path.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{"type": "string", "description": "Repository ID"},
					"code":          map[string]interface{}{"type": "string", "description": "Inline code to explain"},
					"file_path":     map[string]interface{}{"type": "string", "description": "File path within the repository to explain"},
					"question":      map[string]interface{}{"type": "string", "description": "Specific question about the code (default: 'Explain this code')"},
				},
				"required": []string{"repository_id"},
			},
		},
		{
			Name:        "get_requirements",
			Description: "List requirements tracked for a repository, optionally with their linked code symbols.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{"type": "string", "description": "Repository ID"},
					"include_links": map[string]interface{}{"type": "boolean", "description": "Include linked symbols for each requirement (default false)"},
					"limit":         map[string]interface{}{"type": "integer", "description": "Max results (default 50, max 500)"},
					"offset":        map[string]interface{}{"type": "integer", "description": "Offset for pagination"},
				},
				"required": []string{"repository_id"},
			},
		},
		{
			Name:        "get_impact_report",
			Description: "Get the latest change impact report for a repository, showing which files, symbols, and requirements are affected by recent changes.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{"type": "string", "description": "Repository ID"},
				},
				"required": []string{"repository_id"},
			},
		},
		{
			Name:        "get_cliff_notes",
			Description: "Get the cliff notes (AI-generated summary) for a repository, module, file, symbol, or requirement.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{"type": "string", "description": "Repository ID"},
					"scope_type":    map[string]interface{}{"type": "string", "enum": []string{"repository", "module", "file", "symbol", "requirement"}, "description": "Scope type (default: repository)"},
					"scope_path":    map[string]interface{}{"type": "string", "description": "Scope path (file path, module path, or symbol path like 'file.go#FuncName')"},
					"audience":      map[string]interface{}{"type": "string", "enum": []string{"beginner", "developer"}, "description": "Target audience (default: developer)"},
					"depth":         map[string]interface{}{"type": "string", "enum": []string{"summary", "medium", "deep"}, "description": "Level of detail (default: medium)"},
				},
				"required": []string{"repository_id"},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// tools/call
// ---------------------------------------------------------------------------

func (h *mcpHandler) handleToolsCall(session *mcpSession, msg jsonRPCRequest) jsonRPCResponse {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return errorResponse(msg.ID, -32602, "Invalid params: "+err.Error())
	}

	slog.Info("mcp tool call started", "session_id", session.id, "tool_name", params.Name)
	start := time.Now()

	var result interface{}
	var toolErr error

	switch params.Name {
	case "search_symbols":
		result, toolErr = h.callSearchSymbols(session, params.Arguments)
	case "explain_code":
		result, toolErr = h.callExplainCode(session, params.Arguments)
	case "get_requirements":
		result, toolErr = h.callGetRequirements(session, params.Arguments)
	case "get_impact_report":
		result, toolErr = h.callGetImpactReport(session, params.Arguments)
	case "get_cliff_notes":
		result, toolErr = h.callGetCliffNotes(session, params.Arguments)
	default:
		// Try enterprise tool extender
		if h.toolExtender != nil {
			result, toolErr = h.toolExtender.CallTool(context.Background(), session, params.Name, params.Arguments)
		} else {
			return errorResponse(msg.ID, -32601, fmt.Sprintf("Unknown tool: %s", params.Name))
		}
	}

	elapsed := time.Since(start)
	slog.Info("mcp tool call completed", "session_id", session.id, "tool_name", params.Name, "duration_ms", elapsed.Milliseconds())

	if h.auditLogger != nil {
		repoID := extractRepoID(params.Arguments)
		h.auditLogger.LogToolCall(session.claims.OrgID, session.claims.UserID, params.Name, repoID, elapsed.Milliseconds(), toolErr)
	}

	if toolErr != nil {
		return successResponse(msg.ID, mcpToolResult{
			Content: []mcpContent{{Type: "text", Text: toolErr.Error()}},
			IsError: true,
		})
	}

	// Marshal the result to JSON text for the MCP content
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return successResponse(msg.ID, mcpToolResult{
			Content: []mcpContent{{Type: "text", Text: "Failed to serialize result"}},
			IsError: true,
		})
	}

	return successResponse(msg.ID, mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: string(resultJSON)}},
	})
}

// extractRepoID is a best-effort extraction of repository_id from tool arguments for audit logging.
func extractRepoID(args json.RawMessage) string {
	var parsed struct {
		RepositoryID string `json:"repository_id"`
	}
	_ = json.Unmarshal(args, &parsed)
	return parsed.RepositoryID
}

// ---------------------------------------------------------------------------
// Tool: search_symbols
// ---------------------------------------------------------------------------

func (h *mcpHandler) callSearchSymbols(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string `json:"repository_id"`
		Query        string `json:"query"`
		Kind         string `json:"kind"`
		FilePath     string `json:"file_path"`
		Limit        int    `json:"limit"`
		Offset       int    `json:"offset"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %v", err)
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	if params.Limit <= 0 {
		params.Limit = 50
	}
	if params.Limit > 500 {
		params.Limit = 500
	}

	var kindPtr *string
	if params.Kind != "" {
		kindPtr = &params.Kind
	}
	var queryPtr *string
	if params.Query != "" {
		queryPtr = &params.Query
	}

	var symbols []*graphstore.StoredSymbol
	var total int

	if params.FilePath != "" {
		// Filter by file
		fileSymbols := h.store.GetSymbolsByFile(params.RepositoryID, params.FilePath)
		// Apply query and kind filtering manually
		for _, s := range fileSymbols {
			if queryPtr != nil && !strings.Contains(strings.ToLower(s.Name), strings.ToLower(*queryPtr)) {
				continue
			}
			if kindPtr != nil && s.Kind != *kindPtr {
				continue
			}
			symbols = append(symbols, s)
		}
		total = len(symbols)
		// Apply pagination
		if params.Offset > 0 && params.Offset < len(symbols) {
			symbols = symbols[params.Offset:]
		} else if params.Offset >= len(symbols) {
			symbols = nil
		}
		if len(symbols) > params.Limit {
			symbols = symbols[:params.Limit]
		}
	} else {
		symbols, total = h.store.GetSymbols(params.RepositoryID, queryPtr, kindPtr, params.Limit, params.Offset)
	}

	type symbolResult struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		FilePath string `json:"file_path"`
		Line     int    `json:"line"`
		EndLine  int    `json:"end_line,omitempty"`
	}

	results := make([]symbolResult, 0, len(symbols))
	for _, s := range symbols {
		results = append(results, symbolResult{
			ID:       s.ID,
			Name:     s.Name,
			Kind:     s.Kind,
			FilePath: s.FilePath,
			Line:     s.StartLine,
			EndLine:  s.EndLine,
		})
	}

	return map[string]interface{}{
		"symbols":     results,
		"total_count": total,
	}, nil
}

// ---------------------------------------------------------------------------
// Tool: explain_code
// ---------------------------------------------------------------------------

func (h *mcpHandler) callExplainCode(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string `json:"repository_id"`
		Code         string `json:"code"`
		FilePath     string `json:"file_path"`
		Question     string `json:"question"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %v", err)
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	if h.worker == nil || !h.worker.IsAvailable() {
		return nil, fmt.Errorf("AI worker is not connected. The explain_code tool requires a running worker.")
	}

	question := params.Question
	if question == "" {
		question = "Explain this code"
	}

	code := params.Code
	if code == "" && params.FilePath != "" {
		// Read source from the repository's indexed files
		repo := h.store.GetRepository(params.RepositoryID)
		if repo == nil {
			return nil, fmt.Errorf("Repository not found or not accessible")
		}
		// Get symbols from the file to provide context
		fileSymbols := h.store.GetSymbolsByFile(params.RepositoryID, params.FilePath)
		if len(fileSymbols) > 0 {
			// Build context from symbol signatures and doc comments
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("// File: %s\n", params.FilePath))
			for _, s := range fileSymbols {
				if s.DocComment != "" {
					sb.WriteString(s.DocComment)
					sb.WriteString("\n")
				}
				if s.Signature != "" {
					sb.WriteString(s.Signature)
					sb.WriteString("\n\n")
				}
			}
			code = sb.String()
		}
		if code == "" {
			return nil, fmt.Errorf("Could not read source file: %s. Repository may need reindexing.", params.FilePath)
		}
	}

	if code == "" {
		return nil, fmt.Errorf("Either 'code' or 'file_path' must be provided")
	}

	// Build the question with the code context
	fullQuestion := fmt.Sprintf("%s\n\n```\n%s\n```", question, code)

	ctx, cancel := context.WithTimeout(context.Background(), worker.TimeoutDiscussion)
	defer cancel()

	resp, err := h.worker.AnswerQuestion(ctx, &reasoningv1.AnswerQuestionRequest{
		Question:     fullQuestion,
		RepositoryId: params.RepositoryID,
	})
	if err != nil {
		return nil, fmt.Errorf("AI worker timed out or failed: %v", err)
	}

	return map[string]interface{}{
		"explanation": resp.GetAnswer(),
	}, nil
}

// ---------------------------------------------------------------------------
// Tool: get_requirements
// ---------------------------------------------------------------------------

func (h *mcpHandler) callGetRequirements(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string `json:"repository_id"`
		IncludeLinks bool   `json:"include_links"`
		Limit        int    `json:"limit"`
		Offset       int    `json:"offset"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %v", err)
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	if params.Limit <= 0 {
		params.Limit = 50
	}
	if params.Limit > 500 {
		params.Limit = 500
	}

	reqs, total := h.store.GetRequirements(params.RepositoryID, params.Limit, params.Offset)

	type linkInfo struct {
		SymbolID   string  `json:"symbol_id"`
		SymbolName string  `json:"symbol_name"`
		FilePath   string  `json:"file_path"`
		Confidence float64 `json:"confidence"`
	}
	type reqResult struct {
		ID          string     `json:"id"`
		ExternalID  string     `json:"external_id,omitempty"`
		Title       string     `json:"title"`
		Description string     `json:"description,omitempty"`
		Priority    string     `json:"priority,omitempty"`
		Tags        []string   `json:"tags,omitempty"`
		Links       []linkInfo `json:"links,omitempty"`
	}

	results := make([]reqResult, 0, len(reqs))
	for _, req := range reqs {
		r := reqResult{
			ID:          req.ID,
			ExternalID:  req.ExternalID,
			Title:       req.Title,
			Description: req.Description,
			Priority:    req.Priority,
			Tags:        req.Tags,
		}
		if params.IncludeLinks {
			links := h.store.GetLinksForRequirement(req.ID, false)
			// Batch lookup symbol names
			symIDs := make([]string, 0, len(links))
			for _, l := range links {
				symIDs = append(symIDs, l.SymbolID)
			}
			symMap := h.store.GetSymbolsByIDs(symIDs)

			for _, l := range links {
				li := linkInfo{
					SymbolID:   l.SymbolID,
					Confidence: l.Confidence,
				}
				if s, ok := symMap[l.SymbolID]; ok {
					li.SymbolName = s.Name
					li.FilePath = s.FilePath
				}
				r.Links = append(r.Links, li)
			}
		}
		results = append(results, r)
	}

	return map[string]interface{}{
		"requirements": results,
		"total_count":  total,
	}, nil
}

// ---------------------------------------------------------------------------
// Tool: get_impact_report
// ---------------------------------------------------------------------------

func (h *mcpHandler) callGetImpactReport(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string `json:"repository_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %v", err)
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	report := h.store.GetLatestImpactReport(params.RepositoryID)
	if report == nil {
		return map[string]interface{}{"report": nil}, nil
	}

	return map[string]interface{}{
		"report": map[string]interface{}{
			"id":                    report.ID,
			"old_commit_sha":        report.OldCommitSHA,
			"new_commit_sha":        report.NewCommitSHA,
			"files_changed":         len(report.FilesChanged),
			"symbols_added":         len(report.SymbolsAdded),
			"symbols_modified":      len(report.SymbolsModified),
			"symbols_removed":       len(report.SymbolsRemoved),
			"affected_links":        len(report.AffectedLinks),
			"affected_requirements": len(report.AffectedRequirements),
			"stale_artifacts":       len(report.StaleArtifacts),
			"computed_at":           report.ComputedAt.Format(time.RFC3339),
		},
	}, nil
}

// ---------------------------------------------------------------------------
// Tool: get_cliff_notes
// ---------------------------------------------------------------------------

func (h *mcpHandler) callGetCliffNotes(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string `json:"repository_id"`
		ScopeType    string `json:"scope_type"`
		ScopePath    string `json:"scope_path"`
		Audience     string `json:"audience"`
		Depth        string `json:"depth"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %v", err)
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	if h.knowledgeStore == nil {
		return nil, fmt.Errorf("Knowledge store is not configured. Cliff notes require knowledge persistence.")
	}

	// Apply defaults
	scopeType := knowledge.ScopeType(params.ScopeType)
	if scopeType == "" {
		scopeType = knowledge.ScopeRepository
	}
	audience := knowledge.Audience(params.Audience)
	if audience == "" {
		audience = knowledge.AudienceDeveloper
	}
	depth := knowledge.Depth(params.Depth)
	if depth == "" {
		depth = knowledge.DepthMedium
	}

	key := knowledge.ArtifactKey{
		RepositoryID: params.RepositoryID,
		Type:         knowledge.ArtifactCliffNotes,
		Audience:     audience,
		Depth:        depth,
		Scope: knowledge.ArtifactScope{
			ScopeType: scopeType,
			ScopePath: params.ScopePath,
		},
	}

	artifact := h.knowledgeStore.GetArtifactByKey(key)
	if artifact == nil {
		return map[string]interface{}{
			"artifact": nil,
			"message":  "No cliff notes have been generated for this scope yet.",
		}, nil
	}

	if artifact.Status == knowledge.StatusGenerating {
		return map[string]interface{}{
			"artifact": nil,
			"message":  "Cliff notes are currently being generated. Please try again in a moment.",
		}, nil
	}

	if artifact.Status != knowledge.StatusReady {
		return map[string]interface{}{
			"artifact": nil,
			"message":  fmt.Sprintf("Cliff notes are in '%s' state.", artifact.Status),
		}, nil
	}

	type sectionResult struct {
		Title      string `json:"title"`
		Content    string `json:"content"`
		Summary    string `json:"summary,omitempty"`
		Confidence string `json:"confidence"`
	}

	sections := make([]sectionResult, 0, len(artifact.Sections))
	for _, s := range artifact.Sections {
		sections = append(sections, sectionResult{
			Title:      s.Title,
			Content:    s.Content,
			Summary:    s.Summary,
			Confidence: string(s.Confidence),
		})
	}

	return map[string]interface{}{
		"artifact": map[string]interface{}{
			"id":           artifact.ID,
			"scope_type":   string(artifact.Scope.ScopeType),
			"scope_path":   artifact.Scope.ScopePath,
			"audience":     string(artifact.Audience),
			"depth":        string(artifact.Depth),
			"stale":        artifact.Stale,
			"generated_at": artifact.GeneratedAt.Format(time.RFC3339),
			"sections":     sections,
		},
	}, nil
}

// ---------------------------------------------------------------------------
// resources/list
// ---------------------------------------------------------------------------

func (h *mcpHandler) handleResourcesList(session *mcpSession, msg jsonRPCRequest) jsonRPCResponse {
	repos := h.store.ListRepositories()
	resources := make([]mcpResourceDefinition, 0)

	for _, repo := range repos {
		if !h.isRepoAllowed(session, repo.ID) {
			continue
		}
		resources = append(resources, mcpResourceDefinition{
			URI:         fmt.Sprintf("repository://%s/files", repo.ID),
			Name:        fmt.Sprintf("%s — Files", repo.Name),
			Description: "List of indexed files in the repository",
			MimeType:    "application/json",
		})
		resources = append(resources, mcpResourceDefinition{
			URI:         fmt.Sprintf("repository://%s/symbols", repo.ID),
			Name:        fmt.Sprintf("%s — Symbols", repo.Name),
			Description: "List of indexed code symbols in the repository",
			MimeType:    "application/json",
		})
	}

	return successResponse(msg.ID, map[string]interface{}{"resources": resources})
}

// ---------------------------------------------------------------------------
// resources/read
// ---------------------------------------------------------------------------

func (h *mcpHandler) handleResourcesRead(session *mcpSession, msg jsonRPCRequest) jsonRPCResponse {
	var params struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return errorResponse(msg.ID, -32602, "Invalid params: "+err.Error())
	}

	start := time.Now()

	// Parse URI: repository://{repoID}/{type}
	if !strings.HasPrefix(params.URI, "repository://") {
		return errorResponse(msg.ID, -32602, "Invalid resource URI: must start with repository://")
	}
	rest := strings.TrimPrefix(params.URI, "repository://")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		return errorResponse(msg.ID, -32602, "Invalid resource URI format: expected repository://{id}/{type}")
	}
	repoID := parts[0]
	resourceType := parts[1]

	if err := h.checkRepoAccess(session, repoID); err != nil {
		return errorResponse(msg.ID, -32602, err.Error())
	}

	var content interface{}
	var readErr error

	switch resourceType {
	case "files":
		content, readErr = h.readFilesResource(repoID)
	case "symbols":
		content, readErr = h.readSymbolsResource(repoID)
	default:
		return errorResponse(msg.ID, -32602, fmt.Sprintf("Unknown resource type: %s", resourceType))
	}

	elapsed := time.Since(start)
	slog.Info("mcp resource read", "session_id", session.id, "resource_uri", params.URI, "duration_ms", elapsed.Milliseconds())

	if h.auditLogger != nil {
		h.auditLogger.LogResourceRead(session.claims.OrgID, session.claims.UserID, params.URI, elapsed.Milliseconds(), readErr)
	}

	if readErr != nil {
		return errorResponse(msg.ID, -32603, readErr.Error())
	}

	contentJSON, _ := json.Marshal(content)
	return successResponse(msg.ID, map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"uri":      params.URI,
				"mimeType": "application/json",
				"text":     string(contentJSON),
			},
		},
	})
}

func (h *mcpHandler) readFilesResource(repoID string) (interface{}, error) {
	files := h.store.GetFiles(repoID)
	if files == nil {
		return nil, fmt.Errorf("repository not found or not indexed")
	}

	type fileEntry struct {
		Path     string `json:"path"`
		Language string `json:"language,omitempty"`
	}

	result := make([]fileEntry, 0, len(files))
	for _, f := range files {
		result = append(result, fileEntry{
			Path:     f.Path,
			Language: f.Language,
		})
	}
	return result, nil
}

func (h *mcpHandler) readSymbolsResource(repoID string) (interface{}, error) {
	symbols, _ := h.store.GetSymbols(repoID, nil, nil, 1000, 0)
	if symbols == nil {
		return nil, fmt.Errorf("repository not found or not indexed")
	}

	type symbolEntry struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		FilePath string `json:"file_path"`
		Line     int    `json:"line"`
	}

	result := make([]symbolEntry, 0, len(symbols))
	for _, s := range symbols {
		result = append(result, symbolEntry{
			ID:       s.ID,
			Name:     s.Name,
			Kind:     s.Kind,
			FilePath: s.FilePath,
			Line:     s.StartLine,
		})
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Repo access helpers
// ---------------------------------------------------------------------------

func (h *mcpHandler) checkRepoAccess(session *mcpSession, repoID string) error {
	if repoID == "" {
		return fmt.Errorf("repository_id is required")
	}
	repo := h.store.GetRepository(repoID)
	if repo == nil {
		return fmt.Errorf("Repository not found or not accessible")
	}
	if !h.isRepoAllowed(session, repoID) {
		return fmt.Errorf("Repository not found or not accessible")
	}
	// Enterprise permission check
	if h.permChecker != nil {
		if !h.permChecker.CanAccessRepo(session.claims.OrgID, session.claims.UserID, repoID) {
			return fmt.Errorf("Repository not found or not accessible")
		}
	}
	return nil
}

func (h *mcpHandler) isRepoAllowed(_ *mcpSession, repoID string) bool {
	if h.allowedRepos == nil {
		return true // all repos allowed
	}
	return h.allowedRepos[repoID]
}

// ---------------------------------------------------------------------------
// Streamable HTTP endpoint: POST /api/v1/mcp/http
// ---------------------------------------------------------------------------
// Implements the MCP Streamable HTTP transport. Unlike the SSE transport,
// clients POST JSON-RPC messages to a single endpoint and receive JSON
// responses directly. Session tracking uses the Mcp-Session-Id header.

func (h *mcpHandler) handleStreamableHTTP(w http.ResponseWriter, r *http.Request) {
	claims := auth.GetClaims(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, mcpMaxBodySize+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
		return
	}
	if len(body) > mcpMaxBodySize {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body too large"})
		return
	}

	var msg jsonRPCRequest
	if err := json.Unmarshal(body, &msg); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(errorResponse(nil, -32700, "Parse error: "+err.Error()))
		return
	}

	if msg.JSONRPC != "2.0" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(errorResponse(msg.ID, -32600, "Invalid request: jsonrpc must be '2.0'"))
		return
	}

	// For initialize: create a new session
	if msg.Method == "initialize" {
		if h.maxSessions > 0 && h.sessionCount() >= h.maxSessions {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many MCP sessions"})
			return
		}
		sess := &mcpSession{
			id:        uuid.New().String(),
			claims:    claims,
			createdAt: time.Now(),
			lastUsed:  time.Now(),
			eventCh:   make(chan []byte, 64),
			done:      make(chan struct{}),
		}
		h.sessions.Store(sess.id, sess)
		slog.Info("mcp streamable session created", "session_id", sess.id, "user_id", claims.UserID)

		resp := h.safeDispatch(sess, msg)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", sess.id)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
		return
	}

	// Notifications (no ID) — acknowledge and done
	if msg.ID == nil || string(msg.ID) == "" || string(msg.ID) == "null" {
		// Look up session to update lastUsed if present
		if sid := r.Header.Get("Mcp-Session-Id"); sid != "" {
			if val, ok := h.sessions.Load(sid); ok {
				val.(*mcpSession).lastUsed = time.Now()
			}
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// All other methods: require session
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(errorResponse(msg.ID, -32600, "Missing Mcp-Session-Id header. Send 'initialize' first."))
		return
	}
	val, ok := h.sessions.Load(sessionID)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(errorResponse(msg.ID, -32600, "Invalid or expired session. Re-initialize."))
		return
	}
	sess := val.(*mcpSession)
	sess.lastUsed = time.Now()

	resp := h.safeDispatch(sess, msg)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Mcp-Session-Id", sess.id)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// handleStreamableHTTPDelete handles DELETE requests to terminate sessions.
func (h *mcpHandler) handleStreamableHTTPDelete(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if val, ok := h.sessions.LoadAndDelete(sessionID); ok {
		sess := val.(*mcpSession)
		close(sess.done)
		slog.Info("mcp streamable session terminated", "session_id", sess.id)
	}
	w.WriteHeader(http.StatusOK)
}

// ---------------------------------------------------------------------------
// JSON-RPC helpers
// ---------------------------------------------------------------------------

func successResponse(id json.RawMessage, result interface{}) jsonRPCResponse {
	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
}

func errorResponse(id json.RawMessage, code int, message string) jsonRPCResponse {
	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &jsonRPCError{Code: code, Message: message},
	}
}
