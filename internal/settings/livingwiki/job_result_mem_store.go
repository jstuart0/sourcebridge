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

// Save appends a copy of result to the store (append-only, matching DB semantics).
func (m *MemJobResultStore) Save(_ context.Context, _ string, result *LivingWikiJobResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *result
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

// LastResultForRepo returns the most recently saved result for the given tenant
// and repo, or nil if none exist. The in-memory implementation matches the DB
// semantics (ORDER BY started_at DESC LIMIT 1) by returning the last appended
// matching row.
func (m *MemJobResultStore) LastResultForRepo(_ context.Context, _ string, repoID string) (*LivingWikiJobResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// Walk in reverse — last inserted is the most recent.
	for i := len(m.rows) - 1; i >= 0; i-- {
		if m.rows[i].RepoID == repoID {
			cp := *m.rows[i]
			return &cp, nil
		}
	}
	return nil, nil
}
