// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package indexer

import (
	"context"
	"errors"
	"testing"
)

// TestIndexFiles_StubContract pins the Phase 1.A signature-stub
// behavior of Indexer.IndexFiles so any accidental partial
// implementation that lands before Phase 1.B trips this test.
//
// The contract has three load-bearing properties:
//   - The function exists and compiles with the documented signature
//     (the test would not compile if the signature drifted).
//   - It returns ErrIndexFilesNotImplemented (not nil, not some
//     other error) — downstream slices that compile against the
//     signature need to be able to detect "not yet implemented" via
//     errors.Is so they can fail closed during Phase 1.B/C
//     integration work.
//   - It does NOT mutate previousResult — Phase 1.B's eventual
//     implementation will document non-mutation, and this test
//     prevents the stub itself from accidentally introducing a
//     mutation contract before then.
func TestIndexFiles_StubContract(t *testing.T) {
	idx := NewIndexer(nil)

	prev := &IndexResult{
		RepoName: "test",
		RepoPath: "/tmp/test",
		Branch:   "main",
		Files: []FileResult{
			{Path: "a.go", Language: "go", LineCount: 1},
		},
		TotalFiles:   1,
		TotalSymbols: 0,
	}
	// Snapshot prev's observable shape; if the stub mutates it we'll see
	// the difference in any of the captured fields.
	snap := *prev
	snapFiles := append([]FileResult(nil), prev.Files...)

	got, err := idx.IndexFiles(context.Background(), "/tmp/test", []string{"a.go"}, "main", prev)
	if got != nil {
		t.Fatalf("IndexFiles stub returned non-nil result: %+v", got)
	}
	if !errors.Is(err, ErrIndexFilesNotImplemented) {
		t.Fatalf("IndexFiles stub error = %v, want errors.Is ErrIndexFilesNotImplemented", err)
	}

	// Non-mutation check.
	if prev.RepoName != snap.RepoName || prev.RepoPath != snap.RepoPath ||
		prev.Branch != snap.Branch || prev.TotalFiles != snap.TotalFiles ||
		prev.TotalSymbols != snap.TotalSymbols {
		t.Fatalf("stub mutated previousResult scalar fields: before=%+v after=%+v", snap, *prev)
	}
	if len(prev.Files) != len(snapFiles) {
		t.Fatalf("stub mutated previousResult.Files length: before=%d after=%d", len(snapFiles), len(prev.Files))
	}
	for i := range snapFiles {
		if prev.Files[i].Path != snapFiles[i].Path {
			t.Fatalf("stub mutated previousResult.Files[%d].Path: before=%q after=%q",
				i, snapFiles[i].Path, prev.Files[i].Path)
		}
	}
}
