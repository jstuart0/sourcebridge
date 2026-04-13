// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/auth"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"

	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
)

// ---------------------------------------------------------------------------
// Mock worker
// ---------------------------------------------------------------------------

type mockWorkerCaller struct {
	available  bool
	answerFunc func(ctx context.Context, req *reasoningv1.AnswerQuestionRequest) (*reasoningv1.AnswerQuestionResponse, error)
}

func (m *mockWorkerCaller) IsAvailable() bool { return m.available }

func (m *mockWorkerCaller) AnswerQuestion(ctx context.Context, req *reasoningv1.AnswerQuestionRequest) (*reasoningv1.AnswerQuestionResponse, error) {
	if m.answerFunc != nil {
		return m.answerFunc(ctx, req)
	}
	return &reasoningv1.AnswerQuestionResponse{
		Answer: "Mock explanation of the code.",
	}, nil
}

// ---------------------------------------------------------------------------
// Mock knowledge store (minimal, enough for cliff notes tests)
// ---------------------------------------------------------------------------

type mockKnowledgeStore struct {
	artifacts map[string]*knowledge.Artifact
}

func newMockKnowledgeStore() *mockKnowledgeStore {
	return &mockKnowledgeStore{artifacts: make(map[string]*knowledge.Artifact)}
}

func (m *mockKnowledgeStore) StoreKnowledgeArtifact(a *knowledge.Artifact) (*knowledge.Artifact, error) {
	m.artifacts[a.ID] = a
	return a, nil
}
func (m *mockKnowledgeStore) ClaimArtifact(key knowledge.ArtifactKey, rev knowledge.SourceRevision) (*knowledge.Artifact, bool, error) {
	return nil, false, nil
}
func (m *mockKnowledgeStore) ClaimArtifactWithMode(key knowledge.ArtifactKey, rev knowledge.SourceRevision, mode knowledge.GenerationMode) (*knowledge.Artifact, bool, error) {
	return nil, false, nil
}
func (m *mockKnowledgeStore) GetKnowledgeArtifact(id string) *knowledge.Artifact {
	return m.artifacts[id]
}
func (m *mockKnowledgeStore) GetArtifactByKey(key knowledge.ArtifactKey) *knowledge.Artifact {
	return m.GetArtifactByKeyAndMode(key, "")
}
func (m *mockKnowledgeStore) GetArtifactByKeyAndMode(key knowledge.ArtifactKey, mode knowledge.GenerationMode) *knowledge.Artifact {
	key = key.Normalized()
	for _, a := range m.artifacts {
		aKey := knowledge.ArtifactKey{
			RepositoryID: a.RepositoryID,
			Type:         a.Type,
			Audience:     a.Audience,
			Depth:        a.Depth,
			Scope:        *a.Scope,
		}.Normalized()
		if aKey.RepositoryID == key.RepositoryID &&
			aKey.Type == key.Type &&
			aKey.Audience == key.Audience &&
			aKey.Depth == key.Depth &&
			aKey.ScopeKey() == key.ScopeKey() &&
			(mode == "" || knowledge.NormalizeGenerationMode(a.GenerationMode) == knowledge.NormalizeGenerationMode(mode)) {
			return a
		}
	}
	return nil
}
func (m *mockKnowledgeStore) GetKnowledgeArtifacts(repoID string) []*knowledge.Artifact {
	return nil
}
func (m *mockKnowledgeStore) UpdateKnowledgeArtifactStatus(id string, status knowledge.ArtifactStatus) error {
	if a, ok := m.artifacts[id]; ok {
		a.Status = status
	}
	return nil
}
func (m *mockKnowledgeStore) SetArtifactFailed(id string, code string, message string) error {
	if a, ok := m.artifacts[id]; ok {
		a.Status = knowledge.StatusFailed
		a.ErrorCode = code
		a.ErrorMessage = message
	}
	return nil
}
func (m *mockKnowledgeStore) UpdateKnowledgeArtifactProgress(id string, progress float64) error {
	return nil
}
func (m *mockKnowledgeStore) UpdateKnowledgeArtifactProgressWithPhase(id string, progress float64, phase, message string) error {
	return nil
}
func (m *mockKnowledgeStore) MarkKnowledgeArtifactStale(id string, stale bool) error {
	return nil
}
func (m *mockKnowledgeStore) DeleteKnowledgeArtifact(id string) error { return nil }
func (m *mockKnowledgeStore) SupersedeArtifact(id string, sections []knowledge.Section) error {
	return nil
}
func (m *mockKnowledgeStore) StoreKnowledgeSections(artifactID string, sections []knowledge.Section) error {
	return nil
}
func (m *mockKnowledgeStore) GetKnowledgeSections(artifactID string) []knowledge.Section {
	return nil
}
func (m *mockKnowledgeStore) StoreKnowledgeEvidence(sectionID string, evidence []knowledge.Evidence) error {
	return nil
}
func (m *mockKnowledgeStore) GetKnowledgeEvidence(sectionID string) []knowledge.Evidence {
	return nil
}
func (m *mockKnowledgeStore) StoreRepositoryUnderstanding(u *knowledge.RepositoryUnderstanding) (*knowledge.RepositoryUnderstanding, error) {
	return u, nil
}
func (m *mockKnowledgeStore) GetRepositoryUnderstanding(repoID string, scope knowledge.ArtifactScope) *knowledge.RepositoryUnderstanding {
	return nil
}
func (m *mockKnowledgeStore) GetRepositoryUnderstandings(repoID string) []*knowledge.RepositoryUnderstanding {
	return nil
}
func (m *mockKnowledgeStore) MarkRepositoryUnderstandingNeedsRefresh(repoID string) error {
	return nil
}
func (m *mockKnowledgeStore) AttachArtifactUnderstanding(artifactID, understandingID, revisionFP string) error {
	return nil
}
func (m *mockKnowledgeStore) StoreArtifactDependencies(artifactID string, dependencies []knowledge.ArtifactDependency) error {
	return nil
}
func (m *mockKnowledgeStore) GetArtifactDependencies(artifactID string) []knowledge.ArtifactDependency {
	return nil
}

