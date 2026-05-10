// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package livingwiki

import (
	"context"
	"sync"
)

// MemStore is an in-memory [Store] implementation for tests and local dev.
// Secrets are stored in plaintext (no encryption; it is only for tests).
type MemStore struct {
	mu   sync.RWMutex
	data *Settings
}

// NewMemStore creates an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{}
}

func (m *MemStore) Get(_ context.Context) (*Settings, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.data == nil {
		return &Settings{}, nil
	}
	cp := *m.data
	return &cp, nil
}

func (m *MemStore) Set(_ context.Context, s *Settings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *s
	m.data = &cp
	return nil
}
