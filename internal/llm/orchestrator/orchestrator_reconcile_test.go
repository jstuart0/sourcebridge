// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Tests for CA-175: orchestrator startup reconciliation of zombie jobs.
// Each test exercises a specific invariant of reconcileZombieJobs — see
// the plan at thoughts/shared/plans/active-2026-05-07-diagnose-knowledge-slot-stall.md
// Phase 2 Step 2.5 for the design rationale behind each case.
//
// CA-180 tests (OnJobFailed dispatch) are appended at the end of this file.

package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sourcebridge/sourcebridge/internal/llm"
)

// newReconcileStore returns a fresh MemStore pre-populated with a generating
// job whose ProcessID and UpdatedAt are controlled by the caller. The job is
// placed directly in StatusGenerating to simulate a zombie left by a prior
// process (the orchestrator that owns this test will have a different UUID).
func newReconcileStore(t *testing.T, processID string, updatedAt time.Time) (*llm.MemStore, string) {
	t.Helper()
	store := llm.NewMemStore()
	jobID := uuid.NewString()
	job := &llm.Job{
		ID:        jobID,
		Subsystem: llm.SubsystemKnowledge,
		JobType:   "cliff_notes",
		TargetKey: "reconcile-test-" + jobID,
		RepoID:    "repo-reconcile",
		Status:    llm.StatusGenerating,
		ProcessID: processID,
	}
	if _, err := store.Create(t.Context(), job); err != nil {
		t.Fatalf("newReconcileStore Create: %v", err)
	}
	// Force UpdatedAt to the desired value after Create (Create may stamp now).
	if err := store.ForceUpdatedAt(t.Context(), jobID, updatedAt); err != nil {
		t.Fatalf("newReconcileStore ForceUpdatedAt: %v", err)
	}
	return store, jobID
}

// newReconcileOrchestrator builds an orchestrator against a pre-seeded store
// with reconciliation ENABLED (SkipStartupReconciliation = false, the
// production default). Workers are set to zero so no jobs run.
func newReconcileOrchestrator(t *testing.T, store *llm.MemStore) *Orchestrator {
	t.Helper()
	cfg := Config{
		MaxConcurrency:            0,
		ProgressDebounce:          5 * time.Millisecond,
		Retry:                     RetryPolicy{MaxAttempts: 1},
		SkipStartupReconciliation: false, // exercise production path
	}
	orch := New(store, cfg)
	t.Cleanup(func() { _ = orch.Shutdown(2 * time.Second) })
	return orch
}

// TestReconcileZombieJobs_StalePriorProcess_MarkedFailed verifies the happy
// path: a generating job from a different process whose UpdatedAt is well
// past reconcileStaleThreshold is marked StatusFailed with the
// PROCESS_RESTART_RECONCILIATION error code.
func TestReconcileZombieJobs_StalePriorProcess_MarkedFailed(t *testing.T) {
	priorPID := "old-process-uuid-stale"
	store, jobID := newReconcileStore(t, priorPID, time.Now().Add(-5*time.Minute))

	orch := newReconcileOrchestrator(t, store)

	got := orch.GetJob(jobID)
	if got == nil {
		t.Fatal("expected job to exist after reconciliation")
	}
	if got.Status != llm.StatusFailed {
		t.Fatalf("expected zombie job → StatusFailed; got %v", got.Status)
	}
	if got.ErrorCode != "PROCESS_RESTART_RECONCILIATION" {
		t.Fatalf("expected ErrorCode=PROCESS_RESTART_RECONCILIATION; got %q", got.ErrorCode)
	}
	if !strings.Contains(got.ErrorMessage, priorPID) {
		t.Fatalf("expected error message to contain prior process_id %q; got %q", priorPID, got.ErrorMessage)
	}
}

