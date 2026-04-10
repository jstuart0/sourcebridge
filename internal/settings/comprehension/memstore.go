// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package comprehension

import (
	"fmt"
	"sync"
)

// MemStore is an in-memory implementation of Store for tests.
type MemStore struct {
	mu           sync.RWMutex
	settings     map[string]*Settings          // keyed by "scopeType:scopeKey"
	capabilities map[string]*ModelCapabilities // keyed by modelID
}

// NewMemStore creates an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{
		settings:     make(map[string]*Settings),
		capabilities: make(map[string]*ModelCapabilities),
	}
}

func settingsKey(scope Scope) string {
	return fmt.Sprintf("%s:%s", scope.Type, scope.Key)
}

func (m *MemStore) GetSettings(scope Scope) (*Settings, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.settings[settingsKey(scope)]
	if !ok {
		return nil, nil
	}
	cp := *s
	return &cp, nil
}

func (m *MemStore) SetSettings(s *Settings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := settingsKey(Scope{Type: s.ScopeType, Key: s.ScopeKey})
	m.settings[key] = s
	return nil
}

func (m *MemStore) DeleteSettings(scope Scope) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.settings, settingsKey(scope))
	return nil
}

func (m *MemStore) ListSettings() ([]Settings, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]Settings, 0, len(m.settings))
	for _, s := range m.settings {
		result = append(result, *s)
	}
	return result, nil
}

func (m *MemStore) GetModelCapabilities(modelID string) (*ModelCapabilities, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mc, ok := m.capabilities[modelID]
	if !ok {
		return nil, nil
	}
	cp := *mc
	return &cp, nil
}

func (m *MemStore) SetModelCapabilities(mc *ModelCapabilities) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.capabilities[mc.ModelID] = mc
	return nil
}

func (m *MemStore) DeleteModelCapabilities(modelID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.capabilities, modelID)
	return nil
}

func (m *MemStore) ListModelCapabilities() ([]ModelCapabilities, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]ModelCapabilities, 0, len(m.capabilities))
	for _, mc := range m.capabilities {
		result = append(result, *mc)
	}
	return result, nil
}
