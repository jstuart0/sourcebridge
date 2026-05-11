// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

// ---------------------------------------------------------------------------
// Helpers shared across this file
// ---------------------------------------------------------------------------

// parseDiffReviewResult unmarshals a tools/call response for
// review_diff_against_requirements into a generic map.
func parseDiffReviewResult(t *testing.T, resp jsonRPCResponse) map[string]interface{} {
	t.Helper()
	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("unexpected tool error: %s", text)
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("json.Unmarshal diffReviewResult: %v (text: %s)", err, text)
	}
	return result
}

// touchedFileByPath scans the touched_files array and returns the entry whose
// file_path matches fp, or nil when not found.
func touchedFileByPath(result map[string]interface{}, fp string) map[string]interface{} {
	tfs, _ := result["touched_files"].([]interface{})
	for _, entry := range tfs {
		m, _ := entry.(map[string]interface{})
		if m["file_path"] == fp {
			return m
		}
	}
	return nil
}

// seedCompoundFixture stores a fresh repository in the harness's graphstore
// with known files and symbols, then returns the repo ID and a map of
// symbol name → stored symbol ID. The caller may attach links afterwards.
//
// Symbols stored:
//   - "PublicFunc"  in "api.go"   (go, function, public)
//   - "internalFn" in "api.go"   (go, function, not public)
//   - "Helper"      in "util.go"  (go, function, public)
func seedCompoundFixture(t *testing.T, h *mcpTestHarness) (repoID string, symIDs map[string]string) {
	t.Helper()
	result := &indexer.IndexResult{
		RepoName: "compound-test-repo",
		RepoPath: "/tmp/compound-test-repo",
		Files: []indexer.FileResult{
			{
				Path:     "api.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{Name: "PublicFunc", QualifiedName: "pkg.PublicFunc",
						Kind: "function", Language: "go", FilePath: "api.go",
						StartLine: 1, EndLine: 10},
					{Name: "internalFn", QualifiedName: "pkg.internalFn",
						Kind: "function", Language: "go", FilePath: "api.go",
						StartLine: 11, EndLine: 20},
				},
			},
			{
				Path:     "util.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{Name: "Helper", QualifiedName: "pkg.Helper",
						Kind: "function", Language: "go", FilePath: "util.go",
						StartLine: 1, EndLine: 5},
				},
			},
		},
	}
	repo, err := h.store.StoreIndexResult(t.Context(), result)
	if err != nil {
		t.Fatalf("seedCompoundFixture StoreIndexResult: %v", err)
	}

	symIDs = map[string]string{
		"PublicFunc": lookupSymID(t, h, repo.ID, "api.go", "PublicFunc"),
		"internalFn": lookupSymID(t, h, repo.ID, "api.go", "internalFn"),
		"Helper":     lookupSymID(t, h, repo.ID, "util.go", "Helper"),
	}
	return repo.ID, symIDs
}

// ---------------------------------------------------------------------------
// Case 1: Explicit `files` happy path
// ---------------------------------------------------------------------------

// TestCallReviewDiffAgainstRequirements_ExplicitFilesHappyPath verifies that
// when the caller supplies files: ["api.go"], the response contains
// touched_files with file_path "api.go" and its symbols populated.
func TestCallReviewDiffAgainstRequirements_ExplicitFilesHappyPath(t *testing.T) {
	h := newTestHarness(t)
	repoID, _ := seedCompoundFixture(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 100, "tools/call", map[string]interface{}{
		"name": "review_diff_against_requirements",
		"arguments": map[string]interface{}{
			"repository_id": repoID,
			"files":         []string{"api.go"},
		},
	})

	result := parseDiffReviewResult(t, resp)

	// repository_id must be echoed.
	if got, _ := result["repository_id"].(string); got != repoID {
		t.Errorf("repository_id: got %q, want %q", got, repoID)
	}

	// touched_files must contain exactly the one file we asked about.
	tfs, _ := result["touched_files"].([]interface{})
	if len(tfs) != 1 {
		t.Fatalf("touched_files: expected 1 entry, got %d: %v", len(tfs), tfs)
	}

	entry := touchedFileByPath(result, "api.go")
	if entry == nil {
		t.Fatalf("touched_files: expected entry for api.go, got none")
	}

	// Both symbols in api.go must appear.
	syms, _ := entry["symbols"].([]interface{})
	if len(syms) != 2 {
		t.Errorf("api.go symbols: expected 2, got %d: %v", len(syms), syms)
	}
	symNames := map[string]bool{}
	for _, s := range syms {
		symNames[s.(string)] = true
	}
	if !symNames["PublicFunc"] {
		t.Errorf("api.go symbols: missing PublicFunc; got %v", symNames)
	}
	if !symNames["internalFn"] {
		t.Errorf("api.go symbols: missing internalFn; got %v", symNames)
	}

	// linked_requirements and unlinked_public_surface must be present (may be empty).
	if _, ok := result["linked_requirements"]; !ok {
		t.Error("linked_requirements key missing from response")
	}
	if _, ok := result["unlinked_public_surface"]; !ok {
		t.Error("unlinked_public_surface key missing from response")
	}
}

