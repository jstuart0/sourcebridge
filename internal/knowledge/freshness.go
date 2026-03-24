// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package knowledge

import "log/slog"

// MarkAllStale marks all knowledge artifacts for a repository as stale.
// This should be called after any change that could invalidate generated
// knowledge (reindex, requirements import, link changes).
func MarkAllStale(store KnowledgeStore, repoID string) {
	if store == nil {
		return
	}
	artifacts := store.GetKnowledgeArtifacts(repoID)
	for _, a := range artifacts {
		if a.Status == StatusReady && !a.Stale {
			if err := store.MarkKnowledgeArtifactStale(a.ID, true); err != nil {
				slog.Warn("failed to mark knowledge artifact stale",
					"artifact_id", a.ID,
					"error", err,
				)
			}
		}
	}
}
