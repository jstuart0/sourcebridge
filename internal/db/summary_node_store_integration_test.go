// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

//go:build integration

package db

import (
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
)

// TestSummaryNodeStore_RoundTrip exercises the four SummaryNodeStore methods
// against a real SurrealDB: StoreSummaryNodes → GetSummaryNodes →
// GetSummaryNode → InvalidateSummaryNodes → re-read empty.
//
// This test pins the persistence contract that the MemStore unit tests
// (internal/settings/comprehension/…) cannot exercise: actual SurrealDB
// INSERT/SELECT/DELETE behavior, field serialization, and the invalidation
// cascade.
func TestSummaryNodeStore_RoundTrip(t *testing.T) {
	surreal := startSurrealContainer(t)
	store := surreal

	const corpusID = "summary-node-roundtrip-corpus"

	nodes := []comprehension.SummaryNode{
		{
			CorpusID:      corpusID,
			UnitID:        "unit-1",
			Level:         1,
			SummaryText:   "This unit handles authentication.",
			Headline:      "Auth handler",
			SummaryTokens: 42,
			SourceTokens:  100,
			ContentHash:   "hash-unit-1",
			ModelUsed:     "claude-sonnet",
			Strategy:      "top_down",
			RevisionFP:    "rev-abc",
			GeneratedAt:   time.Now().Truncate(time.Second),
		},
		{
			CorpusID:      corpusID,
			UnitID:        "unit-2",
			Level:         2,
			ParentID:      "unit-1",
			SummaryText:   "Token validation logic.",
			Headline:      "Token validator",
			SummaryTokens: 18,
			SourceTokens:  60,
			ContentHash:   "hash-unit-2",
			ModelUsed:     "claude-sonnet",
			GeneratedAt:   time.Now().Truncate(time.Second),
		},
	}

	// 1. Store
	if err := store.StoreSummaryNodes(t.Context(), nodes); err != nil {
		t.Fatalf("StoreSummaryNodes: %v", err)
	}

	// 2. GetSummaryNodes — both nodes returned
	got, err := store.GetSummaryNodes(t.Context(), corpusID)
	if err != nil {
		t.Fatalf("GetSummaryNodes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("GetSummaryNodes: got %d nodes, want 2", len(got))
	}

	// 3. GetSummaryNode — look up by unitID
	single, err := store.GetSummaryNode(t.Context(), corpusID, "unit-1")
	if err != nil {
		t.Fatalf("GetSummaryNode: %v", err)
	}
	if single == nil {
		t.Fatal("GetSummaryNode: expected non-nil for unit-1")
	}
	if single.Headline != "Auth handler" {
		t.Errorf("GetSummaryNode headline = %q, want %q", single.Headline, "Auth handler")
	}
	if single.SummaryTokens != 42 {
		t.Errorf("GetSummaryNode summary_tokens = %d, want 42", single.SummaryTokens)
	}

	// GetSummaryNode for missing unitID returns nil without error
	missing, err := store.GetSummaryNode(t.Context(), corpusID, "unit-does-not-exist")
	if err != nil {
		t.Fatalf("GetSummaryNode (missing): unexpected error: %v", err)
	}
	if missing != nil {
		t.Errorf("GetSummaryNode (missing): expected nil, got %+v", missing)
	}

	// 4. InvalidateSummaryNodes — deletes all nodes for the corpus
	if err := store.InvalidateSummaryNodes(t.Context(), corpusID); err != nil {
		t.Fatalf("InvalidateSummaryNodes: %v", err)
	}

	// Re-read must be empty
	after, err := store.GetSummaryNodes(t.Context(), corpusID)
	if err != nil {
		t.Fatalf("GetSummaryNodes after invalidation: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("GetSummaryNodes after invalidation: got %d nodes, want 0", len(after))
	}
}