// TestReconcileZombieJobs_FreshPeer_NotReconciled pins the exact boundary of
// the reconcileStaleThreshold. A job whose UpdatedAt is exactly one second
// BEFORE the threshold must NOT be reconciled; one second AFTER must be.
// References the reconcileStaleThreshold constant directly (does not hardcode
// durations) to match the pattern in reaper_ca141_test.go:46-88.
func TestReconcileZombieJobs_FreshPeer_NotReconciled(t *testing.T) {
	cases := []struct {
		name           string
		age            time.Duration
		wantReconciled bool
	}{
		{
			name:           "at_boundary_minus_one_second",
			age:            reconcileStaleThreshold - time.Second,
			wantReconciled: false,
		},
		{
			name:           "at_boundary_plus_one_second",
			age:            reconcileStaleThreshold + time.Second,
			wantReconciled: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			priorPID := "peer-process-" + tc.name
			store, jobID := newReconcileStore(t, priorPID, time.Now().Add(-tc.age))

			orch := newReconcileOrchestrator(t, store)

			got := orch.GetJob(jobID)
			if got == nil {
				t.Fatal("expected job to still exist")
			}
			if tc.wantReconciled {
				if got.Status != llm.StatusFailed {
					t.Fatalf("age=%v (> threshold): expected StatusFailed; got %v", tc.age, got.Status)
				}
			} else {
				if got.Status != llm.StatusGenerating {
					t.Fatalf("age=%v (< threshold): expected StatusGenerating (not reconciled); got %v", tc.age, got.Status)
				}
			}
		})
	}
}

// TestReconcileZombieJobs_LegacyNoProcessID covers pre-migration 058 rows
// whose process_id is absent (empty string in the Go model). Two sub-cases:
//
//   - stale: empty process_id + old UpdatedAt → must be reconciled (old zombie
//     from before the migration, process is long dead).
//   - recent: empty process_id + fresh UpdatedAt → must NOT be reconciled
//     (protects a rolling-restart upgrade where a pre-migration peer replica
//     is still alive and heartbeating, so its rows are still fresh).
func TestReconcileZombieJobs_LegacyNoProcessID(t *testing.T) {
	t.Run("stale", func(t *testing.T) {
		// Empty ProcessID + stale UpdatedAt → should be reconciled.
		store, jobID := newReconcileStore(t, "" /* legacy: no process_id */, time.Now().Add(-5*time.Minute))

		orch := newReconcileOrchestrator(t, store)

		got := orch.GetJob(jobID)
		if got == nil {
			t.Fatal("expected job to exist")
		}
		if got.Status != llm.StatusFailed {
			t.Fatalf("legacy stale row: expected StatusFailed; got %v", got.Status)
		}
		if got.ErrorCode != "PROCESS_RESTART_RECONCILIATION" {
			t.Fatalf("expected PROCESS_RESTART_RECONCILIATION; got %q", got.ErrorCode)
		}
	})

	t.Run("recent", func(t *testing.T) {
		// Empty ProcessID + fresh UpdatedAt (30 s) → must NOT be reconciled.
		// A pre-migration peer whose heartbeat is fresh must be left alone
		// during the rolling-restart upgrade window.
		store, jobID := newReconcileStore(t, "" /* legacy: no process_id */, time.Now().Add(-30*time.Second))

		orch := newReconcileOrchestrator(t, store)

		got := orch.GetJob(jobID)
		if got == nil {
			t.Fatal("expected job to exist")
		}
		if got.Status != llm.StatusGenerating {
			t.Fatalf("legacy fresh row: expected StatusGenerating (not reconciled); got %v", got.Status)
		}
	})
}

