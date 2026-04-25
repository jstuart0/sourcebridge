// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package governance

import (
	"context"
	"fmt"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/manifest"
)

// PromoteToCanonical promotes a sink's edited block content to the canonical
// AST and records the event in the audit log.
//
// Steps performed (per G.3):
//  1. Locates blockID in canonical. Returns an error if not found.
//  2. Stashes the block's current content for revert capability.
//  3. Replaces the block's Content with newContent.
//  4. Flips the block's Owner to OwnerHumanEdited.
//  5. Appends an AuditEntry via log.
//
// The stashed content is returned to the caller for persistence. This function
// does not write to any storage — it returns the new canonical Page and the
// previous content, leaving persistence entirely to the caller. See the
// caller's responsibility note in G.3: "The 'stash' doesn't need a real KV
// store — return the previous content and let the caller persist."
//
// Steps 3–4 of G.3 ("other sinks regenerate" and "stop auto-updating") are
// orchestrator-level concerns: the returned canonical Page reflects the new
// human-edited ownership, which is what regen passes read to determine
// whether to regenerate a block.
func PromoteToCanonical(
	ctx context.Context,
	canonical ast.Page,
	sinkName ast.SinkName,
	sinkUser string,
	blockID ast.BlockID,
	newContent ast.BlockContent,
	log AuditLog,
) (updated ast.Page, stashed ast.BlockContent, err error) {
	idx := findBlockIndex(canonical.Blocks, blockID)
	if idx < 0 {
		return canonical, ast.BlockContent{}, fmt.Errorf("governance: block %q not found in page %q", blockID, canonical.ID)
	}

	// Stash the current auto content for revert capability.
	stashed = canonical.Blocks[idx].Content

	// Build the updated page — copy blocks slice so the original is not mutated.
	updated = copyPage(canonical)
	updated.Blocks[idx].Content = newContent
	updated.Blocks[idx].Owner = ast.OwnerHumanEdited
	updated.Blocks[idx].LastChange = ast.BlockChange{
		Timestamp: time.Now(),
		Source:    string(sinkName),
	}

	entry := AuditEntry{
		BlockID:              string(blockID),
		SourceSink:           string(sinkName),
		SourceUser:           sinkUser,
		TargetCanonicalState: "human-edited",
		Timestamp:            updated.Blocks[idx].LastChange.Timestamp,
		Decision:             "promote_to_canonical",
	}
	if err := log.Append(ctx, entry); err != nil {
		// Audit failure is non-fatal for the promotion itself, but callers
		// should monitor for this — it means the audit trail has a gap.
		return updated, stashed, fmt.Errorf("governance: audit log append failed: %w", err)
	}

	return updated, stashed, nil
}

// MarkBlockAuto flips a human-edited block back to generated ownership.
// This is the UI helper for the "mark as auto again" button described in G.3
// step 4. It is a no-op for blocks that are already OwnerGenerated or
// OwnerHumanOnly — those states are not changed.
//
// Returns the updated page. The original page is not mutated.
func MarkBlockAuto(canonical ast.Page, blockID ast.BlockID) ast.Page {
	updated := copyPage(canonical)
	for i, blk := range updated.Blocks {
		if blk.ID != blockID {
			continue
		}
		if blk.Owner == ast.OwnerHumanEdited {
			updated.Blocks[i].Owner = ast.OwnerGenerated
		}
		// OwnerHumanOnly and OwnerGenerated are unchanged — human-only
		// blocks are never auto-managed, and generated blocks already have
		// the right owner.
		return updated
	}
	// Block not found — return unchanged.
	return updated
}

// InvalidationStaleSignal returns true when the changed symbols match a
// stale_when condition for the given blockID AND the block's owner is
// OwnerHumanEdited.
//
// This is the trigger for "this human-edited block may be stale" PR described
// in G.3 step 4. The signal fires only for human-edited blocks — generated
// blocks are simply regenerated on the next pass, so stale signalling would
// be redundant.
//
// changedSymbols is the set of fully-qualified symbol names that changed in
// the diff (e.g. "auth.Middleware"). The manifest's stale_when conditions are
// checked against this set.
//
// If no block with blockID exists in canonical, returns false.
func InvalidationStaleSignal(
	canonical ast.Page,
	blockID ast.BlockID,
	changedSymbols []string,
) bool {
	idx := findBlockIndex(canonical.Blocks, blockID)
	if idx < 0 {
		return false
	}
	blk := canonical.Blocks[idx]
	if blk.Owner != ast.OwnerHumanEdited {
		return false
	}

	// Evaluate stale_when conditions from the page manifest against the
	// changed symbol set. We reuse the manifest package's logic via a
	// ChangedPair list (symbol only, no path needed for this check).
	if len(canonical.Manifest.StaleWhen) == 0 {
		return false
	}

	changed := make([]manifest.ChangedPair, 0, len(changedSymbols))
	for _, sym := range changedSymbols {
		changed = append(changed, manifest.ChangedPair{Symbol: sym})
	}

	signals := manifest.EvaluateStaleConditions(canonical.Manifest, changed)
	return len(signals) > 0
}

// copyPage returns a shallow copy of page with a fresh Blocks slice so that
// callers can modify blocks without mutating the original.
func copyPage(p ast.Page) ast.Page {
	out := p
	out.Blocks = make([]ast.Block, len(p.Blocks))
	copy(out.Blocks, p.Blocks)
	return out
}

// findBlockIndex returns the slice index of the block with the given ID,
// or -1 if not found.
func findBlockIndex(blocks []ast.Block, id ast.BlockID) int {
	for i, b := range blocks {
		if b.ID == id {
			return i
		}
	}
	return -1
}
