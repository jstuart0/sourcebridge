// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package search

import (
	"testing"

	"github.com/google/uuid"

	"github.com/sourcebridge/sourcebridge/internal/graph"
)

func TestRequirementBooster_NoOpOnZeroLinkRepo(t *testing.T) {
	repoID, s := seedStore(t)
	b := &RequirementBooster{Store: s}

	results := []*Result{
		{EntityType: "symbol", EntityID: "s1", Score: 0.5},
		{EntityType: "symbol", EntityID: "s2", Score: 0.4},
	}
	before := []float64{results[0].Score, results[1].Score}
	b.Apply(results, &Request{Repo: repoID, Query: "x"}, RouterOutput{})
	for i, r := range results {
		if r.Score != before[i] {
			t.Errorf("zero-link repo should leave scores untouched: got %v, want %v", r.Score, before[i])
		}
		if r.Signals.Requirement != 0 {
			t.Errorf("requirement signal should remain 0 on zero-link repo")
		}
	}
}

func TestRequirementBooster_LiftsLinkedSymbols(t *testing.T) {
	repoID, s := seedStore(t)
	// Grab the first symbol ID from the store to attach a link to.
	syms, _ := s.GetSymbols(repoID, nil, nil, 1, 0)
	if len(syms) == 0 {
		t.Fatal("seed store produced no symbols")
	}
	target := syms[0]
	// Create a requirement + link.
	req := &graph.StoredRequirement{ID: uuid.New().String(), RepoID: repoID, ExternalID: "REQ-T1", Title: "Test"}
	s.StoreRequirement(repoID, req)
	linkIn := &graph.StoredLink{
		RequirementID: req.ID,
		SymbolID:      target.ID,
		Confidence:    0.9,
		Source:        "manual",
		LinkType:      "implements",
	}
	s.StoreLink(repoID, linkIn)

	b := &RequirementBooster{Store: s, Weight: 0.2}
	results := []*Result{
		{EntityType: "symbol", EntityID: target.ID, Score: 0.5},
		{EntityType: "symbol", EntityID: "other", Score: 0.5},
	}
	b.Apply(results, &Request{Repo: repoID, Query: "x"}, RouterOutput{})
	if results[0].Score <= 0.5 {
		t.Errorf("linked symbol should have lifted score, got %v", results[0].Score)
	}
	if results[1].Score != 0.5 {
		t.Errorf("unlinked symbol score should be untouched, got %v", results[1].Score)
	}
	if results[0].Signals.Requirement == 0 {
		t.Errorf("linked symbol should carry requirement signal")
	}
}

func TestRequirementBooster_InvalidateRehydrates(t *testing.T) {
	repoID, s := seedStore(t)
	syms, _ := s.GetSymbols(repoID, nil, nil, 1, 0)
	if len(syms) == 0 {
		t.Fatal("seed store empty")
	}
	b := &RequirementBooster{Store: s}

	// First call warms the cache with the current (empty) link set —
	// passing a real result ensures we don't short-circuit before the
	// cache is populated.
	warm := []*Result{{EntityType: "symbol", EntityID: syms[0].ID, Score: 1.0}}
	b.Apply(warm, &Request{Repo: repoID}, RouterOutput{})
	if warm[0].Signals.Requirement != 0 {
		t.Fatalf("unexpected lift before any link was stored")
	}

	// Add a link after warming. The cache is now stale.
	req := &graph.StoredRequirement{ID: uuid.New().String(), RepoID: repoID, ExternalID: "REQ-T2", Title: "T2"}
	s.StoreRequirement(repoID, req)
	s.StoreLink(repoID, &graph.StoredLink{RequirementID: req.ID, SymbolID: syms[0].ID, Confidence: 1.0})

	// Without invalidate the booster uses stale data and does not lift.
	stale := []*Result{{EntityType: "symbol", EntityID: syms[0].ID, Score: 1.0}}
	b.Apply(stale, &Request{Repo: repoID}, RouterOutput{})
	if stale[0].Signals.Requirement != 0 {
		t.Errorf("stale cache should miss the new link; signal=%v", stale[0].Signals.Requirement)
	}

	// After invalidate: cache rehydrates and the new link fires.
	b.Invalidate(repoID)
	fresh := []*Result{{EntityType: "symbol", EntityID: syms[0].ID, Score: 1.0}}
	b.Apply(fresh, &Request{Repo: repoID}, RouterOutput{})
	if fresh[0].Signals.Requirement == 0 {
		t.Errorf("invalidated cache should see the new link; score=%v", fresh[0].Score)
	}
}
