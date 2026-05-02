// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestOnDemandTrackerAdmitRelease verifies basic admit/release semantics.
func TestOnDemandTrackerAdmitRelease(t *testing.T) {
	t.Parallel()

	tr := NewOnDemandTracker()
	if tr.Count() != 0 {
		t.Fatalf("expected count=0, got %d", tr.Count())
	}

	a1 := tr.Admit()
	a2 := tr.Admit()
	if tr.Count() != 2 {
		t.Fatalf("expected count=2 after two admits, got %d", tr.Count())
	}

	a1.Release()
	if tr.Count() != 1 {
		t.Fatalf("expected count=1 after one release, got %d", tr.Count())
	}

	a2.Release()
	if tr.Count() != 0 {
		t.Fatalf("expected count=0 after all releases, got %d", tr.Count())
	}
}

// TestOnDemandTrackerWaitZeroReturnsFast verifies WaitZero returns immediately
// when count is already zero.
func TestOnDemandTrackerWaitZeroReturnsFast(t *testing.T) {
	t.Parallel()

	tr := NewOnDemandTracker()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	start := time.Now()
	if err := tr.WaitZero(ctx); err != nil {
		t.Fatalf("WaitZero on empty tracker: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("WaitZero took %s on empty tracker; expected fast return", elapsed)
	}
}

// TestOnDemandTrackerWaitZeroBlocksUntilRelease verifies WaitZero blocks
// while there are active admissions and returns after the last release.
func TestOnDemandTrackerWaitZeroBlocksUntilRelease(t *testing.T) {
	t.Parallel()

	tr := NewOnDemandTracker()
	adm := tr.Admit()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	waitDone := make(chan error, 1)
	go func() {
		defer wg.Done()
		waitDone <- tr.WaitZero(ctx)
	}()

	// Give the goroutine time to park.
	time.Sleep(20 * time.Millisecond)

	// Release should wake the waiter.
	adm.Release()

	wg.Wait()
	if err := <-waitDone; err != nil {
		t.Fatalf("WaitZero returned error after release: %v", err)
	}
}

// TestOnDemandTrackerWaitZeroCancelled verifies WaitZero returns ctx.Err()
// when the context is cancelled while admissions are still held.
func TestOnDemandTrackerWaitZeroCancelled(t *testing.T) {
	t.Parallel()

	tr := NewOnDemandTracker()
	_ = tr.Admit() // never released — keeps count at 1

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := tr.WaitZero(ctx)
	if err == nil {
		t.Fatal("expected WaitZero to return an error on context cancellation")
	}
}

// TestOnDemandTrackerConcurrentAdmitRelease is a race-detector exercise:
// N goroutines each admit-then-release, with WaitZero racing alongside.
func TestOnDemandTrackerConcurrentAdmitRelease(t *testing.T) {
	t.Parallel()

	tr := NewOnDemandTracker()
	const n = 50

	var startAll sync.WaitGroup
	startAll.Add(n)

	for i := 0; i < n; i++ {
		go func() {
			startAll.Done()
			startAll.Wait() // all goroutines start together
			adm := tr.Admit()
			time.Sleep(time.Millisecond)
			adm.Release()
		}()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := tr.WaitZero(ctx); err != nil {
		t.Fatalf("WaitZero: %v", err)
	}
	if tr.Count() != 0 {
		t.Fatalf("expected count=0 after all goroutines finish, got %d", tr.Count())
	}
}

// TestOnDemandTrackerAdmissionDuringDrain verifies that Admit/Release work
// correctly after a drain flag is set externally (the tracker itself is
// stateless about drain; this is a wiring concern tested here for completeness).
func TestOnDemandTrackerAdmissionDuringDrain(t *testing.T) {
	t.Parallel()

	tr := NewOnDemandTracker()

	// Simulate drain: one active admission, then WaitZero races with Release.
	adm := tr.Admit()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- tr.WaitZero(ctx) }()

	time.Sleep(5 * time.Millisecond)
	adm.Release()

	if err := <-done; err != nil {
		t.Fatalf("WaitZero after drain-admit-release: %v", err)
	}
}
