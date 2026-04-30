// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package indexer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/indexer/testfixtures"
)

// TestIndexFiles_DeltaBudgetUnder100ms is Phase 1 done-definition test
// #6: on a >=500-file fixture, IndexFiles for a single-file delta must
// complete within the 100ms T0 budget that the change-watch router
// (Phase 1.C) and the T0 sync-refresh path (Phase 1.C) both enforce.
//
// The test flow:
//   1. Materialize a synthetic 500-file Go repository.
//   2. Full-index it once via IndexRepository to produce previousResult
//      (analogous to a prior IndexRepositoryIncremental result on a
//      real repo's first index).
//   3. Edit one file on disk to simulate an out-of-band agent edit.
//   4. Call IndexFiles for that one file. Time wall-clock latency.
//   5. Assert latency under 100ms AND that the merged result reflects
//      the edit (parsed symbols carry the new content) AND every other
//      file is carried forward unchanged.
//
// Why measure wall-clock and not CPU: the budget IS wall-clock — the
// agent's MCP read pauses on this call, so the user-perceived latency
// is what matters. Every reviewer (and CI) hits this same metric.
//
// CI slack: the assertion is 100ms — the same number the plan
// specifies as the T0 budget — to keep the contract honest. The
// pre-flight spike measured 13.8ms average on the same fixture shape;
// the ~7x headroom is enough that GitHub Actions shared runners (which
// are slower than local hardware) should still pass cleanly. If a
// future CI environment cannot, the right move is to investigate the
// regression, not to loosen the test — the budget is load-bearing.
func TestIndexFiles_DeltaBudgetUnder100ms(t *testing.T) {
	repo := testfixtures.LargeGoRepo(t, testfixtures.LargeGoRepoSpec{
		FileCount:      500,
		PackageBuckets: 10,
		Branch:         "main",
	})

	idx := NewIndexer(nil)

	// Build the baseline IndexResult.
	prev, err := idx.IndexRepository(context.Background(), repo, ReasonInitialOnboard)
	if err != nil {
		t.Fatalf("baseline IndexRepository: %v", err)
	}
	if prev.TotalFiles != 500 {
		t.Fatalf("baseline TotalFiles = %d, want 500", prev.TotalFiles)
	}

	// Pick a deterministic file to edit. With FileCount=500 and
	// PackageBuckets=10, the generator writes files 1..50 into pkg0,
	// 51..100 into pkg1, etc. pkg4/file250.go is the last file in
	// bucket 4 — far enough into the generation that any
	// off-by-one in the generator would surface.
	target := "pkg4/file250.go"
	if _, statErr := os.Stat(filepath.Join(repo, target)); statErr != nil {
		t.Fatalf("expected fixture file %s: %v", target, statErr)
	}
	// And pick a different bucket for the carry-forward sanity check
	// later, so a bug that scopes the merge to a single bucket
	// surfaces.
	const carryTarget = "pkg0/file1.go"

	// Simulate an agent edit: add a new exported function to the file
	// so the merged result must reflect a symbol that wasn't in the
	// baseline.
	edited := mustReadFile(t, filepath.Join(repo, target)) + `

// AgentAdded was added by the simulated agent edit in
// TestIndexFiles_DeltaBudgetUnder100ms. Its presence in the merged
// result is what proves IndexFiles re-parsed the file rather than
// reusing the baseline's stale FileResult.
func AgentAdded(input string) string {
	return input + "-agent"
}
`
	testfixtures.WriteFile(t, repo, target, edited)

	// Warm tree-sitter (the first parse pays a one-time grammar load
	// cost that is not the steady-state budget). The plan's 100ms
	// budget is the steady-state budget per IndexFiles invocation, not
	// the very first invocation in a process. The router (Phase 1.C)
	// runs IndexFiles many times per process; the warm-up only happens
	// once.
	if _, warmErr := idx.IndexFiles(context.Background(), repo, []string{target}, "main", prev); warmErr != nil {
		t.Fatalf("warmup IndexFiles: %v", warmErr)
	}

	// Re-edit (the warm-up call may have produced a result we didn't
	// stash; re-edit + re-time so we measure a fresh delta).
	testfixtures.WriteFile(t, repo, target, edited)

	t0 := time.Now()
	got, err := idx.IndexFiles(context.Background(), repo, []string{target}, "main", prev)
	elapsed := time.Since(t0)
	if err != nil {
		t.Fatalf("IndexFiles: %v", err)
	}

	if elapsed > 100*time.Millisecond {
		t.Fatalf("IndexFiles single-file delta on 500-file fixture took %s, exceeds 100ms T0 budget; investigate regression rather than loosening the assertion (the budget is load-bearing per Phase 1 done-definition test #6)", elapsed)
	}
	t.Logf("IndexFiles single-file delta on 500-file fixture: %s (budget 100ms, headroom ~%dx)", elapsed, int(100*time.Millisecond/elapsed))

	// Assert the merged result reflects the edit.
	if got.TotalFiles != 500 {
		t.Fatalf("merged TotalFiles = %d, want 500 (no add/drop)", got.TotalFiles)
	}
	if got.Branch != "main" {
		t.Fatalf("merged Branch = %q, want %q", got.Branch, "main")
	}

	var found *FileResult
	for i := range got.Files {
		if got.Files[i].Path == target {
			found = &got.Files[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("merged result missing %s", target)
	}
	hasAgentAdded := false
	for _, sym := range found.Symbols {
		if sym.Name == "AgentAdded" {
			hasAgentAdded = true
			break
		}
	}
	if !hasAgentAdded {
		t.Fatalf("merged FileResult for %s did not include the AgentAdded symbol; IndexFiles must have re-parsed the file. Symbols seen: %d", target, len(found.Symbols))
	}

	// Carry-forward: a non-affected file must match the baseline byte
	// for byte (same FileResult, same hash, same symbols).
	var prevCarry, gotCarry *FileResult
	for i := range prev.Files {
		if prev.Files[i].Path == carryTarget {
			prevCarry = &prev.Files[i]
			break
		}
	}
	for i := range got.Files {
		if got.Files[i].Path == carryTarget {
			gotCarry = &got.Files[i]
			break
		}
	}
	if prevCarry == nil || gotCarry == nil {
		t.Fatalf("carry-forward target missing: prev=%v got=%v", prevCarry != nil, gotCarry != nil)
	}
	if prevCarry.ContentHash != gotCarry.ContentHash {
		t.Fatalf("carry-forward ContentHash drift on %s: prev=%q got=%q", carryTarget, prevCarry.ContentHash, gotCarry.ContentHash)
	}
	if len(prevCarry.Symbols) != len(gotCarry.Symbols) {
		t.Fatalf("carry-forward Symbol count drift on %s: prev=%d got=%d", carryTarget, len(prevCarry.Symbols), len(gotCarry.Symbols))
	}

	// previousResult must be unchanged (non-mutation contract).
	if prev.Branch != "" {
		t.Fatalf("previousResult.Branch was mutated: %q (baseline Indexer paths leave Branch empty)", prev.Branch)
	}
	if prev.TotalFiles != 500 {
		t.Fatalf("previousResult.TotalFiles was mutated: %d", prev.TotalFiles)
	}
}

// TestIndexFiles_BranchMismatchRejected is the in-process half of
// Phase 1 done-definition test #12: when the caller's claimed branch
// does not match git.HeadRef on the working tree, IndexFiles rejects
// with ErrBranchMismatch and includes both branches in the error
// message. This is the load-bearing guard for Risk #4.
//
// The router-level half (where the watcher's detected branch is
// compared against the change event's claimed branch BEFORE IndexFiles
// is even invoked) lands in Phase 1.C; this test exercises only the
// in-process boundary. A separate placeholder test below
// (TestIndexFiles_RouterBranchMismatch_DeferredTo1C) marks the
// deferred half so 1.C has a hook to fill in.
func TestIndexFiles_BranchMismatchRejected(t *testing.T) {
	repo := testfixtures.LargeGoRepo(t, testfixtures.LargeGoRepoSpec{
		FileCount:      10,
		PackageBuckets: 2,
		Branch:         "main",
	})

	idx := NewIndexer(nil)
	prev, err := idx.IndexRepository(context.Background(), repo, ReasonInitialOnboard)
	if err != nil {
		t.Fatalf("baseline IndexRepository: %v", err)
	}

	// Caller claims feature/x while the working tree is on main.
	got, err := idx.IndexFiles(context.Background(), repo, []string{"pkg0/file1.go"}, "feature/x", prev)
	if err == nil {
		t.Fatalf("IndexFiles with mismatched branch returned nil error; want ErrBranchMismatch")
	}
	if got != nil {
		t.Fatalf("IndexFiles with mismatched branch returned non-nil result: %+v", got)
	}
	if !errors.Is(err, ErrBranchMismatch) {
		t.Fatalf("err = %v, want errors.Is ErrBranchMismatch", err)
	}
	// The wrapped message must include both branches so the router's
	// rejected_branch_mismatch log entry has the diagnostic data the
	// plan specifies.
	if !strings.Contains(err.Error(), `claimed="feature/x"`) || !strings.Contains(err.Error(), `head="main"`) {
		t.Fatalf("err message missing claimed/head: %v", err)
	}
}

// TestIndexFiles_BranchMatchAccepted is the positive twin of the
// branch-mismatch test: when the caller's claimed branch matches HEAD,
// IndexFiles records that branch on the returned IndexResult so the
// freshness envelope (Phase 1.C) can propagate it to MCP reads.
func TestIndexFiles_BranchMatchAccepted(t *testing.T) {
	repo := testfixtures.LargeGoRepo(t, testfixtures.LargeGoRepoSpec{
		FileCount:      10,
		PackageBuckets: 2,
		Branch:         "feature/branch-thread",
	})

	idx := NewIndexer(nil)
	prev, err := idx.IndexRepository(context.Background(), repo, ReasonInitialOnboard)
	if err != nil {
		t.Fatalf("baseline IndexRepository: %v", err)
	}

	got, err := idx.IndexFiles(context.Background(), repo, []string{"pkg0/file1.go"}, "feature/branch-thread", prev)
	if err != nil {
		t.Fatalf("IndexFiles: %v", err)
	}
	if got.Branch != "feature/branch-thread" {
		t.Fatalf("merged Branch = %q, want %q", got.Branch, "feature/branch-thread")
	}
}

// TestIndexFiles_RouterBranchMismatch_DeferredTo1C is a placeholder
// for the router-level half of Phase 1 done-definition test #12. It
// is skipped here because the changewatch router does not exist yet
// (Phase 1.C work). The test name and skip-message are pinned so 1.C
// has a clean hook to fill in.
//
// What the deferred test must cover:
//   - Watcher detects an fsnotify event on branch feature/x.
//   - Router builds a ChangeEvent with branch="feature/x".
//   - Router compares ChangeEvent.branch against
//     git.HeadRef(repoPath); on the happy path they match.
//   - Branch-mismatch path: a record_change event claims branch="main"
//     while the working tree HEAD is on feature/x. The router rejects
//     the event with rejected_branch_mismatch BEFORE IndexFiles is
//     invoked; both branches are in the structured log.
//
// Mirrors the 1.A pattern (TestIndexRepository_RouterPathDeferred)
// established for cross-phase placeholders.
func TestIndexFiles_RouterBranchMismatch_DeferredTo1C(t *testing.T) {
	t.Skip("router-level branch-mismatch validation lands in Phase 1.C (internal/changewatch); this in-process test covers only the indexer boundary")
}

// TestIndexFiles_EmptyFilesRejected covers the input-validation
// boundary. The router (Phase 1.C) enforces the non-empty-delta
// guardrail at its own boundary; the indexer surfaces it as a
// programming error so a regression that lets an empty delta through
// the router is caught loudly here too.
func TestIndexFiles_EmptyFilesRejected(t *testing.T) {
	repo := testfixtures.LargeGoRepo(t, testfixtures.LargeGoRepoSpec{
		FileCount:      5,
		PackageBuckets: 1,
	})
	idx := NewIndexer(nil)
	prev, _ := idx.IndexRepository(context.Background(), repo, ReasonInitialOnboard)

	got, err := idx.IndexFiles(context.Background(), repo, nil, "main", prev)
	if got != nil {
		t.Fatalf("IndexFiles with nil files returned non-nil result")
	}
	if !errors.Is(err, ErrEmptyFiles) {
		t.Fatalf("err = %v, want errors.Is ErrEmptyFiles", err)
	}

	got, err = idx.IndexFiles(context.Background(), repo, []string{}, "main", prev)
	if got != nil {
		t.Fatalf("IndexFiles with empty files returned non-nil result")
	}
	if !errors.Is(err, ErrEmptyFiles) {
		t.Fatalf("err = %v, want errors.Is ErrEmptyFiles", err)
	}
}

// TestIndexFiles_NilPreviousResultRejected covers the second
// programming-error boundary.
func TestIndexFiles_NilPreviousResultRejected(t *testing.T) {
	repo := testfixtures.LargeGoRepo(t, testfixtures.LargeGoRepoSpec{
		FileCount:      5,
		PackageBuckets: 1,
	})
	idx := NewIndexer(nil)

	got, err := idx.IndexFiles(context.Background(), repo, []string{"pkg0/file1.go"}, "main", nil)
	if got != nil {
		t.Fatalf("IndexFiles with nil previousResult returned non-nil result")
	}
	if !errors.Is(err, ErrPreviousResultRequired) {
		t.Fatalf("err = %v, want errors.Is ErrPreviousResultRequired", err)
	}
}

// TestIndexFiles_FileDeletion covers the deletion path: a file in the
// affected list that no longer exists on disk is dropped from the
// merged result.
func TestIndexFiles_FileDeletion(t *testing.T) {
	repo := testfixtures.LargeGoRepo(t, testfixtures.LargeGoRepoSpec{
		FileCount:      10,
		PackageBuckets: 2,
	})
	idx := NewIndexer(nil)
	prev, err := idx.IndexRepository(context.Background(), repo, ReasonInitialOnboard)
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if prev.TotalFiles != 10 {
		t.Fatalf("baseline files = %d, want 10", prev.TotalFiles)
	}

	// Delete one file from disk.
	target := "pkg0/file1.go"
	if err := os.Remove(filepath.Join(repo, target)); err != nil {
		t.Fatalf("remove %s: %v", target, err)
	}

	got, err := idx.IndexFiles(context.Background(), repo, []string{target}, "main", prev)
	if err != nil {
		t.Fatalf("IndexFiles: %v", err)
	}
	if got.TotalFiles != 9 {
		t.Fatalf("merged TotalFiles = %d, want 9 (after deletion)", got.TotalFiles)
	}
	for _, f := range got.Files {
		if f.Path == target {
			t.Fatalf("deleted file %s still present in merged result", target)
		}
	}

	// previousResult unchanged.
	if prev.TotalFiles != 10 {
		t.Fatalf("previousResult.TotalFiles was mutated: %d", prev.TotalFiles)
	}
}

// TestIndexFiles_NewFileAddition covers the addition path: a file in
// the affected list that was not in previousResult is appended to the
// merged result.
func TestIndexFiles_NewFileAddition(t *testing.T) {
	repo := testfixtures.LargeGoRepo(t, testfixtures.LargeGoRepoSpec{
		FileCount:      10,
		PackageBuckets: 2,
	})
	idx := NewIndexer(nil)
	prev, err := idx.IndexRepository(context.Background(), repo, ReasonInitialOnboard)
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}

	newRel := "pkg0/newfile.go"
	testfixtures.WriteFile(t, repo, newRel, "package pkg0\n\nfunc Added() int { return 42 }\n")

	got, err := idx.IndexFiles(context.Background(), repo, []string{newRel}, "main", prev)
	if err != nil {
		t.Fatalf("IndexFiles: %v", err)
	}
	if got.TotalFiles != 11 {
		t.Fatalf("merged TotalFiles = %d, want 11 (after addition)", got.TotalFiles)
	}
	found := false
	for _, f := range got.Files {
		if f.Path == newRel {
			found = true
			hasAdded := false
			for _, s := range f.Symbols {
				if s.Name == "Added" {
					hasAdded = true
					break
				}
			}
			if !hasAdded {
				t.Fatalf("added file present but missing Added symbol")
			}
			break
		}
	}
	if !found {
		t.Fatalf("added file %s not in merged result", newRel)
	}
}

// TestIndexFiles_ContextCancellation covers the cooperative-cancel
// boundary: a context cancelled mid-batch returns ctx.Err() promptly.
func TestIndexFiles_ContextCancellation(t *testing.T) {
	repo := testfixtures.LargeGoRepo(t, testfixtures.LargeGoRepoSpec{
		FileCount:      10,
		PackageBuckets: 2,
	})
	idx := NewIndexer(nil)
	prev, _ := idx.IndexRepository(context.Background(), repo, ReasonInitialOnboard)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	got, err := idx.IndexFiles(ctx, repo, []string{"pkg0/file1.go"}, "main", prev)
	if got != nil {
		t.Fatalf("got non-nil result on cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestIndexFiles_NotAGitRepo covers the working-tree validation
// failure mode. A non-git path can't have its branch validated, so
// IndexFiles wraps git.ErrNotAGitRepo cleanly.
func TestIndexFiles_NotAGitRepo(t *testing.T) {
	dir := t.TempDir() // not git-init'd
	idx := NewIndexer(nil)
	prev := &IndexResult{RepoName: "x", RepoPath: dir, Files: []FileResult{}}

	got, err := idx.IndexFiles(context.Background(), dir, []string{"missing.go"}, "main", prev)
	if got != nil {
		t.Fatalf("got non-nil result for non-git dir")
	}
	if err == nil || !strings.Contains(err.Error(), "validating branch") {
		t.Fatalf("err = %v, want wrapped HeadRef error", err)
	}
}

// mustReadFile is a tiny test-helper for reading file contents
// without forcing every test to plumb the same boilerplate.
func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
