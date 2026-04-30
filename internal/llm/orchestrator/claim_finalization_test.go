// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package orchestrator

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/llm"
)

// CA-122 Phase 5b tests: claim primitive + terminal-status guard
// race coverage. These tests verify the structural invariant codex
// r1d/r1e flagged: only ONE caller (run goroutine OR reaper) writes
// the terminal status of any given job.

// randID returns a per-test unique job id so multiple tests can share
// a freshly-created MemStore and not collide on the "id required"
// validation.
func randID(t *testing.T) string {
	t.Helper()
	return "test-" + t.Name()
}

func TestClaimFinalization_FirstCallWins(t *testing.T) {
	o := newTestOrchestrator(t, Config{MaxConcurrency: 1})

	first := o.claimFinalization("job-A")
	second := o.claimFinalization("job-A")

	if !first {
		t.Error("first claim should return true")
	}
	if second {
		t.Error("second claim on same job should return false")
	}
}

func TestClaimFinalization_DifferentJobsBothSucceed(t *testing.T) {
	o := newTestOrchestrator(t, Config{MaxConcurrency: 1})

	if !o.claimFinalization("job-A") {
		t.Error("claim job-A should succeed")
	}
	if !o.claimFinalization("job-B") {
		t.Error("claim job-B should succeed (different job)")
	}
}

func TestClaimFinalization_ReleaseAllowsReclaim(t *testing.T) {
	o := newTestOrchestrator(t, Config{MaxConcurrency: 1})

	o.claimFinalization("job-A")
	o.releaseFinalization("job-A")
	if !o.claimFinalization("job-A") {
		t.Error("after release, reclaim should succeed")
	}
}

func TestClaimFinalization_ConcurrentNGoroutinesExactlyOneWins(t *testing.T) {
	o := newTestOrchestrator(t, Config{MaxConcurrency: 1})

	const N = 100
	var winners atomic.Int32
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			if o.claimFinalization("job-shared") {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := winners.Load(); got != 1 {
		t.Errorf("expected exactly 1 winner, got %d", got)
	}
}

func TestMemStoreSetStatus_TerminalToTerminalIsNoOp(t *testing.T) {
	store := llm.NewMemStore()

	const id = "test-job-1"
	if _, err := store.Create(&llm.Job{
		ID:        id,
		Subsystem: llm.SubsystemKnowledge,
		JobType:   "cliff_notes",
		TargetKey: "repo:1",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Transition pending -> generating -> ready.
	if err := store.SetStatus(id, llm.StatusGenerating); err != nil {
		t.Fatalf("set generating: %v", err)
	}
	if err := store.SetStatus(id, llm.StatusReady); err != nil {
		t.Fatalf("set ready: %v", err)
	}

	// Now attempt to overwrite ready -> failed. Must be no-op.
	if err := store.SetStatus(id, llm.StatusFailed); err != nil {
		t.Errorf("expected no-op (nil error), got %v", err)
	}
	got := store.GetByID(id)
	if got == nil {
		t.Fatal("job vanished")
	}
	if got.Status != llm.StatusReady {
		t.Errorf("status got %v, want ready (terminal-status guard should have blocked)", got.Status)
	}
}

func TestMemStoreSetStatus_TerminalToSelfIsAllowed(t *testing.T) {
	// Same-status terminal writes are allowed (idempotent no-op for
	// the underlying state — fields are re-stamped, but the status
	// stays). The guard explicitly allows status == j.Status.
	store := llm.NewMemStore()
	job, err := store.Create(&llm.Job{
		ID:        randID(t),
		Subsystem: llm.SubsystemKnowledge,
		JobType:   "cliff_notes",
		TargetKey: "repo:1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := job.ID
	_ = store.SetStatus(id, llm.StatusGenerating)
	_ = store.SetStatus(id, llm.StatusFailed)
	if err := store.SetStatus(id, llm.StatusFailed); err != nil {
		t.Errorf("self-write should not error, got %v", err)
	}
	got := store.GetByID(id)
	if got.Status != llm.StatusFailed {
		t.Errorf("status got %v, want failed", got.Status)
	}
}

func TestMemStoreSetError_TerminalToFailedIsAllowed(t *testing.T) {
	store := llm.NewMemStore()
	job, err := store.Create(&llm.Job{
		ID:        randID(t),
		Subsystem: llm.SubsystemKnowledge,
		JobType:   "cliff_notes",
		TargetKey: "repo:1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := job.ID
	_ = store.SetStatus(id, llm.StatusGenerating)
	// SetError implicitly sets Failed; calling on a non-terminal
	// generating job should succeed and write the error.
	if err := store.SetError(id, "DEADLINE_EXCEEDED", "reaped"); err != nil {
		t.Fatalf("set error: %v", err)
	}
	got := store.GetByID(id)
	if got.Status != llm.StatusFailed {
		t.Errorf("status got %v, want failed", got.Status)
	}
	if got.ErrorCode != "DEADLINE_EXCEEDED" {
		t.Errorf("code got %q, want DEADLINE_EXCEEDED", got.ErrorCode)
	}
}

func TestMemStoreSetError_OnAlreadyReadyIsNoOp(t *testing.T) {
	// The reaper must never overwrite a successful Ready into Failed.
	store := llm.NewMemStore()
	job, err := store.Create(&llm.Job{
		ID:        randID(t),
		Subsystem: llm.SubsystemKnowledge,
		JobType:   "cliff_notes",
		TargetKey: "repo:1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := job.ID
	_ = store.SetStatus(id, llm.StatusGenerating)
	_ = store.SetStatus(id, llm.StatusReady)
	// Now the reaper fires and tries to write a failure on an
	// already-ready job. The terminal-status guard must reject it
	// silently (no error, no state change).
	if err := store.SetError(id, "DEADLINE_EXCEEDED", "stale-reap-after-success"); err != nil {
		t.Errorf("expected silent no-op, got %v", err)
	}
	got := store.GetByID(id)
	if got.Status != llm.StatusReady {
		t.Errorf("status got %v, want ready (terminal-status guard should have blocked)", got.Status)
	}
	if got.ErrorCode != "" {
		t.Errorf("error code should be empty on ready job, got %q", got.ErrorCode)
	}
}

func TestMemStoreSetError_OnCancelledIsNoOp(t *testing.T) {
	store := llm.NewMemStore()
	job, err := store.Create(&llm.Job{
		ID:        randID(t),
		Subsystem: llm.SubsystemKnowledge,
		JobType:   "cliff_notes",
		TargetKey: "repo:1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := job.ID
	_ = store.SetStatus(id, llm.StatusGenerating)
	_ = store.SetStatus(id, llm.StatusCancelled)
	if err := store.SetError(id, "DEADLINE_EXCEEDED", "should-not-write"); err != nil {
		t.Errorf("expected silent no-op, got %v", err)
	}
	got := store.GetByID(id)
	if got.Status != llm.StatusCancelled {
		t.Errorf("status got %v, want cancelled", got.Status)
	}
}
