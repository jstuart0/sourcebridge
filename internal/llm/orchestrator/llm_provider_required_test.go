// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/llm"
)

// TestEnqueue_LLMBackedEmptyProvider_ReturnsErrLLMProviderRequired pins the
// R3 followups B1 contract: an LLM-backed enqueue with an empty
// LLMProvider field returns ErrLLMProviderRequired BEFORE any dedupe or
// inflight claim, so a misconfigured-resolver request cannot:
//   - silently persist a job with provider="" (attribution gap), or
//   - attach to an unrelated active job that already owns the same target_key
//     (which would surface that unrelated job to the broken caller as if
//     it were "their" job), or
//   - disturb in-flight registry state we don't own.
func TestEnqueue_LLMBackedEmptyProvider_ReturnsErrLLMProviderRequired(t *testing.T) {
	orch := newTestOrchestrator(t, Config{MaxConcurrency: 1})

	// Capture warn logs so we can assert the structured warn fired.
	var logBuf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	var ran atomic.Bool
	_, err := orch.Enqueue(&llm.EnqueueRequest{
		Subsystem:   llm.SubsystemKnowledge,
		JobType:     "cliff_notes",
		TargetKey:   "repo-1:cliff_notes:empty-provider",
		LLMProvider: "", // empty: this is the failure case
		Run: func(rt llm.Runtime) error {
			ran.Store(true)
			return nil
		},
	})
	if err == nil {
		t.Fatal("expected ErrLLMProviderRequired, got nil")
	}
	if !errors.Is(err, ErrLLMProviderRequired) {
		t.Fatalf("expected errors.Is(err, ErrLLMProviderRequired); got %v", err)
	}
	if !strings.Contains(err.Error(), "/admin/llm") {
		t.Errorf("expected error string to point operator at /admin/llm; got %q", err.Error())
	}

	// The Run closure must NOT have executed.
	time.Sleep(50 * time.Millisecond)
	if ran.Load() {
		t.Error("expected Run closure NOT to execute on rejected enqueue")
	}

	// No job persisted under this target key.
	if existing := orch.store.GetActiveByTargetKey("repo-1:cliff_notes:empty-provider"); existing != nil {
		t.Errorf("expected no active job for rejected target_key; found %+v", existing)
	}

	// Warn log fired.
	if !strings.Contains(logBuf.String(), "llm_job_enqueue_missing_provider") {
		t.Errorf("expected llm_job_enqueue_missing_provider warn log; buf=%q", logBuf.String())
	}
}

// TestEnqueue_LLMBackedEmptyProvider_DoesNotDisturbExistingInflightClaim
// pins the codex r1b Medium fix: the up-front empty-provider check must
// run BEFORE any inflight registry mutation. A pre-existing claim on
// the same target_key must remain untouched after the rejected enqueue.
func TestEnqueue_LLMBackedEmptyProvider_DoesNotDisturbExistingInflightClaim(t *testing.T) {
	orch := newTestOrchestrator(t, Config{MaxConcurrency: 1})
	const targetKey = "repo-1:reasoning:protected_target"

	// Park the first job so it stays in-flight while we attempt the
	// rejected empty-provider enqueue against the same target_key.
	gate := make(chan struct{})
	first, err := orch.Enqueue(&llm.EnqueueRequest{
		Subsystem:   llm.SubsystemReasoning,
		JobType:     "discuss",
		TargetKey:   targetKey,
		LLMProvider: "anthropic", // valid: this enqueue should succeed
		Run: func(rt llm.Runtime) error {
			<-gate
			return nil
		},
	})
	if err != nil {
		t.Fatalf("first enqueue failed: %v", err)
	}
	waitFor(t, time.Second, func() bool {
		j := orch.GetJob(first.ID)
		return j != nil && j.Status == llm.StatusGenerating
	})

	// Snapshot the inflight registry's claim for targetKey.
	winnerBefore, _ := orch.inflight.peek(targetKey)
	if winnerBefore != first.ID {
		t.Fatalf("setup: expected inflight winner %q, got %q", first.ID, winnerBefore)
	}

	// Rejected empty-provider enqueue against the SAME target_key.
	_, badErr := orch.Enqueue(&llm.EnqueueRequest{
		Subsystem:   llm.SubsystemReasoning,
		JobType:     "discuss",
		TargetKey:   targetKey,
		LLMProvider: "", // empty: rejected
		Run: func(rt llm.Runtime) error {
			t.Error("rejected enqueue's Run must not execute")
			return nil
		},
	})
	if !errors.Is(badErr, ErrLLMProviderRequired) {
		t.Fatalf("expected ErrLLMProviderRequired; got %v", badErr)
	}

	// Critical assertion: the existing inflight claim is undisturbed.
	winnerAfter, _ := orch.inflight.peek(targetKey)
	if winnerAfter != first.ID {
		t.Errorf("rejected enqueue disturbed unrelated active claim: before=%q after=%q",
			first.ID, winnerAfter)
	}

	// And the existing job's store row is also undisturbed.
	stored := orch.store.GetByID(first.ID)
	if stored == nil {
		t.Fatal("expected first job's store row to still exist")
	}
	if stored.Status.IsTerminal() {
		t.Errorf("rejected enqueue should not have terminated the unrelated job; got status=%s", stored.Status)
	}

	// Release the gate so the test cleans up.
	close(gate)
	waitFor(t, time.Second, func() bool {
		j := orch.GetJob(first.ID)
		return j != nil && j.Status.IsTerminal()
	})
}