// ---------------------------------------------------------------------------
// Test harness
// ---------------------------------------------------------------------------

type mcpTestHarness struct {
	handler *mcpHandler
	store   *graphstore.Store
	worker  *mockWorkerCaller
	ks      *mockKnowledgeStore
	repoID  string
}

func newTestHarness(t *testing.T) *mcpTestHarness {
	t.Helper()
	store := graphstore.NewStore()
	worker := &mockWorkerCaller{available: true}
	ks := newMockKnowledgeStore()

	// Index a test repository with known data
	result := &indexer.IndexResult{
		RepoName: "test-repo",
		RepoPath: "/tmp/test-repo",
		Files: []indexer.FileResult{
			{
				Path:      "main.go",
				Language:  "go",
				LineCount: 50,
				Symbols: []indexer.Symbol{
					{ID: "sym-1", Name: "HandleRequest", QualifiedName: "main.HandleRequest", Kind: "function", Language: "go", FilePath: "main.go", StartLine: 10, EndLine: 30, Signature: "func HandleRequest(w http.ResponseWriter, r *http.Request)"},
					{ID: "sym-2", Name: "Config", QualifiedName: "main.Config", Kind: "type", Language: "go", FilePath: "main.go", StartLine: 1, EndLine: 8, Signature: "type Config struct"},
				},
			},
			{
				Path:      "utils.go",
				Language:  "go",
				LineCount: 20,
				Symbols: []indexer.Symbol{
					{ID: "sym-3", Name: "ParseJSON", QualifiedName: "main.ParseJSON", Kind: "function", Language: "go", FilePath: "utils.go", StartLine: 5, EndLine: 15, Signature: "func ParseJSON(data []byte) (interface{}, error)"},
				},
			},
		},
	}
	repo, err := store.StoreIndexResult(result)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}

	// All IDs are auto-generated UUIDs, so look them up.
	symbols, _ := store.GetSymbols(repo.ID, nil, nil, 10, 0)
	if len(symbols) == 0 {
		t.Fatal("no symbols after indexing")
	}
	firstSymbolID := symbols[0].ID

	store.StoreRequirement(repo.ID, &graphstore.StoredRequirement{
		ExternalID:  "PROJ-101",
		Title:       "Handle HTTP requests",
		Description: "The system must handle HTTP requests with proper error handling.",
		Priority:    "high",
		Tags:        []string{"api", "http"},
	})
	reqs, _ := store.GetRequirements(repo.ID, 1, 0)
	if len(reqs) == 0 {
		t.Fatal("failed to store test requirement")
	}

	store.StoreLink(repo.ID, &graphstore.StoredLink{
		RequirementID: reqs[0].ID,
		SymbolID:      firstSymbolID,
		Confidence:    0.85,
		Source:        "semantic",
	})

	h := newMCPHandler(store, ks, worker, "", 1*time.Hour, 30*time.Second, 100)

	return &mcpTestHarness{
		handler: h,
		store:   store,
		worker:  worker,
		ks:      ks,
		repoID:  repo.ID,
	}
}

// createSession creates a test session and marks it initialized.
func (h *mcpTestHarness) createSession() *mcpSession {
	sess := &mcpSession{
		id:          "test-session-1",
		claims:      &auth.Claims{UserID: "user-1", OrgID: "org-1"},
		initialized: true,
		createdAt:   time.Now(),
		lastUsed:    time.Now(),
		eventCh:     make(chan []byte, 64),
		done:        make(chan struct{}),
	}
	h.handler.sessions.Store(sess.id, sess)
	return sess
}

// sendRPC sends a JSON-RPC request through dispatch and returns the response.
func (h *mcpTestHarness) sendRPC(session *mcpSession, id int, method string, params interface{}) jsonRPCResponse {
	var paramsRaw json.RawMessage
	if params != nil {
		b, _ := json.Marshal(params)
		paramsRaw = b
	}
	idRaw, _ := json.Marshal(id)
	msg := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      idRaw,
		Method:  method,
		Params:  paramsRaw,
	}
	return h.handler.safeDispatch(session, msg)
}

// parseToolText extracts the text from a tool result response.
func parseToolText(resp jsonRPCResponse) (string, bool) {
	b, _ := json.Marshal(resp.Result)
	var tr mcpToolResult
	if err := json.Unmarshal(b, &tr); err != nil {
		return "", false
	}
	if len(tr.Content) == 0 {
		return "", false
	}
	return tr.Content[0].Text, tr.IsError
}