// TestReconcileZombieJobs_NoActiveJobs_NoOp verifies that reconciliation on
// an empty store is a clean no-op. Primarily tests that the summary log event
// fires with count=0 even when there is nothing to do.
func TestReconcileZombieJobs_NoActiveJobs_NoOp(t *testing.T) {
	logBuf := &syncBuffer{}
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))

	store := llm.NewMemStore()
	orch := newReconcileOrchestrator(t, store)

	// No panics, no jobs in the store — the pass log should fire with count=0.
	output := logBuf.String()
	if !strings.Contains(output, "orchestrator_zombie_reconciled_pass") {
		t.Errorf("expected orchestrator_zombie_reconciled_pass event in logs; got:\n%s", output)
	}
	if !strings.Contains(output, "count=0") {
		t.Errorf("expected count=0 in reconcile pass log; got:\n%s", output)
	}
	// No jobs were created so nothing should be reconciled.
	if strings.Contains(output, "orchestrator_zombie_reconciled\"") {
		t.Errorf("expected no per-zombie log events on empty store; got:\n%s", output)
	}
	_ = orch // suppress unused warning
}

// TestReconcileZombieJobs_ConcurrentIdempotency seeds two zombie jobs and
// spawns two goroutines that each call reconcileZombieJobs concurrently.
// Asserts that each zombie ends up StatusFailed with exactly one
// PROCESS_RESTART_RECONCILIATION error — not double-written — validating
// that claimFinalization provides the race protection (only one goroutine
// wins the claim per job).
func TestReconcileZombieJobs_ConcurrentIdempotency(t *testing.T) {
	// Build a store with two stale zombies from different processes.
	store := llm.NewMemStore()
	staleTime := time.Now().Add(-5 * time.Minute)

	zombie1ID := uuid.NewString()
	zombie2ID := uuid.NewString()
	for _, z := range []struct {
		id        string
		processID string
		targetKey string
	}{
		{zombie1ID, "old-proc-A", "reconcile-concurrent-A"},
		{zombie2ID, "old-proc-B", "reconcile-concurrent-B"},
	} {
		job := &llm.Job{
			ID:        z.id,
			Subsystem: llm.SubsystemKnowledge,
			JobType:   "cliff_notes",
			TargetKey: z.targetKey,
			Status:    llm.StatusGenerating,
			ProcessID: z.processID,
		}
		if _, err := store.Create(t.Context(), job); err != nil {
			t.Fatalf("Create zombie %s: %v", z.id, err)
		}
		if err := store.ForceUpdatedAt(t.Context(), z.id, staleTime); err != nil {
			t.Fatalf("ForceUpdatedAt zombie %s: %v", z.id, err)
		}
	}

	// Build orchestrator with reconciliation disabled so we can call it
	// manually and concurrently.
	cfg := Config{
		MaxConcurrency:            0,
		ProgressDebounce:          5 * time.Millisecond,
		Retry:                     RetryPolicy{MaxAttempts: 1},
		SkipStartupReconciliation: true, // we call reconcileZombieJobs manually
	}
	orch := New(store, cfg)
	t.Cleanup(func() { _ = orch.Shutdown(2 * time.Second) })

	// Launch two goroutines that both call reconcileZombieJobs at the same time.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); orch.reconcileZombieJobs() }()
	go func() { defer wg.Done(); orch.reconcileZombieJobs() }()
	wg.Wait()

	// Each zombie must be StatusFailed with exactly one error code (not doubled).
	for _, jobID := range []string{zombie1ID, zombie2ID} {
		got := orch.GetJob(jobID)
		if got == nil {
			t.Fatalf("job %s missing after concurrent reconcile", jobID)
		}
		if got.Status != llm.StatusFailed {
			t.Fatalf("job %s: expected StatusFailed; got %v", jobID, got.Status)
		}
		if got.ErrorCode != "PROCESS_RESTART_RECONCILIATION" {
			t.Fatalf("job %s: expected PROCESS_RESTART_RECONCILIATION; got %q", jobID, got.ErrorCode)
		}
		// The error message must contain exactly one occurrence of the error code
		// indicator — not a double-write.
		if strings.Count(got.ErrorMessage, "zombie from process_id=") != 1 {
			t.Fatalf("job %s: error message was written more than once: %q", jobID, got.ErrorMessage)
		}
	}
}

