// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors
//
// FrozenResolver implements resolution.Resolver and always returns the same
// Snapshot, regardless of repoID or op. It is used by the Living Wiki
// cold-start runner to pin model identity for the duration of a run, so
// mid-run workspace settings changes cannot split a run across providers or
// cause fingerprint drift.
//
// CR5 (plan 2026-04-29-livingwiki-incremental-publish-redesign.md):
//   The correct freeze strategy is to substitute a FrozenResolver into a NEW
//   *llmcall.Caller via llmcall.New(...). Go methods are not virtual; wrapping
//   *Caller to "override" withResolved would not intercept internal calls.
//   Substituting a different Resolver implementation at construction time is the
//   idiomatic approach.

package resolution

import "context"

// FrozenResolver implements Resolver and always returns the same Snapshot.
// Use NewFrozenResolver to construct one; zero-value is invalid.
type FrozenResolver struct {
	snap Snapshot
}

// NewFrozenResolver returns a Resolver that always returns snap, regardless of
// the (repoID, op) pair. The snapshot is captured once at cold-start run start
// and frozen for the run's lifetime.
func NewFrozenResolver(snap Snapshot) *FrozenResolver {
	return &FrozenResolver{snap: snap}
}

// Resolve always returns the frozen snapshot. The repoID and op arguments are
// intentionally ignored.
func (r *FrozenResolver) Resolve(_ context.Context, _, _ string) (Snapshot, error) {
	return r.snap, nil
}

// InvalidateLocal is a no-op for frozen resolvers. The snapshot is already
// captured; there is no local cache to invalidate.
func (r *FrozenResolver) InvalidateLocal() {}

// Compile-time interface check.
var _ Resolver = (*FrozenResolver)(nil)
