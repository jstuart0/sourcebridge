// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graph

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

// TestStore_PersistsRelationTests confirms that RelationTests edges
// on an IndexResult are written to the testedByGraph and retrievable
// via GetTestsForSymbolPersisted.
func TestStore_PersistsRelationTests(t *testing.T) {
	store := NewStore()

	result := &indexer.IndexResult{
		RepoName: "test-repo",
		RepoPath: "/tmp/test-linkage",
		Files: []indexer.FileResult{
			{
				Path:      "auth.go",
				Language:  "go",
				LineCount: 30,
				Symbols: []indexer.Symbol{
					{ID: "prod-verify", Name: "Verify", Kind: "function", Language: "go", FilePath: "auth.go", StartLine: 10, EndLine: 20},
				},
			},
			{
				Path:      "auth_test.go",
				Language:  "go",
				LineCount: 50,
				Symbols: []indexer.Symbol{
					{ID: "test-verify-valid", Name: "TestVerifyValid", Kind: "function", Language: "go", FilePath: "auth_test.go", StartLine: 10, EndLine: 25, IsTest: true},
					{ID: "test-verify-invalid", Name: "TestVerifyInvalid", Kind: "function", Language: "go", FilePath: "auth_test.go", StartLine: 30, EndLine: 45, IsTest: true},
				},
			},
		},
		Relations: []indexer.Relation{
			{SourceID: "test-verify-valid", TargetID: "prod-verify", Type: indexer.RelationTests},
			{SourceID: "test-verify-invalid", TargetID: "prod-verify", Type: indexer.RelationTests},
		},
	}

	repo, err := store.StoreIndexResult(result)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}

	// Find the stored IDs by file + name (the in-memory store
	// regenerates IDs from the IndexResult, so we can't use the
	// tmp-* IDs directly).
	var prodID string
	var testIDs []string
	for _, s := range store.GetSymbolsByFile(repo.ID, "auth.go") {
		if s.Name == "Verify" {
			prodID = s.ID
		}
	}
	for _, s := range store.GetSymbolsByFile(repo.ID, "auth_test.go") {
		if s.IsTest {
			testIDs = append(testIDs, s.ID)
		}
	}
	if prodID == "" || len(testIDs) != 2 {
		t.Fatalf("fixture setup: prodID=%q testIDs=%v", prodID, testIDs)
	}

	got := store.GetTestsForSymbolPersisted(prodID)
	if len(got) != 2 {
		t.Errorf("expected 2 test edges for Verify, got %d: %v", len(got), got)
	}
	// Ensure every returned ID matches one of our test symbols.
	testSet := map[string]bool{testIDs[0]: true, testIDs[1]: true}
	for _, id := range got {
		if !testSet[id] {
			t.Errorf("unexpected test ID in edge result: %q", id)
		}
	}
}

// TestStore_NoRelationTestsForUntested asserts that symbols with no
// edges return an empty slice (not a crash, not nil).
func TestStore_NoRelationTestsForUntested(t *testing.T) {
	store := NewStore()
	got := store.GetTestsForSymbolPersisted("nonexistent-id")
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}
