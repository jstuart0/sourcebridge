// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

// seedCallGraphTestData stores additional indexer data in the test
// harness's store — call relations, imports, and a second file — so
// Phase 1a tests can exercise the call graph / imports walks against
// a known shape.
//
// Produces this topology:
//
//   main.go:
//     HandleRequest  → ParseJSON, Config
//     Config
//   utils.go:
//     ParseJSON      → (leaf)
//
//   imports:
//     main.go  → "./utils"  → resolves to utils.go (suffix match)
//     utils.go → "encoding/json" (external — no match in repo)
func seedCallGraphTestData(t *testing.T, h *mcpTestHarness) (handleID, parseID string) {
	t.Helper()

	// Overwrite the harness's index with a richer version. Start from
	// the original and add relations + imports. We populate
	// IndexResult.Relations directly (the in-real-indexing
	// resolveCallGraph step) so the store persists caller→callee
	// edges against the generated UUIDs.
	result := &indexer.IndexResult{
		RepoName: "test-repo",
		RepoPath: "/tmp/test-repo",
		Files: []indexer.FileResult{
			{
				Path:      "main.go",
				Language:  "go",
				LineCount: 50,
				Symbols: []indexer.Symbol{
					{ID: "tmp-handle", Name: "HandleRequest", QualifiedName: "main.HandleRequest", Kind: "function", Language: "go", FilePath: "main.go", StartLine: 10, EndLine: 30, Signature: "func HandleRequest(w, r)"},
					{ID: "tmp-config", Name: "Config", QualifiedName: "main.Config", Kind: "type", Language: "go", FilePath: "main.go", StartLine: 1, EndLine: 8},
				},
				Imports: []indexer.Import{
					{Path: "./utils", FilePath: "main.go", Line: 3},
				},
			},
			{
				Path:      "utils.go",
				Language:  "go",
				LineCount: 20,
				Symbols: []indexer.Symbol{
					{ID: "tmp-parse", Name: "ParseJSON", QualifiedName: "main.ParseJSON", Kind: "function", Language: "go", FilePath: "utils.go", StartLine: 5, EndLine: 15, Signature: "func ParseJSON(data []byte) (interface{}, error)"},
				},
				Imports: []indexer.Import{
					{Path: "encoding/json", FilePath: "utils.go", Line: 3},
				},
			},
		},
		Relations: []indexer.Relation{
			// HandleRequest → ParseJSON
			{SourceID: "tmp-handle", TargetID: "tmp-parse", Type: indexer.RelationCalls},
			// HandleRequest → Config
			{SourceID: "tmp-handle", TargetID: "tmp-config", Type: indexer.RelationCalls},
		},
	}
	repo, err := h.store.ReplaceIndexResult(h.repoID, result)
	if err != nil {
		t.Fatalf("ReplaceIndexResult: %v", err)
	}
	h.repoID = repo.ID

	// Look up generated symbol IDs.
	for _, s := range h.store.GetSymbolsByFile(h.repoID, "main.go") {
		if s.Name == "HandleRequest" {
			handleID = s.ID
		}
	}
	for _, s := range h.store.GetSymbolsByFile(h.repoID, "utils.go") {
		if s.Name == "ParseJSON" {
			parseID = s.ID
		}
	}
	if handleID == "" || parseID == "" {
		t.Fatalf("expected both HandleRequest and ParseJSON symbols to be stored")
	}
	return handleID, parseID
}

// ---------------------------------------------------------------------------
// get_callers / get_callees
// ---------------------------------------------------------------------------

func TestMCP_GetCallees_Direct(t *testing.T) {
	h := newTestHarness(t)
	seedCallGraphTestData(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name": "get_callees",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"file_path":     "main.go",
			"symbol_name":   "HandleRequest",
		},
	})
	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("tool returned error: %s", text)
	}

	var result callGraphResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if result.Root.SymbolName != "HandleRequest" {
		t.Errorf("expected root HandleRequest, got %q", result.Root.SymbolName)
	}
	// Should see at least ParseJSON at hop 1.
	foundParse := false
	for _, s := range result.Symbols {
		if s.SymbolName == "ParseJSON" {
			foundParse = true
			if s.HopsFromRoot != 1 {
				t.Errorf("ParseJSON should be at hop 1, got %d", s.HopsFromRoot)
			}
		}
	}
	if !foundParse {
		t.Error("expected ParseJSON in callees of HandleRequest")
	}
}

func TestMCP_GetCallers_Direct(t *testing.T) {
	h := newTestHarness(t)
	seedCallGraphTestData(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name": "get_callers",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"file_path":     "utils.go",
			"symbol_name":   "ParseJSON",
		},
	})
	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("tool returned error: %s", text)
	}

	var result callGraphResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	foundHandle := false
	for _, s := range result.Symbols {
		if s.SymbolName == "HandleRequest" {
			foundHandle = true
		}
	}
	if !foundHandle {
		t.Error("expected HandleRequest in callers of ParseJSON")
	}
}

