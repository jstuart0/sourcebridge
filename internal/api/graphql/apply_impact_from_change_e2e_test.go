// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/appdeps"
	"github.com/sourcebridge/sourcebridge/internal/featureflags"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
	"github.com/sourcebridge/sourcebridge/internal/indexer/testfixtures"
	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
)

// TestReindexRepository_AppliesImpactFromChange_EndToEnd is the
// end-to-end half of Phase 1 done-definition test #11. The 1.A
// helper-level tests (apply_impact_from_change_test.go) verified that
// the extracted helper preserves the verbatim post-state for both
// feature-flag branches; this test verifies the integration path:
//
//  1. The ReindexRepository GraphQL mutation, when run against a real
//     on-disk git repository, drives the indexer end-to-end and
//     reaches r.applyImpactFromChange with a meaningful ImpactReport.
//  2. The same observable post-state the helper-level test asserts
//     (selective stale flagging on the matching artifact, persistence
//     of the impact report, surgical StaleArtifacts list) holds when
//     reached via the mutation.
//  3. The mutation completes without error on the happy path
//     (incremental reindex, hash-skipping, ReplaceIndexResult, impact
//     computation, applyImpactFromChange).
//
// What this test pins that the helper-level test cannot:
//
//   - The plumbing from ReindexRepository's ComputeImpact output
//     through the helper continues to produce the same StaleArtifacts
//     shape the helper produced when called directly. If a future
//     change to the mutation's input-construction (DiffSymbols,
//     ComputeImpact, fileDiffs assembly) were to silently change the
//     shape of what reaches the helper, this test catches it.
//   - The end-to-end mutation completes cleanly on a representative
//     real-world fixture (>= 500 files), not just the synthetic
//     ImpactReport the helper-level tests construct directly.
//
// Why this test belongs to Phase 1.B and not 1.A: 1.A's commit message
// for the helper extraction explicitly noted that the e2e exercise
// requires fixture-repo scaffolding the graphql package didn't have.
// Phase 1.B's IndexFiles budget test (#6) needs the same scaffolding;
// 1.B builds the shared internal/indexer/testfixtures package and
// uses it for both #6 and this test.
func TestReindexRepository_AppliesImpactFromChange_EndToEnd(t *testing.T) {
	t.Setenv("SOURCEBRIDGE_SELECTIVE_INVALIDATION", "true")
	t.Setenv("SOURCEBRIDGE_DELTA_REGEN_MODE", "off") // gate off the goroutine; we assert sync state only

	// Materialize a synthetic repository on disk. The default 500-file
	// shape exercises the mutation's incremental path on a meaningfully
	// large input, matching the same scaffolding test #6 uses.
	repoPath := testfixtures.LargeGoRepo(t, testfixtures.LargeGoRepoSpec{
		FileCount:      500,
		PackageBuckets: 10,
		Branch:         "main",
	})

	// Build a Resolver with the minimum dependencies ReindexRepository
	// touches: a Store (for repository lookup, file/symbol persistence,
	// ImpactReport storage) and a KnowledgeStore (for stale-artifact
	// flagging). No Worker, no LLMCaller — the mutation does not drive
	// them.
	store := graphstore.NewStore()
	knowledgeStore := knowledgepkg.NewMemStore()
	r := &mutationResolver{&Resolver{
		Deps: &appdeps.AppDeps{
			KnowledgeStore: knowledgeStore,
			// Flags must be set explicitly because applyImpactFromChange now
			// reads r.Deps.Flags.SelectiveInvalidationEnabled directly (boot-resolved
			// value) rather than re-reading the env var at call time. The Setenv
			// above is preserved so deltaRegenMode / deltaRegenModeWithFlags (which
			// still read SOURCEBRIDGE_DELTA_REGEN_MODE from env) behave correctly.
			Flags: featureflags.Flags{SelectiveInvalidationEnabled: true},
		},
		Store: store,
	}}

	// Register the repository with the local path (non-remote so the
	// mutation skips the clone/pull branch entirely).
	repo, err := store.CreateRepository(t.Context(), "phase1b-fixture", repoPath)
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	// Seed the initial index: run IndexRepository once and call
	// StoreIndexResult so subsequent ReindexRepository sees the prior
	// state and can compute hashes / diffs against it.
	idx := indexer.NewIndexer(nil)
	initial, err := idx.IndexRepository(context.Background(), repoPath, indexer.ReasonInitialOnboard)
	if err != nil {
		t.Fatalf("seed IndexRepository: %v", err)
	}
	initial.RepoName = repo.Name
	initial.RepoPath = repo.Path
	if _, err := store.ReplaceIndexResult(t.Context(), repo.ID, initial); err != nil {
		t.Fatalf("seed ReplaceIndexResult: %v", err)
	}
	// Seed the repo's commit SHA so the mutation's DiffRefs path
	// fires (oldCommitSHA != newCommitSHA after the test edit's
	// commit). Without this, the resolver falls into the
	// changedFiles="every file" fallback and the impact report's
	// FilesChanged stays empty, which would silently bypass the
	// file-keyed evidence path we are trying to exercise.
	headBytes, err := exec.Command("git", "-C", repoPath, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("seed rev-parse: %v", err)
	}
	store.UpdateRepositoryMeta(t.Context(), repo.ID, graphstore.RepositoryMeta{
		CommitSHA: strings.TrimSpace(string(headBytes)),
		Branch:    "main",
	})

	// Seed knowledge artifacts with FILE-keyed evidence (not symbol-keyed).
	// File paths are stable across reindex passes; symbol IDs are not
	// (graphstore.ReplaceIndexResult assigns fresh UUIDs to every
	// symbol on each call, so symbol-keyed evidence can never match a
	// post-reindex impact report's modified-symbol IDs). File-keyed
	// evidence is the representative shape MarkStaleForImpact's
	// real-world callers use anyway: knowledge-artifact construction
	// produces file paths from the indexer's FileResult, and those are
	// stable.
	//
	// Hit's evidence references the file we are about to edit;
	// miss's evidence references a different, untouched file.
	const hitFile = "pkg4/file250.go"
	const missFile = "pkg0/file1.go"
	hit := seedArtifactWithFileEvidence(t, knowledgeStore, repo.ID,
		knowledgepkg.ArtifactCliffNotes, hitFile)
	miss := seedArtifactWithFileEvidence(t, knowledgeStore, repo.ID,
		knowledgepkg.ArtifactLearningPath, missFile)

	// Edit the hit's source file to provoke a real symbol-modification
	// signal. The synthetic generator's Process() method body is
	// recognizable; replacing it with a different body leaves the
	// signature intact (so "modified" rather than "added"/"removed").
	target := "pkg4/file250.go"
	original := mustReadFile(t, filepath.Join(repoPath, target))
	edited := strings.Replace(original,
		`func (s *Service250) Process(input string) error {`,
		`func (s *Service250) Process(input string) error {
	// Phase 1.B end-to-end test edit: change the body to provoke a
	// "modified" symbol diff that propagates to ImpactReport.SymbolsModified.`,
		1)
	if edited == original {
		t.Fatalf("edit did not modify content; the synthetic fixture shape may have drifted")
	}
	if err := os.WriteFile(filepath.Join(repoPath, target), []byte(edited), 0o644); err != nil {
		t.Fatalf("write edit: %v", err)
	}
	// Commit the edit so DiffRefs has an old-vs-new SHA delta to compute.
	testfixtures.Commit(t, repoPath, "phase1b e2e: modify Service250.Process body")

	// Run the mutation under test.
	updated, err := r.ReindexRepository(context.Background(), repo.ID)
	if err != nil {
		t.Fatalf("ReindexRepository: %v", err)
	}
	if updated == nil {
		t.Fatalf("ReindexRepository returned nil repo")
	}

	// The persisted ImpactReport must exist and contain the expected
	// surgical StaleArtifacts (selective path: hit only, not miss).
	persisted := store.GetLatestImpactReport(t.Context(), repo.ID)
	if persisted == nil {
		t.Fatalf("no ImpactReport persisted; the helper extraction may be bypassed")
	}

	// At minimum: the persisted report must include the hit and not
	// the miss. The selective invalidation policy is the same algorithm
	// the helper-level test asserts; this test confirms the mutation's
	// inputs continue to drive that algorithm correctly.
	hasHit := false
	hasMiss := false
	for _, id := range persisted.StaleArtifacts {
		switch id {
		case hit.ID:
			hasHit = true
		case miss.ID:
			hasMiss = true
		}
	}
	if !hasHit {
		t.Fatalf("persisted StaleArtifacts missing hit %s; got %v", hit.ID, persisted.StaleArtifacts)
	}
	if hasMiss {
		t.Fatalf("persisted StaleArtifacts contains miss %s — selective path leaked an unrelated artifact: %v", miss.ID, persisted.StaleArtifacts)
	}

	// And the live KnowledgeStore must reflect the same flag state.
	hitState := knowledgeStore.GetKnowledgeArtifact(t.Context(), hit.ID)
	if hitState == nil || !hitState.Stale {
		t.Fatalf("hit artifact should be staled in knowledge store, got %+v", hitState)
	}
	missState := knowledgeStore.GetKnowledgeArtifact(t.Context(), miss.ID)
	if missState != nil && missState.Stale {
		t.Fatalf("miss artifact should NOT be staled (selective path), got %+v", missState)
	}
}

// seedArtifactWithFileEvidence is the file-evidence twin of
// seedArtifactWithSymbolEvidence (declared in
// apply_impact_from_change_test.go). The shape is identical except the
// section's evidence row uses SourceType=EvidenceFile + FilePath
// instead of SourceType=EvidenceSymbol + SourceID. File-keyed evidence
// is what MarkStaleForImpact's real callers produce and what survives
// the post-reindex symbol-ID reset (graphstore.ReplaceIndexResult
// assigns fresh UUIDs each pass).
func seedArtifactWithFileEvidence(
	t *testing.T,
	store *knowledgepkg.MemStore,
	repoID string,
	typ knowledgepkg.ArtifactType,
	filePath string,
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
		{SourceType: knowledgepkg.EvidenceFile, FilePath: filePath},
	}); err != nil {
		t.Fatalf("StoreKnowledgeEvidence: %v", err)
	}
	return store.GetKnowledgeArtifact(t.Context(), a.ID)
}

// mustReadFile is the same helper used by the indexer-package
// IndexFiles tests. Duplicated here because cross-package _test.go
// helpers aren't shareable; the duplication is two lines.
func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
