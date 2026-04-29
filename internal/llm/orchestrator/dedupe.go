// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package orchestrator

import "sync"

// inflightRegistry is an in-process cache of jobs that are currently
// enqueued or running, keyed by TargetKey. It serves as the fast path for
// deduplication — a burst of identical requests never reaches the store at
// all. The JobStore's GetActiveByTargetKey is the durable backstop for
// cases where the orchestrator process has restarted but the DB still has
// an active record.
//
// The registry tracks job ids, not full job records, so it stays light.
// Callers resolve the id back to a full record via JobStore.GetByID.
type inflightRegistry struct {
	mu    sync.RWMutex
	byKey map[string]string // targetKey -> jobID
}

func newInflightRegistry() *inflightRegistry {
	return &inflightRegistry{byKey: make(map[string]string)}
}

// claim registers a target key to a job id. Returns the existing job id
// (and false) if the key is already claimed, or the new id (and true) on
// success. The operation is atomic so two goroutines racing to enqueue
// the same target cannot both win.
func (r *inflightRegistry) claim(targetKey, jobID string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.byKey[targetKey]; ok && existing != "" {
		return existing, false
	}
	r.byKey[targetKey] = jobID
	return jobID, true
}

// release removes a target key from the registry. Called when a job
// reaches a terminal state. Safe to call with an empty key (no-op).
func (r *inflightRegistry) release(targetKey string) {
	if targetKey == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byKey, targetKey)
}

// size returns the number of currently-tracked jobs. Useful for tests
// and for the Monitor page's queue-depth metric.
func (r *inflightRegistry) size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byKey)
}

// peek returns the currently-claimed job id for a target key, without
// mutating the registry. (jobID, true) when claimed; ("", false) when
// not. R3 followups B1 tests assert that a rejected empty-provider
// enqueue does not disturb an unrelated active claim.
func (r *inflightRegistry) peek(targetKey string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.byKey[targetKey]
	if !ok || id == "" {
		return "", false
	}
	return id, true
}
