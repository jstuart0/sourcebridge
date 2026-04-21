// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package trash

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

func newTestStore(t *testing.T) *MemStore {
	t.Helper()
	s := NewMemStore()
	s.SetClock(fixedClock(time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)))
	return s
}

func TestMoveRequirement_TombstonesExternalID(t *testing.T) {
	s := newTestStore(t)
	s.Register(RegisterOptions{
		Type: TypeRequirement, ID: "req-1", RepositoryID: "repo-1",
		NaturalKey: "AUTH-001", Label: "AUTH-001 · Authenticate user",
	})

	entry, err := s.MoveToTrash(context.Background(), TypeRequirement, "req-1",
		MoveOptions{UserID: "jay", Reason: "duplicate"})
	if err != nil {
		t.Fatalf("MoveToTrash: %v", err)
	}
	if entry.OriginalKey != "AUTH-001" {
		t.Errorf("entry.OriginalKey = %q, want AUTH-001", entry.OriginalKey)
	}
	if entry.DeletedBy != "jay" {
		t.Errorf("entry.DeletedBy = %q, want jay", entry.DeletedBy)
	}
	if entry.TrashBatchID == "" {
		t.Error("entry.TrashBatchID should be populated")
	}
	key, _ := s.LookupKey(TypeRequirement, "req-1")
	if !strings.HasPrefix(key, "AUTH-001"+TombstoneKeyPrefix) {
		t.Errorf("tombstoned key = %q, want tombstone prefix on AUTH-001", key)
	}
	if !s.IsTrashed(TypeRequirement, "req-1") {
		t.Error("requirement should be trashed")
	}
}

func TestRestoreRequirement_FreeKey(t *testing.T) {
	s := newTestStore(t)
	s.Register(RegisterOptions{
		Type: TypeRequirement, ID: "req-1", RepositoryID: "repo-1",
		NaturalKey: "AUTH-001", Label: "x",
	})
	_, err := s.MoveToTrash(context.Background(), TypeRequirement, "req-1", MoveOptions{UserID: "jay"})
	if err != nil {
		t.Fatalf("move: %v", err)
	}

	result, err := s.RestoreFromTrash(context.Background(), TypeRequirement, "req-1",
		RestoreOptions{UserID: "jay"})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if result.Renamed {
		t.Error("should not need rename")
	}
	key, _ := s.LookupKey(TypeRequirement, "req-1")
	if key != "AUTH-001" {
		t.Errorf("restored key = %q, want AUTH-001", key)
	}
	if s.IsTrashed(TypeRequirement, "req-1") {
		t.Error("should no longer be trashed")
	}
}

func TestRestoreRequirement_TakenKey_ReturnsConflict(t *testing.T) {
	s := newTestStore(t)
	s.Register(RegisterOptions{
		Type: TypeRequirement, ID: "req-1", RepositoryID: "repo-1",
		NaturalKey: "AUTH-001", Label: "first",
	})
	_, _ = s.MoveToTrash(context.Background(), TypeRequirement, "req-1", MoveOptions{UserID: "jay"})

	// Second active row now takes the key.
	s.Register(RegisterOptions{
		Type: TypeRequirement, ID: "req-2", RepositoryID: "repo-1",
		NaturalKey: "AUTH-001", Label: "replacement",
	})

	_, err := s.RestoreFromTrash(context.Background(), TypeRequirement, "req-1",
		RestoreOptions{UserID: "jay"})
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected ConflictError, got %T: %v", err, err)
	}
	if conflict.OriginalKey != "AUTH-001" {
		t.Errorf("conflict.OriginalKey = %q, want AUTH-001", conflict.OriginalKey)
	}
}

