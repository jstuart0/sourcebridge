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
type OnDemandTracker struct {
	mu    sync.Mutex
	cond  *sync.Cond
	count int64
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
	t.mu.Lock()
	t.count++
	t.mu.Unlock()
	return &Admission{t: t}
}

// Release decrements the counter and wakes any goroutine waiting in
// WaitZero. It is idempotent and safe to call from a defer.
func (a *Admission) Release() {
	t := a.t
	t.mu.Lock()
	if t.count > 0 {
		t.count--
	}
	t.cond.Broadcast()
	t.mu.Unlock()
}

// Count returns the current number of admitted (in-flight) requests.
// Used for logging; no locking guarantees freshness across calls.
func (t *OnDemandTracker) Count() int64 {
	return atomic.LoadInt64(&t.count)
}

// WaitZero blocks until the in-flight count reaches zero or ctx is
// cancelled. Returns ctx.Err() if the context is cancelled before the
// count reaches zero, nil otherwise.
func (t *OnDemandTracker) WaitZero(ctx context.Context) error {
	// Wake the waiter when the context finishes so it can stop blocking.
	stop := context.AfterFunc(ctx, func() {
		t.cond.Broadcast()
	})
	defer stop()

	t.mu.Lock()
	defer t.mu.Unlock()
	for t.count > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		t.cond.Wait()
	}
	return ctx.Err()
}
