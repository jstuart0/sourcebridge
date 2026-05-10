// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

// ---------------------------------------------------------------------------
// Phase 2a (CA-154): get_changed_symbols tests
//
// Coverage:
//   - HappyPath_Files: files: ["a.go","b.go"] → both projections populated
//   - HappyPath_CommitRange: real git repo in TempDir with 2 commits +
//     HEAD~1..HEAD; verify changed_files and changed_symbols populated
//   - NeitherInputProvided: no commit_range, no files → errInvalidArguments
//   - DeduplicatedSymbols: same symbol referenced from multiple files →
//     appears once in flat changed_symbols
//   - MaxSymbolsCap: diff with many symbols, max_symbols:10 → capped,
//     truncated:true, both projections respect the cap
//   - CrossRepoIsolation: symbol from repo B, request says repo A → not returned
//   - RepoNotFound: MCPErrRepositoryNotIndexed
//   - EmptyDiff: valid files list that touches no known files → empty arrays, no error
//   - HydratesViaGetSymbolsByIDs: regression guard — symbols hydrated from flat
//     ID list (GetSymbolsByIDs), not from name-based lookup (dexter M4)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// changedSymbolsFixture holds IDs seeded for the changed-symbols tests.
type changedSymbolsFixture struct {
	RepoID  string
	RepoBID string // second repo for cross-repo isolation

	// Repo A symbols.
	FuncAID string // "FuncA" in a.go
	FuncBID string // "FuncB" in b.go
	FuncCID string // "FuncC" in b.go (second symbol in b.go)
}

// seedChangedSymbolsFixture creates two repos with a symbol set covering all
// get_changed_symbols test scenarios.
func seedChangedSymbolsFixture(t *testing.T, h *mcpTestHarness) changedSymbolsFixture {
	t.Helper()

	fix := changedSymbolsFixture{}

	// ---- Repo A ----
	resultA := &indexer.IndexResult{
		RepoName: "cs-test-repo-a",
		RepoPath: "/tmp/cs-test-repo-a",
		Files: []indexer.FileResult{
			{
				Path:     "a.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{Name: "FuncA", Kind: "function", Language: "go",
						FilePath: "a.go", StartLine: 1, EndLine: 10},
				},
			},
			{
				Path:     "b.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{Name: "FuncB", Kind: "function", Language: "go",
						FilePath: "b.go", StartLine: 1, EndLine: 10},
					{Name: "FuncC", Kind: "function", Language: "go",
						FilePath: "b.go", StartLine: 11, EndLine: 20},
				},
			},
		},
	}
	repoA, err := h.store.StoreIndexResult(t.Context(), resultA)
	if err != nil {
		t.Fatalf("StoreIndexResult repoA: %v", err)
	}
	fix.RepoID = repoA.ID

	symsA, _ := h.store.GetSymbols(t.Context(), fix.RepoID, nil, nil, 0, 0)
	for _, s := range symsA {
		switch s.Name {
		case "FuncA":
			fix.FuncAID = s.ID
		case "FuncB":
			fix.FuncBID = s.ID
		case "FuncC":
			fix.FuncCID = s.ID
		}
	}

	// ---- Repo B (cross-repo isolation) ----
	resultB := &indexer.IndexResult{
		RepoName: "cs-test-repo-b",
		RepoPath: "/tmp/cs-test-repo-b",
		Files: []indexer.FileResult{
			{
				Path:     "other.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{Name: "OtherFunc", Kind: "function", Language: "go",
						FilePath: "other.go", StartLine: 1, EndLine: 10},
				},
			},
		},
	}
	repoB, err := h.store.StoreIndexResult(t.Context(), resultB)
	if err != nil {
		t.Fatalf("StoreIndexResult repoB: %v", err)
	}
	fix.RepoBID = repoB.ID

	return fix
}

// parseChangedSymbolsResult extracts the response map from a get_changed_symbols
// tools/call response. Fails the test on tool errors.
func parseChangedSymbolsResult(t *testing.T, resp jsonRPCResponse) map[string]interface{} {
	t.Helper()
	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("unexpected tool error: %s", text)
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("json.Unmarshal: %v (text: %s)", err, text)
	}
	return result
}

