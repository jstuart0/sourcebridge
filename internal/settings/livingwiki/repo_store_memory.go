// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package livingwiki

import (
	"context"
	"sync"
)

// RepoSettingsMemStore is an in-memory [RepoSettingsStore] for tests and
// local dev. Not safe for production (no persistence, no encryption).
type RepoSettingsMemStore struct {
	mu   sync.RWMutex
	rows map[repoKey]RepositoryLivingWikiSettings
}

type repoKey struct{ tenantID, repoID string }

// NewRepoSettingsMemStore returns an empty in-memory store.
func NewRepoSettingsMemStore() *RepoSettingsMemStore {
	return &RepoSettingsMemStore{
		rows: make(map[repoKey]RepositoryLivingWikiSettings),
	}
}

// Compile-time interface check.
var _ RepoSettingsStore = (*RepoSettingsMemStore)(nil)

func (m *RepoSettingsMemStore) GetRepoSettings(_ context.Context, tenantID, repoID string) (*RepositoryLivingWikiSettings, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if s, ok := m.rows[repoKey{tenantID, repoID}]; ok {
		cp := s
		return &cp, nil
	}
	return nil, nil
}

func (m *RepoSettingsMemStore) SetRepoSettings(_ context.Context, s RepositoryLivingWikiSettings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, exists := m.rows[repoKey{s.TenantID, s.RepoID}]
	if exists {
		s.Version = existing.Version + 1
	} else {
		s.Version = 1
	}
	m.rows[repoKey{s.TenantID, s.RepoID}] = s
	return nil
}

// SetRepoSettingsIfVersion is the optimistic-concurrency variant (CA-158).
// Returns ErrLWikiSettingsVersionConflict when the stored version no longer
// matches expectedVersion.  expectedVersion==0 is treated as unconditional
// (no prior row to compare against).
func (m *RepoSettingsMemStore) SetRepoSettingsIfVersion(_ context.Context, s RepositoryLivingWikiSettings, expectedVersion int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, exists := m.rows[repoKey{s.TenantID, s.RepoID}]
	if expectedVersion != 0 && exists && existing.Version != expectedVersion {
		return ErrLWikiSettingsVersionConflict
	}
	if exists {
		s.Version = existing.Version + 1
	} else {
		s.Version = 1
	}
	m.rows[repoKey{s.TenantID, s.RepoID}] = s
	return nil
}

func (m *RepoSettingsMemStore) ListEnabledRepos(_ context.Context, tenantID string) ([]RepositoryLivingWikiSettings, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []RepositoryLivingWikiSettings
	for k, s := range m.rows {
		if k.tenantID == tenantID && s.Enabled {
			cp := s
			out = append(out, cp)
		}
	}
	return out, nil
}

func (m *RepoSettingsMemStore) DeleteRepoSettings(_ context.Context, tenantID, repoID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, repoKey{tenantID, repoID})
	return nil
}

func (m *RepoSettingsMemStore) RepositoriesUsingSink(_ context.Context, tenantID, integrationName string) ([]RepositoryLivingWikiSettings, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []RepositoryLivingWikiSettings
	for k, s := range m.rows {
		if k.tenantID != tenantID {
			continue
		}
		for _, sink := range s.Sinks {
			if sink.IntegrationName == integrationName {
				cp := s
				out = append(out, cp)
				break
			}
		}
	}
	return out, nil
}
