// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

// ---------------------------------------------------------------------------
// Phase 2b (CA-154): find_importers tests
//
// Coverage:
//   - HappyPath: known package with 2 importers → both returned
//   - NoImporters: package exists but nothing imports it → empty importers, count 0
//   - PackageNotFound: file_path resolves to a directory not in the dep graph → empty, not error
//   - FilePathStripping: path.Dir("internal/auth/handler.go") == "internal/auth" matches dep.Package
//   - CrossRepoIsolation: package in repo A, request for repo B → empty result
//   - RepoNotFound: unknown repository_id → MCPErrRepositoryNotIndexed
//   - RootDirectory: file_path "main.go" → path.Dir returns "." → handled gracefully
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// importersFixture holds IDs and names seeded for find_importers tests.
type importersFixture struct {
	RepoID  string
	RepoBID string // second repo for cross-repo isolation
}

// seedImportersFixture creates two repos. Repo A has:
//   - "internal/auth/handler.go" that is imported by two packages:
//     "internal/api" (via "internal/api/router.go") and "cmd/server" (via "cmd/server/main.go")
//   - "internal/api/router.go" which has no importers itself in this fixture
//
// Repo B has a package at "internal/auth" to test cross-repo isolation.
func seedImportersFixture(t *testing.T, h *mcpTestHarness) importersFixture {
	t.Helper()

	fix := importersFixture{}

	// ---- Repo A ----
	// "internal/auth/handler.go" imports nothing.
	// "internal/api/router.go" imports "internal/auth".
	// "cmd/server/main.go" imports "internal/auth".
	resultA := &indexer.IndexResult{
		RepoName: "dep-test-repo-a",
		RepoPath: "/tmp/dep-test-repo-a",
		Files: []indexer.FileResult{
			{
				Path:     "internal/auth/handler.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{Name: "AuthHandler", Kind: "function", Language: "go",
						FilePath: "internal/auth/handler.go", StartLine: 1, EndLine: 20},
				},
				// No imports — this package is the importee.
			},
			{
				Path:     "internal/api/router.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{Name: "Route", Kind: "function", Language: "go",
						FilePath: "internal/api/router.go", StartLine: 1, EndLine: 10},
				},
				Imports: []indexer.Import{
					{Path: "internal/auth", Line: 3},
				},
			},
			{
				Path:     "cmd/server/main.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{Name: "main", Kind: "function", Language: "go",
						FilePath: "cmd/server/main.go", StartLine: 1, EndLine: 15},
				},
				Imports: []indexer.Import{
					{Path: "internal/auth", Line: 5},
				},
			},
		},
	}
	repoA, err := h.store.StoreIndexResult(resultA)
	if err != nil {
		t.Fatalf("StoreIndexResult repoA: %v", err)
	}
	fix.RepoID = repoA.ID

	// Compute package-level dependency graph for repo A.
	h.store.RecomputePackageDependencies(fix.RepoID)

	// ---- Repo B (cross-repo isolation) ----
	// Identical package path "internal/auth" — must not leak into repo A queries.
	resultB := &indexer.IndexResult{
		RepoName: "dep-test-repo-b",
		RepoPath: "/tmp/dep-test-repo-b",
		Files: []indexer.FileResult{
			{
				Path:     "internal/auth/handler.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{Name: "BAuthHandler", Kind: "function", Language: "go",
						FilePath: "internal/auth/handler.go", StartLine: 1, EndLine: 20},
				},
			},
			{
				Path:     "internal/other/consumer.go",
				Language: "go",
				Imports: []indexer.Import{
					{Path: "internal/auth", Line: 2},
				},
			},
		},
	}
	repoB, err := h.store.StoreIndexResult(resultB)
	if err != nil {
		t.Fatalf("StoreIndexResult repoB: %v", err)
	}
	fix.RepoBID = repoB.ID
	h.store.RecomputePackageDependencies(fix.RepoBID)

	return fix
}

