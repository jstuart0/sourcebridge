// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/capabilities"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

// ---------------------------------------------------------------------------
// seedSymbolContextData sets up a repo with call-graph edges, file-level
// imports, and a mock file reader so get_symbol_context tests can exercise
// all four sections of the bundle.
//
// Graph:
//
//	main.go:
//	  HandleRequest  (callee from external? — leaf in callers, calls ParseJSON)
//	  Config         (called by HandleRequest)
//	utils.go:
//	  ParseJSON      ← called by HandleRequest
//
// Imports:
//
//	main.go  → "./utils", "net/http"
//	utils.go → "encoding/json"
//
// Returns the auto-generated symbol IDs for HandleRequest, ParseJSON.
// ---------------------------------------------------------------------------

func seedSymbolContextData(t *testing.T, h *mcpTestHarness) (handleID, parseID string, fr *mockFileReader) {
	t.Helper()

	// Reuse the call-graph fixture from mcp_accessors_test.go (same topology,
	// extended with a net/http import so we can verify multiple imports).
	result := &indexer.IndexResult{
		RepoName: "ctx-test-repo",
		RepoPath: "/tmp/ctx-test-repo",
		Files: []indexer.FileResult{
			{
				Path:      "main.go",
				Language:  "go",
				LineCount: 30,
				Symbols: []indexer.Symbol{
					{
						ID: "ctx-handle", Name: "HandleRequest",
						QualifiedName: "main.HandleRequest", Kind: "function",
						Language: "go", FilePath: "main.go",
						StartLine: 10, EndLine: 25,
						Signature: "func HandleRequest(w http.ResponseWriter, r *http.Request)",
					},
					{
						ID: "ctx-config", Name: "Config",
						QualifiedName: "main.Config", Kind: "type",
						Language: "go", FilePath: "main.go",
						StartLine: 1, EndLine: 8,
					},
				},
				Imports: []indexer.Import{
					{Path: "./utils", FilePath: "main.go", Line: 3},
					{Path: "net/http", FilePath: "main.go", Line: 4},
				},
			},
			{
				Path:      "utils.go",
				Language:  "go",
				LineCount: 20,
				Symbols: []indexer.Symbol{
					{
						ID: "ctx-parse", Name: "ParseJSON",
						QualifiedName: "main.ParseJSON", Kind: "function",
						Language: "go", FilePath: "utils.go",
						StartLine: 5, EndLine: 15,
						Signature: "func ParseJSON(data []byte) (interface{}, error)",
					},
				},
				Imports: []indexer.Import{
					{Path: "encoding/json", FilePath: "utils.go", Line: 3},
				},
			},
		},
		Relations: []indexer.Relation{
			{SourceID: "ctx-handle", TargetID: "ctx-parse", Type: indexer.RelationCalls},
			{SourceID: "ctx-handle", TargetID: "ctx-config", Type: indexer.RelationCalls},
		},
	}
	repo, err := h.store.ReplaceIndexResult(h.repoID, result)
	if err != nil {
		t.Fatalf("ReplaceIndexResult: %v", err)
	}
	h.repoID = repo.ID

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
		t.Fatalf("expected HandleRequest and ParseJSON; got handle=%q parse=%q", handleID, parseID)
	}

	// Wire a file reader with minimal content for each symbol file.
	mainLines := make([]string, 30)
	mainLines[0] = "package main"
	mainLines[1] = ""
	mainLines[2] = `import "./utils"`
	mainLines[3] = `import "net/http"`
	mainLines[4] = ""
	mainLines[5] = "// Config type"
	mainLines[6] = "type Config struct {"
	mainLines[7] = "}"
	mainLines[8] = ""
	mainLines[9] = "// HandleRequest handles requests"
	mainLines[10] = "func HandleRequest(w http.ResponseWriter, r *http.Request) {"
	for i := 11; i <= 24; i++ {
		mainLines[i] = fmt.Sprintf("    _ = %d", i)
	}
	mainLines[25] = "}"
	mainLines[26] = ""
	mainLines[27] = "// end"
	mainLines[28] = ""
	mainLines[29] = "// eof"
	mainContent := strings.Join(mainLines, "\n")

	utilsLines := make([]string, 20)
	utilsLines[0] = "package main"
	utilsLines[1] = ""
	utilsLines[2] = `import "encoding/json"`
	utilsLines[3] = ""
	utilsLines[4] = "func ParseJSON(data []byte) (interface{}, error) {"
	for i := 5; i <= 13; i++ {
		utilsLines[i] = fmt.Sprintf("    _ = %d", i)
	}
	utilsLines[14] = "    return nil, nil"
	utilsLines[15] = "}"
	utilsLines[16] = ""
	utilsLines[17] = "// util2"
	utilsLines[18] = ""
	utilsLines[19] = "// eof"
	utilsContent := strings.Join(utilsLines, "\n")

	fr = newMockFileReader()
	fr.add(h.repoID, "main.go", mainContent)
	fr.add(h.repoID, "utils.go", utilsContent)
	h.handler.WithFileReader(fr)

	return handleID, parseID, fr
}

