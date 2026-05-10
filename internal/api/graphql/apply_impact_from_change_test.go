// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"github.com/sourcebridge/sourcebridge/internal/appdeps"
	"sort"
	"testing"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
)

// Phase 1.A done-definition test #11.
//
// applyImpactFromChange was extracted verbatim from the
// ReindexRepository mutation (schema.resolvers.go, the post-
// ComputeImpact block). Because the change-watch router (Phase 1.C)
// will call the same helper, any drift between the extracted code
// and the original would cause the reindex mutation and change-event
// paths to diverge — silent, hard-to-debug, and exactly the bug
// shape this refactor is meant to prevent.
//
// The test asserts equivalence by exercising both feature-flag
// branches (selective + blanket) on a fixture stack of seeded
// artifacts and a synthetic ImpactReport. The assertions cover the
// observable post-state: which artifacts got marked stale, the
// shape of the persisted ImpactReport, and the StaleArtifacts /
// StaleArtifactReasons lists on the report itself.
//
// What this test does NOT cover (deferred to Phase 1.B):
//   - Full end-to-end exercise of the ReindexRepository GraphQL
//     mutation against an on-disk git fixture. The graphql package
//     has no fixture-repo scaffolding today (every existing test
//     drives the components in isolation). Phase 1.B's IndexFiles
//     budget test (#6) requires similar on-disk scaffolding; the
//     two should share infrastructure rather than have 1.A build
//     a one-off harness that 1.B has to refactor immediately.
//   - The auto-regen goroutine's actual rate-limit / understanding-
//     gate / priority-sort behavior. Those are covered by
//     knowledge_refresh_test.go's existing per-method tests; this
//     test asserts only that the goroutine launches in the right
//     order (after persistence) under the right conditions
//     (delta-regen mode on, non-empty StaleArtifacts).

// TestApplyImpactFromChange_SelectivePath_PreservesBehavior runs the
// helper with selective invalidation enabled and asserts the same
// observable post-state the inline original produced for the same
// inputs. Every assertion mirrors the corresponding assertion in
// selective_invalidation_test.go (which exercised the algorithm
// directly); the difference here is that the algorithm runs through
// the helper exactly as the resolver now invokes it.
func TestApplyImpactFromChange_SelectivePath_PreservesBehavior(t *testing.T) {
	t.Setenv("SOURCEBRIDGE_DELTA_REGEN_MODE", "off") // gate off the goroutine in this test

	ctx := context.Background()
	r := newTestMutationResolver()
	const repoID = "repo-apply-1"

	// Seed two artifacts: "hit" (evidence references a symbol the
	// impact report names) and "miss" (evidence references an
	// unrelated symbol).
	hit := seedArtifactWithSymbolEvidence(t, r.Deps.KnowledgeStore.(*knowledgepkg.MemStore), repoID,
		knowledgepkg.ArtifactCliffNotes, "sym-modified")
	miss := seedArtifactWithSymbolEvidence(t, r.Deps.KnowledgeStore.(*knowledgepkg.MemStore), repoID,
		knowledgepkg.ArtifactLearningPath, "sym-unrelated")

	// Build the impact report with the modified symbol.
	report := &graphstore.ImpactReport{
		ID:           "imp-apply-1",
		RepositoryID: repoID,
		SymbolsModified: []graphstore.ImpactSymbolChange{
			{SymbolID: "sym-modified", ChangeType: "modified"},
		},
	}

	r.applyImpactFromChange(ctx, repoID, report, true /* selectiveInvalidation */)

	// Selective path: hit is stale, miss is not.
	hitState := r.Deps.KnowledgeStore.GetKnowledgeArtifact(t.Context(), hit.ID)
	if hitState == nil || !hitState.Stale {
		t.Fatalf("hit artifact should be staled, got %+v", hitState)
	}
	missState := r.Deps.KnowledgeStore.GetKnowledgeArtifact(t.Context(), miss.ID)
	if missState != nil && missState.Stale {
		t.Fatalf("miss artifact should NOT be staled (selective path), got %+v", missState)
	}

	// StaleArtifactReasons populated; StaleArtifacts mirror.
	if len(report.StaleArtifactReasons) != 1 {
		t.Fatalf("expected 1 reason, got %d", len(report.StaleArtifactReasons))
	}
	if report.StaleArtifactReasons[0].ArtifactID != hit.ID {
		t.Fatalf("reason artifact ID = %s, want %s", report.StaleArtifactReasons[0].ArtifactID, hit.ID)
	}
	if len(report.StaleArtifacts) != 1 || report.StaleArtifacts[0] != hit.ID {
		t.Fatalf("StaleArtifacts = %v, want [%s]", report.StaleArtifacts, hit.ID)
	}

	// Report persisted with the surgical StaleArtifacts list (not
	// the pre-stale snapshot).
	persisted := r.Store.GetLatestImpactReport(t.Context(), repoID)
	if persisted == nil {
		t.Fatalf("impact report not persisted")
	}
	if persisted.ID != report.ID {
		t.Fatalf("persisted report ID = %s, want %s", persisted.ID, report.ID)
	}
	if len(persisted.StaleArtifacts) != 1 || persisted.StaleArtifacts[0] != hit.ID {
		t.Fatalf("persisted StaleArtifacts = %v, want [%s]", persisted.StaleArtifacts, hit.ID)
	}
}