// parseFindImportersResult extracts the response map from a find_importers
// tools/call response. Fails the test on tool errors.
func parseFindImportersResult(t *testing.T, resp jsonRPCResponse) map[string]interface{} {
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

// importersList extracts the importers string slice from a result map.
func importersList(t *testing.T, result map[string]interface{}) []string {
	t.Helper()
	raw, ok := result["importers"].([]interface{})
	if !ok {
		t.Fatalf("importers not a slice: %T", result["importers"])
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, _ := v.(string)
		out = append(out, s)
	}
	return out
}

// ---------------------------------------------------------------------------
// TestMCP_FindImporters_HappyPath
// ---------------------------------------------------------------------------

// TestMCP_FindImporters_HappyPath: file_path "internal/auth/handler.go" resolves
// to package "internal/auth", which is imported by two packages. Both are returned.
func TestMCP_FindImporters_HappyPath(t *testing.T) {
	h := newTestHarness(t)
	fix := seedImportersFixture(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 1, "tools/call", map[string]interface{}{
		"name": "find_importers",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoID,
			"file_path":     "internal/auth/handler.go",
		},
	})

	result := parseFindImportersResult(t, resp)

	// repository_id echoed.
	if got, _ := result["repository_id"].(string); got != fix.RepoID {
		t.Errorf("repository_id: got %q, want %q", got, fix.RepoID)
	}

	// file_path echoed.
	if got, _ := result["file_path"].(string); got != "internal/auth/handler.go" {
		t.Errorf("file_path: got %q, want %q", got, "internal/auth/handler.go")
	}

	// package must be the derived directory.
	if got, _ := result["package"].(string); got != "internal/auth" {
		t.Errorf("package: got %q, want %q", got, "internal/auth")
	}

	// importers must contain both importing packages.
	importers := importersList(t, result)
	if len(importers) != 2 {
		t.Errorf("importers count: got %d, want 2 (got %v)", len(importers), importers)
	}

	containsStr := func(sl []string, s string) bool {
		for _, v := range sl {
			if v == s {
				return true
			}
		}
		return false
	}
	if !containsStr(importers, "internal/api") {
		t.Errorf("importers: expected \"internal/api\" (got %v)", importers)
	}
	if !containsStr(importers, "cmd/server") {
		t.Errorf("importers: expected \"cmd/server\" (got %v)", importers)
	}

	// importer_count must match.
	if ct, _ := result["importer_count"].(float64); int(ct) != 2 {
		t.Errorf("importer_count: got %v, want 2", result["importer_count"])
	}

	// _meta must be present with note key.
	meta, ok := result["_meta"].(map[string]interface{})
	if !ok {
		t.Fatal("_meta missing or wrong type")
	}
	if _, ok := meta["note"]; !ok {
		t.Error("_meta.note key missing")
	}
}

// ---------------------------------------------------------------------------
// TestMCP_FindImporters_NoImporters
// ---------------------------------------------------------------------------

// TestMCP_FindImporters_NoImporters: a package exists in the dep graph but
// nothing imports it. Returns empty importers [], importer_count 0, no error.
func TestMCP_FindImporters_NoImporters(t *testing.T) {
	h := newTestHarness(t)
	fix := seedImportersFixture(t, h)
	sess := h.createSession()

	// "internal/api/router.go" has an import but nothing imports "internal/api".
	resp := h.sendRPC(sess, 2, "tools/call", map[string]interface{}{
		"name": "find_importers",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoID,
			"file_path":     "internal/api/router.go",
		},
	})

	result := parseFindImportersResult(t, resp)

	importers := importersList(t, result)
	if len(importers) != 0 {
		t.Errorf("importers: expected empty, got %v", importers)
	}

	if ct, _ := result["importer_count"].(float64); int(ct) != 0 {
		t.Errorf("importer_count: got %v, want 0", result["importer_count"])
	}

	if pkg, _ := result["package"].(string); pkg != "internal/api" {
		t.Errorf("package: got %q, want %q", pkg, "internal/api")
	}
}

// ---------------------------------------------------------------------------
// TestMCP_FindImporters_PackageNotFound
// ---------------------------------------------------------------------------

// TestMCP_FindImporters_PackageNotFound: the file_path resolves to a directory
// not in GetPackageDependencies. Returns empty importers [], count 0. Not an error.
func TestMCP_FindImporters_PackageNotFound(t *testing.T) {
	h := newTestHarness(t)
	fix := seedImportersFixture(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name": "find_importers",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoID,
			"file_path":     "internal/nonexistent/types.go",
		},
	})

	result := parseFindImportersResult(t, resp)

	importers := importersList(t, result)
	if len(importers) != 0 {
		t.Errorf("importers: expected empty, got %v", importers)
	}

	if ct, _ := result["importer_count"].(float64); int(ct) != 0 {
		t.Errorf("importer_count: got %v, want 0", result["importer_count"])
	}

	if pkg, _ := result["package"].(string); pkg != "internal/nonexistent" {
		t.Errorf("package: got %q, want %q", pkg, "internal/nonexistent")
	}
}

// ---------------------------------------------------------------------------
// TestMCP_FindImporters_FilePathStripping
// ---------------------------------------------------------------------------