// TestEnqueueAfterReconcile_FreshJobNotDeduped verifies the end-to-end UX fix:
// after a zombie job is reconciled at startup, a fresh Enqueue for the same
// target_key returns a new job (not the reconciled zombie), and the new job
// runs to completion. This is the bench-blocking scenario from CA-175.
func TestEnqueueAfterReconcile_FreshJobNotDeduped(t *testing.T) {
	priorPID := "old-proc-zombie"
	targetKey := "reconcile-enqueue-after-" + uuid.NewString()

	store := llm.NewMemStore()
	zombieJob := &llm.Job{
		ID:        uuid.NewString(),
		Subsystem: llm.SubsystemKnowledge,
		JobType:   "cliff_notes",
		TargetKey: targetKey,
		Status:    llm.StatusGenerating,
		ProcessID: priorPID,
	}
	if _, err := store.Create(t.Context(), zombieJob); err != nil {
		t.Fatalf("Create zombie: %v", err)
	}
	if err := store.ForceUpdatedAt(t.Context(), zombieJob.ID, time.Now().Add(-5*time.Minute)); err != nil {
		t.Fatalf("ForceUpdatedAt: %v", err)
	}

	// Build orchestrator with reconciliation enabled and one worker.
	cfg := Config{
		MaxConcurrency:            1,
		ProgressDebounce:          5 * time.Millisecond,
		Retry:                     RetryPolicy{MaxAttempts: 1},
		SkipStartupReconciliation: false,
	}
	orch := New(store, cfg)
	t.Cleanup(func() { _ = orch.Shutdown(2 * time.Second) })

	// Zombie must be marked failed by reconciliation.
	zombie := orch.GetJob(zombieJob.ID)
	if zombie == nil || zombie.Status != llm.StatusFailed {
		t.Fatalf("expected zombie to be reconciled to StatusFailed; got %v", zombie)
	}
	if zombie.ErrorCode != "PROCESS_RESTART_RECONCILIATION" {
		t.Fatalf("expected PROCESS_RESTART_RECONCILIATION on zombie; got %q", zombie.ErrorCode)
	}

	// Fresh Enqueue for the same target_key must return a different, runnable job.
	freshJob, err := orch.Enqueue(&llm.EnqueueRequest{
		Subsystem:   llm.SubsystemKnowledge,
		LLMProvider: "test",
		JobType:     "cliff_notes",
		TargetKey:   targetKey,
		Run: func(rt llm.Runtime) error {
			return nil // completes immediately
		},
	})
	if err != nil {
		t.Fatalf("Enqueue after reconcile: %v", err)
	}
	if freshJob.ID == zombieJob.ID {
		t.Fatalf("Enqueue returned the reconciled zombie job (ID=%s); expected a fresh job", zombieJob.ID)
	}

	// Fresh job must progress to completion, not stall.
	waitFor(t, 2*time.Second, func() bool {
		j := orch.GetJob(freshJob.ID)
		return j != nil && j.Status == llm.StatusReady
	})

	final := orch.GetJob(freshJob.ID)
	if final == nil || final.Status != llm.StatusReady {
		t.Fatalf("fresh job did not complete: status=%v", final)
	}
}

