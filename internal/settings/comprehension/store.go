// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package comprehension

import "context"

// Store is the persistence interface for comprehension settings and
// model capabilities. Implementations include SurrealDB (production)
// and in-memory (tests).
type Store interface {
	// --- Strategy settings ---

	// GetSettings returns the settings for a specific scope, or nil if none exist.
	GetSettings(ctx context.Context, scope Scope) (*Settings, error)

	// SetSettings creates or replaces settings for a scope. Zero-value
	// fields mean "inherit from parent" — the caller is responsible for
	// only setting fields that should be overridden.
	SetSettings(ctx context.Context, s *Settings) error

	// DeleteSettings removes the settings for a scope, reverting it
	// to pure inheritance.
	DeleteSettings(ctx context.Context, scope Scope) error

	// ListSettings returns all saved settings records.
	ListSettings(ctx context.Context) ([]Settings, error)

	// --- Model capabilities ---

	// GetModelCapabilities returns the capability profile for a model.
	GetModelCapabilities(ctx context.Context, modelID string) (*ModelCapabilities, error)

	// SetModelCapabilities creates or updates a model capability profile.
	SetModelCapabilities(ctx context.Context, m *ModelCapabilities) error

	// DeleteModelCapabilities removes a model from the registry.
	DeleteModelCapabilities(ctx context.Context, modelID string) error

	// ListModelCapabilities returns all model capability profiles.
	ListModelCapabilities(ctx context.Context) ([]ModelCapabilities, error)
}