// ---------------------------------------------------------------------------
// Test 1: SSE Connection
// ---------------------------------------------------------------------------

func TestMCP_SSEConnection(t *testing.T) {
	h := newTestHarness(t)

	// Create a fake authenticated request
	req := httptest.NewRequest("GET", "/api/v1/mcp/sse", nil)
	ctx := context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{UserID: "user-1"})
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()

	// Run handleSSE in a goroutine since it blocks
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.handler.handleSSE(rr, req)
	}()

	// Give it a moment to start writing
	time.Sleep(50 * time.Millisecond)

	// The response should have started with text/event-stream
	if ct := rr.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %q", ct)
	}

	// Check that the endpoint event was sent
	body := rr.Body.String()
	if !strings.Contains(body, "event: endpoint") {
		t.Error("expected 'event: endpoint' in SSE stream")
	}
	if !strings.Contains(body, "/api/v1/mcp/message?sessionId=") {
		t.Error("expected endpoint URL with sessionId in SSE stream")
	}
}

// ---------------------------------------------------------------------------
// Test 2: SSE Requires Auth
// ---------------------------------------------------------------------------

func TestMCP_SSERequiresAuth(t *testing.T) {
	h := newTestHarness(t)

	// Request without claims
	req := httptest.NewRequest("GET", "/api/v1/mcp/sse", nil)
	rr := httptest.NewRecorder()

	// Run handleSSE — it should reject immediately
	h.handler.handleSSE(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Test 3: Initialize Handshake
// ---------------------------------------------------------------------------

func TestMCP_InitializeHandshake(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()
	sess.initialized = false // not yet initialized

	resp := h.sendRPC(sess, 1, "initialize", map[string]interface{}{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]interface{}{"name": "test-client", "version": "1.0"},
	})

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	if !sess.initialized {
		t.Error("session should be initialized after handshake")
	}

	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatal("expected map result")
	}
	if result["protocolVersion"] != mcpProtocolVersion {
		t.Errorf("expected protocol version %s, got %v", mcpProtocolVersion, result["protocolVersion"])
	}
	serverInfo, ok := result["serverInfo"].(map[string]interface{})
	if !ok {
		t.Fatal("expected serverInfo in result")
	}
	if serverInfo["name"] != mcpServerName {
		t.Errorf("expected server name %s, got %v", mcpServerName, serverInfo["name"])
	}
}

// ---------------------------------------------------------------------------
// Test 4: Initialize with different protocol version (should succeed with negotiation)
// ---------------------------------------------------------------------------

func TestMCP_InitializeDifferentVersion(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()
	sess.initialized = false

	// Server should accept any version and respond with its own — clients decide compatibility
	resp := h.sendRPC(sess, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2025-06-18", // Codex uses this version
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]interface{}{"name": "codex", "version": "0.116.0"},
	})

	if resp.Error != nil {
		t.Fatalf("expected successful initialize with different version, got error: %s", resp.Error.Message)
	}
	// Server should respond with its own version
	result, _ := resp.Result.(map[string]interface{})
	if result["protocolVersion"] != mcpProtocolVersion {
		t.Errorf("server should respond with its own version %s, got %v", mcpProtocolVersion, result["protocolVersion"])
	}
	if !sess.initialized {
		t.Error("session should be initialized")
	}
}

// ---------------------------------------------------------------------------
// Test 5: Tools List
// ---------------------------------------------------------------------------

func TestMCP_ToolsList(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 2, "tools/list", nil)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatal("expected map result")
	}

	tools, ok := result["tools"].([]mcpToolDefinition)
	if !ok {
		t.Fatal("expected tools array in result")
	}
	if len(tools) != 5 {
		t.Fatalf("expected 5 tools, got %d", len(tools))
	}

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}
	expected := []string{"search_symbols", "explain_code", "get_requirements", "get_impact_report", "get_cliff_notes"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("missing tool: %s", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Test 6: Resources List
// ---------------------------------------------------------------------------

func TestMCP_ResourcesList(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 2, "resources/list", nil)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatal("expected map result")
	}

	resources, ok := result["resources"].([]mcpResourceDefinition)
	if !ok {
		t.Fatal("expected resources array in result")
	}

	// Should have 2 resources per repo (files + symbols)
	if len(resources) < 2 {
		t.Fatalf("expected at least 2 resources, got %d", len(resources))
	}

	hasFiles := false
	hasSymbols := false
	for _, r := range resources {
		if strings.Contains(r.URI, "/files") {
			hasFiles = true
		}
		if strings.Contains(r.URI, "/symbols") {
			hasSymbols = true
		}
	}
	if !hasFiles {
		t.Error("missing files resource")
	}
	if !hasSymbols {
		t.Error("missing symbols resource")
	}
}

// ---------------------------------------------------------------------------
// Test 7: SearchSymbols Basic
// ---------------------------------------------------------------------------

func TestMCP_SearchSymbols_Basic(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name":      "search_symbols",
		"arguments": map[string]interface{}{"repository_id": h.repoID, "query": "Handle"},
	})

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("tool returned error: %s", text)
	}

	var result struct {
		Symbols    []json.RawMessage `json:"symbols"`
		TotalCount int               `json:"total_count"`
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(result.Symbols) == 0 {
		t.Error("expected at least one symbol matching 'Handle'")
	}
}

