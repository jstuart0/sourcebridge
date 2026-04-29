// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package livingwiki

import (
	"context"
	"sync"
)

// MemJobResultStore is an in-memory [JobResultStore] for tests and local dev.
// Results are stored in insertion order. Not safe for production (no persistence).
type MemJobResultStore struct {
	mu   sync.RWMutex
	rows []*LivingWikiJobResult
}

// NewMemJobResultStore returns an empty in-memory store.
func NewMemJobResultStore() *MemJobResultStore {
	return &MemJobResultStore{}
}

// Compile-time interface check.
var _ JobResultStore = (*MemJobResultStore)(nil)

// Save persists a copy of result. Idempotent by JobID: a second Save with
// the same JobID replaces the existing row in place. Matches the SurrealDB
// implementation's UPSERT-by-job-id semantics.
func (m *MemJobResultStore) Save(_ context.Context, _ string, result *LivingWikiJobResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *result
	for i, existing := range m.rows {
		if existing.JobID == cp.JobID {
			m.rows[i] = &cp
			return nil
		}
	}
	m.rows = append(m.rows, &cp)
	return nil
}

// GetByJobID returns the result with the given JobID, or nil if not found.
func (m *MemJobResultStore) GetByJobID(_ context.Context, jobID string) (*LivingWikiJobResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, r := range m.rows {
		if r.JobID == jobID {
			cp := *r
			return &cp, nil
		}
	}
	return nil, nil
}

// LastResultForRepo returns the result with the greatest StartedAt for the
// given (tenantID, repoID), matching the Surreal implementation. After Save
// became upsert-by-job-id, scanning insertion order alone is no longer
// correct — an upserted row keeps its old slice position even after its
// StartedAt changes, so we explicitly select by max StartedAt.
func (m *MemJobResultStore) LastResultForRepo(_ context.Context, _ string, repoID string) (*LivingWikiJobResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var latest *LivingWikiJobResult
	for _, r := range m.rows {
		if r.RepoID != repoID {
			continue
		}
		if latest == nil || r.StartedAt.After(latest.StartedAt) {
			latest = r
		}
	}
	if latest == nil {
		return nil, nil
	}
	cp := *latest
	return &cp, nil
}