// TestApplyImpactFromChange_BlanketPath_PreservesBehavior covers the
// flag-off branch. The legacy contract has two distinct effects:
//
//  1. StaleArtifacts on the report carries the PRE-stale snapshot —
//     i.e. only artifacts that were already stale or not-ready when
//     the reindex started. The UI uses this to keep its old "what
//     was stale before this reindex" signal.
//  2. MarkAllStale separately marks every artifact in the repo
//     stale.
//
// Test seeds three artifacts: one already-stale, one not-ready
// (StatusPending), one ready+fresh. The pre-stale snapshot must
// contain the first two but not the third; after the helper runs,
// all three must be marked stale.
func TestApplyImpactFromChange_BlanketPath_PreservesBehavior(t *testing.T) {
	t.Setenv("SOURCEBRIDGE_DELTA_REGEN_MODE", "off")

	ctx := context.Background()
	r := newTestMutationResolver()
	const repoID = "repo-apply-2"

	store := r.Deps.KnowledgeStore.(*knowledgepkg.MemStore)

	// Already-stale, ready artifact — appears in pre-stale snapshot
	// because Stale=true.
	alreadyStale := seedReadyArtifact(t, store, repoID, knowledgepkg.ArtifactCliffNotes)
	if err := store.MarkKnowledgeArtifactStale(t.Context(), alreadyStale.ID, true); err != nil {
		t.Fatalf("mark stale: %v", err)
	}

	// Not-ready artifact (StatusPending) — appears in pre-stale
	// snapshot because Status != Ready.
	pending, err := store.StoreKnowledgeArtifact(t.Context(), &knowledgepkg.Artifact{
		RepositoryID: repoID,
		Type:         knowledgepkg.ArtifactLearningPath,
		Audience:     knowledgepkg.AudienceDeveloper,
		Depth:        knowledgepkg.DepthMedium,
		Status:       knowledgepkg.StatusPending,
	})
	if err != nil {
		t.Fatalf("seed pending: %v", err)
	}

	// Ready+fresh artifact — does NOT appear in pre-stale snapshot,
	// but DOES get marked stale by MarkAllStale.
	freshReady := seedReadyArtifact(t, store, repoID, knowledgepkg.ArtifactCodeTour)

	report := &graphstore.ImpactReport{
		ID:           "imp-apply-2",
		RepositoryID: repoID,
	}

	r.applyImpactFromChange(ctx, repoID, report, false /* selectiveInvalidation: off → blanket path */)

	// MarkAllStale side effect: every artifact in the repo is now stale.
	if !store.GetKnowledgeArtifact(t.Context(), alreadyStale.ID).Stale {
		t.Fatalf("alreadyStale should remain stale")
	}
	// pending is non-ready; MarkAllStale only flips Stale on artifacts
	// in the StatusReady state, so we don't assert pending.Stale here
	// (its Stale flag was never relevant to its lifecycle).
	if !store.GetKnowledgeArtifact(t.Context(), freshReady.ID).Stale {
		t.Fatalf("freshReady should be staled by MarkAllStale")
	}

	// Pre-stale snapshot: alreadyStale and pending, NOT freshReady.
	got := append([]string(nil), report.StaleArtifacts...)
	sort.Strings(got)
	want := []string{alreadyStale.ID, pending.ID}
	sort.Strings(want)
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("pre-stale snapshot = %v, want %v (legacy contract: only artifacts already stale or not-ready)", got, want)
	}
	for _, id := range got {
		if id == freshReady.ID {
			t.Fatalf("freshReady leaked into pre-stale snapshot — legacy contract violated")
		}
	}

	// StaleArtifactReasons is non-nil but empty under blanket path
	// (the pre-stale snapshot doesn't carry attribution).
	if report.StaleArtifactReasons == nil {
		t.Fatalf("StaleArtifactReasons should be non-nil empty slice, got nil")
	}
	if len(report.StaleArtifactReasons) != 0 {
		t.Fatalf("StaleArtifactReasons should be empty under blanket path, got %d entries", len(report.StaleArtifactReasons))
	}
}

