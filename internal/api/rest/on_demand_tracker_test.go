// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"sync"
	"testing"
	"time"
)

// mustAdmit calls TryAdmit and fails the test if admission is rejected.
func mustAdmit(t *testing.T, tr *OnDemandTracker) *Admission {
	t.Helper()
	adm, ok := tr.TryAdmit()
	if !ok {
		t.Fatal("TryAdmit returned false (draining) unexpectedly")
	}
	return adm
}

// TestOnDemandTrackerAdmitRelease verifies basic admit/release semantics.
func TestOnDemandTrackerAdmitRelease(t *testing.T) {
	t.Parallel()

	tr := NewOnDemandTracker()
	if tr.Count() != 0 {
		t.Fatalf("expected count=0, got %d", tr.Count())
	}

	a1 := mustAdmit(t, tr)
	a2 := mustAdmit(t, tr)
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
	adm := mustAdmit(t, tr)

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
	_ = mustAdmit(t, tr) // never released — keeps count at 1

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
			adm, ok := tr.TryAdmit()
			if !ok {
				// Admission rejected means MarkDraining was called concurrently;
				// that's fine for this concurrent test.
				return
			}
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

// TestOnDemandTrackerTryAdmitRejectsDuringDrain verifies TryAdmit returns
// (nil, false) after MarkDraining is called. CA-142.
func TestOnDemandTrackerTryAdmitRejectsDuringDrain(t *testing.T) {
	t.Parallel()

	tr := NewOnDemandTracker()
	tr.MarkDraining()

	adm, ok := tr.TryAdmit()
	if ok {
		t.Fatal("TryAdmit should return false after MarkDraining")
	}
	if adm != nil {
		t.Fatal("TryAdmit should return nil admission after MarkDraining")
	}
	if tr.Count() != 0 {
		t.Fatalf("expected count=0 after rejected TryAdmit, got %d", tr.Count())
	}
}

// TestOnDemandTrackerMarkDrainingIdempotent verifies MarkDraining is safe
// to call multiple times and does not panic. CA-142.
func TestOnDemandTrackerMarkDrainingIdempotent(t *testing.T) {
	t.Parallel()

	tr := NewOnDemandTracker()
	tr.MarkDraining()
	tr.MarkDraining() // must not panic or corrupt state

	if !tr.IsDraining() {
		t.Fatal("expected IsDraining to be true")
	}
}

// TestOnDemandTrackerTryAdmitAtomicWithMarkDraining verifies the atomicity
// guarantee: if MarkDraining fires while a TryAdmit goroutine is in flight,
// exactly one of them wins cleanly — either the admission is granted (count=1,
// not draining at admit time) or rejected (count=0, draining at admit time).
// Under the race detector, any missed lock would produce a data-race failure.
// CA-142.
func TestOnDemandTrackerTryAdmitAtomicWithMarkDraining(t *testing.T) {
	t.Parallel()

	const trials = 200
	for i := 0; i < trials; i++ {
		tr := NewOnDemandTracker()

		// Two goroutines race: one calls TryAdmit, the other calls MarkDraining.
		var wg sync.WaitGroup
		wg.Add(2)

		var (
			admitted bool
			adm      *Admission
		)

		go func() {
			defer wg.Done()
			adm, admitted = tr.TryAdmit()
		}()
		go func() {
			defer wg.Done()
			tr.MarkDraining()
		}()

		wg.Wait()

		if admitted {
			// Admitted before drain: count must be 1 (or 0 if released),
			// no negative or inconsistent state.
			if tr.Count() < 0 {
				t.Fatalf("trial %d: count went negative after atomic admit", i)
			}
			adm.Release()
			if tr.Count() != 0 {
				t.Fatalf("trial %d: count=%d after release; expected 0", i, tr.Count())
			}
		} else {
			// Rejected: count must be 0.
			if tr.Count() != 0 {
				t.Fatalf("trial %d: count=%d after rejected TryAdmit; expected 0", i, tr.Count())
			}
		}
	}
}

// TestOnDemandTrackerAdmissionDuringDrainReachesZero verifies WaitZero
// completes after an in-flight admission releases, even when MarkDraining
// has been called. CA-142.
func TestOnDemandTrackerAdmissionDuringDrainReachesZero(t *testing.T) {
	t.Parallel()

	tr := NewOnDemandTracker()

	// Admit before drain starts.
	adm := mustAdmit(t, tr)

	// Now begin drain — future admits are blocked.
	tr.MarkDraining()

	// Subsequent admit must be rejected.
	if _, ok := tr.TryAdmit(); ok {
		t.Fatal("TryAdmit should return false after MarkDraining")
	}

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