// TestReconcileZombieJobs_LogsPassEvent verifies the structured log contract:
// after seeding one stale zombie, reconcileZombieJobs must emit exactly one
// orchestrator_zombie_reconciled_pass event with count=1, the current
// process_id, and the zombie's job ID in reconciled_ids.
// Pattern mirrors TestReaper_LogsThresholdKind.
func TestReconcileZombieJobs_LogsPassEvent(t *testing.T) {
	priorPID := "log-event-prior-pid"
	store, zombieID := newReconcileStore(t, priorPID, time.Now().Add(-5*time.Minute))

	logBuf := &syncBuffer{}
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg := Config{
		MaxConcurrency:            0,
		ProgressDebounce:          5 * time.Millisecond,
		Retry:                     RetryPolicy{MaxAttempts: 1},
		SkipStartupReconciliation: false,
	}
	orch := New(store, cfg)
	t.Cleanup(func() { _ = orch.Shutdown(2 * time.Second) })

	output := logBuf.String()

	// The pass summary event must be present.
	if !strings.Contains(output, "orchestrator_zombie_reconciled_pass") {
		t.Errorf("expected orchestrator_zombie_reconciled_pass event; got:\n%s", output)
	}
	// count=1 because exactly one zombie was seeded.
	if !strings.Contains(output, "count=1") {
		t.Errorf("expected count=1 in pass event; got:\n%s", output)
	}
	// current_process_id must appear as the named field with the orchestrator's UUID.
	if !strings.Contains(output, "current_process_id="+orch.ProcessID()) {
		t.Errorf("expected log to contain current_process_id=%s; got:\n%s", orch.ProcessID(), output)
	}
	// The zombie's job ID must appear in the reconciled_ids sample.
	if !strings.Contains(output, zombieID) {
		t.Errorf("expected zombie job ID %q in reconciled_ids; got:\n%s", zombieID, output)
	}
	// The per-zombie warn event must also be present.
	if !strings.Contains(output, "event=orchestrator_zombie_reconciled ") &&
		!strings.Contains(output, "event=orchestrator_zombie_reconciled\n") {
		t.Errorf("expected per-zombie orchestrator_zombie_reconciled warn event; got:\n%s", output)
	}

	// Verify the zombie's final state for completeness.
	got := orch.GetJob(zombieID)
	if got == nil || got.Status != llm.StatusFailed {
		t.Fatalf("expected zombie StatusFailed; got %v", got)
	}
}

// ---------------------------------------------------------------------------
// CA-180: OnJobFailed callback dispatch tests
//
// Each test verifies that Config.OnJobFailed fires from one of the three
// orchestrator failure paths. Fresh construction blocks are used (not
// newReconcileOrchestrator) because that helper does not expose Config for
// caller mutation — wiring OnJobFailed into cfg BEFORE New() is the key
// requirement; a nil callback silently does nothing.
// ---------------------------------------------------------------------------