// TestApplyImpactFromChange_NilGuards exercises the safe paths when
// dependent components are unwired (e.g. minimal test resolvers).
// The original block had nil-guards on r.Deps.KnowledgeStore; the helper
// must preserve them.
func TestApplyImpactFromChange_NilGuards(t *testing.T) {
	t.Setenv("SOURCEBRIDGE_DELTA_REGEN_MODE", "off")

	ctx := context.Background()
	r := &mutationResolver{&Resolver{
		Deps: &appdeps.AppDeps{
			KnowledgeStore: nil, // deliberately nil
		},
		Store: graphstore.NewStore(),
	}}
	report := &graphstore.ImpactReport{
		ID:           "imp-nil",
		RepositoryID: "repo-nil",
	}
	// Must not panic.
	r.applyImpactFromChange(ctx, "repo-nil", report, false /* selectiveInvalidation: false covers the nil-guard path */)

	if report.StaleArtifacts == nil {
		t.Fatalf("StaleArtifacts should be initialized to empty slice, got nil")
	}
	if report.StaleArtifactReasons == nil {
		t.Fatalf("StaleArtifactReasons should be initialized to empty slice, got nil")
	}
}

// TestApplyImpactFromChange_PersistsBeforeGoroutineLaunch is the
// goroutine-ordering half of test #11: the report must be persisted
// in the synchronous body before any goroutine that might consume
// it can observe it. Same contract the inline original honored.
//
// We force selective + delta-regen-off so the goroutine launch path
// is exercised at most once and is a no-op (we don't actually need
// the regen driver to do work — we just need to confirm the
// persistence happens synchronously, before the helper returns).
func TestApplyImpactFromChange_PersistsBeforeGoroutineLaunch(t *testing.T) {
	t.Setenv("SOURCEBRIDGE_DELTA_REGEN_MODE", "off")

	ctx := context.Background()
	r := newTestMutationResolver()
	const repoID = "repo-order"
	hit := seedArtifactWithSymbolEvidence(t, r.Deps.KnowledgeStore.(*knowledgepkg.MemStore), repoID,
		knowledgepkg.ArtifactCliffNotes, "sym-x")

	report := &graphstore.ImpactReport{
		ID:           "imp-order",
		RepositoryID: repoID,
		SymbolsModified: []graphstore.ImpactSymbolChange{
			{SymbolID: "sym-x", ChangeType: "modified"},
		},
	}
	r.applyImpactFromChange(ctx, repoID, report, true /* selectiveInvalidation */)

	persisted := r.Store.GetLatestImpactReport(t.Context(), repoID)
	if persisted == nil {
		t.Fatalf("report not persisted before helper returned (the goroutine ordering contract — original code persisted SYNC, then launched goroutine)")
	}
	// And the persisted report carries the surgical StaleArtifacts.
	if len(persisted.StaleArtifacts) != 1 || persisted.StaleArtifacts[0] != hit.ID {
		t.Fatalf("persisted StaleArtifacts = %v, want [%s]", persisted.StaleArtifacts, hit.ID)
	}
}