func TestRestoreRequirement_Rename_Succeeds(t *testing.T) {
	s := newTestStore(t)
	s.Register(RegisterOptions{
		Type: TypeRequirement, ID: "req-1", RepositoryID: "repo-1",
		NaturalKey: "AUTH-001", Label: "first",
	})
	_, _ = s.MoveToTrash(context.Background(), TypeRequirement, "req-1", MoveOptions{UserID: "jay"})
	s.Register(RegisterOptions{
		Type: TypeRequirement, ID: "req-2", RepositoryID: "repo-1",
		NaturalKey: "AUTH-001", Label: "replacement",
	})

	result, err := s.RestoreFromTrash(context.Background(), TypeRequirement, "req-1",
		RestoreOptions{UserID: "jay", Resolve: RestoreRename, NewKey: "AUTH-001B"})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !result.Renamed {
		t.Error("expected Renamed=true")
	}
	if result.NewKey != "AUTH-001B" {
		t.Errorf("NewKey = %q, want AUTH-001B", result.NewKey)
	}
	key, _ := s.LookupKey(TypeRequirement, "req-1")
	if key != "AUTH-001B" {
		t.Errorf("restored key = %q, want AUTH-001B", key)
	}
}

func TestRestoreRequirement_Rename_RequiresNewKey(t *testing.T) {
	s := newTestStore(t)
	s.Register(RegisterOptions{
		Type: TypeRequirement, ID: "req-1", RepositoryID: "repo-1",
		NaturalKey: "AUTH-001", Label: "first",
	})
	_, _ = s.MoveToTrash(context.Background(), TypeRequirement, "req-1", MoveOptions{UserID: "jay"})
	s.Register(RegisterOptions{
		Type: TypeRequirement, ID: "req-2", RepositoryID: "repo-1",
		NaturalKey: "AUTH-001", Label: "replacement",
	})
	_, err := s.RestoreFromTrash(context.Background(), TypeRequirement, "req-1",
		RestoreOptions{UserID: "jay", Resolve: RestoreRename}) // NewKey missing
	if err == nil || !strings.Contains(err.Error(), "NewKey") {
		t.Errorf("want NewKey-required error, got %v", err)
	}
}

func TestCascade_ArtifactChildrenFollow(t *testing.T) {
	s := newTestStore(t)
	s.Register(RegisterOptions{Type: TypeKnowledgeArtifact, ID: "art-1", RepositoryID: "repo-1", NaturalKey: "rep/all", Label: "artifact"})
	// Cascade children: sections hanging off the artifact.
	s.Register(RegisterOptions{Type: TypeKnowledgeArtifact, ID: "sec-a", RepositoryID: "repo-1", ParentID: "art-1", Label: "section a"})
	s.Register(RegisterOptions{Type: TypeKnowledgeArtifact, ID: "sec-b", RepositoryID: "repo-1", ParentID: "art-1", Label: "section b"})

	entry, err := s.MoveToTrash(context.Background(), TypeKnowledgeArtifact, "art-1", MoveOptions{UserID: "jay"})
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if !s.IsTrashed(TypeKnowledgeArtifact, "art-1") ||
		!s.IsTrashed(TypeKnowledgeArtifact, "sec-a") ||
		!s.IsTrashed(TypeKnowledgeArtifact, "sec-b") {
		t.Error("cascade incomplete")
	}
	if entry.TrashBatchID == "" {
		t.Error("batch id missing")
	}
}

func TestRestore_CascadeBringsChildrenBack(t *testing.T) {
	s := newTestStore(t)
	s.Register(RegisterOptions{Type: TypeKnowledgeArtifact, ID: "art-1", RepositoryID: "repo-1", NaturalKey: "rep/all", Label: "artifact"})
	s.Register(RegisterOptions{Type: TypeKnowledgeArtifact, ID: "sec-a", RepositoryID: "repo-1", ParentID: "art-1", Label: "section a"})

	_, _ = s.MoveToTrash(context.Background(), TypeKnowledgeArtifact, "art-1", MoveOptions{UserID: "jay"})

	result, err := s.RestoreFromTrash(context.Background(), TypeKnowledgeArtifact, "art-1", RestoreOptions{UserID: "jay"})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if result.BatchSize < 2 {
		t.Errorf("BatchSize = %d, want >= 2 (parent + child)", result.BatchSize)
	}
	if s.IsTrashed(TypeKnowledgeArtifact, "art-1") || s.IsTrashed(TypeKnowledgeArtifact, "sec-a") {
		t.Error("restore did not bring children back")
	}
}