// ---------------------------------------------------------------------------
// Case 2: commit_range happy path (real minimal git repo in t.TempDir())
// ---------------------------------------------------------------------------

// TestCallReviewDiffAgainstRequirements_CommitRangeHappyPath exercises the
// commit_range code path end-to-end using a hermetic git repo in t.TempDir().
// This test also verifies the Phase 2a.1 runGitLog parser fix: the \x1e
// record separator is now placed at the START of the format string so that
// --name-only file lines for each commit land in the correct split segment.
func TestCallReviewDiffAgainstRequirements_CommitRangeHappyPath(t *testing.T) {
	// Verify git is available on this machine; skip cleanly if not.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available in PATH; skipping commit_range test")
	}

	repoDir := t.TempDir()

	runGitCmd := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Bootstrap a minimal git repo with two commits so we can form a valid
	// A..B range. Commit 1 (HEAD~1): adds init.go. Commit 2 (HEAD): adds api.go.
	// The range HEAD~1..HEAD covers only the second commit (api.go).
	runGitCmd("init", "-b", "main")
	runGitCmd("config", "user.email", "test@test.test")
	runGitCmd("config", "user.name", "Test User")

	// Commit 1 — establishes the base so HEAD~1..HEAD is a non-empty range.
	if err := os.WriteFile(filepath.Join(repoDir, "init.go"), []byte("package pkg\n"), 0644); err != nil {
		t.Fatalf("WriteFile init.go: %v", err)
	}
	runGitCmd("add", "init.go")
	runGitCmd("commit", "-m", "initial commit")

	// Commit 2 (HEAD) — adds api.go; this is the commit the range covers.
	if err := os.WriteFile(filepath.Join(repoDir, "api.go"), []byte("package pkg\n"), 0644); err != nil {
		t.Fatalf("WriteFile api.go: %v", err)
	}
	runGitCmd("add", "api.go")
	runGitCmd("commit", "-m", "add api.go")

	// Seed the store with a repo pointing to repoDir (via ClonePath) and
	// symbols matching the file committed above.
	h := newTestHarness(t)
	repoID, _ := seedCompoundFixture(t, h)

	// Point the stored repo's clone path at the real git checkout so
	// runGitLog finds a valid .git directory.
	h.store.UpdateRepositoryMeta(t.Context(), repoID, graphstore.RepositoryMeta{
		ClonePath: repoDir,
	})

	sess := h.createSession()

	resp := h.sendRPC(sess, 101, "tools/call", map[string]interface{}{
		"name": "review_diff_against_requirements",
		"arguments": map[string]interface{}{
			"repository_id": repoID,
			"commit_range":  "HEAD~1..HEAD",
		},
	})

	// The call must succeed (no error) — the git root is valid.
	result := parseDiffReviewResult(t, resp)

	// repository_id and commit_range must be echoed.
	if got, _ := result["repository_id"].(string); got != repoID {
		t.Errorf("repository_id: got %q, want %q", got, repoID)
	}
	if got, _ := result["commit_range"].(string); got != "HEAD~1..HEAD" {
		t.Errorf("commit_range: got %q, want %q", got, "HEAD~1..HEAD")
	}

	// The result must have the standard shape keys regardless of content.
	if _, ok := result["touched_files"]; !ok {
		t.Error("touched_files key missing from response")
	}
	if _, ok := result["linked_requirements"]; !ok {
		t.Error("linked_requirements key missing from response")
	}
	if _, ok := result["unlinked_public_surface"]; !ok {
		t.Error("unlinked_public_surface key missing from response")
	}

	// The parser fix (Phase 2a.1) moved \x1e to the start of the format
	// string so that --name-only file lines are captured correctly.
	// touched_files must now contain api.go (added in the HEAD commit,
	// which is included in the range HEAD~1..HEAD).
	tfs, _ := result["touched_files"].([]interface{})
	if len(tfs) == 0 {
		t.Fatalf("touched_files is empty — runGitLog parser fix may have regressed; expected api.go to appear")
	}
	if touchedEntry := touchedFileByPath(result, "api.go"); touchedEntry == nil {
		t.Errorf("touched_files: expected entry for api.go; got %v", tfs)
	}
}