// TestOnJobFailed_FinalizeFailedPath verifies that OnJobFailed is invoked
// when a job exhausts its retry budget (the finalizeFailed path). The job's
// Run function returns an error immediately so it fails on the first attempt.
func TestOnJobFailed_FinalizeFailedPath(t *testing.T) {
	var capturedJobs []*llm.Job
	var mu sync.Mutex

	store := llm.NewMemStore()
	cfg := Config{
		MaxConcurrency:            1,
		ProgressDebounce:          5 * time.Millisecond,
		Retry:                     RetryPolicy{MaxAttempts: 1}, // fail on first attempt
		SkipStartupReconciliation: true,
		OnJobFailed: func(job *llm.Job) {
			mu.Lock()
			capturedJobs = append(capturedJobs, job)
			mu.Unlock()
		},
	}
	orch := New(store, cfg)
	t.Cleanup(func() { _ = orch.Shutdown(2 * time.Second) })

	job, err := orch.Enqueue(&llm.EnqueueRequest{
		Subsystem:   llm.SubsystemKnowledge,
		LLMProvider: "test",
		JobType:     "build_repository_understanding",
		ArtifactID:  "understanding-id-finalizer",
		TargetKey:   "onfailed-finalize-" + uuid.NewString(),
		Run: func(rt llm.Runtime) error {
			return fmt.Errorf("simulated llm failure")
		},
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Wait for the job to fail.
	waitFor(t, 3*time.Second, func() bool {
		j := orch.GetJob(job.ID)
		return j != nil && j.Status == llm.StatusFailed
	})

	mu.Lock()
	n := len(capturedJobs)
	mu.Unlock()
	if n == 0 {
		t.Fatal("expected OnJobFailed to fire from finalizeFailed; got 0 calls")
	}
	mu.Lock()
	captured := capturedJobs[0]
	mu.Unlock()
	if captured.ID != job.ID {
		t.Fatalf("OnJobFailed received wrong job: got %s, want %s", captured.ID, job.ID)
	}
}

// TestOnJobFailed_ReaperPath verifies that OnJobFailed fires when the reaper
// marks a generating job as stale.
func TestOnJobFailed_ReaperPath(t *testing.T) {
	var callCount atomic.Int64

	store := llm.NewMemStore()
	cfg := Config{
		MaxConcurrency:            1,
		ProgressDebounce:          5 * time.Millisecond,
		Retry:                     RetryPolicy{MaxAttempts: 1},
		SkipStartupReconciliation: true,
		OnJobFailed: func(job *llm.Job) {
			callCount.Add(1)
		},
	}
	orch := New(store, cfg)
	t.Cleanup(func() { _ = orch.Shutdown(2 * time.Second) })

	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })

	job, err := orch.Enqueue(&llm.EnqueueRequest{
		Subsystem:   llm.SubsystemKnowledge,
		LLMProvider: "test",
		JobType:     "build_repository_understanding",
		ArtifactID:  "understanding-id-reaper",
		TargetKey:   "onfailed-reaper-" + uuid.NewString(),
		RunWithContext: func(ctx context.Context, _ llm.Runtime) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-hold:
				return nil
			}
		},
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Wait until the worker sets StatusGenerating before backdating.
	waitFor(t, 2*time.Second, func() bool {
		j := orch.GetJob(job.ID)
		return j != nil && j.Status == llm.StatusGenerating
	})

	// Backdate past staleGeneratingThreshold so the reaper picks it up.
	backdate(t, orch, job.ID, staleGeneratingThreshold+time.Second)
	orch.reapStaleJobs()

	waitFor(t, 2*time.Second, func() bool {
		j := orch.GetJob(job.ID)
		return j != nil && j.Status == llm.StatusFailed
	})

	if callCount.Load() == 0 {
		t.Fatal("expected OnJobFailed to fire from reaper; got 0 calls")
	}
}

// TestOnJobFailed_ReconcilerPath verifies that OnJobFailed fires when
// reconcileZombieJobs marks a startup zombie as failed. The orchestrator is
// constructed with reconciliation enabled and a pre-seeded stale zombie.
func TestOnJobFailed_ReconcilerPath(t *testing.T) {
	var callCount atomic.Int64

	priorPID := "old-proc-onfailed-test"
	store, zombieID := newReconcileStore(t, priorPID, time.Now().Add(-5*time.Minute))

	// Wire OnJobFailed BEFORE New() — this is the critical requirement.
	// reconcileZombieJobs runs synchronously inside New() so the callback
	// must already be set on cfg at that point.
	cfg := Config{
		MaxConcurrency:            0,
		ProgressDebounce:          5 * time.Millisecond,
		Retry:                     RetryPolicy{MaxAttempts: 1},
		SkipStartupReconciliation: false, // exercise the reconciler path
		OnJobFailed: func(job *llm.Job) {
			callCount.Add(1)
		},
	}
	orch := New(store, cfg)
	t.Cleanup(func() { _ = orch.Shutdown(2 * time.Second) })

	// Reconciliation runs inside New() — by the time we reach here the
	// zombie must already be failed and the callback must have fired.
	zombie := orch.GetJob(zombieID)
	if zombie == nil || zombie.Status != llm.StatusFailed {
		t.Fatalf("expected zombie reconciled to StatusFailed; got %v", zombie)
	}

	if callCount.Load() == 0 {
		t.Fatal("expected OnJobFailed to fire from reconcileZombieJobs; got 0 calls")
	}
}