func TestCascade_IndependentlyTrashedChildNotRestored(t *testing.T) {
	// A child trashed independently before the parent has its own
	// batch id. Restoring the parent must not resurrect that child.
	s := newTestStore(t)
	s.Register(RegisterOptions{Type: TypeKnowledgeArtifact, ID: "art-1", RepositoryID: "repo-1", NaturalKey: "rep/all", Label: "artifact"})
	s.Register(RegisterOptions{Type: TypeKnowledgeArtifact, ID: "sec-a", RepositoryID: "repo-1", ParentID: "art-1", Label: "section a"})

	// Trash sec-a first; its batch id differs from the parent batch.
	_, _ = s.MoveToTrash(context.Background(), TypeKnowledgeArtifact, "sec-a", MoveOptions{UserID: "jay"})
	_, _ = s.MoveToTrash(context.Background(), TypeKnowledgeArtifact, "art-1", MoveOptions{UserID: "jay"})

	_, _ = s.RestoreFromTrash(context.Background(), TypeKnowledgeArtifact, "art-1", RestoreOptions{UserID: "jay"})

	if s.IsTrashed(TypeKnowledgeArtifact, "art-1") {
		t.Error("parent should be restored")
	}
	if !s.IsTrashed(TypeKnowledgeArtifact, "sec-a") {
		t.Error("independently-trashed child must remain in trash")
	}
}

func TestPermanentDelete_RemovesRow(t *testing.T) {
	s := newTestStore(t)
	s.Register(RegisterOptions{Type: TypeRequirement, ID: "req-1", RepositoryID: "repo-1", NaturalKey: "AUTH-001", Label: "x"})
	_, _ = s.MoveToTrash(context.Background(), TypeRequirement, "req-1", MoveOptions{UserID: "jay"})

	if err := s.PermanentlyDelete(context.Background(), TypeRequirement, "req-1"); err != nil {
		t.Fatalf("permanent delete: %v", err)
	}
	// Register a fresh AUTH-001; should succeed since the trashed one is gone.
	s.Register(RegisterOptions{Type: TypeRequirement, ID: "req-2", RepositoryID: "repo-1", NaturalKey: "AUTH-001", Label: "fresh"})
	k, _ := s.LookupKey(TypeRequirement, "req-2")
	if k != "AUTH-001" {
		t.Errorf("fresh key = %q, want AUTH-001", k)
	}
}

func TestPermanentDelete_RejectsLiveRow(t *testing.T) {
	s := newTestStore(t)
	s.Register(RegisterOptions{Type: TypeRequirement, ID: "req-1", RepositoryID: "repo-1", NaturalKey: "AUTH-001", Label: "x"})
	err := s.PermanentlyDelete(context.Background(), TypeRequirement, "req-1")
	if err == nil || !strings.Contains(err.Error(), "not in trash") {
		t.Errorf("expected refusal to delete live row, got %v", err)
	}
}

