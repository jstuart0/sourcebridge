// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Shutdown sequence integration tests (CA-142).
//
// These verify the BeginDrain → AwaitDrain → CancelAndWait lifecycle that
// cli/serve.go exercises on SIGTERM. Tests are in the rest package so they
// can construct the Server struct directly without requiring a live JWTManager
// or HTTP listener.
package rest

import (
	"context"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/orchestrator"
)

// newDrainTestServer builds a minimal Server with an in-memory orchestrator
// for shutdown-sequence tests. It bypasses setupRouter entirely to avoid
// dependencies on config, JWT, etc.
func newDrainTestServer(t *testing.T) *Server {
	t.Helper()
	store := llm.NewMemStore()
	orch := orchestrator.New(store, orchestrator.Config{
		MaxConcurrency:   2,
		ProgressDebounce: 5 * time.Millisecond,
		Retry:            orchestrator.RetryPolicy{MaxAttempts: 1},
	})
	t.Cleanup(func() { _ = orch.Shutdown(2 * time.Second) })

	s := &Server{
		orchestrator: orch,
		OnDemand:     NewOnDemandTracker(),
	}
	return s
}

// TestBeginDrainIdempotent verifies that a second BeginDrain call is a no-op
// (returns false) — analogous to the SIGTERM-dedup Once in cli/serve.go. CA-142.
func TestBeginDrainIdempotent(t *testing.T) {
	t.Parallel()

	s := newDrainTestServer(t)

	first := s.BeginDrain("test-first")
	if !first {
		t.Fatal("expected first BeginDrain to return true")
	}

	second := s.BeginDrain("test-second")
	if second {
		t.Fatal("expected second BeginDrain to return false (idempotent)")
	}

	if !s.IsDraining() {
		t.Fatal("expected IsDraining to be true after BeginDrain")
	}
}

// TestAwaitDrainEmptyQueueReturnsImmediately verifies AwaitDrain returns nil
// immediately when there are no in-flight jobs. CA-142.
func TestAwaitDrainEmptyQueueReturnsImmediately(t *testing.T) {
	t.Parallel()

	s := newDrainTestServer(t)
	s.BeginDrain("test")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	start := time.Now()
	if err := s.AwaitDrain(ctx); err != nil {
		t.Fatalf("AwaitDrain on empty queue: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("AwaitDrain took %s on empty queue; expected fast return", elapsed)
	}
}

// TestAwaitDrainWaitsForInFlightJob verifies AwaitDrain blocks while a job is
// running and returns after it completes — the core CA-142 guarantee. CA-142.
func TestAwaitDrainWaitsForInFlightJob(t *testing.T) {
	t.Parallel()

	s := newDrainTestServer(t)
	orch := s.orchestrator

	jobStarted := make(chan struct{})
	allowFinish := make(chan struct{})

	_, err := orch.Enqueue(&llm.EnqueueRequest{
		Subsystem:   "living_wiki",
		LLMProvider: "test",
		JobType:     "cold_start",
		TargetKey:   "lw:test:await-drain-repo",
		Run: func(rt llm.Runtime) error {
			close(jobStarted)
			<-allowFinish
			return nil
		},
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	select {
	case <-jobStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("job did not start in time")
	}

	s.BeginDrain("test")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	drainDone := make(chan error, 1)
	go func() { drainDone <- s.AwaitDrain(ctx) }()

	select {
	case err := <-drainDone:
		t.Fatalf("AwaitDrain returned early (err=%v); expected to wait for job", err)
	case <-time.After(50 * time.Millisecond):
		// expected — still blocking
	}

	close(allowFinish)

	select {
	case err := <-drainDone:
		if err != nil {
			t.Fatalf("AwaitDrain returned error after job finished: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("AwaitDrain did not return after job finished")
	}
}

// TestCancelAndWaitAfterAwaitDrain verifies the full three-step sequence:
// BeginDrain → AwaitDrain → CancelAndWait completes without error and leaves
// zero active worker goroutines. CA-142.
func TestCancelAndWaitAfterAwaitDrain(t *testing.T) {
	t.Parallel()

	s := newDrainTestServer(t)
	orch := s.orchestrator

	s.BeginDrain("test")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := s.AwaitDrain(ctx); err != nil {
		t.Fatalf("AwaitDrain: %v", err)
	}

	if err := orch.CancelAndWait(5 * time.Second); err != nil {
		t.Fatalf("CancelAndWait: %v", err)
	}

	if n := orch.ActiveWorkerCount(); n != 0 {
		t.Fatalf("expected 0 active workers after CancelAndWait, got %d", n)
	}
}

// TestBeginDrainSetsMarkDraining verifies that BeginDrain sets IntakePaused
// on the orchestrator, which confirms MarkDraining is also called (the two
// are always done together in BeginDrain). CA-142 Critical #1.
func TestBeginDrainSetsMarkDraining(t *testing.T) {
	t.Parallel()

	s := newDrainTestServer(t)
	if !s.BeginDrain("test") {
		t.Fatal("expected first BeginDrain to return true")
	}

	if !s.orchestrator.IntakePaused() {
		t.Fatal("expected IntakePaused to be true after BeginDrain")
	}
}