// ---------------------------------------------------------------------------
// Test 8: SearchSymbols By Kind
// ---------------------------------------------------------------------------

func TestMCP_SearchSymbols_ByKind(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name":      "search_symbols",
		"arguments": map[string]interface{}{"repository_id": h.repoID, "query": "", "kind": "type"},
	})

	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("tool returned error: %s", text)
	}

	var result struct {
		Symbols []struct {
			Kind string `json:"kind"`
		} `json:"symbols"`
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	for _, s := range result.Symbols {
		if s.Kind != "type" {
			t.Errorf("expected kind 'type', got %q", s.Kind)
		}
	}
}

// ---------------------------------------------------------------------------
// Test 9: SearchSymbols By File
// ---------------------------------------------------------------------------

func TestMCP_SearchSymbols_ByFile(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name":      "search_symbols",
		"arguments": map[string]interface{}{"repository_id": h.repoID, "query": "", "file_path": "utils.go"},
	})

	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("tool returned error: %s", text)
	}

	var result struct {
		Symbols []struct {
			FilePath string `json:"file_path"`
		} `json:"symbols"`
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if len(result.Symbols) == 0 {
		t.Error("expected symbols from utils.go")
	}
	for _, s := range result.Symbols {
		if s.FilePath != "utils.go" {
			t.Errorf("expected file_path 'utils.go', got %q", s.FilePath)
		}
	}
}

// ---------------------------------------------------------------------------
// Test 10: SearchSymbols Repo Not Found
// ---------------------------------------------------------------------------

func TestMCP_SearchSymbols_RepoNotFound(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name":      "search_symbols",
		"arguments": map[string]interface{}{"repository_id": "nonexistent", "query": "foo"},
	})

	_, isErr := parseToolText(resp)
	if !isErr {
		t.Error("expected isError for nonexistent repo")
	}
}

// ---------------------------------------------------------------------------
// Test 11: ExplainCode With Code
// ---------------------------------------------------------------------------

func TestMCP_ExplainCode_WithCode(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name": "explain_code",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"code":          "func hello() { fmt.Println(\"hello\") }",
		},
	})

	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("tool returned error: %s", text)
	}

	var result struct {
		Explanation string `json:"explanation"`
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if result.Explanation == "" {
		t.Error("expected non-empty explanation")
	}
}

// ---------------------------------------------------------------------------
// Test 12: ExplainCode With FilePath
// ---------------------------------------------------------------------------

func TestMCP_ExplainCode_WithFilePath(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name": "explain_code",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"file_path":     "main.go",
		},
	})

	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("tool returned error: %s", text)
	}

	var result struct {
		Explanation string `json:"explanation"`
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if result.Explanation == "" {
		t.Error("expected non-empty explanation")
	}
}

// ---------------------------------------------------------------------------
// Test 13: ExplainCode Worker Down
// ---------------------------------------------------------------------------

func TestMCP_ExplainCode_WorkerDown(t *testing.T) {
	h := newTestHarness(t)
	h.worker.available = false
	sess := h.createSession()

	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name": "explain_code",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"code":          "func foo() {}",
		},
	})

	text, isErr := parseToolText(resp)
	if !isErr {
		t.Error("expected isError when worker is down")
	}
	if !strings.Contains(text, "worker") {
		t.Errorf("expected worker-related error message, got %q", text)
	}
}

// ---------------------------------------------------------------------------
// Test 14: GetRequirements Basic
// ---------------------------------------------------------------------------

func TestMCP_GetRequirements_Basic(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name": "get_requirements",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"include_links": true,
		},
	})

	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("tool returned error: %s", text)
	}

	var result struct {
		Requirements []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			Links []struct {
				SymbolID   string `json:"symbol_id"`
				SymbolName string `json:"symbol_name"`
			} `json:"links"`
		} `json:"requirements"`
		TotalCount int `json:"total_count"`
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if result.TotalCount == 0 {
		t.Error("expected at least one requirement")
	}
	if len(result.Requirements[0].Links) == 0 {
		t.Error("expected links for requirement")
	}
	if result.Requirements[0].Links[0].SymbolName == "" {
		t.Error("expected resolved symbol name in link")
	}
}

// ---------------------------------------------------------------------------
// Test 15: GetRequirements No Links
// ---------------------------------------------------------------------------

func TestMCP_GetRequirements_NoLinks(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name": "get_requirements",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"include_links": false,
		},
	})

	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("tool returned error: %s", text)
	}

	var result struct {
		Requirements []struct {
			Links []json.RawMessage `json:"links"`
		} `json:"requirements"`
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	// When include_links is false, links should be null or absent (not populated)
	if len(result.Requirements) > 0 && len(result.Requirements[0].Links) > 0 {
		t.Error("expected empty/null links when include_links is false")
	}
}

// ---------------------------------------------------------------------------
// Test 16: GetRequirements Pagination
// ---------------------------------------------------------------------------

