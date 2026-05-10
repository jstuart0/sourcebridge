// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

//go:build integration

// CA-304: ReplaceIndexResult silent data loss for test-linkage edges.
// LOAD-BEARING test: re-indexing a repo with test files MUST preserve the
// RelationTests rows. Two stacked bugs were closed together:
// (a) the DELETE block didn't drop ca_tests, so orphan rows survived;
// (b) the relation re-insert loop only handled RelationCalls, silently
//     dropping every RelationTests on every re-index.
// This test fails on the pre-fix code at both `len(testEdges) > 0` and
// the symbol-id reference check; the post-fix code passes both.

package db

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

func TestReplaceIndexResultPreservesTestLinkageEdges(t *testing.T) {
	s := startSurrealContainer(t)
	store := NewSurrealStore(s)

	// First index: a production symbol "Greet" + a test symbol "TestGreet"
	// + a RelationTests edge from TestGreet to Greet.
	initial := &indexer.IndexResult{
		RepoName:   "test-repo",
		RepoPath:   "/tmp/test-repo",
		TotalFiles: 2,
		Files: []indexer.FileResult{
			{
				Path:      "greet.go",
				Language:  "go",
				LineCount: 5,
				Symbols: []indexer.Symbol{
					{ID: "sym-greet", Name: "Greet", QualifiedName: "main.Greet", Kind: indexer.SymbolFunction, Language: "go", FilePath: "greet.go", StartLine: 1, EndLine: 5},
				},
			},
			{
				Path:      "greet_test.go",
				Language:  "go",
				LineCount: 7,
				Symbols: []indexer.Symbol{
					{ID: "sym-test-greet", Name: "TestGreet", QualifiedName: "main.TestGreet", Kind: indexer.SymbolFunction, Language: "go", FilePath: "greet_test.go", StartLine: 1, EndLine: 7, IsTest: true},
				},
			},
		},
		Relations: []indexer.Relation{
			{Type: indexer.RelationTests, SourceID: "sym-test-greet", TargetID: "sym-greet"},
		},
	}

	repo, err := store.StoreIndexResult(t.Context(), initial)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}

	// Sanity check: GetTestsForSymbolPersisted finds the linkage.
	syms, _ := store.GetSymbols(t.Context(), repo.ID, nil, nil, 100, 0)
	var greetID string
	for _, sym := range syms {
		if sym.Name == "Greet" {
			greetID = sym.ID
			break
		}
	}
	if greetID == "" {
		t.Fatal("Greet symbol not found after StoreIndexResult")
	}
	tests := store.GetTestsForSymbolPersisted(t.Context(), greetID)
	if len(tests) != 1 {
		t.Fatalf("post-StoreIndexResult: want 1 test edge for Greet, got %d", len(tests))
	}

	// Re-index with the same content — exercises ReplaceIndexResult.
	// Pre-fix code: this DELETE leaves orphan ca_tests + the re-insert
	// skips RelationTests, so the test-linkage edges vanish.
	// Post-fix code: edges are preserved.
	reindex := &indexer.IndexResult{
		RepoName:   initial.RepoName,
		RepoPath:   initial.RepoPath,
		TotalFiles: initial.TotalFiles,
		Files:      initial.Files,
		Relations:  initial.Relations,
	}
	_, err = store.ReplaceIndexResult(t.Context(), repo.ID, reindex)
	if err != nil {
		t.Fatalf("ReplaceIndexResult: %v", err)
	}

	// Symbols are recreated with new IDs; look up the new Greet.
	symsAfter, _ := store.GetSymbols(t.Context(), repo.ID, nil, nil, 100, 0)
	var greetIDAfter string
	for _, sym := range symsAfter {
		if sym.Name == "Greet" {
			greetIDAfter = sym.ID
			break
		}
	}
	if greetIDAfter == "" {
		t.Fatal("Greet symbol not found after ReplaceIndexResult")
	}
	if greetIDAfter == greetID {
		t.Fatal("expected fresh symbol ID after ReplaceIndexResult; got the original (was the DELETE skipped?)")
	}

	testsAfter := store.GetTestsForSymbolPersisted(t.Context(), greetIDAfter)
	if len(testsAfter) != 1 {
		t.Fatalf("CA-304 regression: want 1 test edge for Greet after re-index, got %d (test-linkage edges silently dropped)", len(testsAfter))
	}
}
