// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
)

// applyImpactFromChange consumes a freshly-computed ImpactReport, runs
// the selective-vs-blanket invalidation policy against the knowledge
// store, persists the report, and (if delta-regen is enabled and there
// is anything to regenerate) launches the stale-artifact refresh
// goroutine.
//
// The behavior is the verbatim block previously inlined in
// ReindexRepository at the post-ComputeImpact stage. The extraction
// is Phase 1.A's last refactor: the change-watch router (Phase 1.C)
// will call this helper after its own delta-only IndexFiles invocation,
// guaranteeing both the reindex mutation AND change events follow the
// same invalidation policy.
//
// selectiveInvalidation must be the boot-resolved value from
// r.Deps.Flags.SelectiveInvalidationEnabled. It is passed explicitly so
// changing the environment after process start cannot alter the
// behavior of an in-flight request path.
//
// Side effects (must remain identical to the inline original):
//   - mutates impactReport.StaleArtifacts and StaleArtifactReasons.
//   - calls knowledgepkg.MarkStaleForImpact OR MarkAllStale on
//     r.Deps.KnowledgeStore depending on the feature flag.
//   - persists the report via r.getStore(ctx).StoreImpactReport.
//   - launches r.enqueueStaleArtifactRefresh as a goroutine when
//     deltaRegenMode != Off and StaleArtifacts is non-empty. The
//     goroutine is launched AFTER the report is persisted, matching
//     the original ordering — the regeneration driver reads the
//     persisted report.
//
// Plan reference: thoughts/shared/plans/2026-04-29-mcp-edits-feedback-loop.md
// (v5, file-level audit + Phase 1 done-definition test #11).
func (r *mutationResolver) applyImpactFromChange(
	ctx context.Context,
	repoID string,
	impactReport *graphstore.ImpactReport,
	selectiveInvalidation bool,
) {
	// Mark knowledge artifacts stale — selectively if the feature flag is on,
	// otherwise fall back to the legacy blanket behavior. In both cases the
	// resulting StaleArtifacts / StaleArtifactReasons list is then recorded
	// on the impact report for downstream consumers.
	if r.Deps.KnowledgeStore != nil && selectiveInvalidation {
		symbolIDs := make([]string, 0, len(impactReport.SymbolsModified)+len(impactReport.SymbolsRemoved))
		for _, sc := range impactReport.SymbolsModified {
			if sc.SymbolID != "" {
				symbolIDs = append(symbolIDs, sc.SymbolID)
			}
		}
		for _, sc := range impactReport.SymbolsRemoved {
			if sc.SymbolID != "" {
				symbolIDs = append(symbolIDs, sc.SymbolID)
			}
		}
		var filePaths []string
		for _, fd := range impactReport.FilesChanged {
			if fd.Status == "added" {
				continue
			}
			if fd.Path != "" {
				filePaths = append(filePaths, fd.Path)
			}
			// Renamed files: evidence still references the pre-rename path.
			if fd.Status == "renamed" && fd.OldPath != "" && fd.OldPath != fd.Path {
				filePaths = append(filePaths, fd.OldPath)
			}
		}
		reasons := knowledgepkg.MarkStaleForImpact(
			ctx,
			r.Deps.KnowledgeStore,
			repoID,
			symbolIDs,
			filePaths,
			impactReport.ID,
			selectiveInvalidationMaxChanges(),
		)
		impactReport.StaleArtifactReasons = reasons
		for _, reason := range reasons {
			impactReport.StaleArtifacts = append(impactReport.StaleArtifacts, reason.ArtifactID)
		}
	} else {
		// Legacy blanket path. Preserve the previous behavior of listing
		// pre-stale artifacts on the report so the UI keeps its old signal.
		if r.Deps.KnowledgeStore != nil {
			for _, a := range r.Deps.KnowledgeStore.GetKnowledgeArtifacts(ctx, repoID) {
				if a.Stale || a.Status != knowledgepkg.StatusReady {
					impactReport.StaleArtifacts = append(impactReport.StaleArtifacts, a.ID)
				}
			}
		}
		knowledgepkg.MarkAllStale(ctx, r.Deps.KnowledgeStore, repoID)
	}
	if impactReport.StaleArtifacts == nil {
		impactReport.StaleArtifacts = []string{}
	}
	if impactReport.StaleArtifactReasons == nil {
		impactReport.StaleArtifactReasons = []graphstore.StaleArtifactReason{}
	}

	// Persist the report after invalidation so StaleArtifacts reflects the
	// surgically-chosen set (not the pre-stale snapshot).
	r.getStore(ctx).StoreImpactReport(ctx, repoID, impactReport)

	// Phase 2: delta-driven auto-regeneration. No-op when the mode is off.
	// Shadow mode logs what it would do; live mode actually enqueues. Runs
	// in a goroutine so the reindex mutation returns promptly; the driver
	// uses its own background context.
	// deltaRegenModeWithFlags uses the boot-resolved selectiveInvalidation flag
	// so a runtime env change cannot alter this gate for in-flight requests.
	if deltaRegenModeWithFlags(selectiveInvalidation) != DeltaRegenModeOff && len(impactReport.StaleArtifacts) > 0 {
		staleIDs := make([]string, len(impactReport.StaleArtifacts))
		copy(staleIDs, impactReport.StaleArtifacts)
		reportID := impactReport.ID
		go r.enqueueStaleArtifactRefresh(repoID, staleIDs, reportID)
	}
}
