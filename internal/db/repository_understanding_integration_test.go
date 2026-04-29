// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

//go:build integration

package db

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/knowledge"
)

// These tests lock the running-vs-terminal progress-zeroing invariant
// against a real SurrealDB. The MemStore unit test
// (`internal/knowledge/memstore_test.go`) covers the in-memory mirror, but
// the SurrealStore implementation uses Go-side conditional code (in
// StoreRepositoryUnderstanding) and SQL-side WHERE clauses (in
// UpdateRepositoryUnderstandingProgress and MarkRepositoryUnderstandingNeedsRefresh)
// that the MemStore test cannot exercise. A silently-broken WHERE clause —
// e.g., a typo'd stage value or a Surreal v2 INSIDE-array semantics drift —
// would pass MemStore tests but break production.
//
// The contract under test (see `internal/knowledge/models.go:301-312`):
//
//   - Only BUILDING_TREE and DEEPENING are running stages.
//   - StoreRepositoryUnderstanding must zero progress / phase / message
//     when the incoming Stage is non-running.
//   - UpdateRepositoryUnderstandingProgress (heartbeat) must no-op when
//     the row's current stage is non-running.
//   - MarkRepositoryUnderstandingNeedsRefresh must zero progress fields
//     when transitioning a row to NEEDS_REFRESH.

func TestSurrealStore_RepositoryUnderstanding_NonRunningStageZeroesProgress(t *testing.T) {
	s := startSurrealContainer(t)
	store := NewSurrealStore(s)

	scope := &knowledge.ArtifactScope{ScopeType: knowledge.ScopeRepository}

	// Sanity: a write with a running stage preserves the progress fields.
	running, err := store.StoreRepositoryUnderstanding(&knowledge.RepositoryUnderstanding{
		RepositoryID:    "repo-progress-running",
		Scope:           scope,
		Stage:           knowledge.UnderstandingBuildingTree,
		TreeStatus:      knowledge.UnderstandingTreePartial,
		Progress:        0.78,
		ProgressPhase:   "generating",
		ProgressMessage: "synthesising root",
	})
	if err != nil {
		t.Fatalf("StoreRepositoryUnderstanding(running): %v", err)
	}
	if running == nil {
		t.Fatal("running write returned nil")
	}
	if running.Progress != 0.78 || running.ProgressPhase != "generating" || running.ProgressMessage != "synthesising root" {
		t.Fatalf("running stage must preserve progress fields, got progress=%v phase=%q message=%q",
			running.Progress, running.ProgressPhase, running.ProgressMessage)
	}

	// Each non-running stage must be zeroed by the store on write.
	for _, stage := range []knowledge.RepositoryUnderstandingStage{
		knowledge.UnderstandingFailed,
		knowledge.UnderstandingReady,
		knowledge.UnderstandingFirstPassReady,
		knowledge.UnderstandingNeedsRefresh,
	} {
		stage := stage
		t.Run(string(stage), func(t *testing.T) {
			repoID := "repo-progress-" + string(stage)
			out, err := store.StoreRepositoryUnderstanding(&knowledge.RepositoryUnderstanding{
				RepositoryID:    repoID,
				Scope:           scope,
				Stage:           stage,
				TreeStatus:      knowledge.UnderstandingTreePartial,
				Progress:        0.78,
				ProgressPhase:   "generating",
				ProgressMessage: "synthesising root",
				ErrorMessage:    "rpc unavailable",
			})
			if err != nil {
				t.Fatalf("StoreRepositoryUnderstanding(%s): %v", stage, err)
			}
			if out == nil {
				t.Fatalf("non-running write returned nil")
			}
			if out.Progress != 0 || out.ProgressPhase != "" || out.ProgressMessage != "" {
				t.Fatalf("stage %s must zero progress fields, got progress=%v phase=%q message=%q",
					stage, out.Progress, out.ProgressPhase, out.ProgressMessage)
			}

			// A late heartbeat must NOT re-stamp progress on a terminal row —
			// the SQL WHERE clause in UpdateRepositoryUnderstandingProgress
			// is the line under test.
			if err := store.UpdateRepositoryUnderstandingProgress(out.ID, 0.5, "queued", "rebuilding"); err != nil {
				t.Fatalf("UpdateRepositoryUnderstandingProgress on terminal row: %v", err)
			}
			again := store.GetRepositoryUnderstanding(repoID, *scope)
			if again == nil {
				t.Fatal("GetRepositoryUnderstanding after late heartbeat returned nil")
			}
			if again.Progress != 0 || again.ProgressPhase != "" || again.ProgressMessage != "" {
				t.Fatalf("late heartbeat re-stamped progress on stage %s: progress=%v phase=%q message=%q",
					stage, again.Progress, again.ProgressPhase, again.ProgressMessage)
			}
		})
	}
}

