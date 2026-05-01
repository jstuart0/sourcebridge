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

// TestIndexFiles_DeltaBudget is Phase 1 done-definition test #6: on a
// >=500-file fixture, IndexFiles for a single-file delta must be
// substantially faster than a full IndexRepository on the same fixture.
// This is the load-bearing performance contract for the change-watch
// router (Phase 1.C) and the T0 sync-refresh path (Phase 1.C); a
// regression where IndexFiles silently falls back to full-index would
// blow the agent's MCP read budget.
//
// The test flow:
//  1. Materialize a synthetic 500-file Go repository.
//  2. Full-index it once via IndexRepository — captures the baseline
//     wall-clock time AND produces previousResult (analogous to a
//     prior IndexRepositoryIncremental on a real repo).
//  3. Edit one file on disk to simulate an out-of-band agent edit.
//  4. Warm tree-sitter (one-time grammar load cost is not the
//     steady-state budget).
//  5. Time IndexFiles for the single-file delta (steady state).
//  6. Assert: (a) delta is substantially faster than the baseline
//     (ratio gate — catches "delta accidentally re-indexed everything"
//     regressions), (b) delta is under a generous absolute upper bound
//     (sanity gate — catches catastrophic regressions even on the
//     slowest runners), (c) the merged result reflects the edit,
//     (d) every unaffected file is carried forward, (e) previousResult
//     is not mutated.
//
// Why ratio + sanity instead of a single absolute budget: the original
// test asserted `< 100ms` wall-clock, which is the right number for
// a developer laptop but marginal for a shared GitHub Actions runner
// where IO and scheduler jitter can push a 14ms operation past 100ms
// without any code regression. The ratio gate is hardware-independent
// (slower runners take longer for both baseline and delta, so the
// ratio stays roughly constant) and catches the regression class the
// 100ms gate was actually defending against — a delta-path bug that
// re-walks the full repo would push the ratio toward 1.0 immediately.
// The sanity gate (500ms) is a soft ceiling that fires only on truly
// pathological regressions that the ratio gate might miss on an
// already-slow baseline.
func TestIndexFiles_DeltaBudget(t *testing.T) {
	repo := testfixtures.LargeGoRepo(t, testfixtures.LargeGoRepoSpec{
		FileCount:      500,
		PackageBuckets: 10,
		Branch:         "main",
	})

	idx := NewIndexer(nil)

	// Build the baseline IndexResult AND time the full-index for the
	// ratio gate below.
	tBaseline := time.Now()
	prev, err := idx.IndexRepository(context.Background(), repo, ReasonInitialOnboard)
	baselineElapsed := time.Since(tBaseline)
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
// TestIndexFiles_DeltaBudget. Its presence in the merged
// result is what proves IndexFiles re-parsed the file rather than
// reusing the baseline's stale FileResult.
func AgentAdded(input string) string {
	return input + "-agent"
}
`
	testfixtures.WriteFile(t, repo, target, edited)

	// Warm tree-sitter (the first parse pays a one-time grammar load
	// cost that is not the steady-state budget). The router (Phase 1.C)
	// runs IndexFiles many times per process; the warm-up only happens
	// once. We measure the SECOND call so the ratio gate compares
	// like-for-like with the (already-warm) baseline IndexRepository.
	if _, warmErr := idx.IndexFiles(context.Background(), repo, []string{target}, "main", prev); warmErr != nil {
		t.Fatalf("warmup IndexFiles: %v", warmErr)
	}

	// Re-edit (the warm-up call may have produced a result we didn't
	// stash; re-edit + re-time so we measure a fresh delta).
	testfixtures.WriteFile(t, repo, target, edited)

	t0 := time.Now()
	got, err := idx.IndexFiles(context.Background(), repo, []string{target}, "main", prev)
	deltaElapsed := time.Since(t0)
	if err != nil {
		t.Fatalf("IndexFiles: %v", err)
	}

	// Ratio gate: a single-file delta should re-parse 1 file out of
	// 500. Even with constant per-call overhead, a healthy delta
	// should be at least 4x faster than the baseline full-index. We
	// gate at 25% (delta/baseline ≤ 0.25) — substantial slack over
	// the ~70x speedup the steady-state observes locally, but tight
	// enough that any regression collapsing the delta path back to
	// full-index (ratio ≈ 1.0) blows it immediately.
	const maxDeltaRatio = 0.25
	ratio := float64(deltaElapsed) / float64(baselineElapsed)
	if ratio > maxDeltaRatio {
		t.Fatalf("IndexFiles single-file delta took %s vs full-index baseline %s (ratio %.3f > max %.2f); the delta path is likely re-indexing files it shouldn't — investigate the regression rather than loosening the assertion (this is the load-bearing perf contract of the change-watch router)",
			deltaElapsed, baselineElapsed, ratio, maxDeltaRatio)
	}

	// Sanity gate: even a slow shared runner shouldn't take longer
	// than 500ms for a single-file delta on a 500-file fixture. This
	// catches catastrophic regressions (a 100x slowdown that drags
	// the baseline along with it would still satisfy the ratio gate
	// but is clearly broken). 500ms is generous — observed local
	// runs are 10-15ms and the original 100ms gate passed in CI when
	// the runner cooperated.
	const sanityMax = 500 * time.Millisecond
	if deltaElapsed > sanityMax {
		t.Fatalf("IndexFiles single-file delta took %s, exceeds %s sanity ceiling; even on a slow CI runner this should be far quicker — investigate the regression",
			deltaElapsed, sanityMax)
	}

	t.Logf("IndexFiles delta perf: baseline=%s delta=%s ratio=%.3f (gate ≤ %.2f, sanity ceiling %s)",
		baselineElapsed, deltaElapsed, ratio, maxDeltaRatio, sanityMax)

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
// is even invoked) lives in Phase 1.C as
// internal/changewatch.TestRouter_RejectsBranchMismatch; the
// indexer-side guard below (TestIndexFiles_RouterBranchMismatchHookedTo1C)
// pins the cross-package reference so a rename of the canonical test
// surfaces here too.
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

// TestIndexFiles_RouterBranchMismatchHookedTo1C is the cross-phase
// pointer for the router-level half of Phase 1 done-definition test
// #12. The canonical assertion lives in
// internal/changewatch.TestRouter_RejectsBranchMismatch (Phase 1.C).
//
// We can't import internal/changewatch from internal/indexer (that's
// the wrong direction — changewatch imports indexer for IndexResult
// and Indexer), so this test verifies via source scan that the
// canonical test still exists in the changewatch package. A future
// rename or accidental delete of the canonical test fails this guard
// at the indexer boundary, which is where readers expect to find the
// branch-threading invariant documented.
func TestIndexFiles_RouterBranchMismatchHookedTo1C(t *testing.T) {
	canonicalTest := "TestRouter_RejectsBranchMismatch"
	pkgFile := filepath.Join("..", "changewatch", "router_test.go")
	body, err := os.ReadFile(pkgFile)
	if err != nil {
		t.Fatalf("cannot read internal/changewatch/router_test.go: %v (Phase 1.C must ship this file)", err)
	}
	if !strings.Contains(string(body), "func "+canonicalTest+"(") {
		t.Errorf("internal/changewatch/router_test.go is missing %q — the router-level branch-mismatch test (#12 router half) must live in the changewatch package. Update both this guard and the canonical test if the test name changes.", canonicalTest)
	}
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
