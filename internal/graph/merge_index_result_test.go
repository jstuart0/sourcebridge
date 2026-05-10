// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graph

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

// fixtureTwoFileRepo returns the IndexResult for a tiny synthetic repo
// with two source files and two test-linkage relations. The shape is
// deliberately small so the merge cases below are easy to read; the
// real router-level integration tests in internal/changewatch exercise
// merging on larger fixtures.
func fixtureTwoFileRepo() *indexer.IndexResult {
	return &indexer.IndexResult{
		RepoName: "merge-fixture",
		RepoPath: "/tmp/merge-fixture",
		Files: []indexer.FileResult{
			{
				Path:        "auth.go",
				Language:    "go",
				LineCount:   30,
				ContentHash: "hash-auth-1",
				Symbols: []indexer.Symbol{
					{ID: "tmp-verify", Name: "Verify", QualifiedName: "auth.Verify", Kind: indexer.SymbolFunction, Language: "go", FilePath: "auth.go", StartLine: 10, EndLine: 20},
					{ID: "tmp-helper", Name: "helper", QualifiedName: "auth.helper", Kind: indexer.SymbolFunction, Language: "go", FilePath: "auth.go", StartLine: 22, EndLine: 28},
				},
			},
			{
				Path:        "auth_test.go",
				Language:    "go",
				LineCount:   40,
				ContentHash: "hash-auth-test-1",
				Symbols: []indexer.Symbol{
					{ID: "tmp-test-verify", Name: "TestVerify", QualifiedName: "auth.TestVerify", Kind: indexer.SymbolFunction, Language: "go", FilePath: "auth_test.go", StartLine: 5, EndLine: 25, IsTest: true},
				},
			},
		},
		Relations: []indexer.Relation{
			{SourceID: "tmp-verify", TargetID: "tmp-helper", Type: indexer.RelationCalls},
			{SourceID: "tmp-test-verify", TargetID: "tmp-verify", Type: indexer.RelationTests},
		},
		TotalFiles:   2,
		TotalSymbols: 3,
	}
}