// ---------------------------------------------------------------------------
// Case 3: runGitLog error path — repo has no git root
// ---------------------------------------------------------------------------

// TestCallReviewDiffAgainstRequirements_GitLogErrorPath verifies that when the
// repository has neither a ClonePath nor a Path on disk (empty strings), the
// tool returns an error rather than silently succeeding with no touched files.
func TestCallReviewDiffAgainstRequirements_GitLogErrorPath(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	// Store a repo with an empty path so there is no git root on disk.
	repo, err := h.store.StoreIndexResult(t.Context(), &indexer.IndexResult{
		RepoName: "no-git-root-repo",
		RepoPath: "", // empty — no path on disk
	})
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}

	resp := h.sendRPC(sess, 102, "tools/call", map[string]interface{}{
		"name": "review_diff_against_requirements",
		"arguments": map[string]interface{}{
			"repository_id": repo.ID,
			// No files provided — forces the git log branch.
		},
	})

	text, isErr := parseToolText(resp)
	if !isErr {
		t.Fatalf("expected an error when repo has no git root; got success: %s", text)
	}
	// The error must mention the missing git root or files.
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "git") && !strings.Contains(lower, "files") && !strings.Contains(lower, "path") {
		t.Errorf("error should mention git or path; got: %s", text)
	}
}

// ---------------------------------------------------------------------------
// Case 4: File with no symbols — symbols must be [] not omitted
// ---------------------------------------------------------------------------

// TestCallReviewDiffAgainstRequirements_FileWithNoSymbols verifies that when
// the touched file exists in the store but has no indexed symbols, the
// touched_files entry has an empty symbols slice (not omitted).
func TestCallReviewDiffAgainstRequirements_FileWithNoSymbols(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	// Store a repo with a file that has no symbols.
	repo, err := h.store.StoreIndexResult(t.Context(), &indexer.IndexResult{
		RepoName: "no-symbols-repo",
		RepoPath: "/tmp/no-symbols-repo",
		Files: []indexer.FileResult{
			{
				Path:     "empty.go",
				Language: "go",
				Symbols:  []indexer.Symbol{}, // no symbols
			},
		},
	})
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}

	resp := h.sendRPC(sess, 103, "tools/call", map[string]interface{}{
		"name": "review_diff_against_requirements",
		"arguments": map[string]interface{}{
			"repository_id": repo.ID,
			"files":         []string{"empty.go"},
		},
	})

	result := parseDiffReviewResult(t, resp)

	entry := touchedFileByPath(result, "empty.go")
	if entry == nil {
		t.Fatalf("expected touched_files entry for empty.go; got none")
	}

	// symbols must be an empty slice, not nil/omitted.
	symsRaw, present := entry["symbols"]
	if !present {
		t.Fatalf("symbols key must be present even when empty")
	}
	syms, _ := symsRaw.([]interface{})
	if len(syms) != 0 {
		t.Errorf("symbols: expected [], got %v", syms)
	}
}

// ---------------------------------------------------------------------------
// Case 5: Linked requirement included
// ---------------------------------------------------------------------------

// TestCallReviewDiffAgainstRequirements_LinkedRequirementIncluded verifies that
// when a touched symbol has a requirement link, the requirement appears in
// linked_requirements with the expected fields.
func TestCallReviewDiffAgainstRequirements_LinkedRequirementIncluded(t *testing.T) {
	h := newTestHarness(t)
	repoID, symIDs := seedCompoundFixture(t, h)
	sess := h.createSession()

	// Store a requirement and link it to PublicFunc.
	if err := h.store.StoreRequirement(t.Context(), repoID, &graphstore.StoredRequirement{
		ID:         "cpd-req-1",
		ExternalID: "CPD-1",
		Title:      "Compound req title",
		Priority:   "high",
	}); err != nil {
		t.Fatalf("StoreRequirement: %v", err)
	}
	req := h.store.GetRequirementByExternalID(t.Context(), repoID, "CPD-1")
	if req == nil {
		t.Fatal("failed to retrieve seeded requirement CPD-1")
	}
	h.store.StoreLink(t.Context(), repoID, &graphstore.StoredLink{
		RequirementID: req.ID,
		SymbolID:      symIDs["PublicFunc"],
		Confidence:    0.9,
		Source:        "semantic",
	})

	resp := h.sendRPC(sess, 104, "tools/call", map[string]interface{}{
		"name": "review_diff_against_requirements",
		"arguments": map[string]interface{}{
			"repository_id": repoID,
			"files":         []string{"api.go"},
		},
	})

	result := parseDiffReviewResult(t, resp)

	linkedReqs, _ := result["linked_requirements"].([]interface{})
	if len(linkedReqs) != 1 {
		t.Fatalf("linked_requirements: expected 1, got %d: %v", len(linkedReqs), linkedReqs)
	}

	lr := linkedReqs[0].(map[string]interface{})
	if lr["id"] != req.ID {
		t.Errorf("linked_requirements[0].id: got %v, want %s", lr["id"], req.ID)
	}
	if lr["external_id"] != "CPD-1" {
		t.Errorf("linked_requirements[0].external_id: got %v, want CPD-1", lr["external_id"])
	}
	if lr["title"] != "Compound req title" {
		t.Errorf("linked_requirements[0].title: got %v, want 'Compound req title'", lr["title"])
	}
	if lr["priority"] != "high" {
		t.Errorf("linked_requirements[0].priority: got %v, want high", lr["priority"])
	}
}

