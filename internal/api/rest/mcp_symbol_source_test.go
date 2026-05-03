// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

// ---------------------------------------------------------------------------
// mockFileReader — test double for the fileReader interface
// ---------------------------------------------------------------------------

// mockFileReader satisfies the fileReader interface. The files map is keyed
// by "repoID/filePath" so tests can seed multiple repos without collisions.
type mockFileReader struct {
	files map[string]string // key: "repoID\x00filePath"
}

func newMockFileReader() *mockFileReader {
	return &mockFileReader{files: make(map[string]string)}
}

func (m *mockFileReader) add(repoID, filePath, content string) {
	m.files[repoID+"\x00"+filePath] = content
}

func (m *mockFileReader) ReadRepoFile(repoID, filePath string) (string, error) {
	k := repoID + "\x00" + filePath
	content, ok := m.files[k]
	if !ok {
		return "", fmt.Errorf("file not found: %s", filePath)
	}
	return content, nil
}

// ---------------------------------------------------------------------------
// seedSymbolSourceData sets up a repo with symbols whose line ranges
// correspond to real content in the mockFileReader. Returns the symbol IDs
// for HandleRequest and ParseJSON, plus the repo ID.
//
// File layout (main.go, 1-based lines 1–35):
//
//	line  1: package main
//	line  2: (blank)
//	line  3: import "fmt"
//	line  4: (blank)
//	line  5: // Config is a config type
//	line  6: type Config struct {
//	line  7:     Host string
//	line  8: }
//	line  9: (blank)
//	line 10: // HandleRequest handles the request
//	line 11: func HandleRequest(w http.ResponseWriter, r *http.Request) {
//	lines 12-29: body (18 lines)
//	line 30: }
//	lines 31-35: trailing
//
// utils.go (lines 1-20):
//
//	lines 1-4:  header
//	lines 5-15: ParseJSON
//	lines 16-20: trailing
// ---------------------------------------------------------------------------