// flatSymbolIDsFromChangedSymbols extracts symbol_id values from the
// changed_symbols flat list.
func flatSymbolIDsFromChangedSymbols(t *testing.T, result map[string]interface{}) []string {
	t.Helper()
	raw, ok := result["changed_symbols"].([]interface{})
	if !ok {
		t.Fatalf("changed_symbols not a slice: %T", result["changed_symbols"])
	}
	ids := make([]string, 0, len(raw))
	for _, item := range raw {
		m, _ := item.(map[string]interface{})
		ids = append(ids, m["symbol_id"].(string))
	}
	return ids
}

// ---------------------------------------------------------------------------
// TestMCP_GetChangedSymbols_HappyPath_Files
// ---------------------------------------------------------------------------

// TestMCP_GetChangedSymbols_HappyPath_Files: files: ["a.go","b.go"] returns
// symbols from both files in changed_files (grouped) and changed_symbols (flat).
func TestMCP_GetChangedSymbols_HappyPath_Files(t *testing.T) {
	h := newTestHarness(t)
	fix := seedChangedSymbolsFixture(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 1, "tools/call", map[string]interface{}{
		"name": "get_changed_symbols",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoID,
			"files":         []string{"a.go", "b.go"},
		},
	})

	result := parseChangedSymbolsResult(t, resp)

	// repository_id must be echoed.
	if got, _ := result["repository_id"].(string); got != fix.RepoID {
		t.Errorf("repository_id: got %q, want %q", got, fix.RepoID)
	}

	// changed_symbols must contain all 3 symbols.
	ids := flatSymbolIDsFromChangedSymbols(t, result)
	if !containsID(ids, fix.FuncAID) {
		t.Errorf("FuncA (%s) missing from changed_symbols", fix.FuncAID)
	}
	if !containsID(ids, fix.FuncBID) {
		t.Errorf("FuncB (%s) missing from changed_symbols", fix.FuncBID)
	}
	if !containsID(ids, fix.FuncCID) {
		t.Errorf("FuncC (%s) missing from changed_symbols", fix.FuncCID)
	}

	// changed_files must have 2 entries (one per file).
	changedFiles, _ := result["changed_files"].([]interface{})
	if len(changedFiles) != 2 {
		t.Errorf("changed_files: want 2, got %d", len(changedFiles))
	}

	// changed_file_count and changed_symbol_count must be set.
	if fc, _ := result["changed_file_count"].(float64); int(fc) != 2 {
		t.Errorf("changed_file_count: want 2, got %v", result["changed_file_count"])
	}
	if sc, _ := result["changed_symbol_count"].(float64); int(sc) != 3 {
		t.Errorf("changed_symbol_count: want 3, got %v", result["changed_symbol_count"])
	}

	// truncated must be false.
	if trunc, _ := result["truncated"].(bool); trunc {
		t.Error("truncated must be false when no max_symbols cap applied")
	}

	// _meta must be present.
	if _, ok := result["_meta"]; !ok {
		t.Error("_meta key missing from response")
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetChangedSymbols_HappyPath_CommitRange
// ---------------------------------------------------------------------------

// TestMCP_GetChangedSymbols_HappyPath_CommitRange: real git repo in TempDir
// with 2 commits; HEAD~1..HEAD touches service.go which contains indexed
// symbols. Verifies changed_files and changed_symbols are populated.
func TestMCP_GetChangedSymbols_HappyPath_CommitRange(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available in PATH; skipping commit_range test")
	}

	repoDir := t.TempDir()

	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	runGit("init", "-b", "main")
	runGit("config", "user.email", "test@test.test")
	runGit("config", "user.name", "Test User")

	// Commit 1 — base so HEAD~1..HEAD is a non-empty range.
	if err := os.WriteFile(filepath.Join(repoDir, "init.go"), []byte("package pkg\n"), 0644); err != nil {
		t.Fatalf("WriteFile init.go: %v", err)
	}
	runGit("add", "init.go")
	runGit("commit", "-m", "initial commit")

	// Commit 2 (HEAD) — adds service.go which we will index.
	if err := os.WriteFile(filepath.Join(repoDir, "service.go"), []byte("package pkg\n"), 0644); err != nil {
		t.Fatalf("WriteFile service.go: %v", err)
	}
	runGit("add", "service.go")
	runGit("commit", "-m", "add service.go")

	h := newTestHarness(t)

	// Index a repo whose symbol is in service.go (matching the touched file).
	result := &indexer.IndexResult{
		RepoName: "cs-commit-range-repo",
		RepoPath: repoDir,
		Files: []indexer.FileResult{
			{
				Path:     "service.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{Name: "ServeHTTP", Kind: "function", Language: "go",
						FilePath: "service.go", StartLine: 1, EndLine: 15},
				},
			},
		},
	}
	repo, err := h.store.StoreIndexResult(t.Context(), result)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}

	// Point the stored repo's clone path to our real git checkout.
	h.store.UpdateRepositoryMeta(t.Context(), repo.ID, graphstore.RepositoryMeta{
		ClonePath: repoDir,
	})

	sess := h.createSession()

	resp := h.sendRPC(sess, 2, "tools/call", map[string]interface{}{
		"name": "get_changed_symbols",
		"arguments": map[string]interface{}{
			"repository_id": repo.ID,
			"commit_range":  "HEAD~1..HEAD",
		},
	})

	result2 := parseChangedSymbolsResult(t, resp)

	// commit_range must be echoed.
	if got, _ := result2["commit_range"].(string); got != "HEAD~1..HEAD" {
		t.Errorf("commit_range: got %q, want HEAD~1..HEAD", got)
	}

	// changed_symbols must be non-empty (service.go was touched and indexed).
	ids := flatSymbolIDsFromChangedSymbols(t, result2)
	if len(ids) == 0 {
		t.Error("changed_symbols: expected at least 1 symbol from commit_range, got 0")
	}

	// changed_files must be non-empty.
	changedFiles, _ := result2["changed_files"].([]interface{})
	if len(changedFiles) == 0 {
		t.Error("changed_files: expected at least 1 file from commit_range, got 0")
	}

	// Standard shape keys must be present.
	for _, key := range []string{"changed_symbol_count", "changed_file_count", "truncated", "_meta"} {
		if _, ok := result2[key]; !ok {
			t.Errorf("response key %q missing", key)
		}
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetChangedSymbols_NeitherInputProvided
// ---------------------------------------------------------------------------

// TestMCP_GetChangedSymbols_NeitherInputProvided: omitting both commit_range
// and files must return errInvalidArguments.
func TestMCP_GetChangedSymbols_NeitherInputProvided(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name": "get_changed_symbols",
		"arguments": map[string]interface{}{
			"repository_id": h.repoID,
			// neither commit_range nor files
		},
	})

	text, isErr := parseToolText(resp)
	if !isErr {
		t.Fatalf("expected errInvalidArguments for missing diff anchor, got success: %s", text)
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetChangedSymbols_DeduplicatedSymbols
// ---------------------------------------------------------------------------

// TestMCP_GetChangedSymbols_DeduplicatedSymbols: passing the same file twice
// (which yields the same symbols twice from resolveDiffTouchedSymbols) must
// produce a flat changed_symbols list with each symbol appearing exactly once.
func TestMCP_GetChangedSymbols_DeduplicatedSymbols(t *testing.T) {
	h := newTestHarness(t)
	fix := seedChangedSymbolsFixture(t, h)
	sess := h.createSession()

	// Pass b.go twice — the resolver will emit FuncB and FuncC twice each.
	resp := h.sendRPC(sess, 4, "tools/call", map[string]interface{}{
		"name": "get_changed_symbols",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoID,
			"files":         []string{"b.go", "b.go"},
		},
	})

	result := parseChangedSymbolsResult(t, resp)
	ids := flatSymbolIDsFromChangedSymbols(t, result)

	// Count occurrences of each ID — each must appear exactly once.
	counts := make(map[string]int, len(ids))
	for _, id := range ids {
		counts[id]++
	}
	for id, count := range counts {
		if count > 1 {
			t.Errorf("symbol %s appears %d times in changed_symbols; want exactly 1", id, count)
		}
	}

	// FuncB and FuncC must both appear once.
	if !containsID(ids, fix.FuncBID) {
		t.Errorf("FuncB (%s) missing from changed_symbols", fix.FuncBID)
	}
	if !containsID(ids, fix.FuncCID) {
		t.Errorf("FuncC (%s) missing from changed_symbols", fix.FuncCID)
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetChangedSymbols_MaxSymbolsCap
// ---------------------------------------------------------------------------

// TestMCP_GetChangedSymbols_MaxSymbolsCap: a diff that touches more symbols
// than max_symbols is capped; both projections must respect the cap and
// truncated must be true.
func TestMCP_GetChangedSymbols_MaxSymbolsCap(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	// Build a repo with 15 symbols across 3 files (5 per file).
	files := make([]indexer.FileResult, 3)
	for fi := 0; fi < 3; fi++ {
		fname := string(rune('a'+fi)) + ".go"
		syms := make([]indexer.Symbol, 5)
		for si := 0; si < 5; si++ {
			syms[si] = indexer.Symbol{
				Name:      fname[:1] + string(rune('0'+si)),
				Kind:      "function",
				Language:  "go",
				FilePath:  fname,
				StartLine: si*10 + 1,
				EndLine:   si*10 + 9,
			}
		}
		files[fi] = indexer.FileResult{
			Path:     fname,
			Language: "go",
			Symbols:  syms,
		}
	}
	bigResult := &indexer.IndexResult{
		RepoName: "cs-cap-repo",
		RepoPath: "/tmp/cs-cap-repo",
		Files:    files,
	}
	repo, err := h.store.StoreIndexResult(t.Context(), bigResult)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}

	// Request all 3 files with max_symbols=10 (only 10 of 15 returned).
	resp := h.sendRPC(sess, 5, "tools/call", map[string]interface{}{
		"name": "get_changed_symbols",
		"arguments": map[string]interface{}{
			"repository_id": repo.ID,
			"files":         []string{"a.go", "b.go", "c.go"},
			"max_symbols":   10,
		},
	})

	result := parseChangedSymbolsResult(t, resp)

	// truncated must be true.
	if trunc, _ := result["truncated"].(bool); !trunc {
		t.Error("truncated: want true when max_symbols cap applied, got false")
	}

	// changed_symbols must be exactly 10.
	ids := flatSymbolIDsFromChangedSymbols(t, result)
	if len(ids) != 10 {
		t.Errorf("changed_symbols count: want 10, got %d", len(ids))
	}

	// changed_symbol_count must match the actual slice length.
	if sc, _ := result["changed_symbol_count"].(float64); int(sc) != 10 {
		t.Errorf("changed_symbol_count: want 10, got %v", result["changed_symbol_count"])
	}

	// Total symbols across all changed_files entries must also not exceed 10.
	changedFiles, _ := result["changed_files"].([]interface{})
	totalInFiles := 0
	for _, cfRaw := range changedFiles {
		cf, _ := cfRaw.(map[string]interface{})
		syms, _ := cf["symbols"].([]interface{})
		totalInFiles += len(syms)
	}
	if totalInFiles != 10 {
		t.Errorf("total symbols across changed_files: want 10, got %d", totalInFiles)
	}

	// _meta.touched_symbol_count must reflect the pre-cap count (15).
	meta, _ := result["_meta"].(map[string]interface{})
	if tsc, _ := meta["touched_symbol_count"].(float64); int(tsc) != 15 {
		t.Errorf("_meta.touched_symbol_count: want 15, got %v", meta["touched_symbol_count"])
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetChangedSymbols_CrossRepoIsolation
// ---------------------------------------------------------------------------

// TestMCP_GetChangedSymbols_CrossRepoIsolation: requesting repo A's changed
// symbols must not surface symbols that belong to repo B, even if the files
// list overlaps.
func TestMCP_GetChangedSymbols_CrossRepoIsolation(t *testing.T) {
	h := newTestHarness(t)
	fix := seedChangedSymbolsFixture(t, h)
	sess := h.createSession()

	// Request repo A's symbols; repo B has "OtherFunc" in "other.go".
	// We request "other.go" from repo A — it doesn't exist there, so no
	// symbols should be returned, and nothing from repo B should leak.
	resp := h.sendRPC(sess, 6, "tools/call", map[string]interface{}{
		"name": "get_changed_symbols",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoID, // repo A
			"files":         []string{"other.go"},
		},
	})

	result := parseChangedSymbolsResult(t, resp)
	ids := flatSymbolIDsFromChangedSymbols(t, result)

	// Get all symbols from repo B to check for leakage.
	repoBSyms, _ := h.store.GetSymbols(t.Context(), fix.RepoBID, nil, nil, 0, 0)
	for _, bs := range repoBSyms {
		if containsID(ids, bs.ID) {
			t.Errorf("repo B symbol %s (%s) leaked into repo A results", bs.ID, bs.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetChangedSymbols_RepoNotFound
// ---------------------------------------------------------------------------

// TestMCP_GetChangedSymbols_RepoNotFound: an unknown repository_id must return
// MCPErrRepositoryNotIndexed.
func TestMCP_GetChangedSymbols_RepoNotFound(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 7, "tools/call", map[string]interface{}{
		"name": "get_changed_symbols",
		"arguments": map[string]interface{}{
			"repository_id": "repo-does-not-exist",
			"files":         []string{"a.go"},
		},
	})

	text, isErr := parseToolText(resp)
	if !isErr {
		t.Fatalf("expected error for unknown repo, got success: %s", text)
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetChangedSymbols_EmptyDiff
// ---------------------------------------------------------------------------

// TestMCP_GetChangedSymbols_EmptyDiff: a valid files list that touches no
// known indexed files returns empty arrays with no error.
func TestMCP_GetChangedSymbols_EmptyDiff(t *testing.T) {
	h := newTestHarness(t)
	fix := seedChangedSymbolsFixture(t, h)
	sess := h.createSession()

	// Use a file name that does not exist in the indexed repo.
	resp := h.sendRPC(sess, 8, "tools/call", map[string]interface{}{
		"name": "get_changed_symbols",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoID,
			"files":         []string{"nonexistent_file_xyz.go"},
		},
	})

	result := parseChangedSymbolsResult(t, resp)

	// Both arrays must be present and empty (not null).
	changedSymbols, ok := result["changed_symbols"].([]interface{})
	if !ok {
		t.Fatalf("changed_symbols: want slice, got %T", result["changed_symbols"])
	}
	if len(changedSymbols) != 0 {
		t.Errorf("changed_symbols: want 0, got %d", len(changedSymbols))
	}

	changedFiles, ok := result["changed_files"].([]interface{})
	if !ok {
		t.Fatalf("changed_files: want slice, got %T", result["changed_files"])
	}
	if len(changedFiles) != 0 {
		t.Errorf("changed_files: want 0, got %d", len(changedFiles))
	}

	// Counts must be zero.
	if sc, _ := result["changed_symbol_count"].(float64); int(sc) != 0 {
		t.Errorf("changed_symbol_count: want 0, got %v", result["changed_symbol_count"])
	}
	if fc, _ := result["changed_file_count"].(float64); int(fc) != 0 {
		t.Errorf("changed_file_count: want 0, got %v", result["changed_file_count"])
	}

	// truncated must be false.
	if trunc, _ := result["truncated"].(bool); trunc {
		t.Error("truncated: want false for empty diff, got true")
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetChangedSymbols_HydratesViaGetSymbolsByIDs
// ---------------------------------------------------------------------------

// TestMCP_GetChangedSymbols_HydratesViaGetSymbolsByIDs is a regression guard
// for dexter M4: the implementation must hydrate changed_symbols from the flat
// ID list via GetSymbolsByIDs — NOT from name-based lookup. This matters when
// two symbols share a name across different files; only the IDs that were
// actually touched by the diff should appear.
func TestMCP_GetChangedSymbols_HydratesViaGetSymbolsByIDs(t *testing.T) {
	h := newTestHarness(t)
	fix := seedChangedSymbolsFixture(t, h)
	sess := h.createSession()

	// Request only "a.go" — only FuncA must appear, NOT FuncB or FuncC
	// (which live in "b.go"). This verifies that hydration uses the ID list
	// produced by resolveDiffTouchedSymbols for "a.go" only, not a name scan.
	resp := h.sendRPC(sess, 9, "tools/call", map[string]interface{}{
		"name": "get_changed_symbols",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoID,
			"files":         []string{"a.go"},
		},
	})

	result := parseChangedSymbolsResult(t, resp)
	ids := flatSymbolIDsFromChangedSymbols(t, result)

	// FuncA must be present.
	if !containsID(ids, fix.FuncAID) {
		t.Errorf("FuncA (%s) missing from changed_symbols for files=[a.go]", fix.FuncAID)
	}

	// FuncB and FuncC must NOT be present (they are in b.go, not a.go).
	if containsID(ids, fix.FuncBID) {
		t.Errorf("FuncB (%s) must not appear in changed_symbols for files=[a.go] (wrong hydration source)", fix.FuncBID)
	}
	if containsID(ids, fix.FuncCID) {
		t.Errorf("FuncC (%s) must not appear in changed_symbols for files=[a.go] (wrong hydration source)", fix.FuncCID)
	}

	// Exactly 1 symbol expected.
	if len(ids) != 1 {
		t.Errorf("changed_symbol_count: want 1, got %d (IDs: %v)", len(ids), ids)
	}
}