// TestEnqueue_NonLLMBackedEmptyProvider_AcceptsCleanly verifies the
// hard-block is scoped to LLM-backed subsystems. CPU-bound clustering
// graph job_types still accept an empty provider (the AST lint also
// permits empty there).
func TestEnqueue_NonLLMBackedEmptyProvider_AcceptsCleanly(t *testing.T) {
	orch := newTestOrchestrator(t, Config{MaxConcurrency: 1})

	var ran atomic.Bool
	job, err := orch.Enqueue(&llm.EnqueueRequest{
		Subsystem:   llm.SubsystemClustering,
		JobType:     "build_graph", // NOT relabel_clusters; non-LLM-backed
		TargetKey:   "repo-1:clustering:build_graph",
		LLMProvider: "", // empty: ALLOWED for non-LLM-backed
		Run: func(rt llm.Runtime) error {
			ran.Store(true)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("non-LLM-backed enqueue should accept empty provider; got %v", err)
	}
	if job == nil {
		t.Fatal("expected a job from non-LLM-backed enqueue")
	}
	waitFor(t, time.Second, func() bool { return ran.Load() })
}

// TestEnqueue_LLMBackedNonEmptyProvider_StillSucceeds pins that the
// happy path is unaffected by the new check.
func TestEnqueue_LLMBackedNonEmptyProvider_StillSucceeds(t *testing.T) {
	orch := newTestOrchestrator(t, Config{MaxConcurrency: 1})

	var ran atomic.Bool
	job, err := orch.Enqueue(&llm.EnqueueRequest{
		Subsystem:   llm.SubsystemKnowledge,
		JobType:     "cliff_notes",
		TargetKey:   "repo-1:cliff_notes:happy",
		LLMProvider: "anthropic",
		Run: func(rt llm.Runtime) error {
			ran.Store(true)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("LLM-backed enqueue with provider should succeed; got %v", err)
	}
	if job == nil {
		t.Fatal("expected a job")
	}
	waitFor(t, time.Second, func() bool {
		j := orch.GetJob(job.ID)
		return j != nil && j.Status == llm.StatusReady
	})
	if !ran.Load() {
		t.Fatal("Run closure should have executed")
	}
}

// TestEnqueueSync_LLMBackedEmptyProvider_PropagatesError pins that the
// blocking variant propagates ErrLLMProviderRequired (single up-front
// check inside Enqueue covers both async and sync paths).
func TestEnqueueSync_LLMBackedEmptyProvider_PropagatesError(t *testing.T) {
	orch := newTestOrchestrator(t, Config{MaxConcurrency: 1})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := orch.EnqueueSync(ctx, &llm.EnqueueRequest{
		Subsystem:   llm.SubsystemKnowledge,
		JobType:     "cliff_notes",
		TargetKey:   "repo-1:cliff_notes:sync-empty",
		LLMProvider: "",
		Run: func(rt llm.Runtime) error {
			t.Error("Run must not execute on rejected EnqueueSync")
			return nil
		},
	})
	if !errors.Is(err, ErrLLMProviderRequired) {
		t.Fatalf("expected ErrLLMProviderRequired from EnqueueSync; got %v", err)
	}
}