// ---------------------------------------------------------------------------
// TestMCP_GetSymbolContext_HappyPath — all four bundle sections populated
// ---------------------------------------------------------------------------

func TestMCP_GetSymbolContext_HappyPath(t *testing.T) {
	h := newTestHarness(t)
	handleID, parseID, _ := seedSymbolContextData(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 1, "tools/call", map[string]interface{}{
		"name": "get_symbol_context",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"file_path":     "main.go",
			"symbol_name":   "HandleRequest",
		},
	})
	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}

	var result getSymbolContextResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	// Symbol section.
	if result.Symbol.SymbolID != handleID {
		t.Errorf("symbol.symbol_id: got %q want %q", result.Symbol.SymbolID, handleID)
	}
	if result.Symbol.Name != "HandleRequest" {
		t.Errorf("symbol.name: got %q", result.Symbol.Name)
	}
	if result.Symbol.Source == "" {
		t.Error("symbol.source must not be empty")
	}

	// Callees section — HandleRequest calls ParseJSON and Config.
	if len(result.Callees) == 0 {
		t.Error("expected at least one callee of HandleRequest")
	}
	foundParse := false
	for _, c := range result.Callees {
		if c.SymbolID == parseID || c.SymbolName == "ParseJSON" {
			foundParse = true
			if c.HopsFromRoot != 1 {
				t.Errorf("callee hops_from_root: got %d want 1", c.HopsFromRoot)
			}
		}
	}
	if !foundParse {
		t.Error("ParseJSON should be in callees of HandleRequest")
	}

	// Imports section — main.go imports "./utils" and "net/http".
	if len(result.Imports) == 0 {
		t.Error("expected imports for main.go")
	}
	importPaths := make(map[string]bool)
	for _, imp := range result.Imports {
		importPaths[imp.Path] = true
	}
	if !importPaths["./utils"] {
		t.Error("expected ./utils in imports")
	}
	if !importPaths["net/http"] {
		t.Error("expected net/http in imports")
	}

	// Bundle metadata.
	if result.Depth != 1 {
		t.Errorf("depth: got %d want 1", result.Depth)
	}
	if result.Degraded {
		t.Errorf("degraded should be false, got true with reason: %q", result.DegradedReason)
	}
	if result.Truncated {
		t.Error("truncated should be false for this small fixture")
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetSymbolContext_SymbolIDFastPath
// ---------------------------------------------------------------------------

func TestMCP_GetSymbolContext_SymbolIDFastPath(t *testing.T) {
	h := newTestHarness(t)
	handleID, _, _ := seedSymbolContextData(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 2, "tools/call", map[string]interface{}{
		"name": "get_symbol_context",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"symbol_id":     handleID,
		},
	})
	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("unexpected error on symbol_id fast-path: %s", text)
	}

	var result getSymbolContextResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if result.Symbol.SymbolID != handleID {
		t.Errorf("symbol_id fast-path: got %q want %q", result.Symbol.SymbolID, handleID)
	}
	if result.Symbol.Source == "" {
		t.Error("source must not be empty on symbol_id fast-path")
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetSymbolContext_DegradedNoCallGraph — capabilityChecker stubs
// call_graph off; callers/callees empty, degraded=true, source still present.
// This test exercises the WithCapabilityChecker DI from Phase 1.
// ---------------------------------------------------------------------------

func TestMCP_GetSymbolContext_DegradedNoCallGraph(t *testing.T) {
	h := newTestHarness(t)
	seedSymbolContextData(t, h)
	sess := h.createSession()

	// Inject a capability checker that disables call_graph only.
	h.handler.WithCapabilityChecker(func(name string, _ capabilities.Edition) bool {
		return name != "call_graph"
	})

	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name": "get_symbol_context",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"file_path":     "main.go",
			"symbol_name":   "HandleRequest",
		},
	})
	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}

	var result getSymbolContextResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	if !result.Degraded {
		t.Error("degraded should be true when call_graph is disabled")
	}
	if !strings.Contains(result.DegradedReason, "call_graph") {
		t.Errorf("degraded_reason should mention call_graph: %q", result.DegradedReason)
	}
	if len(result.Callers) != 0 {
		t.Errorf("callers should be empty when call_graph disabled, got %d", len(result.Callers))
	}
	if len(result.Callees) != 0 {
		t.Errorf("callees should be empty when call_graph disabled, got %d", len(result.Callees))
	}
	// Source section must still be present.
	if result.Symbol.Source == "" {
		t.Error("symbol.source must be present even when call_graph is degraded")
	}
	// Imports should still be populated (only call_graph is off).
	if len(result.Imports) == 0 {
		t.Error("imports should still be populated when only call_graph is disabled")
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetSymbolContext_DepthGreaterThan1Rejected
// ---------------------------------------------------------------------------

func TestMCP_GetSymbolContext_DepthGreaterThan1Rejected(t *testing.T) {
	h := newTestHarness(t)
	seedSymbolContextData(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 4, "tools/call", map[string]interface{}{
		"name": "get_symbol_context",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"file_path":     "main.go",
			"symbol_name":   "HandleRequest",
			"depth":         2,
		},
	})
	text, isErr := parseToolText(resp)
	if !isErr {
		t.Fatalf("expected error for depth=2, got success: %s", text)
	}
	if !strings.Contains(text, MCPErrInvalidArguments) && !strings.Contains(text, "depth") {
		t.Errorf("error should mention depth or INVALID_ARGUMENTS: %q", text)
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetSymbolContext_TruncationCallers — 21 callers → 20 returned,
// truncated=true.
// ---------------------------------------------------------------------------

func TestMCP_GetSymbolContext_TruncationCallers(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	// Build a fixture: one target symbol + 21 caller symbols, all in the
	// same file. Wire Relations so each caller → target.
	const callerCount = 21
	symbols := make([]indexer.Symbol, 0, callerCount+1)
	symbols = append(symbols, indexer.Symbol{
		ID: "trunc-target", Name: "TargetFunc",
		QualifiedName: "main.TargetFunc", Kind: "function",
		Language: "go", FilePath: "trunc.go",
		StartLine: 1, EndLine: 5,
	})
	for i := 0; i < callerCount; i++ {
		symbols = append(symbols, indexer.Symbol{
			ID:            fmt.Sprintf("caller-%d", i),
			Name:          fmt.Sprintf("Caller%d", i),
			QualifiedName: fmt.Sprintf("main.Caller%d", i),
			Kind:          "function",
			Language:      "go",
			FilePath:      "trunc.go",
			StartLine:     10 + i*5,
			EndLine:       13 + i*5,
		})
	}

	relations := make([]indexer.Relation, callerCount)
	for i := 0; i < callerCount; i++ {
		relations[i] = indexer.Relation{
			SourceID: fmt.Sprintf("caller-%d", i),
			TargetID: "trunc-target",
			Type:     indexer.RelationCalls,
		}
	}

	result := &indexer.IndexResult{
		RepoName:  "trunc-repo",
		RepoPath:  "/tmp/trunc-repo",
		Files:     []indexer.FileResult{{Path: "trunc.go", Language: "go", LineCount: 200, Symbols: symbols}},
		Relations: relations,
	}
	repo, err := h.store.ReplaceIndexResult(h.repoID, result)
	if err != nil {
		t.Fatalf("ReplaceIndexResult: %v", err)
	}
	h.repoID = repo.ID

	// Find the generated target symbol ID.
	var targetID string
	for _, s := range h.store.GetSymbolsByFile(h.repoID, "trunc.go") {
		if s.Name == "TargetFunc" {
			targetID = s.ID
			break
		}
	}
	if targetID == "" {
		t.Fatal("TargetFunc not found after indexing")
	}

	// Wire a minimal file reader.
	fileLines := make([]string, 200)
	fileLines[0] = "package main"
	fileLines[1] = ""
	fileLines[2] = "func TargetFunc() {}"
	fr := newMockFileReader()
	fr.add(h.repoID, "trunc.go", strings.Join(fileLines, "\n"))
	h.handler.WithFileReader(fr)

	resp := h.sendRPC(sess, 5, "tools/call", map[string]interface{}{
		"name": "get_symbol_context",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"symbol_id":     targetID,
		},
	})
	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}

	var bundle getSymbolContextResult
	if err := json.Unmarshal([]byte(text), &bundle); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	if len(bundle.Callers) != mcpContextCallersTruncLimit {
		t.Errorf("callers: got %d want %d", len(bundle.Callers), mcpContextCallersTruncLimit)
	}
	if !bundle.Truncated {
		t.Error("truncated should be true with 21 callers")
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetSymbolContext_TruncationCallees — 21 callees → 20 returned.
// ---------------------------------------------------------------------------

func TestMCP_GetSymbolContext_TruncationCallees(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	const calleeCount = 21
	symbols := make([]indexer.Symbol, 0, calleeCount+1)
	symbols = append(symbols, indexer.Symbol{
		ID: "trunc-caller", Name: "CallerFunc",
		QualifiedName: "main.CallerFunc", Kind: "function",
		Language: "go", FilePath: "callees.go",
		StartLine: 1, EndLine: 5,
	})
	for i := 0; i < calleeCount; i++ {
		symbols = append(symbols, indexer.Symbol{
			ID:            fmt.Sprintf("callee-%d", i),
			Name:          fmt.Sprintf("Callee%d", i),
			QualifiedName: fmt.Sprintf("main.Callee%d", i),
			Kind:          "function",
			Language:      "go",
			FilePath:      "callees.go",
			StartLine:     10 + i*5,
			EndLine:       13 + i*5,
		})
	}

	relations := make([]indexer.Relation, calleeCount)
	for i := 0; i < calleeCount; i++ {
		relations[i] = indexer.Relation{
			SourceID: "trunc-caller",
			TargetID: fmt.Sprintf("callee-%d", i),
			Type:     indexer.RelationCalls,
		}
	}

	result := &indexer.IndexResult{
		RepoName:  "callee-repo",
		RepoPath:  "/tmp/callee-repo",
		Files:     []indexer.FileResult{{Path: "callees.go", Language: "go", LineCount: 200, Symbols: symbols}},
		Relations: relations,
	}
	repo, err := h.store.ReplaceIndexResult(h.repoID, result)
	if err != nil {
		t.Fatalf("ReplaceIndexResult: %v", err)
	}
	h.repoID = repo.ID

	var callerSymID string
	for _, s := range h.store.GetSymbolsByFile(h.repoID, "callees.go") {
		if s.Name == "CallerFunc" {
			callerSymID = s.ID
			break
		}
	}
	if callerSymID == "" {
		t.Fatal("CallerFunc not found")
	}

	fileLines := make([]string, 200)
	fileLines[0] = "package main"
	fileLines[1] = ""
	fileLines[2] = "func CallerFunc() {}"
	fr := newMockFileReader()
	fr.add(h.repoID, "callees.go", strings.Join(fileLines, "\n"))
	h.handler.WithFileReader(fr)

	resp := h.sendRPC(sess, 6, "tools/call", map[string]interface{}{
		"name": "get_symbol_context",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"symbol_id":     callerSymID,
		},
	})
	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}

	var bundle getSymbolContextResult
	if err := json.Unmarshal([]byte(text), &bundle); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	if len(bundle.Callees) != mcpContextCalleesTruncLimit {
		t.Errorf("callees: got %d want %d", len(bundle.Callees), mcpContextCalleesTruncLimit)
	}
	if !bundle.Truncated {
		t.Error("truncated should be true with 21 callees")
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetSymbolContext_TruncationImports — 51 imports → 50 returned.
// ---------------------------------------------------------------------------

func TestMCP_GetSymbolContext_TruncationImports(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	const importCount = 51
	imports := make([]indexer.Import, importCount)
	for i := 0; i < importCount; i++ {
		imports[i] = indexer.Import{
			Path:     fmt.Sprintf("pkg/import%d", i),
			FilePath: "imports.go",
			Line:     i + 1,
		}
	}

	result := &indexer.IndexResult{
		RepoName: "imports-repo",
		RepoPath: "/tmp/imports-repo",
		Files: []indexer.FileResult{
			{
				Path:      "imports.go",
				Language:  "go",
				LineCount: 100,
				Symbols: []indexer.Symbol{
					{
						ID: "imp-sym", Name: "ImportHeavy",
						QualifiedName: "main.ImportHeavy", Kind: "function",
						Language: "go", FilePath: "imports.go",
						StartLine: 60, EndLine: 80,
					},
				},
				Imports: imports,
			},
		},
	}
	repo, err := h.store.ReplaceIndexResult(h.repoID, result)
	if err != nil {
		t.Fatalf("ReplaceIndexResult: %v", err)
	}
	h.repoID = repo.ID

	var symID string
	for _, s := range h.store.GetSymbolsByFile(h.repoID, "imports.go") {
		if s.Name == "ImportHeavy" {
			symID = s.ID
			break
		}
	}
	if symID == "" {
		t.Fatal("ImportHeavy not found")
	}

	fileLines := make([]string, 100)
	fileLines[0] = "package main"
	fileLines[59] = "func ImportHeavy() {"
	fileLines[79] = "}"
	fr := newMockFileReader()
	fr.add(h.repoID, "imports.go", strings.Join(fileLines, "\n"))
	h.handler.WithFileReader(fr)

	resp := h.sendRPC(sess, 7, "tools/call", map[string]interface{}{
		"name": "get_symbol_context",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"symbol_id":     symID,
		},
	})
	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}

	var bundle getSymbolContextResult
	if err := json.Unmarshal([]byte(text), &bundle); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	if len(bundle.Imports) != mcpContextImportsTruncLimit {
		t.Errorf("imports: got %d want %d", len(bundle.Imports), mcpContextImportsTruncLimit)
	}
	if !bundle.Truncated {
		t.Error("truncated should be true with 51 imports")
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetSymbolContext_MissingCallerSymbol_Skipped — GetSymbolsByIDs
// returns nil for one ID (partial re-index); that caller is silently
// skipped rather than failing the whole bundle.
// ---------------------------------------------------------------------------

func TestMCP_GetSymbolContext_MissingCallerSymbol_Skipped(t *testing.T) {
	// We can't directly poison GetSymbolsByIDs, but we can verify that
	// the handler returns a result (not an error) even when the store
	// has a dangling edge. The harness uses a real in-process store, so
	// we simulate the scenario by building a fixture where the Relations
	// reference a symbol ID that is NOT present in the symbol table
	// (the indexer fixture omits the caller symbol).
	h := newTestHarness(t)
	sess := h.createSession()

	result := &indexer.IndexResult{
		RepoName: "dangling-repo",
		RepoPath: "/tmp/dangling-repo",
		Files: []indexer.FileResult{
			{
				Path:      "target.go",
				Language:  "go",
				LineCount: 10,
				Symbols: []indexer.Symbol{
					{
						ID: "dangle-target", Name: "TargetFn",
						QualifiedName: "main.TargetFn", Kind: "function",
						Language: "go", FilePath: "target.go",
						StartLine: 1, EndLine: 5,
					},
				},
			},
		},
		// "ghost-caller" is NOT in any FileResult.Symbols — it's a
		// dangling reference. The store will store the edge but when
		// GetSymbolsByIDs is called, the map entry will be nil.
		Relations: []indexer.Relation{
			{SourceID: "ghost-caller", TargetID: "dangle-target", Type: indexer.RelationCalls},
		},
	}
	repo, err := h.store.ReplaceIndexResult(h.repoID, result)
	if err != nil {
		t.Fatalf("ReplaceIndexResult: %v", err)
	}
	h.repoID = repo.ID

	var targetSymID string
	for _, s := range h.store.GetSymbolsByFile(h.repoID, "target.go") {
		if s.Name == "TargetFn" {
			targetSymID = s.ID
			break
		}
	}
	if targetSymID == "" {
		t.Fatal("TargetFn not found")
	}

	lines := make([]string, 10)
	lines[0] = "package main"
	lines[1] = ""
	lines[2] = "func TargetFn() {}"
	fr := newMockFileReader()
	fr.add(h.repoID, "target.go", strings.Join(lines, "\n"))
	h.handler.WithFileReader(fr)

	resp := h.sendRPC(sess, 8, "tools/call", map[string]interface{}{
		"name": "get_symbol_context",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"symbol_id":     targetSymID,
		},
	})
	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("dangling caller should be silently skipped, got error: %s", text)
	}

	// The bundle should arrive without error, callers may or may not be
	// empty depending on whether the store persisted the ghost edge — the
	// important thing is no panic and no error response.
	var bundle getSymbolContextResult
	if err := json.Unmarshal([]byte(text), &bundle); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if bundle.Symbol.SymbolID != targetSymID {
		t.Errorf("symbol_id: got %q want %q", bundle.Symbol.SymbolID, targetSymID)
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetSymbolContext_FileImportsDisabled_Degraded
// ---------------------------------------------------------------------------

func TestMCP_GetSymbolContext_FileImportsDisabled_Degraded(t *testing.T) {
	h := newTestHarness(t)
	seedSymbolContextData(t, h)
	sess := h.createSession()

	h.handler.WithCapabilityChecker(func(name string, _ capabilities.Edition) bool {
		return name != "file_imports"
	})

	resp := h.sendRPC(sess, 9, "tools/call", map[string]interface{}{
		"name": "get_symbol_context",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"file_path":     "main.go",
			"symbol_name":   "HandleRequest",
		},
	})
	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}

	var result getSymbolContextResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	if !result.Degraded {
		t.Error("degraded should be true when file_imports is disabled")
	}
	if !strings.Contains(result.DegradedReason, "file_imports") {
		t.Errorf("degraded_reason should mention file_imports: %q", result.DegradedReason)
	}
	if len(result.Imports) != 0 {
		t.Errorf("imports should be empty when file_imports disabled, got %d", len(result.Imports))
	}
	// Callers and callees should still be populated.
	if len(result.Callees) == 0 {
		t.Error("callees should still be populated when only file_imports is disabled")
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetSymbolContext_DegradedBoth — both call_graph AND file_imports
// disabled; callers, callees, and imports all empty; degraded=true;
// degraded_reason contains both capability names joined with "; ".
// Pins the separator as the wire contract for multi-reason degraded responses.
// ---------------------------------------------------------------------------

func TestMCP_GetSymbolContext_DegradedBoth(t *testing.T) {
	h := newTestHarness(t)
	seedSymbolContextData(t, h)
	sess := h.createSession()

	// Inject a capability checker that disables both call_graph and file_imports.
	h.handler.WithCapabilityChecker(func(name string, _ capabilities.Edition) bool {
		return name != "call_graph" && name != "file_imports"
	})

	resp := h.sendRPC(sess, 12, "tools/call", map[string]interface{}{
		"name": "get_symbol_context",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"file_path":     "main.go",
			"symbol_name":   "HandleRequest",
		},
	})
	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}

	var result getSymbolContextResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	if !result.Degraded {
		t.Error("degraded should be true when both call_graph and file_imports are disabled")
	}
	// Both capability names must appear in the reason.
	if !strings.Contains(result.DegradedReason, "call_graph") {
		t.Errorf("degraded_reason should mention call_graph: %q", result.DegradedReason)
	}
	if !strings.Contains(result.DegradedReason, "file_imports") {
		t.Errorf("degraded_reason should mention file_imports: %q", result.DegradedReason)
	}
	// Separator contract: multiple reasons are joined with "; " (wire format).
	if !strings.Contains(result.DegradedReason, "; ") {
		t.Errorf("degraded_reason should join multiple reasons with \"; \": %q", result.DegradedReason)
	}

	// All three arrays must be empty.
	if len(result.Callers) != 0 {
		t.Errorf("callers should be empty when call_graph disabled, got %d", len(result.Callers))
	}
	if len(result.Callees) != 0 {
		t.Errorf("callees should be empty when call_graph disabled, got %d", len(result.Callees))
	}
	if len(result.Imports) != 0 {
		t.Errorf("imports should be empty when file_imports disabled, got %d", len(result.Imports))
	}

	// Symbol.Source must still be present — symbol retrieval is never gated.
	if result.Symbol.Source == "" {
		t.Error("symbol.source must be present even when both capabilities are degraded")
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetSymbolContext_DepthNegativeRejected — depth=-1 must return
// INVALID_ARGUMENTS rather than being silently treated as depth=1.
// ---------------------------------------------------------------------------

func TestMCP_GetSymbolContext_DepthNegativeRejected(t *testing.T) {
	h := newTestHarness(t)
	seedSymbolContextData(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 13, "tools/call", map[string]interface{}{
		"name": "get_symbol_context",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"file_path":     "main.go",
			"symbol_name":   "HandleRequest",
			"depth":         -1,
		},
	})
	text, isErr := parseToolText(resp)
	if !isErr {
		t.Fatalf("expected INVALID_ARGUMENTS for depth=-1, got success: %s", text)
	}
	if !strings.Contains(text, MCPErrInvalidArguments) && !strings.Contains(text, "depth") {
		t.Errorf("error should mention depth or INVALID_ARGUMENTS: %q", text)
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetSymbolContext_SymbolNotFound
// ---------------------------------------------------------------------------

func TestMCP_GetSymbolContext_SymbolNotFound(t *testing.T) {
	h := newTestHarness(t)
	seedSymbolContextData(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 10, "tools/call", map[string]interface{}{
		"name": "get_symbol_context",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"file_path":     "main.go",
			"symbol_name":   "DoesNotExistAtAll",
		},
	})
	text, isErr := parseToolText(resp)
	if !isErr {
		t.Fatalf("expected error for missing symbol, got: %s", text)
	}
	if !strings.Contains(text, MCPErrSymbolNotFound) && !strings.Contains(text, "not found") {
		t.Errorf("error should indicate symbol not found: %q", text)
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetSymbolContext_CrossRepoLeakageBlocked
// ---------------------------------------------------------------------------

func TestMCP_GetSymbolContext_CrossRepoLeakageBlocked(t *testing.T) {
	h := newTestHarness(t)
	handleID, _, _ := seedSymbolContextData(t, h)
	sess := h.createSession()

	// Create a second empty repo.
	repoB, err := h.store.StoreIndexResult(&indexer.IndexResult{
		RepoName: "repo-b-ctx",
		RepoPath: "/tmp/repo-b-ctx",
		Files:    []indexer.FileResult{},
	})
	if err != nil {
		t.Fatalf("StoreIndexResult repo-b: %v", err)
	}

	// Request handleID (from repo A) via repo B's repository_id.
	resp := h.sendRPC(sess, 11, "tools/call", map[string]interface{}{
		"name": "get_symbol_context",
		"arguments": map[string]interface{}{
			"repository_id": repoB.ID,
			"symbol_id":     handleID,
		},
	})
	text, isErr := parseToolText(resp)
	if !isErr {
		t.Fatalf("expected cross-repo leakage to be blocked, got success: %s", text)
	}
	// Must not leak that the symbol exists in repo A.
	if strings.Contains(text, "HandleRequest") && !strings.Contains(text, "not found") {
		t.Errorf("error may leak cross-repo symbol existence: %q", text)
	}
}

// ---------------------------------------------------------------------------
// BenchmarkMCP_GetSymbolContext — establishes the per-call latency baseline.
//
// Fixture: 100 symbols in a single file with 10 callees from the root symbol
// and 5 imports. This represents a typical "open a function and understand its
// context" MCP call. The goal is to record the baseline; no p50 gate is
// enforced day-1 (see plan Phase 3.4 bench note).
//
// newTestHarness only accepts *testing.T; we use testing.T's via the standard
// testing.B helper bridge used elsewhere in this codebase.
// ---------------------------------------------------------------------------

// benchHarness is a minimal mcpTestHarness equivalent for benchmarks.
type benchHarness struct {
	handler *mcpHandler
	store   *graphstore.Store
	repoID  string
	sess    *mcpSession
}

func newBenchHarness(b *testing.B) *benchHarness {
	b.Helper()
	store := graphstore.NewStore()
	worker := &mockWorkerCaller{available: true}
	ks := newMockKnowledgeStore()

	// Seed an initial repo so the handler has a valid repoID to work with.
	init := &indexer.IndexResult{
		RepoName: "bench-repo",
		RepoPath: "/tmp/bench-repo",
		Files:    []indexer.FileResult{},
	}
	repo, err := store.StoreIndexResult(init)
	if err != nil {
		b.Fatalf("StoreIndexResult: %v", err)
	}

	h := newMCPHandler(store, ks, worker, "", 1*time.Hour, 30*time.Second, 100, nil)

	chans := &mcpLocalChans{
		eventCh: make(chan []byte, 64),
		done:    make(chan struct{}),
	}
	sess := &mcpSession{
		id:          "bench-session",
		claims:      &auth.Claims{UserID: "bench-user", OrgID: "bench-org"},
		initialized: true,
		createdAt:   time.Now(),
		lastUsed:    time.Now(),
		chans:       chans,
	}
	_ = h.sessionStore.Save(context.Background(), sess.toState(), time.Hour)
	h.localChans.Store(sess.id, chans)

	return &benchHarness{
		handler: h,
		store:   store,
		repoID:  repo.ID,
		sess:    sess,
	}
}

func BenchmarkMCP_GetSymbolContext(b *testing.B) {
	const symCount = 100
	const calleeCount = 10
	const importCount = 5

	bh := newBenchHarness(b)

	symbols := make([]indexer.Symbol, 0, symCount)
	symbols = append(symbols, indexer.Symbol{
		ID: "bench-root", Name: "BenchRoot",
		QualifiedName: "main.BenchRoot", Kind: "function",
		Language: "go", FilePath: "bench.go",
		StartLine: 1, EndLine: 20,
	})
	for i := 0; i < symCount-1; i++ {
		symbols = append(symbols, indexer.Symbol{
			ID:            fmt.Sprintf("bench-sym-%d", i),
			Name:          fmt.Sprintf("BenchSym%d", i),
			QualifiedName: fmt.Sprintf("main.BenchSym%d", i),
			Kind:          "function",
			Language:      "go",
			FilePath:      "bench.go",
			StartLine:     21 + i*3,
			EndLine:       22 + i*3,
		})
	}

	relations := make([]indexer.Relation, calleeCount)
	for i := 0; i < calleeCount; i++ {
		relations[i] = indexer.Relation{
			SourceID: "bench-root",
			TargetID: fmt.Sprintf("bench-sym-%d", i),
			Type:     indexer.RelationCalls,
		}
	}

	imports := make([]indexer.Import, importCount)
	for i := 0; i < importCount; i++ {
		imports[i] = indexer.Import{
			Path:     fmt.Sprintf("pkg/bench%d", i),
			FilePath: "bench.go",
			Line:     i + 1,
		}
	}

	indexResult := &indexer.IndexResult{
		RepoName: "bench-repo-seeded",
		RepoPath: "/tmp/bench-repo-seeded",
		Files: []indexer.FileResult{
			{
				Path:      "bench.go",
				Language:  "go",
				LineCount: 400,
				Symbols:   symbols,
				Imports:   imports,
			},
		},
		Relations: relations,
	}
	repo, err := bh.store.ReplaceIndexResult(bh.repoID, indexResult)
	if err != nil {
		b.Fatalf("ReplaceIndexResult: %v", err)
	}
	bh.repoID = repo.ID

	var rootID string
	for _, s := range bh.store.GetSymbolsByFile(bh.repoID, "bench.go") {
		if s.Name == "BenchRoot" {
			rootID = s.ID
			break
		}
	}
	if rootID == "" {
		b.Fatal("BenchRoot not found after indexing")
	}

	fileLines := make([]string, 400)
	fileLines[0] = "func BenchRoot() {"
	fileLines[19] = "}"
	for i := 20; i < 400; i++ {
		fileLines[i] = fmt.Sprintf("// line %d", i+1)
	}
	fr := newMockFileReader()
	fr.add(bh.repoID, "bench.go", strings.Join(fileLines, "\n"))
	bh.handler.WithFileReader(fr)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		paramsRaw, _ := json.Marshal(map[string]interface{}{
			"name": "get_symbol_context",
			"arguments": map[string]interface{}{
				"repository_id": bh.repoID,
				"symbol_id":     rootID,
			},
		})
		idRaw, _ := json.Marshal(i + 1)
		msg := jsonRPCRequest{
			JSONRPC: "2.0",
			ID:      idRaw,
			Method:  "tools/call",
			Params:  paramsRaw,
		}
		resp := bh.handler.safeDispatch(bh.sess, msg)
		_, isErr := parseToolText(resp)
		if isErr {
			b.Fatal("benchmark call returned error")
		}
	}
}