func seedSymbolSourceData(t *testing.T, h *mcpTestHarness) (handleID, parseID string, fr *mockFileReader) {
	t.Helper()

	// Build content for main.go (35 lines).
	mainLines := make([]string, 35)
	mainLines[0] = "package main"
	mainLines[1] = ""
	mainLines[2] = `import "fmt"`
	mainLines[3] = ""
	mainLines[4] = "// Config is a config type"
	mainLines[5] = "type Config struct {"
	mainLines[6] = "    Host string"
	mainLines[7] = "}"
	mainLines[8] = ""
	mainLines[9] = "// HandleRequest handles the request"
	mainLines[10] = "func HandleRequest(w http.ResponseWriter, r *http.Request) {"
	for i := 11; i <= 28; i++ {
		mainLines[i] = fmt.Sprintf("    line%d := %d", i+1, i+1)
	}
	mainLines[29] = "}"
	mainLines[30] = ""
	mainLines[31] = "// trailing"
	mainLines[32] = "var x = 1"
	mainLines[33] = ""
	mainLines[34] = "// end"
	mainContent := strings.Join(mainLines, "\n")

	// Build content for utils.go (20 lines).
	utilsLines := make([]string, 20)
	utilsLines[0] = "package main"
	utilsLines[1] = ""
	utilsLines[2] = `import "encoding/json"`
	utilsLines[3] = ""
	for i := 4; i <= 14; i++ {
		utilsLines[i] = fmt.Sprintf("    // line %d", i+1)
	}
	utilsLines[4] = "func ParseJSON(data []byte) (interface{}, error) {"
	utilsLines[14] = "}"
	utilsLines[15] = ""
	utilsLines[16] = "// util2"
	utilsLines[17] = "func Util2() {}"
	utilsLines[18] = ""
	utilsLines[19] = "// end"
	utilsContent := strings.Join(utilsLines, "\n")

	result := &indexer.IndexResult{
		RepoName: "source-test-repo",
		RepoPath: "/tmp/source-test-repo",
		Files: []indexer.FileResult{
			{
				Path:      "main.go",
				Language:  "go",
				LineCount: 35,
				Symbols: []indexer.Symbol{
					{
						ID: "src-handle", Name: "HandleRequest",
						QualifiedName: "main.HandleRequest", Kind: "function",
						Language: "go", FilePath: "main.go",
						StartLine: 11, EndLine: 30,
						Signature: "func HandleRequest(w http.ResponseWriter, r *http.Request)",
					},
					{
						ID: "src-config", Name: "Config",
						QualifiedName: "main.Config", Kind: "type",
						Language: "go", FilePath: "main.go",
						StartLine: 6, EndLine: 8,
					},
				},
			},
			{
				Path:      "utils.go",
				Language:  "go",
				LineCount: 20,
				Symbols: []indexer.Symbol{
					{
						ID: "src-parse", Name: "ParseJSON",
						QualifiedName: "main.ParseJSON", Kind: "function",
						Language: "go", FilePath: "utils.go",
						StartLine: 5, EndLine: 15,
						Signature: "func ParseJSON(data []byte) (interface{}, error)",
					},
				},
			},
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
		t.Fatalf("expected HandleRequest and ParseJSON symbols; got handle=%q parse=%q", handleID, parseID)
	}

	fr = newMockFileReader()
	fr.add(h.repoID, "main.go", mainContent)
	fr.add(h.repoID, "utils.go", utilsContent)
	h.handler.WithFileReader(fr)
	return handleID, parseID, fr
}

// ---------------------------------------------------------------------------
// TestMCP_GetSymbolSource_HappyPath
// ---------------------------------------------------------------------------

func TestMCP_GetSymbolSource_HappyPath(t *testing.T) {
	h := newTestHarness(t)
	handleID, _, _ := seedSymbolSourceData(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 1, "tools/call", map[string]interface{}{
		"name": "get_symbol_source",
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

	var result getSymbolSourceResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if result.SymbolID != handleID {
		t.Errorf("symbol_id: got %q, want %q", result.SymbolID, handleID)
	}
	if result.Name != "HandleRequest" {
		t.Errorf("name: got %q", result.Name)
	}
	if result.FilePath != "main.go" {
		t.Errorf("file_path: got %q", result.FilePath)
	}
	if result.Language != "go" {
		t.Errorf("language: got %q", result.Language)
	}
	if result.Kind != "function" {
		t.Errorf("kind: got %q", result.Kind)
	}
	if result.Source == "" {
		t.Error("source must not be empty")
	}
	if !strings.Contains(result.Source, "func HandleRequest") {
		t.Errorf("source does not contain function declaration: %q", result.Source)
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetSymbolSource_SymbolIDFastPath
// ---------------------------------------------------------------------------

func TestMCP_GetSymbolSource_SymbolIDFastPath(t *testing.T) {
	h := newTestHarness(t)
	handleID, _, _ := seedSymbolSourceData(t, h)
	sess := h.createSession()

	// Provide symbol_id only — no file_path or symbol_name.
	resp := h.sendRPC(sess, 2, "tools/call", map[string]interface{}{
		"name": "get_symbol_source",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"symbol_id":     handleID,
		},
	})
	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}

	var result getSymbolSourceResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if result.SymbolID != handleID {
		t.Errorf("symbol_id: got %q, want %q", result.SymbolID, handleID)
	}
	if result.Source == "" {
		t.Error("source must not be empty on fast path")
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetSymbolSource_FilePathFallback
// ---------------------------------------------------------------------------

func TestMCP_GetSymbolSource_FilePathFallback(t *testing.T) {
	h := newTestHarness(t)
	_, parseID, _ := seedSymbolSourceData(t, h)
	sess := h.createSession()

	// file_path + symbol_name only, no symbol_id.
	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name": "get_symbol_source",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"file_path":     "utils.go",
			"symbol_name":   "ParseJSON",
		},
	})
	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}

	var result getSymbolSourceResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if result.SymbolID != parseID {
		t.Errorf("symbol_id: got %q, want %q", result.SymbolID, parseID)
	}
	if !strings.Contains(result.Source, "ParseJSON") {
		t.Errorf("source does not contain ParseJSON: %q", result.Source)
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetSymbolSource_SymbolNotFound
// ---------------------------------------------------------------------------

func TestMCP_GetSymbolSource_SymbolNotFound(t *testing.T) {
	h := newTestHarness(t)
	seedSymbolSourceData(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 4, "tools/call", map[string]interface{}{
		"name": "get_symbol_source",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"file_path":     "main.go",
			"symbol_name":   "DoesNotExist",
		},
	})
	text, isErr := parseToolText(resp)
	if !isErr {
		t.Fatalf("expected error for missing symbol, got: %s", text)
	}
	if !strings.Contains(text, MCPErrSymbolNotFound) {
		// The text is the human-readable message; the code lives in
		// _meta but we can also confirm the error message shape.
		// errSymbolNotFound sets Code=MCPErrSymbolNotFound and embeds the
		// symbol name in Message — both should be detectable.
		if !strings.Contains(text, "DoesNotExist") && !strings.Contains(text, "not found") {
			t.Errorf("error message should reference the missing symbol: %q", text)
		}
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetSymbolSource_CrossRepoLeakageBlocked
// ---------------------------------------------------------------------------

func TestMCP_GetSymbolSource_CrossRepoLeakageBlocked(t *testing.T) {
	h := newTestHarness(t)
	handleID, _, _ := seedSymbolSourceData(t, h)
	sess := h.createSession()

	// Store a second empty repo. The symbol exists in h.repoID (repo A);
	// we request it via repo B — the symbol_id fast path validates
	// RepoID, so it falls through to file_path resolution which finds
	// nothing in repo B.
	resultB := &indexer.IndexResult{
		RepoName: "repo-b",
		RepoPath: "/tmp/repo-b",
		Files:    []indexer.FileResult{},
	}
	repoB, err := h.store.StoreIndexResult(resultB)
	if err != nil {
		t.Fatalf("StoreIndexResult repo-b: %v", err)
	}

	// Attempt: provide symbol_id from repo A, but repository_id = repo B.
	resp := h.sendRPC(sess, 5, "tools/call", map[string]interface{}{
		"name": "get_symbol_source",
		"arguments": map[string]interface{}{
			"repository_id": repoB.ID,
			"symbol_id":     handleID,
			// No file_path/symbol_name fallback available in repo B.
		},
	})
	text, isErr := parseToolText(resp)
	if !isErr {
		t.Fatalf("expected cross-repo leakage to be blocked, got success: %s", text)
	}
	// Must NOT reveal that the symbol exists in repo A — should look like
	// "symbol not found" or "must provide..." rather than exposing repo A data.
	if strings.Contains(text, "HandleRequest") && !strings.Contains(text, "not found") {
		t.Errorf("error message may leak cross-repo symbol existence: %q", text)
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetSymbolSource_FileDeletedSinceIndex
// ---------------------------------------------------------------------------

func TestMCP_GetSymbolSource_FileDeletedSinceIndex(t *testing.T) {
	h := newTestHarness(t)
	seedSymbolSourceData(t, h)
	sess := h.createSession()

	// Replace the mock reader with one that has no files — simulates
	// the file being deleted after indexing.
	h.handler.WithFileReader(newMockFileReader())

	resp := h.sendRPC(sess, 6, "tools/call", map[string]interface{}{
		"name": "get_symbol_source",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"file_path":     "main.go",
			"symbol_name":   "HandleRequest",
		},
	})
	text, isErr := parseToolText(resp)
	if !isErr {
		t.Fatalf("expected stale error when file is gone, got: %s", text)
	}
	if !strings.Contains(text, MCPErrRepositoryStale) && !strings.Contains(text, "unavailable") {
		t.Errorf("expected stale error message, got: %q", text)
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetSymbolSource_LineRange_1Based
// ---------------------------------------------------------------------------

// TestMCP_GetSymbolSource_LineRange_1Based pins the 1-based inclusive
// contract (Decision D2): start_line and end_line in the response must
// match the stored symbol metadata exactly, and the source returned must
// contain the line at start_line.
func TestMCP_GetSymbolSource_LineRange_1Based(t *testing.T) {
	h := newTestHarness(t)
	seedSymbolSourceData(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 7, "tools/call", map[string]interface{}{
		"name": "get_symbol_source",
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

	var result getSymbolSourceResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	// HandleRequest is indexed at StartLine=11, EndLine=30.
	if result.StartLine != 11 {
		t.Errorf("start_line: got %d, want 11", result.StartLine)
	}
	if result.EndLine != 30 {
		t.Errorf("end_line: got %d, want 30", result.EndLine)
	}

	// The first line of source must be the line at start_line (1-based).
	// Our fixture puts "func HandleRequest(...) {" on line 11 (index 10).
	firstLine := strings.SplitN(result.Source, "\n", 2)[0]
	if !strings.Contains(firstLine, "func HandleRequest") {
		t.Errorf("first line of source should be the symbol declaration, got: %q", firstLine)
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetSymbolSource_LargeSymbolSourceNote
// ---------------------------------------------------------------------------

func TestMCP_GetSymbolSource_LargeSymbolSourceNote(t *testing.T) {
	h := newTestHarness(t)
	seedSymbolSourceData(t, h)
	sess := h.createSession()

	// Build a large symbol: 501 lines in a new file.
	const bigStart = 1
	const bigEnd = 501
	bigLines := make([]string, bigEnd)
	bigLines[0] = "func BigFunc() {"
	for i := 1; i < bigEnd-1; i++ {
		bigLines[i] = fmt.Sprintf("    _ = %d", i)
	}
	bigLines[bigEnd-1] = "}"
	bigContent := strings.Join(bigLines, "\n")

	bigResult := &indexer.IndexResult{
		RepoName: "big-repo",
		RepoPath: "/tmp/big-repo",
		Files: []indexer.FileResult{
			{
				Path:      "big.go",
				Language:  "go",
				LineCount: bigEnd,
				Symbols: []indexer.Symbol{
					{
						ID: "big-func", Name: "BigFunc",
						QualifiedName: "main.BigFunc", Kind: "function",
						Language: "go", FilePath: "big.go",
						StartLine: bigStart, EndLine: bigEnd,
					},
				},
			},
		},
	}
	repo, err := h.store.ReplaceIndexResult(h.repoID, bigResult)
	if err != nil {
		t.Fatalf("ReplaceIndexResult: %v", err)
	}
	h.repoID = repo.ID

	fr := newMockFileReader()
	fr.add(h.repoID, "big.go", bigContent)
	h.handler.WithFileReader(fr)

	resp := h.sendRPC(sess, 8, "tools/call", map[string]interface{}{
		"name": "get_symbol_source",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"file_path":     "big.go",
			"symbol_name":   "BigFunc",
		},
	})
	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}

	var result getSymbolSourceResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if result.SourceNote == "" {
		t.Errorf("expected source_note for symbol with %d lines (> %d threshold)",
			bigEnd-bigStart+1, mcpSymbolLargeLineThreshold)
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetSymbolSource_ContextLines_OverCapClamps
// ---------------------------------------------------------------------------

func TestMCP_GetSymbolSource_ContextLines_OverCapClamps(t *testing.T) {
	h := newTestHarness(t)
	seedSymbolSourceData(t, h)
	sess := h.createSession()

	// context_lines=15 exceeds the cap of 10; should be silently clamped
	// and the call should succeed without error.
	resp := h.sendRPC(sess, 9, "tools/call", map[string]interface{}{
		"name": "get_symbol_source",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			"file_path":     "utils.go",
			"symbol_name":   "ParseJSON",
			"context_lines": 15,
		},
	})
	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("context_lines over cap should be silently clamped, got error: %s", text)
	}

	var result getSymbolSourceResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	// ParseJSON is at lines 5–15. With context clamped to 10, the
	// slice is at most lines max(1, 5-10)–(15+10)=25, but our file
	// only has 20 lines so it clamps to [1, 20]. Either way, the
	// source must be non-empty and contain ParseJSON.
	if result.Source == "" {
		t.Error("source must not be empty with context_lines clamped")
	}
	if !strings.Contains(result.Source, "ParseJSON") {
		t.Errorf("source should contain ParseJSON: %q", result.Source)
	}
}
