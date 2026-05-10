// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/changewatch"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
	"github.com/sourcebridge/sourcebridge/internal/indexer/testfixtures"
)

// TestPhase1_EndToEnd_ChangeWatchToFreshness is the cross-package
// integration sweep for Phase 1 (1.A → 1.B → 1.C → 1.D). It exercises
// the closed loop in the order a real edit travels:
//
//  1. Indexer seeds an IndexResult onto a real on-disk fixture repo.
//  2. fsnotify Watcher detects an out-of-band file edit.
//  3. Router validates schema + branch, runs delta-only guardrails,
//     calls IndexFiles under the T0 budget.
//  4. MergeIndexResult applies the per-file delta to the graph store.
//  5. ImpactApplier (the resolver-side post-impact hook) runs.
//  6. Router updates the freshness envelope state.
//  7. Reading freshness via the same FreshnessForExport path the MCP
//     layer's adapter uses returns the post-edit state with state="fresh"
//     and a populated reason.
//
// What this test confirms that lower-level tests don't:
//   - Watcher → Router wiring works end-to-end with the real fsnotify
//     backend (the watcher's classify path, the resolved-symlink trick
//     for macOS, the debounce flush, all together).
//   - The ChangeEvent shape produced by the watcher passes the router's
//     full validation chain (schema, branch, dedup, rate, breaker,
//     containment) without any hand-written shortcuts.
//   - The freshness state surfaced by FreshnessForExport (the function
//     the MCP envelope adapter reads) reflects the edit attribution
//     correctly across the package boundary.
//
// What this test does NOT cover:
//   - The HTTP ingress and record_change MCP tool — those have their
//     own per-tool tests in internal/api/rest. They submit through the
//     same ChangeEventDispatcher interface so the router pipeline is
//     identical; running them through this sweep would 3x the runtime
//     without adding signal.
//   - The real tree-sitter indexer — Phase 1.B has the
//     IndexFiles-against-real-parser test (the 100ms-budget on a
//     500-file fixture); this sweep uses the package-local stub
//     indexer to keep the sweep fast (~300ms wall-clock).
//
// The test is goldenly skip-protected behind testing.Short.
func TestPhase1_EndToEnd_ChangeWatchToFreshness(t *testing.T) {
	if testing.Short() {
		t.Skip("integration sweep — touches the real filesystem and runs the watcher; skip in -short mode")
	}

	repoPath := testfixtures.LargeGoRepo(t, testfixtures.LargeGoRepoSpec{
		FileCount:      10,
		PackageBuckets: 2,
		Branch:         "main",
	})
	if resolved, err := filepath.EvalSymlinks(repoPath); err == nil {
		repoPath = resolved
	}

	store := graphstore.NewStore()
	repo, err := store.CreateRepository(t.Context(), "phase1-sweep", repoPath)
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	prev := &indexer.IndexResult{
		RepoName: "phase1-sweep",
		RepoPath: repoPath,
		Branch:   "main",
		Files: []indexer.FileResult{
			{Path: "pkg0/file1.go", Language: "go", LineCount: 10, ContentHash: "h1"},
			{Path: "pkg0/file2.go", Language: "go", LineCount: 10, ContentHash: "h2"},
		},
	}
	if _, err := store.ReplaceIndexResult(t.Context(), repo.ID, prev); err != nil {
		t.Fatalf("ReplaceIndexResult: %v", err)
	}

	idx := &recordingIndexer{}
	impact := &recordingImpact{}
	cfg := changewatch.Config{
		Enabled:           true,
		RateLimitPerMin:   100,
		RepoBreakerPerMin: 100,
		T0BudgetMs:        500,
		DedupWindow:       10 * time.Second,
	}
	router := changewatch.NewRouter(cfg, store, idx, impact, changewatch.HeadRefBranchValidator{})
	router.SeedPrevious(repo.ID, prev)

	watcher, err := changewatch.NewWatcher(router, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	t.Cleanup(func() { _ = watcher.Close() })
	if err := watcher.Watch(repo.ID, repoPath); err != nil {
		t.Fatalf("watcher.Watch: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = watcher.Run(ctx) }()

	beforeFresh := router.FreshnessForExport(repo.ID)
	if beforeFresh.State != "fresh" {
		t.Errorf("baseline freshness.state=%q, want %q", beforeFresh.State, "fresh")
	}

	// Out-of-band edit: agent in their IDE, no SourceBridge API call.
	edited := filepath.Join(repoPath, "pkg0", "file1.go")
	bytesBefore, err := os.ReadFile(edited)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(edited, append(bytesBefore, []byte("\n// phase1 sweep edit\n")...), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		fresh := router.FreshnessForExport(repo.ID)
		if fresh.LastVerifiedAt.After(beforeFresh.LastVerifiedAt) {
			// Cross-package contract assertions:
			if fresh.State != "fresh" {
				t.Errorf("post-edit freshness.state=%q, want %q", fresh.State, "fresh")
			}
			if fresh.Branch != "main" {
				t.Errorf("post-edit freshness.branch=%q, want %q", fresh.Branch, "main")
			}
			if fresh.PartialRefresh {
				t.Errorf("post-edit freshness.partial_refresh=true, want false (full refresh ran)")
			}
			if fresh.Tier != "T0" {
				t.Errorf("post-edit freshness.tier=%q, want T0", fresh.Tier)
			}
			if fresh.Reason == "" {
				t.Errorf("post-edit freshness.reason is empty (attribution dropped)")
			}
			if idx.callCount() == 0 {
				t.Errorf("IndexFiles never called — watcher → router wiring broken")
			}
			if impact.callCount() == 0 {
				t.Errorf("ImpactApplier never invoked — post-impact wiring broken")
			}
			lastFiles := idx.lastFiles()
			matched := false
			for _, f := range lastFiles {
				if f == "pkg0/file1.go" {
					matched = true
					break
				}
			}
			if !matched {
				t.Errorf("IndexFiles call did not include pkg0/file1.go (got %v)", lastFiles)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("freshness did not advance within 5s of out-of-band edit (last_verified_at=%v)", beforeFresh.LastVerifiedAt)
}

// recordingIndexer is the cross-package test stub for the changewatch
// Indexer interface. We can't use changewatch's package-local stub
// from this integration test, so we declare a tiny mirror here.
type recordingIndexer struct {
	calls [][]string
}

func (r *recordingIndexer) IndexFiles(_ context.Context, repoPath string, files []string, branch string, prev *indexer.IndexResult) (*indexer.IndexResult, error) {
	cp := append([]string(nil), files...)
	r.calls = append(r.calls, cp)
	out := *prev
	out.Branch = branch
	return &out, nil
}

func (r *recordingIndexer) callCount() int { return len(r.calls) }
func (r *recordingIndexer) lastFiles() []string {
	if len(r.calls) == 0 {
		return nil
	}
	return r.calls[len(r.calls)-1]
}

// recordingImpact is the cross-package test stub for the changewatch
// ImpactApplier interface.
type recordingImpact struct {
	calls int
}

func (r *recordingImpact) ApplyImpact(_ context.Context, _ string, _ *graphstore.ImpactReport) {
	r.calls++
}

func (r *recordingImpact) callCount() int { return r.calls }
