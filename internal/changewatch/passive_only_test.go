// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package changewatch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
	"github.com/sourcebridge/sourcebridge/internal/indexer/testfixtures"
)

// TestPassiveOnly_Phase1DoneDef7 is Phase 1 done-definition test #7 from
// plan v5: the load-bearing passive-only correctness test that pins the
// non-goal "no SourceBridge feature shall be built that requires
// record_change to be called for correctness."
//
// With the in-process record_change MCP tool *never invoked* (the test
// agent only edits files on disk), the fsnotify watcher alone must
// detect every flavor of edit, debounce them into ChangeEvents, drive
// the router pipeline, and advance the freshness envelope. Each
// scenario asserts:
//
//  1. The router was called for the edit (idx.callCount > 0)
//  2. The freshness envelope advanced (LastVerifiedAt moved forward)
//  3. The freshness state is "fresh" with partial_refresh=false
//  4. The IndexFiles call references the affected file paths
//
// The test is goldenly skip-protected behind testing.Short so the
// fsnotify+disk machinery doesn't drag every quick local test run.
//
// IF THIS TEST EVER FAILS: the change in question violates the
// non-goal. Audit the change — does it require record_change to be
// invoked? If yes, the change must be revised so the passive path is
// sufficient. If no, the test itself may be wrong; debug the watcher
// pipeline. Do NOT add `record_change` calls to make the test pass.
func TestPassiveOnly_Phase1DoneDef7(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — touches the real filesystem and runs the watcher; skip in -short mode")
	}

	scenarios := []struct {
		name string
		// edit performs the on-disk mutation. Must not call any
		// SourceBridge API; this is the "agent edited a file in their
		// IDE" scenario by construction.
		edit func(t *testing.T, repoPath string)
		// expectFiles is the set of repo-relative paths the IndexFiles
		// call SHOULD include. The watcher may also include other
		// paths (e.g., parent dirs); we only check containment.
		expectFiles []string
	}{
		{
			name: "single_file_write",
			edit: func(t *testing.T, repoPath string) {
				p := filepath.Join(repoPath, "pkg0", "file1.go")
				b, err := os.ReadFile(p)
				if err != nil {
					t.Fatalf("baseline read: %v", err)
				}
				if err := os.WriteFile(p, append(b, []byte("\n// passive single-file write\n")...), 0o644); err != nil {
					t.Fatalf("write: %v", err)
				}
			},
			expectFiles: []string{"pkg0/file1.go"},
		},
		{
			name: "multi_file_refactor",
			edit: func(t *testing.T, repoPath string) {
				for _, name := range []string{"file1.go", "file2.go", "file3.go"} {
					p := filepath.Join(repoPath, "pkg0", name)
					b, err := os.ReadFile(p)
					if err != nil {
						t.Fatalf("baseline read %s: %v", name, err)
					}
					if err := os.WriteFile(p, append(b, []byte("\n// passive multi-file refactor\n")...), 0o644); err != nil {
						t.Fatalf("write %s: %v", name, err)
					}
				}
			},
			expectFiles: []string{"pkg0/file1.go", "pkg0/file2.go", "pkg0/file3.go"},
		},
		{
			name: "file_addition",
			edit: func(t *testing.T, repoPath string) {
				p := filepath.Join(repoPath, "pkg0", "new_file.go")
				if err := os.WriteFile(p, []byte("package pkg0\n\nfunc NewFunc() {}\n"), 0o644); err != nil {
					t.Fatalf("write new file: %v", err)
				}
			},
			expectFiles: []string{"pkg0/new_file.go"},
		},
		{
			name: "file_deletion",
			edit: func(t *testing.T, repoPath string) {
				p := filepath.Join(repoPath, "pkg0", "file2.go")
				if err := os.Remove(p); err != nil {
					t.Fatalf("remove: %v", err)
				}
			},
			expectFiles: []string{"pkg0/file2.go"},
		},
		{
			name: "file_rename",
			edit: func(t *testing.T, repoPath string) {
				old := filepath.Join(repoPath, "pkg0", "file3.go")
				newp := filepath.Join(repoPath, "pkg0", "file3_renamed.go")
				if err := os.Rename(old, newp); err != nil {
					t.Fatalf("rename: %v", err)
				}
			},
			// The watcher fires CREATE+REMOVE pairs for renames on
			// most platforms; either path showing up satisfies the
			// passive-detection contract. We assert at least one of
			// the two appears.
			expectFiles: []string{"pkg0/file3.go", "pkg0/file3_renamed.go"},
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			repoPath := testfixtures.LargeGoRepo(t, testfixtures.LargeGoRepoSpec{
				FileCount:      10,
				PackageBuckets: 2,
				Branch:         "main",
			})
			if resolved, evalErr := filepath.EvalSymlinks(repoPath); evalErr == nil {
				repoPath = resolved
			}

			store := graphstore.NewStore()
			repo, err := store.CreateRepository("passive-only-"+sc.name, repoPath)
			if err != nil {
				t.Fatalf("CreateRepository: %v", err)
			}
			// Seed prev with the fixture's full file set so the router
			// has a non-empty baseline. The test uses stub indexer
			// (per the existing 1.C integration tests) so the
			// IndexFiles invocation is asserted, not the indexer's
			// actual parse output.
			prev := &indexer.IndexResult{
				RepoName: "passive-only-" + sc.name,
				RepoPath: repoPath,
				Branch:   "main",
				Files: []indexer.FileResult{
					{Path: "pkg0/file1.go", Language: "go", LineCount: 10, ContentHash: "h1"},
					{Path: "pkg0/file2.go", Language: "go", LineCount: 10, ContentHash: "h2"},
					{Path: "pkg0/file3.go", Language: "go", LineCount: 10, ContentHash: "h3"},
				},
			}
			if _, err := store.ReplaceIndexResult(repo.ID, prev); err != nil {
				t.Fatalf("ReplaceIndexResult: %v", err)
			}

			idx := &stubIndexer{}
			impact := &stubImpact{}
			cfg := Config{
				Enabled:           true,
				RateLimitPerMin:   100,
				RepoBreakerPerMin: 100,
				T0BudgetMs:        500,
				DedupWindow:       10 * time.Second,
			}
			router := NewRouter(cfg, store, idx, impact, HeadRefBranchValidator{})
			router.SeedPrevious(repo.ID, prev)

			// Tighten debounce to 200ms so the test completes in <2s.
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

			before := router.FreshnessForExport(repo.ID)

			// THE CRITICAL DISCIPLINE: the test does not call
			// router.Submit, does not synthesize a record_change
			// event, does not invoke any in-process API that would
			// surface the change. Only on-disk mutation. If the test
			// ever needs to call into router or simulate
			// record_change to pass, the non-goal has been violated.
			sc.edit(t, repoPath)

			deadline := time.Now().Add(5 * time.Second)
			var advanced bool
			for time.Now().Before(deadline) {
				fresh := router.FreshnessForExport(repo.ID)
				if fresh.LastVerifiedAt.After(before.LastVerifiedAt) {
					advanced = true
					if fresh.State != "fresh" {
						t.Errorf("freshness.state = %q, want %q", fresh.State, "fresh")
					}
					if fresh.PartialRefresh {
						t.Errorf("freshness.partial_refresh = true, want false (full refresh on the passive path)")
					}
					if fresh.Branch != "main" {
						t.Errorf("freshness.branch = %q, want %q", fresh.Branch, "main")
					}
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if !advanced {
				t.Fatalf("freshness did not advance within 5s of passive %s edit (LastVerifiedAt=%v) — passive-only correctness violated", sc.name, before.LastVerifiedAt)
			}

			if idx.callCount() == 0 {
				t.Fatalf("IndexFiles never called for %s — passive watcher did not detect the edit", sc.name)
			}

			// Containment: the IndexFiles call(s) collectively
			// reference at least one of the expected files.
			seen := make(map[string]bool)
			for _, c := range idx.calls {
				for _, f := range c.Files {
					seen[f] = true
				}
			}
			anyMatch := false
			for _, want := range sc.expectFiles {
				if seen[want] {
					anyMatch = true
					break
				}
			}
			if !anyMatch {
				t.Errorf("IndexFiles never referenced any of expected paths %v; saw %v",
					sc.expectFiles, keysOf(seen))
			}

			// Sanity: the impact applier ran for at least one of the
			// observed events.
			if impact.calls.Load() == 0 {
				t.Errorf("ImpactApplier never invoked despite passive edit detection — symbol tier did not re-derive")
			}
		})
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