func TestMCP_GetRequirements_Pagination(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	// Add more requirements
	for i := 2; i <= 15; i++ {
		h.store.StoreRequirement(h.repoID, &graphstore.StoredRequirement{
			ID:     fmt.Sprintf("req-%d", i),
			RepoID: h.repoID,
			Title:  fmt.Sprintf("Requirement %d", i),
		})
	}

	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name": "get_requirements",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"limit":         5,
			"offset":        0,
		},
	})

	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("tool returned error: %s", text)
	}

	var result struct {
		Requirements []json.RawMessage `json:"requirements"`
		TotalCount   int               `json:"total_count"`
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if len(result.Requirements) > 5 {
		t.Errorf("expected at most 5 requirements, got %d", len(result.Requirements))
	}
	if result.TotalCount < 15 {
		t.Errorf("expected total_count >= 15, got %d", result.TotalCount)
	}
}

// ---------------------------------------------------------------------------
// Test 17: GetImpactReport Exists
// ---------------------------------------------------------------------------

func TestMCP_GetImpactReport_Exists(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	// Store an impact report
	h.store.StoreImpactReport(h.repoID, &graphstore.ImpactReport{
		ID:           "impact-1",
		RepositoryID: h.repoID,
		OldCommitSHA: "abc123",
		NewCommitSHA: "def456",
		FilesChanged: []graphstore.ImpactFileDiff{{Path: "main.go", Status: "modified"}},
		ComputedAt:   time.Now(),
	})

	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name":      "get_impact_report",
		"arguments": map[string]interface{}{"repository_id": h.repoID},
	})

	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("tool returned error: %s", text)
	}

	var result struct {
		Report map[string]interface{} `json:"report"`
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if result.Report == nil {
		t.Error("expected non-nil report")
	}
}

// ---------------------------------------------------------------------------
// Test 18: GetImpactReport No Report
// ---------------------------------------------------------------------------

func TestMCP_GetImpactReport_NoReport(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	// Don't store any impact report — the harness repo has none by default.
	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name":      "get_impact_report",
		"arguments": map[string]interface{}{"repository_id": h.repoID},
	})

	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("tool returned error: %s", text)
	}

	var result struct {
		Report interface{} `json:"report"`
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if result.Report != nil {
		t.Error("expected null report")
	}
}

// ---------------------------------------------------------------------------
// Test 19: GetCliffNotes Ready
// ---------------------------------------------------------------------------

func TestMCP_GetCliffNotes_Ready(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	scope := knowledge.ArtifactScope{ScopeType: knowledge.ScopeRepository}.NormalizePtr()
	h.ks.artifacts["cn-1"] = &knowledge.Artifact{
		ID:           "cn-1",
		RepositoryID: h.repoID,
		Type:         knowledge.ArtifactCliffNotes,
		Audience:     knowledge.AudienceDeveloper,
		Depth:        knowledge.DepthMedium,
		Scope:        scope,
		Status:       knowledge.StatusReady,
		GeneratedAt:  time.Now(),
		Sections: []knowledge.Section{
			{Title: "Overview", Content: "This is the main module.", Confidence: knowledge.ConfidenceHigh},
		},
	}

	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name":      "get_cliff_notes",
		"arguments": map[string]interface{}{"repository_id": h.repoID},
	})

	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("tool returned error: %s", text)
	}

	var result struct {
		Artifact struct {
			Sections []struct {
				Title   string `json:"title"`
				Content string `json:"content"`
			} `json:"sections"`
		} `json:"artifact"`
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if len(result.Artifact.Sections) == 0 {
		t.Error("expected sections in cliff notes")
	}
	if result.Artifact.Sections[0].Title != "Overview" {
		t.Errorf("expected section title 'Overview', got %q", result.Artifact.Sections[0].Title)
	}
}

// ---------------------------------------------------------------------------
// Test 20: GetCliffNotes Not Generated
// ---------------------------------------------------------------------------

func TestMCP_GetCliffNotes_NotGenerated(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	// No artifacts stored — cliff notes don't exist
	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name":      "get_cliff_notes",
		"arguments": map[string]interface{}{"repository_id": h.repoID},
	})

	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("tool returned error: %s", text)
	}

	var result struct {
		Artifact interface{} `json:"artifact"`
		Message  string      `json:"message"`
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if result.Artifact != nil {
		t.Error("expected null artifact")
	}
	if result.Message == "" {
		t.Error("expected non-empty message")
	}
}

// ---------------------------------------------------------------------------
// Test 21: GetCliffNotes Generating
// ---------------------------------------------------------------------------

func TestMCP_GetCliffNotes_Generating(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	scope := knowledge.ArtifactScope{ScopeType: knowledge.ScopeRepository}.NormalizePtr()
	h.ks.artifacts["cn-gen"] = &knowledge.Artifact{
		ID:           "cn-gen",
		RepositoryID: h.repoID,
		Type:         knowledge.ArtifactCliffNotes,
		Audience:     knowledge.AudienceDeveloper,
		Depth:        knowledge.DepthMedium,
		Scope:        scope,
		Status:       knowledge.StatusGenerating,
	}

	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name":      "get_cliff_notes",
		"arguments": map[string]interface{}{"repository_id": h.repoID},
	})

	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("tool returned error: %s", text)
	}

	var result struct {
		Artifact interface{} `json:"artifact"`
		Message  string      `json:"message"`
	}
	json.Unmarshal([]byte(text), &result)
	if result.Artifact != nil {
		t.Error("expected null artifact for generating state")
	}
	if !strings.Contains(result.Message, "currently being generated") {
		t.Errorf("expected 'currently being generated' in message, got %q", result.Message)
	}
}

