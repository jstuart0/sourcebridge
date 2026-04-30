// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/sinks"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates/architecture"
	livingwiki "github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

// FixupDispatchFunc is the per-page dispatch function the fix-up pass calls
// after re-rendering a page. The function is responsible for writing the page
// to every configured sink and recording the outcome in the status store.
// It is injected so callers (tests and the coldstart runner) can swap in
// their own dispatch logic.
type FixupDispatchFunc func(ctx context.Context, page ast.Page) error

// FixupRequest carries all inputs for one fix-up pass.
type FixupRequest struct {
	// RepoID is the opaque repository identifier.
	RepoID string

	// PlannedPages is the full taxonomy manifest for this run. Fix-up
	// iterates over the skipNeedsFixup bucket pages and looks up each one
	// in this slice to get the PackageInfo (callers/callees) and RelatedPageIDsByLabel.
	PlannedPages []PlannedPage

	// StatusStore is used to query pending rows and update fixup_status.
	StatusStore livingwiki.PagePublishStatusStore

	// Writers is the list of sinks to re-dispatch each fixed-up page to.
	Writers []sinks.NamedSinkWriter

	// PageStore is used to load the stored proposed_ast for re-rendering.
	PageStore PageStore

	// PR is the WikiPR used for the current cold-start run. Its ID is used
	// to load the proposed AST that was published on the first pass.
	PR WikiPR

	// Dispatch, when non-nil, overrides the default fix-up dispatch behavior.
	// When nil, defaultFixupDispatch is used (30-second per-page timeout).
	Dispatch FixupDispatchFunc
}

// FixupResult summarises the outcome of one fix-up pass.
type FixupResult struct {
	// PagesFixedUp is the number of pages that were successfully re-rendered
	// and re-dispatched.
	PagesFixedUp int

	// PagesDeferred is the number of pages whose stub targets were still
	// pending on at least one sink; these pages are left with
	// fixup_status='pending' for the next run.
	PagesDeferred int

	// PagesFailed is the number of pages whose re-render or re-dispatch
	// returned an error. Their fixup_status is set to 'failed'.
	PagesFailed int
}

// RunStubFixup is the Phase 3 end-of-run fix-up pass. It iterates over every
// page that was published with stub macros (fixup_status='pending'), checks
// whether all stub targets are now ready on every configured sink, re-renders
// the "Related pages" block using NullLinkResolver{} (so all stubs become real
// ac:links), and re-dispatches the page.
//
// Re-render cost is ~50ms/page (no LLM call). The 60-min coldStartTimeBudget
// is unaffected.
//
// Per-page failures set fixup_status='failed' and do NOT abort the pass; the
// next run's skipNeedsFixup bucket will retry them.
//
// Idempotency (L2): running fix-up twice is safe. The second pass either
// finds fixup_status='done' (skip) or re-renders to identical XHTML
// (ConfluenceWriter's read-diff-write detects no change and exits cheaply).
func RunStubFixup(ctx context.Context, req FixupRequest) (FixupResult, error) {
	if req.StatusStore == nil || req.PageStore == nil || req.PR == nil {
		return FixupResult{}, nil
	}

	pendingIDs, err := collectPendingPageIDs(ctx, req.RepoID, req.StatusStore)
	if err != nil {
		return FixupResult{}, fmt.Errorf("fixup: loading pending page IDs: %w", err)
	}
	if len(pendingIDs) == 0 {
		return FixupResult{}, nil
	}

	allRows, err := req.StatusStore.LoadFingerprints(ctx, req.RepoID)
	if err != nil {
		return FixupResult{}, fmt.Errorf("fixup: loading fingerprints: %w", err)
	}

	readyByKey := buildReadyByKey(allRows, req.Writers)

	plannedByID := make(map[string]PlannedPage, len(req.PlannedPages))
	for _, p := range req.PlannedPages {
		plannedByID[p.ID] = p
	}

	dispatch := req.Dispatch
	if dispatch == nil {
		dispatch = defaultFixupDispatch(req.Writers)
	}

	prID := req.PR.ID()
	now := time.Now().UTC()
	var result FixupResult

	for pageID := range pendingIDs {
		planned, ok := plannedByID[pageID]
		if !ok {
			continue
		}

		pageRows, _ := allRows[pageID]
		allTargetsReady := true
		for _, row := range pageRows {
			for _, tgt := range row.StubTargetPageIDs {
				if !isReadyOnAllSinks(tgt, readyByKey, req.Writers) {
					allTargetsReady = false
					break
				}
			}
			if !allTargetsReady {
				break
			}
		}

		if !allTargetsReady {
			result.PagesDeferred++
			slog.Debug("fixup: stub targets still pending; deferring",
				"repo_id", req.RepoID, "page_id", pageID)
			continue
		}

		stored, found, loadErr := req.PageStore.GetProposed(ctx, req.RepoID, prID, pageID)
		if loadErr != nil || !found {
			slog.Warn("fixup: could not load stored page; skipping",
				"repo_id", req.RepoID, "page_id", pageID, "found", found, "error", loadErr)
			result.PagesFailed++
			recordFixupFailed(ctx, req.StatusStore, req.RepoID, pageID, req.Writers)
			continue
		}

		rebuilt, rebuildErr := rebuildPageForFixup(stored, planned, now)
		if rebuildErr != nil {
			slog.Warn("fixup: failed to re-render page",
				"repo_id", req.RepoID, "page_id", pageID, "error", rebuildErr)
			result.PagesFailed++
			recordFixupFailed(ctx, req.StatusStore, req.RepoID, pageID, req.Writers)
			continue
		}

		dispatchErr := dispatch(ctx, rebuilt)
		if dispatchErr != nil {
			slog.Warn("fixup: dispatch failed",
				"repo_id", req.RepoID, "page_id", pageID, "error", dispatchErr)
			result.PagesFailed++
			recordFixupFailed(ctx, req.StatusStore, req.RepoID, pageID, req.Writers)
			continue
		}

		for _, w := range req.Writers {
			_ = req.StatusStore.UpdateFixupStatus(ctx, livingwiki.UpdateFixupStatusArgs{
				RepoID:          req.RepoID,
				PageID:          pageID,
				SinkKind:        string(w.Writer.Kind()),
				IntegrationName: w.Name,
				FixupStatus:     livingwiki.FixupStatusDone,
				HasStubs:        false,
			})
		}
		result.PagesFixedUp++
		slog.Info("fixup: page re-rendered and dispatched",
			"repo_id", req.RepoID, "page_id", pageID)
	}

	return result, nil
}