// findSymbolByName looks up a symbol in the store by repo / file / name.
func findSymbolByName(t *testing.T, store *Store, repoID, filePath, name string) *StoredSymbol {
	t.Helper()
	for _, s := range store.GetSymbolsByFile(t.Context(), repoID, filePath) {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// TestMergeIndexResult_NewRepoFromInitialReplace establishes the
// baseline: ReplaceIndexResult creates the repo + symbols + relations,
// and the assertions match the fixture.
func TestMergeIndexResult_NewRepoFromInitialReplace(t *testing.T) {
	store := NewStore()
	repo, err := store.CreateRepository(t.Context(), "merge-fixture", "/tmp/merge-fixture")
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	if _, err := store.ReplaceIndexResult(t.Context(), repo.ID, fixtureTwoFileRepo()); err != nil {
		t.Fatalf("ReplaceIndexResult: %v", err)
	}
	if findSymbolByName(t, store, repo.ID, "auth.go", "Verify") == nil {
		t.Fatalf("expected Verify symbol in store after Replace")
	}
	verify := findSymbolByName(t, store, repo.ID, "auth.go", "Verify")
	helper := findSymbolByName(t, store, repo.ID, "auth.go", "helper")
	if verify == nil || helper == nil {
		t.Fatalf("missing fixture symbols: verify=%v helper=%v", verify, helper)
	}
	if got := store.GetCallees(t.Context(), verify.ID); len(got) != 1 || got[0] != helper.ID {
		t.Errorf("Verify callees = %v, want [%s]", got, helper.ID)
	}
}

// TestMergeIndexResult_PreservesUnaffectedFile asserts the load-bearing
// behavior: a per-file delta on auth.go does NOT clobber auth_test.go's
// existing rows, IDs, or edges.
//
// Without MergeIndexResult, the only available primitive is
// ReplaceIndexResult, which would have wiped auth_test.go's symbols
// and rebuilt them with fresh UUIDs — invalidating any caller that
// retained a reference to the prior IDs.
func TestMergeIndexResult_PreservesUnaffectedFile(t *testing.T) {
	store := NewStore()
	repo, _ := store.CreateRepository(t.Context(), "merge-fixture", "/tmp/merge-fixture")
	if _, err := store.ReplaceIndexResult(t.Context(), repo.ID, fixtureTwoFileRepo()); err != nil {
		t.Fatalf("ReplaceIndexResult: %v", err)
	}
	// Capture the test file's symbol ID — this must survive the merge.
	testVerifyBefore := findSymbolByName(t, store, repo.ID, "auth_test.go", "TestVerify")
	if testVerifyBefore == nil {
		t.Fatalf("missing TestVerify before merge")
	}
	testVerifyIDBefore := testVerifyBefore.ID

	// Build a merged result that re-parses ONLY auth.go (the test file
	// is carry-forward — same symbols, same hash, no changes).
	merged := fixtureTwoFileRepo()
	// Mutate auth.go's symbols slightly: rename helper to inner so we
	// can confirm the per-file re-insertion path actually fired.
	merged.Files[0].Symbols[1].Name = "inner"
	merged.Files[0].Symbols[1].QualifiedName = "auth.inner"
	// Relations: indexer.IndexFiles would have recomputed these over
	// the merged file set. The call edge target is now "inner" by name.
	merged.Relations = []indexer.Relation{
		{SourceID: "tmp-verify", TargetID: "tmp-helper", Type: indexer.RelationCalls},
		{SourceID: "tmp-test-verify", TargetID: "tmp-verify", Type: indexer.RelationTests},
	}

	if _, err := store.MergeIndexResult(t.Context(), repo.ID, []string{"auth.go"}, merged); err != nil {
		t.Fatalf("MergeIndexResult: %v", err)
	}

	// The unaffected test file's symbol ID must be preserved.
	testVerifyAfter := findSymbolByName(t, store, repo.ID, "auth_test.go", "TestVerify")
	if testVerifyAfter == nil {
		t.Fatalf("TestVerify disappeared after merge (carry-forward broken)")
	}
	if testVerifyAfter.ID != testVerifyIDBefore {
		t.Errorf("TestVerify ID changed across merge: before=%q after=%q (carry-forward broken)", testVerifyIDBefore, testVerifyAfter.ID)
	}
	// auth.go's symbols must be fresh (rename took effect).
	if findSymbolByName(t, store, repo.ID, "auth.go", "helper") != nil {
		t.Errorf("old helper symbol survived merge — file delta did not drop the old row")
	}
	if findSymbolByName(t, store, repo.ID, "auth.go", "inner") == nil {
		t.Errorf("new inner symbol missing — file delta did not insert the new row")
	}
}

// TestMergeIndexResult_TestLinkageReconnectsCarryForward asserts the
// load-bearing edge-rewriting behavior: a relation whose Target is a
// carry-forward symbol (test linkage from auth_test.go::TestVerify to
// auth.go::Verify) is correctly re-inserted against the carry-forward
// symbol's existing store ID after the merge — not silently dropped.
func TestMergeIndexResult_TestLinkageReconnectsCarryForward(t *testing.T) {
	store := NewStore()
	repo, _ := store.CreateRepository(t.Context(), "merge-fixture", "/tmp/merge-fixture")
	if _, err := store.ReplaceIndexResult(t.Context(), repo.ID, fixtureTwoFileRepo()); err != nil {
		t.Fatalf("ReplaceIndexResult: %v", err)
	}
	testVerifyBefore := findSymbolByName(t, store, repo.ID, "auth_test.go", "TestVerify")
	if testVerifyBefore == nil {
		t.Fatalf("missing TestVerify before merge")
	}

	// Re-merge auth.go alone with a fresh set of symbols (Verify and
	// helper preserved, but the merge re-inserts them with fresh IDs).
	// Relations must be re-resolved against the new auth.go symbols
	// AND against the carry-forward TestVerify symbol.
	merged := fixtureTwoFileRepo()
	if _, err := store.MergeIndexResult(t.Context(), repo.ID, []string{"auth.go"}, merged); err != nil {
		t.Fatalf("MergeIndexResult: %v", err)
	}
	verifyAfter := findSymbolByName(t, store, repo.ID, "auth.go", "Verify")
	if verifyAfter == nil {
		t.Fatalf("Verify missing after merge")
	}
	tests := store.GetTestsForSymbolPersisted(t.Context(), verifyAfter.ID)
	if len(tests) != 1 {
		t.Fatalf("test linkage edges for Verify after merge = %v, want exactly 1 (carry-forward TestVerify)", tests)
	}
	if tests[0] != testVerifyBefore.ID {
		t.Errorf("test linkage points to wrong symbol: got %q, want carry-forward TestVerify ID %q", tests[0], testVerifyBefore.ID)
	}
}

// TestMergeIndexResult_DeletionDropsFile asserts that a path in
// affectedPaths but absent from result.Files is treated as a deletion —
// the file row, its symbols, and any dependent edges are dropped.
func TestMergeIndexResult_DeletionDropsFile(t *testing.T) {
	store := NewStore()
	repo, _ := store.CreateRepository(t.Context(), "merge-fixture", "/tmp/merge-fixture")
	if _, err := store.ReplaceIndexResult(t.Context(), repo.ID, fixtureTwoFileRepo()); err != nil {
		t.Fatalf("ReplaceIndexResult: %v", err)
	}

	// Build a result where auth_test.go is deleted (absent from Files).
	merged := &indexer.IndexResult{
		RepoName: "merge-fixture",
		RepoPath: "/tmp/merge-fixture",
		Files:    []indexer.FileResult{fixtureTwoFileRepo().Files[0]}, // only auth.go
		// No Relations: with auth_test.go deleted there's no test edge to
		// re-resolve. The call edge inside auth.go would normally be
		// recomputed by indexer.IndexFiles; we pass nil here to confirm
		// the merge does not re-introduce the dropped test edge.
		Relations: nil,
	}
	if _, err := store.MergeIndexResult(t.Context(), repo.ID, []string{"auth_test.go"}, merged); err != nil {
		t.Fatalf("MergeIndexResult: %v", err)
	}

	// auth_test.go should be gone.
	if got := store.GetSymbolsByFile(t.Context(), repo.ID, "auth_test.go"); len(got) != 0 {
		t.Errorf("auth_test.go symbols after deletion = %v, want empty", got)
	}
	// auth.go should be intact.
	if findSymbolByName(t, store, repo.ID, "auth.go", "Verify") == nil {
		t.Errorf("Verify disappeared during deletion of auth_test.go (cross-file leak)")
	}
}

// TestMergeIndexResult_AdditionInsertsNewFile asserts that a path in
// affectedPaths AND in result.Files but absent from the prior store is
// inserted as net-new.
func TestMergeIndexResult_AdditionInsertsNewFile(t *testing.T) {
	store := NewStore()
	repo, _ := store.CreateRepository(t.Context(), "merge-fixture", "/tmp/merge-fixture")
	if _, err := store.ReplaceIndexResult(t.Context(), repo.ID, fixtureTwoFileRepo()); err != nil {
		t.Fatalf("ReplaceIndexResult: %v", err)
	}

	// Build a result that adds a third file new.go alongside the
	// originals. IndexFiles would have done this for an "added" delta.
	merged := fixtureTwoFileRepo()
	merged.Files = append(merged.Files, indexer.FileResult{
		Path:      "new.go",
		Language:  "go",
		LineCount: 10,
		Symbols: []indexer.Symbol{
			{ID: "tmp-newsym", Name: "NewSym", QualifiedName: "auth.NewSym", Kind: indexer.SymbolFunction, Language: "go", FilePath: "new.go", StartLine: 1, EndLine: 5},
		},
	})
	if _, err := store.MergeIndexResult(t.Context(), repo.ID, []string{"new.go"}, merged); err != nil {
		t.Fatalf("MergeIndexResult: %v", err)
	}
	if findSymbolByName(t, store, repo.ID, "new.go", "NewSym") == nil {
		t.Fatalf("NewSym missing after addition")
	}
	// Carry-forward files must be unaffected.
	if findSymbolByName(t, store, repo.ID, "auth.go", "Verify") == nil {
		t.Errorf("Verify disappeared during addition of new.go (cross-file leak)")
	}
}

// TestMergeIndexResult_RecomputesAggregates asserts FunctionCount and
// FileCount on the Repository row are recomputed from the merged set.
func TestMergeIndexResult_RecomputesAggregates(t *testing.T) {
	store := NewStore()
	repo, _ := store.CreateRepository(t.Context(), "merge-fixture", "/tmp/merge-fixture")
	if _, err := store.ReplaceIndexResult(t.Context(), repo.ID, fixtureTwoFileRepo()); err != nil {
		t.Fatalf("ReplaceIndexResult: %v", err)
	}
	// Fixture: 3 functions across 2 files.
	if r := store.GetRepository(t.Context(), repo.ID); r.FileCount != 2 || r.FunctionCount != 3 {
		t.Fatalf("baseline aggregates: file=%d func=%d, want 2/3", r.FileCount, r.FunctionCount)
	}

	// Drop auth_test.go: 1 function, 1 file gone.
	merged := &indexer.IndexResult{
		RepoName: "merge-fixture",
		RepoPath: "/tmp/merge-fixture",
		Files:    []indexer.FileResult{fixtureTwoFileRepo().Files[0]},
	}
	if _, err := store.MergeIndexResult(t.Context(), repo.ID, []string{"auth_test.go"}, merged); err != nil {
		t.Fatalf("MergeIndexResult: %v", err)
	}
	r := store.GetRepository(t.Context(), repo.ID)
	if r.FileCount != 1 {
		t.Errorf("FileCount after deletion = %d, want 1", r.FileCount)
	}
	if r.FunctionCount != 2 {
		t.Errorf("FunctionCount after deletion = %d, want 2 (Verify + helper, no TestVerify)", r.FunctionCount)
	}
}

// TestMergeIndexResult_NilRepository asserts the missing-repo case
// surfaces as a clear error rather than panicking.
func TestMergeIndexResult_NilRepository(t *testing.T) {
	store := NewStore()
	_, err := store.MergeIndexResult(t.Context(), "nonexistent-repo", []string{"a.go"}, fixtureTwoFileRepo())
	if err == nil {
		t.Fatalf("MergeIndexResult on nonexistent repo: err = nil, want non-nil")
	}
}

// TestMergeIndexResult_NilResult asserts the input-validation boundary.
func TestMergeIndexResult_NilResult(t *testing.T) {
	store := NewStore()
	repo, _ := store.CreateRepository(t.Context(), "merge-fixture", "/tmp/merge-fixture")
	_, err := store.MergeIndexResult(t.Context(), repo.ID, []string{"a.go"}, nil)
	if err == nil {
		t.Fatalf("MergeIndexResult with nil result: err = nil, want non-nil")
	}
}

// TestMergeIndexResult_RecordsBranch asserts the branch threading from
// the plan v5 (HIGH fix #6 / Risk #4) lands on the Repository row when
// result.Branch is set. The change-watch router stamps the validated
// branch onto the merged IndexResult before calling MergeIndexResult.
func TestMergeIndexResult_RecordsBranch(t *testing.T) {
	store := NewStore()
	repo, _ := store.CreateRepository(t.Context(), "merge-fixture", "/tmp/merge-fixture")
	if _, err := store.ReplaceIndexResult(t.Context(), repo.ID, fixtureTwoFileRepo()); err != nil {
		t.Fatalf("ReplaceIndexResult: %v", err)
	}
	merged := fixtureTwoFileRepo()
	merged.Branch = "feature/x"
	if _, err := store.MergeIndexResult(t.Context(), repo.ID, []string{"auth.go"}, merged); err != nil {
		t.Fatalf("MergeIndexResult: %v", err)
	}
	if got := store.GetRepository(t.Context(), repo.ID).Branch; got != "feature/x" {
		t.Errorf("repo.Branch after merge = %q, want feature/x", got)
	}
}
