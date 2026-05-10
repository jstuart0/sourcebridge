// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package orchestrator_test

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
)

// buildCallGraphStore seeds an in-memory graph store via StoreIndexResult,
// creating two packages:
//
//	internal/auth    — symbol "Middleware"
//	internal/billing — symbol "Charge" that calls auth.Middleware
//
// Returns the store and the repo ID assigned by the store.
func buildCallGraphStore(t *testing.T) (gs *graph.Store, repoID string) {
	t.Helper()
	gs = graph.NewStore()

	result := &indexer.IndexResult{
		RepoName: "test-repo",
		RepoPath: "/tmp/graph-metrics-test",
		Files: []indexer.FileResult{
			{
				Path:      "internal/auth/auth.go",
				Language:  "go",
				LineCount: 30,
				Symbols: []indexer.Symbol{
					{
						ID:        "auth-middleware",
						Name:      "Middleware",
						Kind:      "function",
						Language:  "go",
						FilePath:  "internal/auth/auth.go",
						StartLine: 10,
						EndLine:   25,
					},
				},
			},
			{
				Path:      "internal/billing/billing.go",
				Language:  "go",
				LineCount: 20,
				Symbols: []indexer.Symbol{
					{
						ID:        "billing-charge",
						Name:      "Charge",
						Kind:      "function",
						Language:  "go",
						FilePath:  "internal/billing/billing.go",
						StartLine: 5,
						EndLine:   18,
					},
				},
			},
		},
		// billing.Charge → auth.Middleware (RelationCalls)
		Relations: []indexer.Relation{
			{
				SourceID: "billing-charge",
				TargetID: "auth-middleware",
				Type:     indexer.RelationCalls,
			},
		},
	}

	repo, err := gs.StoreIndexResult(t.Context(), result)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}
	return gs, repo.ID
}

// TestGraphStoreMetrics_EmptyStore verifies that an empty store returns 0
// counts without panicking for any page type.
func TestGraphStoreMetrics_EmptyStore(t *testing.T) {
	t.Parallel()

	gs := graph.NewStore()
	m := orchestrator.NewGraphStoreMetrics(gs)

	cases := []struct {
		repoID string
		pageID string
	}{
		{"test-repo", "test-repo.arch.internal.auth"},
		{"test-repo", "test-repo.api_reference"},
		{"test-repo", "test-repo.system_overview"},
		{"test-repo", "test-repo.glossary"},
		{"", "arch.internal.auth"},
	}

	for _, tc := range cases {
		refs := m.PageReferenceCount(tc.repoID, tc.pageID)
		rels := m.GraphRelationCount(tc.repoID, tc.pageID)
		if refs != 0 {
			t.Errorf("PageReferenceCount(%q, %q) = %d, want 0 (empty store)", tc.repoID, tc.pageID, refs)
		}
		if rels != 0 {
			t.Errorf("GraphRelationCount(%q, %q) = %d, want 0 (empty store)", tc.repoID, tc.pageID, rels)
		}
	}
}

// TestGraphStoreMetrics_AuthPackage verifies inbound counts for the auth
// package, which is called by billing.
func TestGraphStoreMetrics_AuthPackage(t *testing.T) {
	t.Parallel()

	gs, repoID := buildCallGraphStore(t)
	m := orchestrator.NewGraphStoreMetrics(gs)

	// auth.Middleware is called by billing.Charge.
	// PageReferenceCount should be 1 (billing is one distinct caller package).
	// GraphRelationCount should be 1 (one call edge pointing in).
	//
	// The pageID uses the real repoID returned by StoreIndexResult, which is a
	// UUID.  We derive the page ID from it using the same convention as
	// TaxonomyResolver: repoID + ".arch." + dotted-package.
	authPageID := repoID + ".arch.internal.auth"
	refs := m.PageReferenceCount(repoID, authPageID)
	rels := m.GraphRelationCount(repoID, authPageID)

	if refs != 1 {
		t.Errorf("PageReferenceCount for auth = %d, want 1 (billing calls it)", refs)
	}
	if rels != 1 {
		t.Errorf("GraphRelationCount for auth = %d, want 1 edge", rels)
	}
}

// TestGraphStoreMetrics_BillingPackage verifies that the billing package
// has 0 inbound references (nothing calls into it in our fixture).
func TestGraphStoreMetrics_BillingPackage(t *testing.T) {
	t.Parallel()

	gs, repoID := buildCallGraphStore(t)
	m := orchestrator.NewGraphStoreMetrics(gs)

	billingPageID := repoID + ".arch.internal.billing"
	refs := m.PageReferenceCount(repoID, billingPageID)
	rels := m.GraphRelationCount(repoID, billingPageID)

	if refs != 0 {
		t.Errorf("PageReferenceCount for billing = %d, want 0", refs)
	}
	if rels != 0 {
		t.Errorf("GraphRelationCount for billing = %d, want 0", rels)
	}
}

// TestGraphStoreMetrics_NonArchPage verifies that the api_reference page
// (a non-arch page) aggregates across all packages — at least 1 reference
// since billing calls auth.
func TestGraphStoreMetrics_NonArchPage(t *testing.T) {
	t.Parallel()

	gs, repoID := buildCallGraphStore(t)
	m := orchestrator.NewGraphStoreMetrics(gs)

	apiPageID := repoID + ".api_reference"
	refs := m.PageReferenceCount(repoID, apiPageID)
	rels := m.GraphRelationCount(repoID, apiPageID)

	// At least 1 caller package and 1 relation (billing → auth).
	if refs < 1 {
		t.Errorf("PageReferenceCount for api_reference = %d, want ≥1", refs)
	}
	if rels < 1 {
		t.Errorf("GraphRelationCount for api_reference = %d, want ≥1", rels)
	}
}