// rebuildPageForFixup re-renders the "Related pages" freeform block in stored
// using NullLinkResolver{} so all stub macros become real ac:links. All other
// blocks are preserved exactly as stored. No LLM call is made.
func rebuildPageForFixup(stored ast.Page, planned PlannedPage, now time.Time) (ast.Page, error) {
	if planned.PackageInfo == nil {
		return stored, nil
	}

	targetBlockID := ast.GenerateBlockID(stored.ID, "Related pages", ast.BlockKindFreeform, 0)
	repoID := planned.Input.RepoID

	freshXHTML, _ := architecture.RebuildRelatedPagesXHTML(
		repoID,
		stored.ID,
		planned.PackageInfo.Callers,
		planned.PackageInfo.Callees,
		planned.Input.RelatedPageIDsByLabel,
		NullLinkResolver{},
	)

	rebuilt := ast.Page{
		ID:                stored.ID,
		Manifest:          stored.Manifest,
		Provenance:        stored.Provenance,
		StubTargetPageIDs: nil,
		Blocks:            make([]ast.Block, len(stored.Blocks)),
	}
	copy(rebuilt.Blocks, stored.Blocks)

	for i, blk := range rebuilt.Blocks {
		if blk.ID == targetBlockID && blk.Kind == ast.BlockKindFreeform && blk.Content.Freeform != nil {
			rebuilt.Blocks[i].Content.Freeform = &ast.FreeformContent{Raw: freshXHTML}
			rebuilt.Blocks[i].LastChange = ast.BlockChange{
				Timestamp: now,
				Source:    "sourcebridge-fixup",
			}
			break
		}
	}

	return rebuilt, nil
}

// defaultFixupDispatch returns a FixupDispatchFunc that writes the page to
// every configured sink with a 30-second per-page timeout.
func defaultFixupDispatch(writers []sinks.NamedSinkWriter) FixupDispatchFunc {
	return func(ctx context.Context, page ast.Page) error {
		writeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		for _, w := range writers {
			if err := w.Writer.WritePage(writeCtx, page); err != nil {
				return fmt.Errorf("fixup: sink %q write failed for page %q: %w",
					w.Name, page.ID, err)
			}
		}
		return nil
	}
}

// collectPendingPageIDs queries the status store and returns the set of page
// IDs whose fixup_status is 'pending' for this repo (across any sink).
func collectPendingPageIDs(ctx context.Context, repoID string, store livingwiki.PagePublishStatusStore) (map[string]struct{}, error) {
	rows, err := store.ListByRepo(ctx, repoID)
	if err != nil {
		return nil, err
	}
	pending := make(map[string]struct{})
	for _, row := range rows {
		if row.FixupStatus == livingwiki.FixupStatusPending {
			pending[row.PageID] = struct{}{}
		}
	}
	return pending, nil
}

// buildReadyByKey returns a set of page IDs that are 'ready' on ALL configured
// sinks. A page is only "ready" for the purposes of the fix-up check when every
// writer has a row with status='ready' for that page.
func buildReadyByKey(allRows map[string]map[string]livingwiki.PagePublishStatusRow, writers []sinks.NamedSinkWriter) map[string]struct{} {
	ready := make(map[string]struct{})
	for pageID, sinkMap := range allRows {
		allReady := true
		for _, w := range writers {
			key := fixupSinkKey(string(w.Writer.Kind()), w.Name)
			row, ok := sinkMap[key]
			if !ok || row.Status != "ready" {
				allReady = false
				break
			}
		}
		if allReady {
			ready[pageID] = struct{}{}
		}
	}
	return ready
}

// isReadyOnAllSinks reports whether targetPageID is ready on every configured sink.
func isReadyOnAllSinks(targetPageID string, readyByKey map[string]struct{}, writers []sinks.NamedSinkWriter) bool {
	if len(writers) == 0 {
		return true
	}
	_, ok := readyByKey[targetPageID]
	return ok
}

// fixupSinkKey returns the composite key used in LoadFingerprints' inner map.
func fixupSinkKey(sinkKind, integrationName string) string {
	return sinkKind + "/" + integrationName
}

// recordFixupFailed records a fixup_status='failed' for every sink.
func recordFixupFailed(ctx context.Context, store livingwiki.PagePublishStatusStore, repoID, pageID string, writers []sinks.NamedSinkWriter) {
	statusCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, w := range writers {
		_ = store.UpdateFixupStatus(statusCtx, livingwiki.UpdateFixupStatusArgs{
			RepoID:          repoID,
			PageID:          pageID,
			SinkKind:        string(w.Writer.Kind()),
			IntegrationName: w.Name,
			FixupStatus:     livingwiki.FixupStatusFailed,
			HasStubs:        true,
		})
	}
}