func TestMCP_GetCallees_UnknownSymbol(t *testing.T) {
	h := newTestHarness(t)
	seedCallGraphTestData(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name": "get_callees",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"file_path":     "main.go",
			"symbol_name":   "DoesNotExist",
		},
	})
	_, isErr := parseToolText(resp)
	if !isErr {
		t.Error("expected error for unknown symbol")
	}
}

func TestMCP_GetCallees_MaxHopsCap(t *testing.T) {
	h := newTestHarness(t)
	seedCallGraphTestData(t, h)
	sess := h.createSession()

	// max_hops=99 should be capped to 3; the tool still returns
	// successfully with whatever the graph contains.
	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name": "get_callees",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"file_path":     "main.go",
			"symbol_name":   "HandleRequest",
			"max_hops":      99,
		},
	})
	_, isErr := parseToolText(resp)
	if isErr {
		t.Error("expected successful response when max_hops is above cap")
	}
}

// ---------------------------------------------------------------------------
// get_file_imports
// ---------------------------------------------------------------------------

func TestMCP_GetFileImports_Direct(t *testing.T) {
	h := newTestHarness(t)
	seedCallGraphTestData(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name": "get_file_imports",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"file_path":     "main.go",
		},
	})
	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("tool returned error: %s", text)
	}

	var result fileImportsResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if result.FilePath != "main.go" {
		t.Errorf("expected file_path main.go, got %q", result.FilePath)
	}
	if len(result.Imports) == 0 {
		t.Error("expected at least one import")
	}
	for _, imp := range result.Imports {
		if imp.Depth != 1 {
			t.Errorf("expected depth 1 for direct imports, got %d for %s", imp.Depth, imp.Path)
		}
	}
}

func TestMCP_GetFileImports_MissingFilePath(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name": "get_file_imports",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
		},
	})
	_, isErr := parseToolText(resp)
	if !isErr {
		t.Error("expected error when file_path is missing")
	}
}

// ---------------------------------------------------------------------------
// get_architecture_diagram
// ---------------------------------------------------------------------------

func TestMCP_GetArchitectureDiagram_NotGenerated(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name": "get_architecture_diagram",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
		},
	})
	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("tool returned error: %s", text)
	}
	var result architectureDiagramResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if result.Found {
		t.Error("expected Found=false when no diagram has been generated")
	}
	if result.Message == "" {
		t.Error("expected a helpful Message for the not-generated case")
	}
}

// ---------------------------------------------------------------------------
// get_recent_changes
// ---------------------------------------------------------------------------

func TestMCP_GetRecentChanges_NoGitRoot(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	// The test harness indexes a repo with RepoPath /tmp/test-repo
	// (which isn't a git repo). `git log` should fail; the tool
	// should surface the error rather than panic.
	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name": "get_recent_changes",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"limit":         5,
		},
	})
	_, isErr := parseToolText(resp)
	if !isErr {
		t.Error("expected error when repository has no git root on disk")
	}
}

// ---------------------------------------------------------------------------
// Shared — resolveSymbol edge cases
// ---------------------------------------------------------------------------

func TestMCP_ResolveSymbol_AmbiguousByName(t *testing.T) {
	// Build a fixture where two symbols share a name in the same
	// file (overloaded-ish — e.g. two `helper` defs at different
	// lines). Without line_start the resolver picks the earliest.
	h := newTestHarness(t)
	result := &indexer.IndexResult{
		RepoName: "ambiguous-repo",
		RepoPath: "/tmp/amb",
		Files: []indexer.FileResult{
			{
				Path:      "multi.go",
				Language:  "go",
				LineCount: 100,
				Symbols: []indexer.Symbol{
					{ID: "m1", Name: "helper", Kind: "function", Language: "go", FilePath: "multi.go", StartLine: 10, EndLine: 20},
					{ID: "m2", Name: "helper", Kind: "function", Language: "go", FilePath: "multi.go", StartLine: 40, EndLine: 50},
				},
			},
		},
	}
	repo, err := h.store.ReplaceIndexResult(h.repoID, result)
	if err != nil {
		t.Fatalf("ReplaceIndexResult: %v", err)
	}
	h.repoID = repo.ID

	sess := h.createSession()

	// Without line_start — should resolve successfully (picks earliest).
	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name": "get_callees",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"file_path":     "multi.go",
			"symbol_name":   "helper",
		},
	})
	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("expected success on ambiguous name without line_start: %s", text)
	}
	var result2 callGraphResult
	if err := json.Unmarshal([]byte(text), &result2); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if result2.Root.StartLine != 10 {
		t.Errorf("expected earliest match (line 10), got %d", result2.Root.StartLine)
	}

	// With line_start=40 — should resolve to the second instance.
	resp = h.sendRPC(sess, 4, "tools/call", map[string]interface{}{
		"name": "get_callees",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"file_path":     "multi.go",
			"symbol_name":   "helper",
			"line_start":    40,
		},
	})
	text, isErr = parseToolText(resp)
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}
	if err := json.Unmarshal([]byte(text), &result2); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if result2.Root.StartLine != 40 {
		t.Errorf("expected line_start=40 match, got %d", result2.Root.StartLine)
	}
}