func TestList_FilteredByRepoAndType(t *testing.T) {
	s := newTestStore(t)
	s.Register(RegisterOptions{Type: TypeRequirement, ID: "r1", RepositoryID: "repo-a", NaturalKey: "A-1", Label: "a-1"})
	s.Register(RegisterOptions{Type: TypeRequirement, ID: "r2", RepositoryID: "repo-b", NaturalKey: "B-1", Label: "b-1"})
	s.Register(RegisterOptions{Type: TypeRequirementLink, ID: "l1", RepositoryID: "repo-a", Label: "link-a"})

	_, _ = s.MoveToTrash(context.Background(), TypeRequirement, "r1", MoveOptions{UserID: "jay"})
	_, _ = s.MoveToTrash(context.Background(), TypeRequirement, "r2", MoveOptions{UserID: "jay"})
	_, _ = s.MoveToTrash(context.Background(), TypeRequirementLink, "l1", MoveOptions{UserID: "jay"})

	entries, total, err := s.List(context.Background(), ListFilter{RepositoryID: "repo-a"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 {
		t.Errorf("total in repo-a = %d, want 2", total)
	}
	if len(entries) != 2 {
		t.Errorf("len(entries) = %d, want 2", len(entries))
	}

	entries, total, _ = s.List(context.Background(), ListFilter{RepositoryID: "repo-a", Types: []TrashableType{TypeRequirement}})
	if total != 1 {
		t.Errorf("requirements-only total = %d, want 1", total)
	}
	if len(entries) == 0 || entries[0].Type != TypeRequirement {
		t.Error("expected requirement entry")
	}
}

func TestSweepExpired_DropsPastRetention(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	s.SetClock(fixedClock(now))

	s.Register(RegisterOptions{Type: TypeRequirement, ID: "old", RepositoryID: "r", NaturalKey: "O", Label: "old"})
	s.Register(RegisterOptions{Type: TypeRequirement, ID: "new", RepositoryID: "r", NaturalKey: "N", Label: "new"})
	_, _ = s.MoveToTrash(context.Background(), TypeRequirement, "old", MoveOptions{UserID: "jay"})

	// Advance time past retention, then trash "new".
	s.SetClock(fixedClock(now.Add(10 * 24 * time.Hour)))
	_, _ = s.MoveToTrash(context.Background(), TypeRequirement, "new", MoveOptions{UserID: "jay"})

	// At this clock, "old" is 10 days old; retention = 7 days → purge it.
	purged, err := s.SweepExpired(context.Background(), 7*24*time.Hour, 100)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if purged != 1 {
		t.Errorf("purged = %d, want 1 (only 'old' past retention)", purged)
	}
	// "new" should still be trashed.
	if !s.IsTrashed(TypeRequirement, "new") {
		t.Error("'new' must still be trashed")
	}
}

func TestSweepExpired_RespectsBatchLimit(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	s.SetClock(fixedClock(now))
	for i := 0; i < 10; i++ {
		id := "r-" + string(rune('a'+i))
		s.Register(RegisterOptions{Type: TypeRequirement, ID: id, RepositoryID: "r", NaturalKey: id, Label: id})
		_, _ = s.MoveToTrash(context.Background(), TypeRequirement, id, MoveOptions{UserID: "jay"})
	}
	s.SetClock(fixedClock(now.Add(30 * 24 * time.Hour)))

	purged, err := s.SweepExpired(context.Background(), 7*24*time.Hour, 3)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if purged != 3 {
		t.Errorf("purged = %d, want 3 (capped by maxBatch)", purged)
	}
}

func TestMoveToTrash_DoubleMoveRejected(t *testing.T) {
	s := newTestStore(t)
	s.Register(RegisterOptions{Type: TypeRequirement, ID: "r", RepositoryID: "repo", NaturalKey: "A", Label: "x"})
	_, err := s.MoveToTrash(context.Background(), TypeRequirement, "r", MoveOptions{UserID: "jay"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.MoveToTrash(context.Background(), TypeRequirement, "r", MoveOptions{UserID: "jay"})
	if err == nil || !strings.Contains(err.Error(), "already in trash") {
		t.Errorf("want already-in-trash error, got %v", err)
	}
}

func TestList_AdvisoryCanRestoreFlagsTakenKey(t *testing.T) {
	s := newTestStore(t)
	s.Register(RegisterOptions{Type: TypeRequirement, ID: "r1", RepositoryID: "repo", NaturalKey: "A", Label: "x"})
	_, _ = s.MoveToTrash(context.Background(), TypeRequirement, "r1", MoveOptions{UserID: "jay"})
	s.Register(RegisterOptions{Type: TypeRequirement, ID: "r2", RepositoryID: "repo", NaturalKey: "A", Label: "replacement"})

	entries, _, _ := s.List(context.Background(), ListFilter{RepositoryID: "repo"})
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].CanRestore {
		t.Error("CanRestore should be false when original key is taken")
	}
	if entries[0].RestoreConflict == "" {
		t.Error("RestoreConflict should describe the collision")
	}
}