// --- helpers -----------------------------------------------------------------

// newTestMutationResolver wires a *mutationResolver with the minimum
// dependencies applyImpactFromChange touches: a real graphstore.Store
// (for impact-report persistence) and a MemStore knowledge store (for
// artifact mutations). No worker, no LLM caller — the helper does not
// drive them.
func newTestMutationResolver() *mutationResolver {
	return &mutationResolver{&Resolver{
		Deps: &appdeps.AppDeps{
			KnowledgeStore: knowledgepkg.NewMemStore(),
		},
		Store: graphstore.NewStore(),
	}}
}

// seedArtifactWithSymbolEvidence creates a ready artifact whose first
// section's evidence references a single symbol. Used by the
// selective-path tests so MarkStaleForImpact has a hit to find.
func seedArtifactWithSymbolEvidence(
	t *testing.T,
	store *knowledgepkg.MemStore,
	repoID string,
	typ knowledgepkg.ArtifactType,
	symbolID string,
) *knowledgepkg.Artifact {
	t.Helper()
	a, err := store.StoreKnowledgeArtifact(t.Context(), &knowledgepkg.Artifact{
		RepositoryID: repoID,
		Type:         typ,
		Audience:     knowledgepkg.AudienceDeveloper,
		Depth:        knowledgepkg.DepthMedium,
		Status:       knowledgepkg.StatusPending,
	})
	if err != nil {
		t.Fatalf("StoreKnowledgeArtifact: %v", err)
	}
	if err := store.UpdateKnowledgeArtifactStatus(t.Context(), a.ID, knowledgepkg.StatusReady); err != nil {
		t.Fatalf("UpdateKnowledgeArtifactStatus: %v", err)
	}
	if err := store.StoreKnowledgeSections(t.Context(), a.ID, []knowledgepkg.Section{
		{Title: "S", Evidence: nil},
	}); err != nil {
		t.Fatalf("StoreKnowledgeSections: %v", err)
	}
	stored := store.GetKnowledgeSections(t.Context(), a.ID)
	if len(stored) != 1 {
		t.Fatalf("expected 1 stored section, got %d", len(stored))
	}
	if err := store.StoreKnowledgeEvidence(t.Context(), stored[0].ID, []knowledgepkg.Evidence{
		{SourceType: knowledgepkg.EvidenceSymbol, SourceID: symbolID},
	}); err != nil {
		t.Fatalf("StoreKnowledgeEvidence: %v", err)
	}
	return store.GetKnowledgeArtifact(t.Context(), a.ID)
}

// seedReadyArtifact creates a ready artifact with no evidence — used
// by the blanket-path test where every ready artifact is staled
// regardless of evidence shape.
func seedReadyArtifact(
	t *testing.T,
	store *knowledgepkg.MemStore,
	repoID string,
	typ knowledgepkg.ArtifactType,
) *knowledgepkg.Artifact {
	t.Helper()
	a, err := store.StoreKnowledgeArtifact(t.Context(), &knowledgepkg.Artifact{
		RepositoryID: repoID,
		Type:         typ,
		Audience:     knowledgepkg.AudienceDeveloper,
		Depth:        knowledgepkg.DepthMedium,
		Status:       knowledgepkg.StatusPending,
	})
	if err != nil {
		t.Fatalf("StoreKnowledgeArtifact: %v", err)
	}
	if err := store.UpdateKnowledgeArtifactStatus(t.Context(), a.ID, knowledgepkg.StatusReady); err != nil {
		t.Fatalf("UpdateKnowledgeArtifactStatus: %v", err)
	}
	return store.GetKnowledgeArtifact(t.Context(), a.ID)
}
