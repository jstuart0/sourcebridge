// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package orchestrator_test

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
)

// readySet is a helper that turns variadic strings into a ReadySet.
func readySet(ids ...string) orchestrator.ReadySet {
	m := make(orchestrator.ReadySet, len(ids))
	for _, id := range ids {
		m[id] = struct{}{}
	}
	return m
}

// plannedSet is a helper that turns variadic strings into a planned-IDs map.
func plannedSet(ids ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		m[id] = struct{}{}
	}
	return m
}

// ─── livePageStatusResolver tests ──────────────────────────────────────────

// TestLinkResolverShouldStub_ReadyTargetNotStubbed asserts that a target
// which is both planned and already ready is NOT stubbed.
func TestLinkResolverShouldStub_ReadyTargetNotStubbed(t *testing.T) {
	r := orchestrator.NewLivePageStatusResolver(
		readySet("overview.pkg-a"),
		plannedSet("overview.pkg-a"),
	)
	if r.ShouldStub("overview.pkg-b", "overview.pkg-a") {
		t.Fatal("expected false for a ready target; got true")
	}
}

// TestLinkResolverShouldStub_UnreadyTargetStubbed asserts that a target which
// is planned but not yet ready IS stubbed (step 5 of the decision matrix).
func TestLinkResolverShouldStub_UnreadyTargetStubbed(t *testing.T) {
	r := orchestrator.NewLivePageStatusResolver(
		readySet(), // nothing ready
		plannedSet("overview.pkg-a"),
	)
	if !r.ShouldStub("overview.pkg-b", "overview.pkg-a") {
		t.Fatal("expected true for an unready planned target; got false")
	}
}

// TestLinkResolverDoesNotStubCrossModeLinks verifies LD-11: links from an
// overview page to a detail page and vice-versa are never stubbed.
func TestLinkResolverDoesNotStubCrossModeLinks(t *testing.T) {
	r := orchestrator.NewLivePageStatusResolver(
		readySet(),
		plannedSet("detail.pkg-a", "overview.pkg-b", "arch.pkg-c"),
	)
	cases := []struct {
		src, tgt string
	}{
		{"overview.pkg-b", "detail.pkg-a"},  // overview → detail
		{"detail.pkg-a", "overview.pkg-b"},  // detail → overview
		{"arch.pkg-c", "overview.pkg-b"},    // arch → overview
		{"overview.pkg-b", "arch.pkg-c"},    // overview → arch
	}
	for _, tc := range cases {
		if r.ShouldStub(tc.src, tc.tgt) {
			t.Errorf("ShouldStub(%q, %q) = true; want false (cross-mode link)", tc.src, tc.tgt)
		}
	}
}

// TestLinkResolverShouldStub_SameModeLinksStubbed verifies that same-mode links
// (overview→overview, detail→detail, arch→arch) to unready planned targets are
// correctly stubbed.
func TestLinkResolverShouldStub_SameModeLinksStubbed(t *testing.T) {
	r := orchestrator.NewLivePageStatusResolver(
		readySet(),
		plannedSet("overview.pkg-a", "detail.pkg-b", "arch.pkg-c"),
	)
	cases := []struct {
		src, tgt string
	}{
		{"overview.pkg-z", "overview.pkg-a"},
		{"detail.pkg-z", "detail.pkg-b"},
		{"arch.pkg-z", "arch.pkg-c"},
	}
	for _, tc := range cases {
		if !r.ShouldStub(tc.src, tc.tgt) {
			t.Errorf("ShouldStub(%q, %q) = false; want true (same-mode unready)", tc.src, tc.tgt)
		}
	}
}

// TestLinkResolverShouldStub_DanglingRefNotStubbed asserts that a target ID
// which is not in the manifest is not stubbed (step 3).
func TestLinkResolverShouldStub_DanglingRefNotStubbed(t *testing.T) {
	r := orchestrator.NewLivePageStatusResolver(
		readySet(),
		plannedSet("overview.pkg-a"), // pkg-unknown is not planned
	)
	if r.ShouldStub("overview.pkg-a", "overview.pkg-unknown") {
		t.Fatal("expected false for a dangling (unplanned) reference; got true")
	}
}

// TestLinkResolverShouldStub_EmptyIDsReturnFalse asserts the defensive empty
// check (step 1): empty source or target → never stub.
func TestLinkResolverShouldStub_EmptyIDsReturnFalse(t *testing.T) {
	r := orchestrator.NewLivePageStatusResolver(
		readySet(),
		plannedSet("overview.pkg-a"),
	)
	if r.ShouldStub("", "overview.pkg-a") {
		t.Error("ShouldStub with empty source should return false")
	}
	if r.ShouldStub("overview.pkg-b", "") {
		t.Error("ShouldStub with empty target should return false")
	}
}

// ─── NullLinkResolver tests ─────────────────────────────────────────────────

// TestNullLinkResolverNeverStubs verifies that NullLinkResolver always returns
// false, regardless of inputs. Used in fix-up re-renders to clear all stubs.
func TestNullLinkResolverNeverStubs(t *testing.T) {
	var r orchestrator.NullLinkResolver
	cases := [][2]string{
		{"overview.pkg-a", "overview.pkg-b"},
		{"", ""},
		{"detail.pkg-x", "arch.pkg-y"},
		{"any-string", "other-string"},
	}
	for _, tc := range cases {
		if r.ShouldStub(tc[0], tc[1]) {
			t.Errorf("NullLinkResolver.ShouldStub(%q, %q) = true; want always false", tc[0], tc[1])
		}
	}
}

// ─── AllStubLinkResolver tests ──────────────────────────────────────────────

// TestAllStubLinkResolverStubsManifestTargets verifies that AllStubLinkResolver
// stubs same-mode planned targets and correctly skips cross-mode links (LD-11).
func TestAllStubLinkResolverStubsManifestTargets(t *testing.T) {
	r := orchestrator.AllStubLinkResolver{
		PlannedIDs: plannedSet("overview.pkg-a", "overview.pkg-b"),
	}

	// Same-mode planned target → stub.
	if !r.ShouldStub("overview.pkg-z", "overview.pkg-a") {
		t.Error("AllStubLinkResolver: same-mode planned target should be stubbed")
	}

	// Cross-mode link → never stub even if planned (LD-11).
	if r.ShouldStub("detail.pkg-z", "overview.pkg-a") {
		t.Error("AllStubLinkResolver: cross-mode link should not be stubbed")
	}

	// Target not in PlannedIDs → not stubbed.
	if r.ShouldStub("overview.pkg-z", "overview.pkg-unknown") {
		t.Error("AllStubLinkResolver: unplanned target should not be stubbed")
	}
}