// ---------------------------------------------------------------------------
// Test 22: ResourceRead Files
// ---------------------------------------------------------------------------

func TestMCP_ResourceRead_Files(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 3, "resources/read", map[string]interface{}{
		"uri": fmt.Sprintf("repository://%s/files", h.repoID),
	})

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	result, _ := resp.Result.(map[string]interface{})
	contents, _ := result["contents"].([]map[string]interface{})
	if len(contents) == 0 {
		t.Fatal("expected non-empty contents")
	}

	text, _ := contents[0]["text"].(string)
	var files []struct {
		Path string `json:"path"`
	}
	json.Unmarshal([]byte(text), &files)
	if len(files) < 2 {
		t.Errorf("expected at least 2 files, got %d", len(files))
	}
}

// ---------------------------------------------------------------------------
// Test 23: ResourceRead Symbols
// ---------------------------------------------------------------------------

func TestMCP_ResourceRead_Symbols(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 3, "resources/read", map[string]interface{}{
		"uri": fmt.Sprintf("repository://%s/symbols", h.repoID),
	})

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	result, _ := resp.Result.(map[string]interface{})
	contents, _ := result["contents"].([]map[string]interface{})
	if len(contents) == 0 {
		t.Fatal("expected non-empty contents")
	}

	text, _ := contents[0]["text"].(string)
	var symbols []struct {
		Name string `json:"name"`
	}
	json.Unmarshal([]byte(text), &symbols)
	if len(symbols) < 3 {
		t.Errorf("expected at least 3 symbols, got %d", len(symbols))
	}
}

// ---------------------------------------------------------------------------
// Test 24: ResourceRead Invalid URI
// ---------------------------------------------------------------------------

func TestMCP_ResourceRead_InvalidURI(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 3, "resources/read", map[string]interface{}{
		"uri": "invalid://foo",
	})

	if resp.Error == nil {
		t.Error("expected error for invalid URI")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("expected error code -32602, got %d", resp.Error.Code)
	}
}

// ---------------------------------------------------------------------------
// Test 25: Repo Access Denied
// ---------------------------------------------------------------------------

func TestMCP_RepoAccessDenied(t *testing.T) {
	store := graphstore.NewStore()
	worker := &mockWorkerCaller{available: true}
	ks := newMockKnowledgeStore()

	// Create a repo
	result := &indexer.IndexResult{
		RepoName: "allowed-repo",
		RepoPath: "/tmp/allowed",
		Files:    []indexer.FileResult{{Path: "a.go", Language: "go", LineCount: 10}},
	}
	repo, _ := store.StoreIndexResult(result)

	// Create handler that only allows a different repo
	h := newMCPHandler(store, ks, worker, "other-repo-id", 1*time.Hour, 30*time.Second, 100)

	sess := &mcpSession{
		id:          "sess-restricted",
		claims:      &auth.Claims{UserID: "user-1"},
		initialized: true,
		createdAt:   time.Now(),
		lastUsed:    time.Now(),
		eventCh:     make(chan []byte, 64),
		done:        make(chan struct{}),
	}
	h.sessions.Store(sess.id, sess)

	idRaw, _ := json.Marshal(3)
	argsRaw, _ := json.Marshal(map[string]interface{}{
		"name":      "search_symbols",
		"arguments": map[string]interface{}{"repository_id": repo.ID, "query": "foo"},
	})
	resp := h.safeDispatch(sess, jsonRPCRequest{JSONRPC: "2.0", ID: idRaw, Method: "tools/call", Params: argsRaw})

	_, isErr := parseToolText(resp)
	if !isErr {
		t.Error("expected isError for repo access denied")
	}
}

// ---------------------------------------------------------------------------
// Test 26: Invalid Session ID
// ---------------------------------------------------------------------------

func TestMCP_InvalidSessionID(t *testing.T) {
	h := newTestHarness(t)

	req := httptest.NewRequest("POST", "/api/v1/mcp/message?sessionId=bad-session", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.handler.handleMessage(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Test 27: Method Not Found
// ---------------------------------------------------------------------------

func TestMCP_MethodNotFound(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 5, "tools/unknown_method", nil)

	if resp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("expected error code -32601, got %d", resp.Error.Code)
	}
}

// ---------------------------------------------------------------------------
// Test 28: Ping
// ---------------------------------------------------------------------------

func TestMCP_Ping(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 5, "ping", nil)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	// Result should be an empty struct
	if resp.Result == nil {
		t.Error("expected non-nil result for ping")
	}
}

// ---------------------------------------------------------------------------
// Test 29: Notification Ignored
// ---------------------------------------------------------------------------