// TestMCP_FindImporters_FilePathStripping: verifies that path.Dir correctly
// strips the filename so "internal/auth/handler.go" matches dep.Package "internal/auth".
func TestMCP_FindImporters_FilePathStripping(t *testing.T) {
	h := newTestHarness(t)
	fix := seedImportersFixture(t, h)
	sess := h.createSession()

	// Three different filenames in the same package — all should return the
	// same importer set because they all resolve to "internal/auth".
	filenames := []string{
		"internal/auth/handler.go",
		"internal/auth/middleware.go",
		"internal/auth/types.go",
	}

	for _, fp := range filenames {
		resp := h.sendRPC(sess, 10, "tools/call", map[string]interface{}{
			"name": "find_importers",
			"arguments": map[string]interface{}{
				"repository_id": fix.RepoID,
				"file_path":     fp,
			},
		})
		result := parseFindImportersResult(t, resp)

		if pkg, _ := result["package"].(string); pkg != "internal/auth" {
			t.Errorf("file_path %q: package got %q, want %q", fp, pkg, "internal/auth")
		}

		if ct, _ := result["importer_count"].(float64); int(ct) != 2 {
			t.Errorf("file_path %q: importer_count got %v, want 2", fp, result["importer_count"])
		}
	}
}

// ---------------------------------------------------------------------------
// TestMCP_FindImporters_CrossRepoIsolation
// ---------------------------------------------------------------------------

// TestMCP_FindImporters_CrossRepoIsolation: repo B has "internal/auth" with one
// importer. A request for repo A must not see repo B's importers.
func TestMCP_FindImporters_CrossRepoIsolation(t *testing.T) {
	h := newTestHarness(t)
	fix := seedImportersFixture(t, h)
	sess := h.createSession()

	// Repo B's "internal/auth" has one importer: "internal/other".
	// Repo A's "internal/auth" has two importers: "internal/api" and "cmd/server".
	// The request specifies repo B — must only see repo B's data.
	resp := h.sendRPC(sess, 4, "tools/call", map[string]interface{}{
		"name": "find_importers",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoBID,
			"file_path":     "internal/auth/handler.go",
		},
	})

	result := parseFindImportersResult(t, resp)
	importers := importersList(t, result)

	// Repo B has exactly one importer: "internal/other".
	if len(importers) != 1 {
		t.Errorf("cross-repo: expected 1 importer for repo B, got %d (%v)", len(importers), importers)
	}

	// Repo A's importers must not appear.
	for _, imp := range importers {
		if imp == "internal/api" || imp == "cmd/server" {
			t.Errorf("cross-repo: repo A importer %q leaked into repo B results", imp)
		}
	}
}

// ---------------------------------------------------------------------------
// TestMCP_FindImporters_RepoNotFound
// ---------------------------------------------------------------------------

// TestMCP_FindImporters_RepoNotFound: an unknown repository_id returns
// MCPErrRepositoryNotIndexed (isError true).
func TestMCP_FindImporters_RepoNotFound(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 5, "tools/call", map[string]interface{}{
		"name": "find_importers",
		"arguments": map[string]interface{}{
			"repository_id": "repo-does-not-exist-xyz",
			"file_path":     "internal/auth/handler.go",
		},
	})

	text, isErr := parseToolText(resp)
	if !isErr {
		t.Fatalf("expected tool error for unknown repo, got success: %s", text)
	}
}

// ---------------------------------------------------------------------------
// TestMCP_FindImporters_RootDirectory
// ---------------------------------------------------------------------------

// TestMCP_FindImporters_RootDirectory: file_path "main.go" (no directory
// component) causes path.Dir to return ".". This is handled gracefully:
// if no dep.Package == "." exists, return empty importers without error.
func TestMCP_FindImporters_RootDirectory(t *testing.T) {
	h := newTestHarness(t)
	fix := seedImportersFixture(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 6, "tools/call", map[string]interface{}{
		"name": "find_importers",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoID,
			"file_path":     "main.go",
		},
	})

	// Must not error — graceful empty result.
	result := parseFindImportersResult(t, resp)

	if pkg, _ := result["package"].(string); pkg != "." {
		t.Errorf("package: got %q, want \".\"", pkg)
	}

	importers := importersList(t, result)
	if len(importers) != 0 {
		t.Errorf("importers: expected empty for root-level file, got %v", importers)
	}

	if ct, _ := result["importer_count"].(float64); int(ct) != 0 {
		t.Errorf("importer_count: got %v, want 0", result["importer_count"])
	}
}