// TestSurrealStore_UpdateProgress_RunningStageWritesThrough confirms the
// other half of the heartbeat gate: when stage IS running, the update
// actually lands. Without this we'd be catching only one direction of a
// bug — a WHERE clause that silently rejects everything would pass the
// terminal-stage tests above.
func TestSurrealStore_UpdateProgress_RunningStageWritesThrough(t *testing.T) {
	s := startSurrealContainer(t)
	store := NewSurrealStore(s)

	scope := &knowledge.ArtifactScope{ScopeType: knowledge.ScopeRepository}

	row, err := store.StoreRepositoryUnderstanding(&knowledge.RepositoryUnderstanding{
		RepositoryID: "repo-progress-running-update",
		Scope:        scope,
		Stage:        knowledge.UnderstandingBuildingTree,
		TreeStatus:   knowledge.UnderstandingTreePartial,
	})
	if err != nil || row == nil {
		t.Fatalf("seed running row: err=%v row=%v", err, row)
	}

	if err := store.UpdateRepositoryUnderstandingProgress(row.ID, 0.42, "files", "analysing 14 files"); err != nil {
		t.Fatalf("UpdateRepositoryUnderstandingProgress: %v", err)
	}
	got := store.GetRepositoryUnderstanding("repo-progress-running-update", *scope)
	if got == nil {
		t.Fatal("GetRepositoryUnderstanding returned nil")
	}
	if got.Progress != 0.42 || got.ProgressPhase != "files" || got.ProgressMessage != "analysing 14 files" {
		t.Fatalf("running heartbeat did not land: progress=%v phase=%q message=%q",
			got.Progress, got.ProgressPhase, got.ProgressMessage)
	}
}

// TestSurrealStore_MarkNeedsRefresh_ZeroesProgressFields verifies the
// MarkRepositoryUnderstandingNeedsRefresh SQL flips a terminal row to
// NEEDS_REFRESH AND zeroes its progress fields. The MemStore mirror at
// memstore.go:446-451 does this in Go; the SurrealStore does it in SQL.
func TestSurrealStore_MarkNeedsRefresh_ZeroesProgressFields(t *testing.T) {
	s := startSurrealContainer(t)
	store := NewSurrealStore(s)

	scope := &knowledge.ArtifactScope{ScopeType: knowledge.ScopeRepository}
	repoID := "repo-needs-refresh"

	// Seed a row in FIRST_PASS_READY. The store will already zero progress
	// on this write (per the StoreRepositoryUnderstanding invariant), so to
	// prove MarkRepositoryUnderstandingNeedsRefresh actually does its own
	// zeroing we cheat the row up to non-zero progress via a direct
	// UPDATE to building_tree, write progress, then flip back to ready.
	if _, err := store.StoreRepositoryUnderstanding(&knowledge.RepositoryUnderstanding{
		RepositoryID: repoID,
		Scope:        scope,
		Stage:        knowledge.UnderstandingBuildingTree,
		TreeStatus:   knowledge.UnderstandingTreePartial,
	}); err != nil {
		t.Fatalf("seed building_tree: %v", err)
	}
	row := store.GetRepositoryUnderstanding(repoID, *scope)
	if row == nil {
		t.Fatal("seed lookup returned nil")
	}
	if err := store.UpdateRepositoryUnderstandingProgress(row.ID, 0.91, "root", "synthesising"); err != nil {
		t.Fatalf("seed heartbeat: %v", err)
	}
	if _, err := store.StoreRepositoryUnderstanding(&knowledge.RepositoryUnderstanding{
		ID:           row.ID,
		RepositoryID: repoID,
		Scope:        scope,
		Stage:        knowledge.UnderstandingReady,
		TreeStatus:   knowledge.UnderstandingTreeComplete,
	}); err != nil {
		t.Fatalf("transition to ready: %v", err)
	}
	// Sanity: the ready transition should already have zeroed progress.
	ready := store.GetRepositoryUnderstanding(repoID, *scope)
	if ready == nil {
		t.Fatal("ready lookup returned nil")
	}
	if ready.Progress != 0 || ready.ProgressPhase != "" || ready.ProgressMessage != "" {
		t.Fatalf("ready transition should have zeroed progress, got progress=%v phase=%q message=%q",
			ready.Progress, ready.ProgressPhase, ready.ProgressMessage)
	}

	// Now flip to NEEDS_REFRESH. This is the path under test.
	if err := store.MarkRepositoryUnderstandingNeedsRefresh(repoID); err != nil {
		t.Fatalf("MarkRepositoryUnderstandingNeedsRefresh: %v", err)
	}
	flipped := store.GetRepositoryUnderstanding(repoID, *scope)
	if flipped == nil {
		t.Fatal("post-flip lookup returned nil")
	}
	if flipped.Stage != knowledge.UnderstandingNeedsRefresh {
		t.Fatalf("expected stage %s, got %s", knowledge.UnderstandingNeedsRefresh, flipped.Stage)
	}
	if flipped.Progress != 0 || flipped.ProgressPhase != "" || flipped.ProgressMessage != "" {
		t.Fatalf("MarkNeedsRefresh must zero progress fields, got progress=%v phase=%q message=%q",
			flipped.Progress, flipped.ProgressPhase, flipped.ProgressMessage)
	}
}