func TestMCP_NotificationIgnored(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	// POST a notification (no id field) to the message handler
	body := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	req := httptest.NewRequest("POST", "/api/v1/mcp/message?sessionId="+sess.id, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.handler.handleMessage(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d", rr.Code)
	}

	// No response should have been sent to the event channel
	select {
	case data := <-sess.eventCh:
		t.Errorf("expected no response for notification, got: %s", string(data))
	default:
		// Good — no response
	}
}

// ---------------------------------------------------------------------------
// Test 30: Session Cleanup
// ---------------------------------------------------------------------------

func TestMCP_SessionCleanup(t *testing.T) {
	h := newTestHarness(t)

	// Create a session via SSE, then cancel the request context
	req := httptest.NewRequest("GET", "/api/v1/mcp/sse", nil)
	ctx, cancel := context.WithCancel(req.Context())
	ctx = context.WithValue(ctx, auth.ClaimsKey, &auth.Claims{UserID: "user-1"})
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.handler.handleSSE(rr, req)
	}()

	// Wait for session to be created
	time.Sleep(50 * time.Millisecond)

	// Should have 1 session
	count := h.handler.sessionCount()
	if count != 1 {
		t.Errorf("expected 1 session, got %d", count)
	}

	// Cancel the context — simulates SSE disconnect
	cancel()
	<-done

	// Session should be cleaned up
	count = h.handler.sessionCount()
	if count != 0 {
		t.Errorf("expected 0 sessions after cleanup, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// Test 31: Pre-Init Method Rejected
// ---------------------------------------------------------------------------

func TestMCP_PreInitMethodRejected(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()
	sess.initialized = false // not yet initialized

	resp := h.sendRPC(sess, 2, "tools/list", nil)

	if resp.Error == nil {
		t.Fatal("expected error for pre-init tools/list")
	}
	if resp.Error.Code != -32600 {
		t.Errorf("expected error code -32600, got %d", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "not initialized") {
		t.Errorf("expected 'not initialized' in error message, got %q", resp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// Test 32: Max Sessions Enforced
// ---------------------------------------------------------------------------

func TestMCP_MaxSessionsEnforced(t *testing.T) {
	store := graphstore.NewStore()
	worker := &mockWorkerCaller{available: true}
	ks := newMockKnowledgeStore()
	h := newMCPHandler(store, ks, worker, "", 1*time.Hour, 30*time.Second, 2) // max 2 sessions

	// Create 2 sessions
	for i := 0; i < 2; i++ {
		sess := &mcpSession{
			id:        fmt.Sprintf("sess-%d", i),
			claims:    &auth.Claims{UserID: "user-1"},
			createdAt: time.Now(),
			lastUsed:  time.Now(),
			eventCh:   make(chan []byte, 64),
			done:      make(chan struct{}),
		}
		h.sessions.Store(sess.id, sess)
	}

	// Try to create a 3rd via SSE — should get 429
	req := httptest.NewRequest("GET", "/api/v1/mcp/sse", nil)
	ctx := context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{UserID: "user-2"})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	h.handleSSE(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Test: Full SSE + Message flow via HTTP
// ---------------------------------------------------------------------------

func TestMCP_FullHTTPFlow(t *testing.T) {
	h := newTestHarness(t)

	// Start SSE connection
	req := httptest.NewRequest("GET", "/api/v1/mcp/sse", nil)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	ctx = context.WithValue(ctx, auth.ClaimsKey, &auth.Claims{UserID: "user-1"})
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.handler.handleSSE(rr, req)
	}()

	time.Sleep(50 * time.Millisecond)

	// Parse the endpoint event to get sessionId
	body := rr.Body.String()
	scanner := bufio.NewScanner(strings.NewReader(body))
	var sessionID string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if strings.Contains(data, "sessionId=") {
				parts := strings.Split(data, "sessionId=")
				if len(parts) == 2 {
					sessionID = parts[1]
				}
			}
		}
	}
	if sessionID == "" {
		t.Fatal("could not extract sessionId from SSE stream")
	}

	// Send initialize via message endpoint
	initBody := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"%s","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`, mcpProtocolVersion)
	msgReq := httptest.NewRequest("POST", "/api/v1/mcp/message?sessionId="+sessionID, strings.NewReader(initBody))
	msgReq.Header.Set("Content-Type", "application/json")
	msgRR := httptest.NewRecorder()
	h.handler.handleMessage(msgRR, msgReq)

	if msgRR.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d", msgRR.Code)
	}

	// The response should be in the session's event channel — wait briefly
	time.Sleep(20 * time.Millisecond)

	// Verify session is now initialized
	val, ok := h.handler.sessions.Load(sessionID)
	if !ok {
		t.Fatal("session not found")
	}
	if !val.(*mcpSession).initialized {
		t.Error("session should be initialized after handshake")
	}

	cancel()
	<-done
}

// ---------------------------------------------------------------------------
// Streamable HTTP transport tests
// ---------------------------------------------------------------------------

func TestMCP_StreamableHTTP_FullHandshake(t *testing.T) {
	h := newTestHarness(t)

	// 1. Initialize — should create session and return Mcp-Session-Id
	initBody, _ := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params: json.RawMessage(`{
			"protocolVersion": "` + mcpProtocolVersion + `",
			"clientInfo": {"name": "codex-test", "version": "1.0"}
		}`),
	})

	req := httptest.NewRequest("POST", "/api/v1/mcp/http", strings.NewReader(string(initBody)))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{UserID: "user-1"})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.handler.handleStreamableHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("initialize: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	sessionID := rr.Header().Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatal("expected Mcp-Session-Id header in initialize response")
	}

	var initResp jsonRPCResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &initResp); err != nil {
		t.Fatalf("failed to parse init response: %v", err)
	}
	if initResp.Error != nil {
		t.Fatalf("initialize returned error: %s", initResp.Error.Message)
	}

	// 2. Send notifications/initialized (no ID — should get 202)
	notifBody, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	req2 := httptest.NewRequest("POST", "/api/v1/mcp/http", strings.NewReader(string(notifBody)))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Mcp-Session-Id", sessionID)
	ctx2 := context.WithValue(req2.Context(), auth.ClaimsKey, &auth.Claims{UserID: "user-1"})
	req2 = req2.WithContext(ctx2)
	rr2 := httptest.NewRecorder()
	h.handler.handleStreamableHTTP(rr2, req2)

	if rr2.Code != http.StatusAccepted {
		t.Fatalf("notification: expected 202, got %d", rr2.Code)
	}

	// 3. tools/list — should return tools
	listBody, _ := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`2`),
		Method:  "tools/list",
	})
	req3 := httptest.NewRequest("POST", "/api/v1/mcp/http", strings.NewReader(string(listBody)))
	req3.Header.Set("Content-Type", "application/json")
	req3.Header.Set("Mcp-Session-Id", sessionID)
	ctx3 := context.WithValue(req3.Context(), auth.ClaimsKey, &auth.Claims{UserID: "user-1"})
	req3 = req3.WithContext(ctx3)
	rr3 := httptest.NewRecorder()
	h.handler.handleStreamableHTTP(rr3, req3)

	if rr3.Code != http.StatusOK {
		t.Fatalf("tools/list: expected 200, got %d: %s", rr3.Code, rr3.Body.String())
	}
	var listResp jsonRPCResponse
	json.Unmarshal(rr3.Body.Bytes(), &listResp)
	if listResp.Error != nil {
		t.Fatalf("tools/list returned error: %s", listResp.Error.Message)
	}

	// Verify tools are present in the result
	resultJSON, _ := json.Marshal(listResp.Result)
	if !strings.Contains(string(resultJSON), "search_symbols") {
		t.Error("expected search_symbols in tools/list result")
	}

	// 4. tools/call — search_symbols
	callBody, _ := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`3`),
		Method:  "tools/call",
		Params: json.RawMessage(fmt.Sprintf(`{
			"name": "search_symbols",
			"arguments": {"repository_id": "%s", "query": "Handle"}
		}`, h.repoID)),
	})
	req4 := httptest.NewRequest("POST", "/api/v1/mcp/http", strings.NewReader(string(callBody)))
	req4.Header.Set("Content-Type", "application/json")
	req4.Header.Set("Mcp-Session-Id", sessionID)
	ctx4 := context.WithValue(req4.Context(), auth.ClaimsKey, &auth.Claims{UserID: "user-1"})
	req4 = req4.WithContext(ctx4)
	rr4 := httptest.NewRecorder()
	h.handler.handleStreamableHTTP(rr4, req4)

	if rr4.Code != http.StatusOK {
		t.Fatalf("tools/call: expected 200, got %d: %s", rr4.Code, rr4.Body.String())
	}
	var callResp jsonRPCResponse
	json.Unmarshal(rr4.Body.Bytes(), &callResp)
	if callResp.Error != nil {
		t.Fatalf("tools/call returned error: %s", callResp.Error.Message)
	}
}

