// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package auth

// AuthPersister abstracts loading and saving the local admin user.
// Implementations include the SurrealDB-backed persister and a no-op
// in-memory persister for embedded mode.
type AuthPersister interface {
	// LoadUser returns the persisted admin user, or nil if none exists.
	LoadUser() (*LocalUser, error)
	// SaveUser persists a user (on setup or password change).
	SaveUser(user *LocalUser) error
}

// MemoryPersister is a no-op persister for embedded/in-memory mode.
type MemoryPersister struct{}

func (MemoryPersister) LoadUser() (*LocalUser, error) { return nil, nil }
func (MemoryPersister) SaveUser(*LocalUser) error     { return nil }
