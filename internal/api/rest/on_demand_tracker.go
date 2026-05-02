// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"sync"
)

// OnDemandTracker counts active on-demand Living Wiki page-generation
// requests. The counter is incremented BEFORE settings lookup and LLM
// resolution begin (via Admit/TryAdmit) so the drain logic in AwaitDrain
// can rely on the count being accurate throughout the whole request
// lifetime — not just during the LLM call. CA-142.
//
// count and draining are protected by mu. This is intentional: TryAdmit
// must check draining and increment count under the SAME lock that
// BeginDrain uses to flip draining=true, so there is no window where a
// request passes the gate and then drain proceeds before the count
// increments. Using atomic.Int64 for count while writing draining under mu
// would recreate the race; we avoid mixed-concurrency primitives here.
type OnDemandTracker struct {
	mu       sync.Mutex
	cond     *sync.Cond
	count    int64
	draining bool
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

// TryAdmit atomically checks whether the server is draining and, if not,
// increments the in-flight counter in one operation under the same mutex
// that MarkDraining uses. Returns (admission, true) when admitted, or
// (nil, false) when the server is draining. This eliminates the TOCTOU
// race between IsDraining() and AdmitOnDemand() that existed when those
// were separate calls. CA-142.
func (t *OnDemandTracker) TryAdmit() (*Admission, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.draining {
		return nil, false
	}
	t.count++
	return &Admission{t: t}, true
}

// MarkDraining sets the draining flag. Once set, TryAdmit returns
// (nil, false) for all subsequent callers. Uses the same mutex as
// TryAdmit to ensure atomicity. Called from Server.BeginDrain. CA-142.
func (t *OnDemandTracker) MarkDraining() {
	t.mu.Lock()
	t.draining = true
	t.mu.Unlock()
}

// IsDraining reports whether the tracker has been marked as draining.
func (t *OnDemandTracker) IsDraining() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.draining
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
func (t *OnDemandTracker) Count() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.count
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
	for t.count > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		t.cond.Wait()
	}
	return ctx.Err()
}