// ---------------------------------------------------------------------------
// Case 6: Unlinked public surface included
// ---------------------------------------------------------------------------

// TestCallReviewDiffAgainstRequirements_UnlinkedPublicSurfaceIncluded verifies
// that a touched public symbol with no requirement link appears in
// unlinked_public_surface, while a non-public symbol (internalFn) does not.
func TestCallReviewDiffAgainstRequirements_UnlinkedPublicSurfaceIncluded(t *testing.T) {
	h := newTestHarness(t)
	repoID, symIDs := seedCompoundFixture(t, h)
	sess := h.createSession()

	// Link Helper (util.go, public) to a requirement so it does NOT appear
	// in unlinked_public_surface.
	if err := h.store.StoreRequirement(t.Context(), repoID, &graphstore.StoredRequirement{
		ID:         "cpd-req-2",
		ExternalID: "CPD-2",
		Title:      "Helper requirement",
		Priority:   "medium",
	}); err != nil {
		t.Fatalf("StoreRequirement: %v", err)
	}
	req := h.store.GetRequirementByExternalID(t.Context(), repoID, "CPD-2")
	if req == nil {
		t.Fatal("failed to retrieve seeded requirement CPD-2")
	}
	h.store.StoreLink(t.Context(), repoID, &graphstore.StoredLink{
		RequirementID: req.ID,
		SymbolID:      symIDs["Helper"],
		Confidence:    0.8,
		Source:        "semantic",
	})

	// Touch both api.go and util.go. Expected outcome:
	//   - PublicFunc (api.go, public, no link) → in unlinked_public_surface
	//   - internalFn (api.go, not public)      → NOT in unlinked_public_surface
	//   - Helper     (util.go, public, linked) → NOT in unlinked_public_surface
	resp := h.sendRPC(sess, 105, "tools/call", map[string]interface{}{
		"name": "review_diff_against_requirements",
		"arguments": map[string]interface{}{
			"repository_id": repoID,
			"files":         []string{"api.go", "util.go"},
		},
	})

	result := parseDiffReviewResult(t, resp)

	unlinked, _ := result["unlinked_public_surface"].([]interface{})

	// Collect symbol names in unlinked_public_surface.
	unlinkedNames := map[string]bool{}
	for _, entry := range unlinked {
		m, _ := entry.(map[string]interface{})
		name, _ := m["symbol_name"].(string)
		unlinkedNames[name] = true
	}

	if !unlinkedNames["PublicFunc"] {
		t.Errorf("unlinked_public_surface: expected PublicFunc to appear; got %v", unlinkedNames)
	}
	if unlinkedNames["internalFn"] {
		t.Errorf("unlinked_public_surface: internalFn (non-public) must not appear; got %v", unlinkedNames)
	}
	if unlinkedNames["Helper"] {
		t.Errorf("unlinked_public_surface: Helper is linked to a requirement and must not appear; got %v", unlinkedNames)
	}

	// Each entry must carry the required fields.
	for _, entry := range unlinked {
		m, _ := entry.(map[string]interface{})
		if m["symbol_id"] == "" {
			t.Error("unlinked_public_surface entry missing symbol_id")
		}
		if m["file_path"] == "" {
			t.Error("unlinked_public_surface entry missing file_path")
		}
		if m["kind"] == "" {
			t.Error("unlinked_public_surface entry missing kind")
		}
	}
}