func TestMCP_StreamableHTTP_RequiresAuth(t *testing.T) {
	h := newTestHarness(t)

	body, _ := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
	})
	req := httptest.NewRequest("POST", "/api/v1/mcp/http", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	h.handler.handleStreamableHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d", rr.Code)
	}
}

func TestMCP_StreamableHTTP_RequiresSession(t *testing.T) {
	h := newTestHarness(t)

	body, _ := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/list",
	})
	req := httptest.NewRequest("POST", "/api/v1/mcp/http", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{UserID: "user-1"})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.handler.handleStreamableHTTP(rr, req)

	// Should return JSON-RPC error about missing session
	var resp jsonRPCResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Error == nil {
		t.Error("expected error for missing session")
	}
	if !strings.Contains(resp.Error.Message, "Mcp-Session-Id") {
		t.Errorf("expected session error message, got: %s", resp.Error.Message)
	}
}

func TestMCP_StreamableHTTP_DeleteSession(t *testing.T) {
	h := newTestHarness(t)

	// Create a session via initialize
	initBody, _ := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params: json.RawMessage(`{
			"protocolVersion": "` + mcpProtocolVersion + `",
			"clientInfo": {"name": "test", "version": "1.0"}
		}`),
	})
	req := httptest.NewRequest("POST", "/api/v1/mcp/http", strings.NewReader(string(initBody)))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{UserID: "user-1"})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.handler.handleStreamableHTTP(rr, req)

	sessionID := rr.Header().Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatal("no session ID")
	}

	// Delete the session
	delReq := httptest.NewRequest("DELETE", "/api/v1/mcp/http", nil)
	delReq.Header.Set("Mcp-Session-Id", sessionID)
	delRR := httptest.NewRecorder()
	h.handler.handleStreamableHTTPDelete(delRR, delReq)

	if delRR.Code != http.StatusOK {
		t.Errorf("expected 200 for DELETE, got %d", delRR.Code)
	}

	// Session should be gone
	if _, ok := h.handler.sessions.Load(sessionID); ok {
		t.Error("session should be deleted")
	}
}
