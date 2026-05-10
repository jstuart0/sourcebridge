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
	running, err := store.StoreRepositoryUnderstanding(t.Context(), &knowledge.RepositoryUnderstanding{
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
			out, err := store.StoreRepositoryUnderstanding(t.Context(), &knowledge.RepositoryUnderstanding{
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
			if err := store.UpdateRepositoryUnderstandingProgress(t.Context(), out.ID, 0.5, "queued", "rebuilding"); err != nil {
				t.Fatalf("UpdateRepositoryUnderstandingProgress on terminal row: %v", err)
			}
			again := store.GetRepositoryUnderstanding(t.Context(), repoID, *scope)
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

	row, err := store.StoreRepositoryUnderstanding(t.Context(), &knowledge.RepositoryUnderstanding{
		RepositoryID: "repo-progress-running-update",
		Scope:        scope,
		Stage:        knowledge.UnderstandingBuildingTree,
		TreeStatus:   knowledge.UnderstandingTreePartial,
	})
	if err != nil || row == nil {
		t.Fatalf("seed running row: err=%v row=%v", err, row)
	}

	if err := store.UpdateRepositoryUnderstandingProgress(t.Context(), row.ID, 0.42, "files", "analysing 14 files"); err != nil {
		t.Fatalf("UpdateRepositoryUnderstandingProgress: %v", err)
	}
	got := store.GetRepositoryUnderstanding(t.Context(), "repo-progress-running-update", *scope)
	if got == nil {
		t.Fatal("GetRepositoryUnderstanding returned nil")
	}
	if got.Progress != 0.42 || got.ProgressPhase != "files" || got.ProgressMessage != "analysing 14 files" {
		t.Fatalf("running heartbeat did not land: progress=%v phase=%q message=%q",
			got.Progress, got.ProgressPhase, got.ProgressMessage)
	}
}

// TestMarkRepositoryUnderstandingFailed_TransitionsRunningStages verifies the
// three-case idempotency contract for MarkRepositoryUnderstandingFailed:
//
//   - A BUILDING_TREE row transitions to FAILED with error fields set and
//     progress zeroed.
//   - A DEEPENING row transitions to FAILED identically.
//   - A READY row is left untouched (the WHERE gate refuses to overwrite a
//     terminal stage — protects against a late callback racing a successful
//     retry that already wrote READY).
//   - Calling the method again on an already-FAILED row is idempotent (no-op).
func TestMarkRepositoryUnderstandingFailed_TransitionsRunningStages(t *testing.T) {
	s := startSurrealContainer(t)
	store := NewSurrealStore(s)

	scope := &knowledge.ArtifactScope{ScopeType: knowledge.ScopeRepository}
	const errCode = "DEADLINE_EXCEEDED"
	const errMsg = "retry exhaustion"

	for _, startStage := range []knowledge.RepositoryUnderstandingStage{
		knowledge.UnderstandingBuildingTree,
		knowledge.UnderstandingDeepening,
	} {
		startStage := startStage
		t.Run("from_"+string(startStage), func(t *testing.T) {
			repoID := "repo-fail-" + string(startStage)
			row, err := store.StoreRepositoryUnderstanding(t.Context(), &knowledge.RepositoryUnderstanding{
				RepositoryID:    repoID,
				Scope:           scope,
				Stage:           startStage,
				TreeStatus:      knowledge.UnderstandingTreePartial,
				Progress:        0.55,
				ProgressPhase:   "running",
				ProgressMessage: "doing stuff",
			})
			if err != nil || row == nil {
				t.Fatalf("seed %s: err=%v row=%v", startStage, err, row)
			}

			if err := store.MarkRepositoryUnderstandingFailed(t.Context(), row.ID, errCode, errMsg); err != nil {
				t.Fatalf("MarkRepositoryUnderstandingFailed from %s: %v", startStage, err)
			}

			got := store.GetRepositoryUnderstanding(t.Context(), repoID, *scope)
			if got == nil {
				t.Fatal("GetRepositoryUnderstanding after failure returned nil")
			}
			if got.Stage != knowledge.UnderstandingFailed {
				t.Fatalf("expected stage=failed, got %s", got.Stage)
			}
			if got.Progress != 0 || got.ProgressPhase != "" || got.ProgressMessage != "" {
				t.Fatalf("expected progress zeroed, got progress=%v phase=%q message=%q",
					got.Progress, got.ProgressPhase, got.ProgressMessage)
			}
			if got.ErrorCode != errCode {
				t.Fatalf("expected error_code=%q, got %q", errCode, got.ErrorCode)
			}
			if got.ErrorMessage != errMsg {
				t.Fatalf("expected error_message=%q, got %q", errMsg, got.ErrorMessage)
			}

			// Idempotent re-fire: calling again on FAILED must be a no-op.
			if err := store.MarkRepositoryUnderstandingFailed(t.Context(), row.ID, "SECOND_CALL", "should be ignored"); err != nil {
				t.Fatalf("idempotent re-fire: %v", err)
			}
			again := store.GetRepositoryUnderstanding(t.Context(), repoID, *scope)
			if again == nil {
				t.Fatal("idempotent re-fire lookup returned nil")
			}
			if again.ErrorCode != errCode {
				t.Fatalf("idempotent re-fire overwrote error_code: got %q", again.ErrorCode)
			}
		})
	}

	t.Run("no_op_from_ready", func(t *testing.T) {
		// Build a READY row and confirm the method leaves it untouched.
		repoID := "repo-fail-noop-ready"
		row, err := store.StoreRepositoryUnderstanding(t.Context(), &knowledge.RepositoryUnderstanding{
			RepositoryID: repoID,
			Scope:        scope,
			Stage:        knowledge.UnderstandingReady,
			TreeStatus:   knowledge.UnderstandingTreeComplete,
		})
		if err != nil || row == nil {
			t.Fatalf("seed ready: err=%v row=%v", err, row)
		}

		if err := store.MarkRepositoryUnderstandingFailed(t.Context(), row.ID, errCode, errMsg); err != nil {
			t.Fatalf("MarkRepositoryUnderstandingFailed on READY: %v", err)
		}

		got := store.GetRepositoryUnderstanding(t.Context(), repoID, *scope)
		if got == nil {
			t.Fatal("post-no-op lookup returned nil")
		}
		if got.Stage != knowledge.UnderstandingReady {
			t.Fatalf("expected stage=ready unchanged, got %s", got.Stage)
		}
	})
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
	if _, err := store.StoreRepositoryUnderstanding(t.Context(), &knowledge.RepositoryUnderstanding{
		RepositoryID: repoID,
		Scope:        scope,
		Stage:        knowledge.UnderstandingBuildingTree,
		TreeStatus:   knowledge.UnderstandingTreePartial,
	}); err != nil {
		t.Fatalf("seed building_tree: %v", err)
	}
	row := store.GetRepositoryUnderstanding(t.Context(), repoID, *scope)
	if row == nil {
		t.Fatal("seed lookup returned nil")
	}
	if err := store.UpdateRepositoryUnderstandingProgress(t.Context(), row.ID, 0.91, "root", "synthesising"); err != nil {
		t.Fatalf("seed heartbeat: %v", err)
	}
	if _, err := store.StoreRepositoryUnderstanding(t.Context(), &knowledge.RepositoryUnderstanding{
		ID:           row.ID,
		RepositoryID: repoID,
		Scope:        scope,
		Stage:        knowledge.UnderstandingReady,
		TreeStatus:   knowledge.UnderstandingTreeComplete,
	}); err != nil {
		t.Fatalf("transition to ready: %v", err)
	}
	// Sanity: the ready transition should already have zeroed progress.
	ready := store.GetRepositoryUnderstanding(t.Context(), repoID, *scope)
	if ready == nil {
		t.Fatal("ready lookup returned nil")
	}
	if ready.Progress != 0 || ready.ProgressPhase != "" || ready.ProgressMessage != "" {
		t.Fatalf("ready transition should have zeroed progress, got progress=%v phase=%q message=%q",
			ready.Progress, ready.ProgressPhase, ready.ProgressMessage)
	}

	// Now flip to NEEDS_REFRESH. This is the path under test.
	if err := store.MarkRepositoryUnderstandingNeedsRefresh(t.Context(), repoID); err != nil {
		t.Fatalf("MarkRepositoryUnderstandingNeedsRefresh: %v", err)
	}
	flipped := store.GetRepositoryUnderstanding(t.Context(), repoID, *scope)
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

// TestMarkRepositoryUnderstandingNeedsRefresh_FromFailed verifies that
// MarkRepositoryUnderstandingNeedsRefresh transitions a FAILED row to
// NEEDS_REFRESH, clears error_code and error_message, and is idempotent on
// already-needs_refresh rows and a no-op on running (BUILDING_TREE) rows.
//
// Gate symmetry note (load-bearing): MarkRepositoryUnderstandingFailed gates on
// stage INSIDE ['building_tree', 'deepening']; MarkRepositoryUnderstandingNeedsRefresh
// gates on stage INSIDE ['first_pass_ready', 'ready', 'failed']. The two gate sets
// are non-overlapping, so there is no needs_refresh → failed regression possible.
func TestMarkRepositoryUnderstandingNeedsRefresh_FromFailed(t *testing.T) {
	s := startSurrealContainer(t)
	store := NewSurrealStore(s)

	scope := &knowledge.ArtifactScope{ScopeType: knowledge.ScopeRepository}
	const errCode = "TEST_ERR"
	const errMsg = "test"

	// Seed: BUILDING_TREE first so we can progress to FAILED via MarkFailed.
	repoID := "repo-needs-refresh-from-failed"
	if _, err := store.StoreRepositoryUnderstanding(t.Context(), &knowledge.RepositoryUnderstanding{
		RepositoryID: repoID,
		Scope:        scope,
		Stage:        knowledge.UnderstandingBuildingTree,
		TreeStatus:   knowledge.UnderstandingTreePartial,
	}); err != nil {
		t.Fatalf("seed building_tree: %v", err)
	}
	row := store.GetRepositoryUnderstanding(t.Context(), repoID, *scope)
	if row == nil {
		t.Fatal("seed lookup returned nil")
	}

	// Transition to FAILED.
	if err := store.MarkRepositoryUnderstandingFailed(t.Context(), row.ID, errCode, errMsg); err != nil {
		t.Fatalf("MarkRepositoryUnderstandingFailed: %v", err)
	}
	failed := store.GetRepositoryUnderstanding(t.Context(), repoID, *scope)
	if failed == nil {
		t.Fatal("post-failed lookup returned nil")
	}
	if failed.Stage != knowledge.UnderstandingFailed {
		t.Fatalf("expected stage=failed, got %s", failed.Stage)
	}
	if failed.ErrorCode != errCode || failed.ErrorMessage != errMsg {
		t.Fatalf("expected error_code=%q error_message=%q, got code=%q msg=%q",
			errCode, errMsg, failed.ErrorCode, failed.ErrorMessage)
	}

	// Path under test: MarkNeedsRefresh should accept FAILED and clear error fields.
	if err := store.MarkRepositoryUnderstandingNeedsRefresh(t.Context(), repoID); err != nil {
		t.Fatalf("MarkRepositoryUnderstandingNeedsRefresh from failed: %v", err)
	}
	refreshed := store.GetRepositoryUnderstanding(t.Context(), repoID, *scope)
	if refreshed == nil {
		t.Fatal("post-refresh lookup returned nil")
	}
	if refreshed.Stage != knowledge.UnderstandingNeedsRefresh {
		t.Fatalf("expected stage=needs_refresh, got %s", refreshed.Stage)
	}
	if refreshed.ErrorCode != "" {
		t.Fatalf("expected error_code cleared, got %q", refreshed.ErrorCode)
	}
	if refreshed.ErrorMessage != "" {
		t.Fatalf("expected error_message cleared, got %q", refreshed.ErrorMessage)
	}
	if refreshed.Progress != 0 || refreshed.ProgressPhase != "" || refreshed.ProgressMessage != "" {
		t.Fatalf("expected progress zeroed, got progress=%v phase=%q message=%q",
			refreshed.Progress, refreshed.ProgressPhase, refreshed.ProgressMessage)
	}

	// Idempotency: a second call on a needs_refresh row must be a no-op (the WHERE
	// clause excludes needs_refresh, so the row stays at needs_refresh unchanged).
	if err := store.MarkRepositoryUnderstandingNeedsRefresh(t.Context(), repoID); err != nil {
		t.Fatalf("idempotent second call: %v", err)
	}
	again := store.GetRepositoryUnderstanding(t.Context(), repoID, *scope)
	if again == nil {
		t.Fatal("post-idempotent lookup returned nil")
	}
	if again.Stage != knowledge.UnderstandingNeedsRefresh {
		t.Fatalf("idempotent call must not change stage, got %s", again.Stage)
	}

	// Cross-stage no-op: a BUILDING_TREE row must not be touched (running stages
	// are not eligible — they are already processing).
	repoID2 := "repo-needs-refresh-noop-running"
	if _, err := store.StoreRepositoryUnderstanding(t.Context(), &knowledge.RepositoryUnderstanding{
		RepositoryID: repoID2,
		Scope:        scope,
		Stage:        knowledge.UnderstandingBuildingTree,
		TreeStatus:   knowledge.UnderstandingTreePartial,
	}); err != nil {
		t.Fatalf("seed building_tree (noop): %v", err)
	}
	if err := store.MarkRepositoryUnderstandingNeedsRefresh(t.Context(), repoID2); err != nil {
		t.Fatalf("MarkNeedsRefresh on building_tree: %v", err)
	}
	noop := store.GetRepositoryUnderstanding(t.Context(), repoID2, *scope)
	if noop == nil {
		t.Fatal("noop lookup returned nil")
	}
	if noop.Stage != knowledge.UnderstandingBuildingTree {
		t.Fatalf("expected building_tree unchanged, got %s", noop.Stage)
	}
}
