// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package changewatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
	"github.com/sourcebridge/sourcebridge/internal/indexer/testfixtures"
)

// TestIntegration_ExternalEditFlowsToFreshness is Phase 1
// done-definition test #1: an out-of-band edit on disk is detected by
// the fsnotify watcher, debounced into a ChangeEvent, dispatched
// through the router's full pipeline (rate / breaker / dedup / branch /
// merge / impact / freshness), and surfaces in FreshnessForExport
// within ~3s.
//
// The test exercises the *real* fsnotify Watcher and the *real* Router.
// The Indexer dependency is the reusable stubIndexer from router_test.go
// so the test focuses on the loop-close behavior — Phase 1.B already
// covers indexer correctness end-to-end with the real tree-sitter chain
// (TestIndexFiles_DeltaBudgetUnder100ms,
// TestReindexRepository_AppliesImpactFromChange_EndToEnd). Mixing in
// the real indexer here would slow this test ~10x without adding
// signal beyond what those existing tests already provide.
//
// What makes this an "external" integration test:
//   - Real on-disk git working tree (testfixtures.LargeGoRepo creates one).
//   - Real fsnotify subscriptions on the working-tree directories.
//   - Real os.WriteFile to mutate a file outside any SourceBridge code path.
//   - Real branch validation against the git working tree (no stubbed
//     BranchValidator — uses the production HeadRefBranchValidator).
//   - Real freshness envelope read via Router.FreshnessForExport,
//     which is the same call site the MCP layer uses
//     (internal/api/rest/mcp_freshness.go).
//
// The plan's 3s budget includes the watcher's debounce window
// (Balanced default = 2s) plus indexer dispatch slack. We assert with
// a 5s ceiling so a slow CI box doesn't flake; the typical wall-clock
// is well under 1s after debounce. We also tighten the debounce to
// 200ms here so the test completes quickly — the debounce window's
// specific value is covered by separate watcher tests.
func TestIntegration_ExternalEditFlowsToFreshness(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — touches the real filesystem and runs the watcher; skip in -short mode")
	}

	repoPath := testfixtures.LargeGoRepo(t, testfixtures.LargeGoRepoSpec{
		FileCount:      10,
		PackageBuckets: 2,
		Branch:         "main",
	})
	// Resolve symlinks on the test side too so the seed (which the
	// router uses for repo-resolution) and the watcher (which reports
	// fsnotify events at the resolved path) agree on the path. macOS
	// surfaces the classic /var → /private/var symlink here.
	if resolved, evalErr := filepath.EvalSymlinks(repoPath); evalErr == nil {
		repoPath = resolved
	}

	store := graphstore.NewStore()
	repo, err := store.CreateRepository(t.Context(), "integration-repo", repoPath)
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	prev := &indexer.IndexResult{
		RepoName: "integration-repo",
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

	// Production-side branch validator (real git.HeadRef).
	branches := HeadRefBranchValidator{}
	idx := &stubIndexer{}
	impact := &stubImpact{}

	cfg := Config{
		Enabled:           true,
		RateLimitPerMin:   100,
		RepoBreakerPerMin: 100,
		T0BudgetMs:        500,
		DedupWindow:       10 * time.Second,
	}
	router := NewRouter(cfg, store, idx, impact, branches)
	router.SeedPrevious(repo.ID, prev)

	watcher, err := NewWatcher(router, 200*time.Millisecond)
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

	// Out-of-band edit. We deliberately do NOT go through any
	// SourceBridge API — this is the "agent edited a file in their IDE
	// while the indexer is running" scenario.
	editedPath := filepath.Join(repoPath, "pkg0", "file1.go")
	originalBytes, err := os.ReadFile(editedPath)
	if err != nil {
		t.Fatalf("baseline read: %v", err)
	}
	newBytes := append(originalBytes, []byte("\n// out-of-band edit by integration test\n")...)
	if err := os.WriteFile(editedPath, newBytes, 0o644); err != nil {
		t.Fatalf("os.WriteFile: %v", err)
	}

	// Wait for the loop to close. We poll FreshnessForExport's
	// LastVerifiedAt because that's the same field the MCP envelope
	// surfaces — if it advanced, the agent will see fresh data.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		fresh := router.FreshnessForExport(repo.ID)
		if fresh.LastVerifiedAt.After(beforeFresh.LastVerifiedAt) {
			if fresh.State != "fresh" {
				t.Errorf("freshness.state = %q, want %q", fresh.State, "fresh")
			}
			if fresh.Branch != "main" {
				t.Errorf("freshness.branch = %q, want %q", fresh.Branch, "main")
			}
			if fresh.PartialRefresh {
				t.Errorf("freshness.partial_refresh = true, want false (full refresh)")
			}
			if fresh.Reason == "" {
				t.Errorf("freshness.reason is empty")
			}
			if idx.callCount() == 0 {
				t.Errorf("IndexFiles was never called despite out-of-band edit")
			}
			lastCall, _ := idx.lastCall()
			if lastCall.Branch != "main" {
				t.Errorf("IndexFiles called with branch=%q, want main", lastCall.Branch)
			}
			containsEdit := false
			for _, f := range lastCall.Files {
				if f == "pkg0/file1.go" {
					containsEdit = true
					break
				}
			}
			if !containsEdit {
				t.Errorf("IndexFiles files=%v, expected to contain pkg0/file1.go", lastCall.Files)
			}
			if impact.calls.Load() == 0 {
				t.Errorf("ImpactApplier was never invoked despite successful index")
			}
			return // happy path
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("freshness did not advance within 5s of out-of-band edit (LastVerifiedAt=%v)", beforeFresh.LastVerifiedAt)
}

// TestIntegration_RecordChange_FlowsToFreshness is the in-process
// record_change half of Phase 1 done-definition tests #2 + #5 at the
// router level: the public `record_change` MCP tool surface ships in
// 1.D, but the underlying router contract is exercised here. We
// directly construct a ChangeEvent with source.kind=mcp_record_change
// and submit through the same Router instance that the watcher feeds.
//
// A successful submit advances FreshnessForExport identically to the
// fsnotify path; the only difference is `freshness.reason` reflects
// the agent attribution (per plan §Connector model > attribution).
//
// 1.D will replace this synthetic event construction with a real
// MCP-tool call when the public tool ships; this test pins the
// router-level behavior so 1.D's wire-up needs only to exercise the
// HTTP/MCP plumbing on top.
func TestIntegration_RecordChange_FlowsToFreshness(t *testing.T) {
	repoPath := testfixtures.LargeGoRepo(t, testfixtures.LargeGoRepoSpec{
		FileCount:      10,
		PackageBuckets: 2,
		Branch:         "main",
	})
	if resolved, evalErr := filepath.EvalSymlinks(repoPath); evalErr == nil {
		repoPath = resolved
	}

	store := graphstore.NewStore()
	repo, err := store.CreateRepository(t.Context(), "rc-repo", repoPath)
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	prev := &indexer.IndexResult{
		RepoName: "rc-repo",
		RepoPath: repoPath,
		Branch:   "main",
		Files: []indexer.FileResult{
			{Path: "pkg0/file1.go", Language: "go", LineCount: 10, ContentHash: "h1"},
		},
	}
	if _, err := store.ReplaceIndexResult(t.Context(), repo.ID, prev); err != nil {
		t.Fatalf("ReplaceIndexResult: %v", err)
	}

	idx := &stubIndexer{}
	impact := &stubImpact{}
	router := NewRouter(Config{Enabled: true, T0BudgetMs: 500}, store, idx, impact, HeadRefBranchValidator{})
	router.SeedPrevious(repo.ID, prev)

	beforeFresh := router.FreshnessForExport(repo.ID)

	ev := &ChangeEvent{
		SchemaVersion: ChangeEventSchemaVersion,
		EventID:       "rc-evt-1",
		RepositoryID:  repo.ID,
		OccurredAt:    time.Now(),
		Branch:        "main",
		Files: []FileChange{
			{Path: "pkg0/file1.go", Status: FileChangeModified, ContentHashAfter: "sha256:rcedit"},
		},
		Source: ChangeSource{
			Kind:           SourceKindMCPRecordChange,
			ConnectorID:    "in_process:record_change",
			Actor:          "agent:test",
			Intent:         "test record_change attribution",
			RequirementIDs: []string{"REQ-100"},
		},
		Trust: Trust{Verified: true, VerificationMethod: "in_process", ReceivedVia: "in_process"},
	}

	outcome, err := router.Submit(context.Background(), ev)
	if err != nil {
		t.Fatalf("router.Submit: outcome=%q err=%v", outcome, err)
	}
	if outcome != OutcomeIndexing {
		t.Errorf("outcome = %q, want %q", outcome, OutcomeIndexing)
	}

	fresh := router.FreshnessForExport(repo.ID)
	if !fresh.LastVerifiedAt.After(beforeFresh.LastVerifiedAt) {
		t.Errorf("freshness.LastVerifiedAt did not advance after record_change submit")
	}
	if fresh.State != "fresh" {
		t.Errorf("freshness.state = %q, want %q", fresh.State, "fresh")
	}
	if fresh.Branch != "main" {
		t.Errorf("freshness.branch = %q, want %q", fresh.Branch, "main")
	}
	// Attribution should reflect the agent, per
	// plan §Connector model > attribution. describeReason builds the
	// envelope `reason` from ev.Source.Actor when set.
	if fresh.Reason == "" {
		t.Errorf("freshness.reason is empty (attribution missing)")
	}
	if !strings.Contains(fresh.Reason, "agent:test") {
		t.Errorf("freshness.reason = %q, want to contain agent:test (attribution)", fresh.Reason)
	}

	if idx.callCount() != 1 {
		t.Errorf("IndexFiles call count = %d, want 1", idx.callCount())
	}
	if impact.calls.Load() != 1 {
		t.Errorf("ImpactApplier call count = %d, want 1", impact.calls.Load())
	}
}

// Ensure the empty-import compiler check for graphstore stays loud.
var _ = graphstore.NewStore
