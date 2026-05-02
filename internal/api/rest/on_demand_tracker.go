// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"sync"
	"sync/atomic"
)

// OnDemandTracker counts active on-demand Living Wiki page-generation
// requests. The counter is incremented BEFORE settings lookup and LLM
// resolution begin (via Admit) so the drain logic in AwaitDrain can
// rely on the count being accurate throughout the whole request
// lifetime — not just during the LLM call. CA-142.
//
// count uses atomic.Int64 so Count() can read without holding mu (safe
// for logging). Admit and Release use atomic operations too; mu+cond is
// used only by WaitZero for its blocking wait.
type OnDemandTracker struct {
	mu    sync.Mutex
	cond  *sync.Cond
	count atomic.Int64
}

// Admission is a live token that represents one admitted on-demand
// request. Call Release() exactly once when the request completes.
type Admission struct {
	t *OnDemandTracker
}

// NewOnDemandTracker creates a tracker ready for use.
func NewOnDemandTracker() *OnDemandTracker {
	t := &OnDemandTracker{}
	t.cond = sync.NewCond(&t.mu)
	return t
}

// Admit increments the active-request counter and returns an Admission
// token. The caller MUST defer admission.Release() immediately after
// calling Admit so the counter is always decremented.
func (t *OnDemandTracker) Admit() *Admission {
	t.count.Add(1)
	return &Admission{t: t}
}

// Release decrements the counter and wakes any goroutine waiting in
// WaitZero. It is idempotent and safe to call from a defer.
func (a *Admission) Release() {
	t := a.t
	// Guard against double-release driving count negative.
	if t.count.Load() > 0 {
		t.count.Add(-1)
	}
	// Wake WaitZero waiters so they can re-check the count.
	t.mu.Lock()
	t.cond.Broadcast()
	t.mu.Unlock()
}

// Count returns the current number of admitted (in-flight) requests.
// Uses an atomic load — safe for logging without holding mu.
func (t *OnDemandTracker) Count() int64 {
	return t.count.Load()
}

// WaitZero blocks until the in-flight count reaches zero or ctx is
// cancelled. Returns ctx.Err() if the context is cancelled before the
// count reaches zero, nil otherwise.
func (t *OnDemandTracker) WaitZero(ctx context.Context) error {
	// Wake the waiter when the context finishes so it can stop blocking.
	stop := context.AfterFunc(ctx, func() {
		t.mu.Lock()
		t.cond.Broadcast()
		t.mu.Unlock()
	})
	defer stop()

	t.mu.Lock()
	defer t.mu.Unlock()
	for t.count.Load() > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		t.cond.Wait()
	}
	return ctx.Err()
}
