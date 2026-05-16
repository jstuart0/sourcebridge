// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// T-H4: ExplainSystem resolver wiring test.
//
// Verifies that the resolver correctly populates References and RelatedRequirements
// from worker evidence — the wiring at schema.resolvers.go:1826-1827 that was
// previously dead-by-untestedness.

package graphql

import (
	"os"
	"path/filepath"
	"testing"

	knowledgev1 "github.com/sourcebridge/sourcebridge/gen/go/knowledge/v1"
	"github.com/sourcebridge/sourcebridge/internal/appdeps"
	"github.com/sourcebridge/sourcebridge/internal/events"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
	"github.com/sourcebridge/sourcebridge/internal/worker"
	"github.com/sourcebridge/sourcebridge/internal/worker/llmcall"
)

// newResolverWithExplainWorker constructs a resolver whose EnrichFakeWorker returns
// the given ExplainSystem response. Uses enrichFakeWorker (defined in
// enrich_requirement_test.go) with the explainResp field set.
func newResolverWithExplainWorker(t *testing.T, repoDir string, resp *knowledgev1.ExplainSystemResponse) (*Resolver, string) {
	t.Helper()
	store := graphstore.NewStore()
	result := &indexer.IndexResult{
		RepoName: "explain-test-repo",
		RepoPath: repoDir,
	}
	repo, _ := store.StoreIndexResult(t.Context(), result)

	fw := &enrichFakeWorker{explainResp: resp}
	snap := resolution.Snapshot{Provider: "test", Model: "test-model"}
	caller := llmcall.New(fw, resolution.NewFrozenResolver(snap), nil)

	return &Resolver{
		Deps: &appdeps.AppDeps{
			EventBus:  events.NewBus(),
			LLMCaller: caller,
			Worker:    new(worker.Client), // non-nil sentinel; passes availability guard
		},
		Store: store,
	}, repo.ID
}

// TestExplainSystem_PopulatesReferencesAndRelatedRequirements verifies that the
// resolver correctly wires resp.Evidence → result.References and
// result.RelatedRequirements.
func TestExplainSystem_PopulatesReferencesAndRelatedRequirements(t *testing.T) {
	repoDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# Test"), 0o644)

	workerResp := &knowledgev1.ExplainSystemResponse{
		Explanation: "The system does X via Y.",
		Evidence: []*knowledgev1.KnowledgeEvidence{
			// Symbol evidence → References[{kind:"symbol"}]
			{SourceType: "symbol", SourceId: "sym-abc", FilePath: "pkg/foo.go", LineStart: 10, LineEnd: 20},
			// Requirement evidence → References[{kind:"requirement"}] + RelatedRequirements["REQ-42"]
			{SourceType: "requirement", SourceId: "REQ-42"},
		},
	}

	classicMode := KnowledgeGenerationModeClassic
	r, repoID := newResolverWithExplainWorker(t, repoDir, workerResp)
	result, err := r.Mutation().ExplainSystem(t.Context(), ExplainSystemInput{
		RepositoryID:   repoID,
		GenerationMode: &classicMode, // avoids r.Deps.KnowledgeStore nil-deref
	})
	if err != nil {
		t.Fatalf("ExplainSystem: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.Explanation != "The system does X via Y." {
		t.Errorf("Explanation: got %q", result.Explanation)
	}

	// References must contain one symbol ref and one requirement ref.
	if len(result.References) != 2 {
		t.Fatalf("References: expected 2, got %d: %v", len(result.References), result.References)
	}
	kindCounts := map[string]int{}
	for _, ref := range result.References {
		kindCounts[ref.Kind]++
	}
	if kindCounts["symbol"] != 1 {
		t.Errorf("References: expected 1 'symbol' ref, got %d", kindCounts["symbol"])
	}
	if kindCounts["requirement"] != 1 {
		t.Errorf("References: expected 1 'requirement' ref, got %d", kindCounts["requirement"])
	}

	// RelatedRequirements must contain "REQ-42".
	if len(result.RelatedRequirements) != 1 || result.RelatedRequirements[0] != "REQ-42" {
		t.Errorf("RelatedRequirements: got %v, want [REQ-42]", result.RelatedRequirements)
	}
}

// TestExplainSystem_EmptyEvidence_ReturnsEmptySlices verifies that nil evidence
// produces empty (non-nil) slices and no panic.
func TestExplainSystem_EmptyEvidence_ReturnsEmptySlices(t *testing.T) {
	repoDir := t.TempDir()
	workerResp := &knowledgev1.ExplainSystemResponse{
		Explanation: "Simple.",
		Evidence:    nil,
	}

	classicMode := KnowledgeGenerationModeClassic
	r, repoID := newResolverWithExplainWorker(t, repoDir, workerResp)
	result, err := r.Mutation().ExplainSystem(t.Context(), ExplainSystemInput{
		RepositoryID:   repoID,
		GenerationMode: &classicMode,
	})
	if err != nil {
		t.Fatalf("ExplainSystem: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.References == nil {
		t.Error("References should be empty slice, not nil")
	}
	if result.RelatedRequirements == nil {
		t.Error("RelatedRequirements should be empty slice, not nil")
	}
}
