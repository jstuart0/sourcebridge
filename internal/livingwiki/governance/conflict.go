// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package governance

import (
	"context"
	"fmt"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
)

// SyncPRDecision is the outcome of an engineer's review of a sync-PR.
// It is the input to [ResolveSyncPR].
type SyncPRDecision int

const (
	// SyncPRDecisionMerge means the engineer approved the sync-PR.
	// The sink edit is promoted to canonical and removed from the overlay.
	SyncPRDecisionMerge SyncPRDecision = iota

	// SyncPRDecisionReject means the engineer closed the sync-PR without
	// merging. Per the plan (G.4): "edit reverts to local_to_sink for that
	// block in that sink only." The block content stays in the overlay — it
	// is no longer pending promotion but it is preserved as a one-time sink
	// override. Canonical is unchanged.
	SyncPRDecisionReject

	// SyncPRDecisionForceOverwrite is an admin action. The sink edit is
	// discarded entirely (overlay entry removed). Canonical regenerates into
	// all sinks on next pass — the sink will see the canonical content.
	SyncPRDecisionForceOverwrite
)

// String returns a human-readable label for the decision.
func (d SyncPRDecision) String() string {
	switch d {
	case SyncPRDecisionMerge:
		return "merge"
	case SyncPRDecisionReject:
		return "reject"
	case SyncPRDecisionForceOverwrite:
		return "force_overwrite"
	default:
		return "unknown"
	}
}

// ResolveSyncPR applies a sync-PR decision to a block, updating both the
// canonical AST and the sink's overlay as appropriate.
//
// Decision outcomes (per G.4):
//
//   - Merge: promotes the overlay block to canonical via [PromoteToCanonical];
//     removes the overlay entry; clears SinkDivergence[sinkName] on canonical.
//
//   - Reject: the overlay block stays in the overlay as a one-time
//     local_to_sink override. The block is no longer pending promotion.
//     Canonical is unchanged. SinkDivergence[sinkName] remains true.
//
//   - ForceOverwrite: removes the overlay entry entirely. On the next regen
//     pass the sink will receive canonical content. SinkDivergence[sinkName]
//     is cleared.
//
// The sinkUser parameter is the user who triggered the decision (for audit).
// The reviewer parameter is the engineer who reviewed the sync-PR (may be
// empty for ForceOverwrite if it was an automated action).
//
// Returns an error if the blockID is not found in the overlay (the PR may
// have already been resolved or the data is inconsistent).
func ResolveSyncPR(
	ctx context.Context,
	canonical ast.Page,
	overlay ast.SinkOverlay,
	sinkName ast.SinkName,
	sinkUser string,
	blockID ast.BlockID,
	decision SyncPRDecision,
	reviewer string,
	log AuditLog,
) (newCanonical ast.Page, newOverlay ast.SinkOverlay, err error) {
	overlayContent, inOverlay := overlay.Blocks[blockID]
	if !inOverlay {
		return canonical, overlay, fmt.Errorf(
			"governance: block %q not found in overlay for sink %q (page %q)",
			blockID, sinkName, overlay.PageID,
		)
	}

	overlayMeta := overlay.Provenance[blockID]
	_ = overlayMeta // used for context; actual edit info comes from overlay content

	switch decision {
	case SyncPRDecisionMerge:
		// Promote the overlay content to canonical.
		newCanonical, _, err = PromoteToCanonical(
			ctx,
			canonical,
			sinkName,
			sinkUser,
			blockID,
			overlayContent,
			log,
		)
		if err != nil {
			return canonical, overlay, fmt.Errorf("governance: merge failed during promotion: %w", err)
		}

		// Remove from overlay and clear divergence flag.
		newOverlay = copyOverlay(overlay)
		delete(newOverlay.Blocks, blockID)
		delete(newOverlay.Provenance, blockID)
		clearSinkDivergence(&newCanonical, blockID, sinkName)

		// The PromoteToCanonical call above already logged a promote_to_canonical
		// entry. Log the sync-PR merge decision on top for full traceability.
		mergeEntry := AuditEntry{
			BlockID:              string(blockID),
			SourceSink:           string(sinkName),
			SourceUser:           sinkUser,
			TargetCanonicalState: "human-edited",
			Timestamp:            time.Now(),
			Reviewer:             reviewer,
			Decision:             "sync_pr_merge",
		}
		if aerr := log.Append(ctx, mergeEntry); aerr != nil {
			return newCanonical, newOverlay, fmt.Errorf("governance: audit log append failed: %w", aerr)
		}

	case SyncPRDecisionReject:
		// Keep the overlay block as a local_to_sink override — it is no longer
		// pending promotion but the content stays so the sink keeps showing it.
		// Canonical is unchanged. SinkDivergence remains true.
		newCanonical = canonical
		newOverlay = overlay

		rejectEntry := AuditEntry{
			BlockID:              string(blockID),
			SourceSink:           string(sinkName),
			SourceUser:           sinkUser,
			TargetCanonicalState: "unchanged",
			Timestamp:            time.Now(),
			Reviewer:             reviewer,
			Decision:             "sync_pr_reject",
		}
		if aerr := log.Append(ctx, rejectEntry); aerr != nil {
			return newCanonical, newOverlay, fmt.Errorf("governance: audit log append failed: %w", aerr)
		}

	case SyncPRDecisionForceOverwrite:
		// Discard the overlay entry entirely. Canonical is unchanged; the
		// sink's next regen will receive canonical content (overlay cleared).
		newCanonical = copyPage(canonical)
		clearSinkDivergence(&newCanonical, blockID, sinkName)

		newOverlay = copyOverlay(overlay)
		delete(newOverlay.Blocks, blockID)
		delete(newOverlay.Provenance, blockID)

		overwriteEntry := AuditEntry{
			BlockID:              string(blockID),
			SourceSink:           string(sinkName),
			SourceUser:           sinkUser,
			TargetCanonicalState: "unchanged",
			Timestamp:            time.Now(),
			Reviewer:             reviewer,
			Decision:             "sync_pr_force_overwrite",
		}
		if aerr := log.Append(ctx, overwriteEntry); aerr != nil {
			return newCanonical, newOverlay, fmt.Errorf("governance: audit log append failed: %w", aerr)
		}

	default:
		return canonical, overlay, fmt.Errorf("governance: unrecognised SyncPRDecision: %d", decision)
	}

	return newCanonical, newOverlay, nil
}

// copyOverlay returns a shallow copy of o with fresh Blocks and Provenance maps.
func copyOverlay(o ast.SinkOverlay) ast.SinkOverlay {
	out := o
	out.Blocks = make(map[ast.BlockID]ast.BlockContent, len(o.Blocks))
	for k, v := range o.Blocks {
		out.Blocks[k] = v
	}
	out.Provenance = make(map[ast.BlockID]ast.OverlayMeta, len(o.Provenance))
	for k, v := range o.Provenance {
		out.Provenance[k] = v
	}
	return out
}

// clearSinkDivergence removes the sinkName entry from the block's
// SinkDivergence map. It is a no-op if the block is not found or the map
// does not contain the entry.
func clearSinkDivergence(page *ast.Page, blockID ast.BlockID, sinkName ast.SinkName) {
	for i, blk := range page.Blocks {
		if blk.ID == blockID {
			if page.Blocks[i].SinkDivergence != nil {
				delete(page.Blocks[i].SinkDivergence, sinkName)
			}
			return
		}
	}
}
