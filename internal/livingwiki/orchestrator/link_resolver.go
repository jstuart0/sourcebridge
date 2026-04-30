// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package orchestrator

import (
	"strings"

	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
)

// ReadySet is the set of page IDs that have already been published on all
// configured sinks at the time the coldstart resolver is constructed.
// Keyed by page ID string.
type ReadySet = map[string]struct{}

// NewLivePageStatusResolver returns a templates.LinkResolver that stubs links
// to pages that are in the manifest (plannedIDs) but not yet ready (readyIDs).
// Cross-mode links (overview→detail, detail→overview, arch→anything) are never
// stubbed (LD-11). Dangling references (not in manifest) are never stubbed.
//
// readyIDs and plannedIDs are snapshot-at-construction; the returned resolver
// is safe for concurrent use from multiple goroutines after construction.
func NewLivePageStatusResolver(
	readyIDs ReadySet,
	plannedIDs map[string]struct{},
) templates.LinkResolver {
	return &livePageStatusResolver{
		readyIDs:   readyIDs,
		plannedIDs: plannedIDs,
	}
}

// livePageStatusResolver implements templates.LinkResolver for the coldstart
// run. It snapshots ready/planned state at construction time.
type livePageStatusResolver struct {
	readyIDs   map[string]struct{}
	plannedIDs map[string]struct{}
}

// ShouldStub applies the 5-step decision matrix (LD-4):
//
//  1. Empty source or target → false (defensive; never stubs unknown IDs).
//  2. Cross-mode link (overview↔detail, arch↔anything) → false (LD-11).
//  3. Target not in manifest → false (dangling reference — not our page).
//  4. Target in manifest and already ready → false (link can resolve now).
//  5. Target in manifest, not yet ready → true (emit stub macro).
func (r *livePageStatusResolver) ShouldStub(sourcePageID, targetPageID string) bool {
	// Step 1: defensive empty check.
	if sourcePageID == "" || targetPageID == "" {
		return false
	}
	// Step 2: cross-mode links are never stubbed (LD-11).
	if isCrossMode(sourcePageID, targetPageID) {
		return false
	}
	// Step 3: not in manifest → dangling reference, don't stub.
	if _, inManifest := r.plannedIDs[targetPageID]; !inManifest {
		return false
	}
	// Step 4: in manifest and already ready → real link.
	if _, ready := r.readyIDs[targetPageID]; ready {
		return false
	}
	// Step 5: in manifest and not yet ready → stub.
	return true
}

// isCrossMode reports whether a link from sourcePageID to targetPageID crosses
// the overview/detail/arch mode boundary, making it ineligible for stubbing
// (LD-11). Mode is inferred from the page-ID prefix segment.
//
// Repo-wide pages have no mode prefix; they are treated as same-mode with
// everything and may be stubbed normally.
func isCrossMode(sourcePageID, targetPageID string) bool {
	sm := modePrefixOf(sourcePageID)
	tm := modePrefixOf(targetPageID)
	// Both have no prefix → repo-wide pages; treat as same-mode.
	if sm == "" && tm == "" {
		return false
	}
	// One or both have a prefix and they differ → cross-mode.
	return sm != tm
}

// modePrefixOf returns the leading mode segment of a page ID, or "" for
// repo-wide pages. Mode prefixes are "overview", "detail", and "arch".
// Page IDs have the form "<prefix>.<rest>" for mode pages or just an opaque
// string for repo-wide pages.
func modePrefixOf(pageID string) string {
	dot := strings.IndexByte(pageID, '.')
	if dot < 0 {
		return ""
	}
	prefix := pageID[:dot]
	switch prefix {
	case "overview", "detail", "arch":
		return prefix
	}
	return ""
}

// NullLinkResolver is a templates.LinkResolver that never stubs any link.
// Used in fix-up re-renders (Phase 3 / LD-4) to ensure all stub macros are
// replaced with real ac:links now that the target pages are ready.
type NullLinkResolver struct{}

// ShouldStub always returns false: no links are stubbed in fix-up re-renders.
func (NullLinkResolver) ShouldStub(_, _ string) bool { return false }

// AllStubLinkResolver is a templates.LinkResolver that stubs every link whose
// target appears in PlannedIDs. Used in tests to assert that stub macros are
// emitted when expected.
type AllStubLinkResolver struct {
	// PlannedIDs is the set of page IDs to consider "planned but not ready".
	// All same-mode links to a page in this set will be stubbed.
	PlannedIDs map[string]struct{}
}

// ShouldStub returns true when targetPageID is in PlannedIDs and the link is
// not cross-mode (LD-11).
func (r AllStubLinkResolver) ShouldStub(sourcePageID, targetPageID string) bool {
	if isCrossMode(sourcePageID, targetPageID) {
		return false
	}
	_, ok := r.PlannedIDs[targetPageID]
	return ok
}
