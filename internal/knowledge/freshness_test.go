// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package knowledge

import (
	"encoding/json"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/graph"
)

func TestMarkAllStale(t *testing.T) {
	s := NewMemStore()

	// Create two ready artifacts and one pending artifact for the same repo.
	a1, _ := s.StoreKnowledgeArtifact(&Artifact{
		RepositoryID: "repo-1",
		Type:         ArtifactCliffNotes,
		Audience:     AudienceDeveloper,
		Depth:        DepthMedium,
		Status:       StatusPending,
	})
	_ = s.UpdateKnowledgeArtifactStatus(a1.ID, StatusReady)

	a2, _ := s.StoreKnowledgeArtifact(&Artifact{
		RepositoryID: "repo-1",
		Type:         ArtifactLearningPath,
		Audience:     AudienceBeginner,
		Depth:        DepthSummary,
		Status:       StatusPending,
	})
	_ = s.UpdateKnowledgeArtifactStatus(a2.ID, StatusReady)

	pendingArtifact, _ := s.StoreKnowledgeArtifact(&Artifact{
		RepositoryID: "repo-1",
		Type:         ArtifactCodeTour,
		Audience:     AudienceDeveloper,
		Depth:        DepthDeep,
		Status:       StatusPending,
	})

	// Artifact for a different repo — should not be affected.
	otherRepo, _ := s.StoreKnowledgeArtifact(&Artifact{
		RepositoryID: "repo-2",
		Type:         ArtifactCliffNotes,
		Audience:     AudienceDeveloper,
		Depth:        DepthMedium,
		Status:       StatusPending,
	})
	_ = s.UpdateKnowledgeArtifactStatus(otherRepo.ID, StatusReady)

	MarkAllStale(s, "repo-1")

	// Both ready artifacts should now be stale.
	if a := s.GetKnowledgeArtifact(a1.ID); !a.Stale {
		t.Fatalf("expected a1 to be stale")
	}
	if a := s.GetKnowledgeArtifact(a2.ID); !a.Stale {
		t.Fatalf("expected a2 to be stale")
	}

	// Pending artifact should NOT be marked stale.
	if a := s.GetKnowledgeArtifact(pendingArtifact.ID); a.Stale {
		t.Fatalf("expected pending artifact to remain non-stale")
	}

	// Other repo should NOT be affected.
	if a := s.GetKnowledgeArtifact(otherRepo.ID); a.Stale {
		t.Fatalf("expected other-repo artifact to remain non-stale")
	}
}

func TestMarkAllStaleNilStore(t *testing.T) {
	// Should not panic.
	MarkAllStale(nil, "repo-1")
}

func TestMarkAllStaleIdempotent(t *testing.T) {
	s := NewMemStore()

	a, _ := s.StoreKnowledgeArtifact(&Artifact{
		RepositoryID: "repo-1",
		Type:         ArtifactCliffNotes,
		Audience:     AudienceDeveloper,
		Depth:        DepthMedium,
		Status:       StatusPending,
	})
	_ = s.UpdateKnowledgeArtifactStatus(a.ID, StatusReady)

	MarkAllStale(s, "repo-1")
	MarkAllStale(s, "repo-1") // second call — already stale, should be no-op

	if fetched := s.GetKnowledgeArtifact(a.ID); !fetched.Stale {
		t.Fatalf("expected artifact to still be stale after double mark")
	}
}

func TestRefreshClearsStale(t *testing.T) {
	s := NewMemStore()

	a, _ := s.StoreKnowledgeArtifact(&Artifact{
		RepositoryID: "repo-1",
		Type:         ArtifactCliffNotes,
		Audience:     AudienceDeveloper,
		Depth:        DepthMedium,
		Status:       StatusPending,
	})
	_ = s.UpdateKnowledgeArtifactStatus(a.ID, StatusReady)
	_ = s.MarkKnowledgeArtifactStale(a.ID, true)

	if fetched := s.GetKnowledgeArtifact(a.ID); !fetched.Stale {
		t.Fatalf("expected artifact to be stale")
	}

	// Clearing stale flag simulates what happens after a successful refresh.
	_ = s.MarkKnowledgeArtifactStale(a.ID, false)

	if fetched := s.GetKnowledgeArtifact(a.ID); fetched.Stale {
		t.Fatalf("expected artifact to no longer be stale after refresh")
	}
}

func TestMarkAllStaleMarksRepositoryUnderstandingNeedsRefresh(t *testing.T) {
	s := NewMemStore()

	_, err := s.StoreRepositoryUnderstanding(&RepositoryUnderstanding{
		RepositoryID: "repo-1",
		Scope:        (&ArtifactScope{ScopeType: ScopeRepository}).NormalizePtr(),
		RevisionFP:   "rev-1",
		Stage:        UnderstandingReady,
		TreeStatus:   UnderstandingTreeComplete,
		CachedNodes:  10,
		TotalNodes:   10,
	})
	if err != nil {
		t.Fatalf("StoreRepositoryUnderstanding: %v", err)
	}

	MarkAllStale(s, "repo-1")

	u := s.GetRepositoryUnderstanding("repo-1", ArtifactScope{ScopeType: ScopeRepository})
	if u == nil {
		t.Fatal("expected repository understanding to remain present")
	}
	if u.Stage != UnderstandingNeedsRefresh {
		t.Fatalf("expected repository understanding to be marked needs_refresh, got %q", u.Stage)
	}
}

// ---------------------------------------------------------------------------
// Phase 1: selective invalidation tests
// ---------------------------------------------------------------------------

const testMaxChanges = 200

// seedReadyArtifact creates a ready, non-stale artifact with the given
// sections + evidence. Returns the artifact's ID.
func seedReadyArtifact(t *testing.T, s *MemStore, repoID string, typ ArtifactType, sections []Section) string {
	t.Helper()
	a, err := s.StoreKnowledgeArtifact(&Artifact{
		RepositoryID: repoID,
		Type:         typ,
		Audience:     AudienceDeveloper,
		Depth:        DepthMedium,
		Status:       StatusPending,
	})
	if err != nil {
		t.Fatalf("StoreKnowledgeArtifact: %v", err)
	}
	if err := s.UpdateKnowledgeArtifactStatus(a.ID, StatusReady); err != nil {
		t.Fatalf("UpdateKnowledgeArtifactStatus: %v", err)
	}
	if len(sections) > 0 {
		// Persist each section, then attach its evidence by section ID.
		evCopies := make([][]Evidence, len(sections))
		for i, sec := range sections {
			evCopies[i] = sec.Evidence
			sections[i].Evidence = nil
		}
		if err := s.StoreKnowledgeSections(a.ID, sections); err != nil {
			t.Fatalf("StoreKnowledgeSections: %v", err)
		}
		stored := s.GetKnowledgeSections(a.ID)
		for i, sec := range stored {
			if len(evCopies[i]) == 0 {
				continue
			}
			if err := s.StoreKnowledgeEvidence(sec.ID, evCopies[i]); err != nil {
				t.Fatalf("StoreKnowledgeEvidence: %v", err)
			}
		}
	}
	return a.ID
}

func TestMarkStaleForImpact_NoChanges_NoOp(t *testing.T) {
	s := NewMemStore()
	aid := seedReadyArtifact(t, s, "repo-1", ArtifactCliffNotes, []Section{
		{Title: "Intro", Evidence: []Evidence{{SourceType: EvidenceSymbol, SourceID: "sym-unchanged"}}},
	})

	reasons := MarkStaleForImpact(s, "repo-1", nil, nil, "report-A", testMaxChanges)
	if len(reasons) != 0 {
		t.Fatalf("expected no reasons, got %v", reasons)
	}
	if got := s.GetKnowledgeArtifact(aid); got.Stale {
		t.Fatalf("expected artifact to stay fresh")
	}
}

func TestMarkStaleForImpact_SymbolHit(t *testing.T) {
	s := NewMemStore()
	hit := seedReadyArtifact(t, s, "repo-1", ArtifactCliffNotes, []Section{
		{Title: "Hit", Evidence: []Evidence{{SourceType: EvidenceSymbol, SourceID: "sym-A"}}},
	})
	miss := seedReadyArtifact(t, s, "repo-1", ArtifactLearningPath, []Section{
		{Title: "Miss", Evidence: []Evidence{{SourceType: EvidenceSymbol, SourceID: "sym-B"}}},
	})

	reasons := MarkStaleForImpact(s, "repo-1", []string{"sym-A"}, nil, "report-A", testMaxChanges)
	if len(reasons) != 1 || reasons[0].ArtifactID != hit {
		t.Fatalf("expected only hit artifact staled, got %+v", reasons)
	}
	if reasons[0].Blanket {
		t.Fatalf("expected targeted (non-blanket) reason")
	}
	if len(reasons[0].Symbols) != 1 || reasons[0].Symbols[0] != "sym-A" {
		t.Fatalf("expected sym-A in reason, got %+v", reasons[0].Symbols)
	}
	if reasons[0].ReportID != "report-A" {
		t.Fatalf("expected report id to round-trip")
	}

	if !s.GetKnowledgeArtifact(hit).Stale {
		t.Fatalf("expected hit artifact to be stale")
	}
	if s.GetKnowledgeArtifact(miss).Stale {
		t.Fatalf("expected miss artifact to stay fresh")
	}

	// Persisted reason should parse back to the same shape.
	got := s.GetKnowledgeArtifact(hit)
	if got.StaleReasonJSON == "" {
		t.Fatalf("expected stale_reason_json to be populated")
	}
	var parsed graph.StaleArtifactReason
	if err := json.Unmarshal([]byte(got.StaleReasonJSON), &parsed); err != nil {
		t.Fatalf("unmarshal persisted reason: %v", err)
	}
	if parsed.ArtifactID != hit || parsed.ReportID != "report-A" || len(parsed.Symbols) != 1 {
		t.Fatalf("persisted reason round-trip mismatch: %+v", parsed)
	}
}

func TestMarkStaleForImpact_FileHit(t *testing.T) {
	s := NewMemStore()
	hit := seedReadyArtifact(t, s, "repo-1", ArtifactCliffNotes, []Section{
		{Title: "Sec", Evidence: []Evidence{
			{SourceType: EvidenceFile, SourceID: "some-file-source", FilePath: "pkg/foo.go"},
		}},
	})

	reasons := MarkStaleForImpact(s, "repo-1", nil, []string{"pkg/foo.go"}, "report-B", testMaxChanges)
	if len(reasons) != 1 || reasons[0].ArtifactID != hit {
		t.Fatalf("expected only hit staled, got %+v", reasons)
	}
	if reasons[0].Blanket {
		t.Fatalf("expected targeted reason")
	}
	if len(reasons[0].Files) != 1 || reasons[0].Files[0] != "pkg/foo.go" {
		t.Fatalf("expected pkg/foo.go in reason, got %+v", reasons[0].Files)
	}
}

func TestMarkStaleForImpact_RenamedFile_MatchesOldPath(t *testing.T) {
	s := NewMemStore()
	aid := seedReadyArtifact(t, s, "repo-1", ArtifactCliffNotes, []Section{
		{Title: "Sec", Evidence: []Evidence{
			{SourceType: EvidenceFile, SourceID: "ev-1", FilePath: "old/path.go"},
		}},
	})

	// Caller wires both the new path and the old path into filesChanged, so
	// renamed-file evidence still matches.
	reasons := MarkStaleForImpact(s, "repo-1", nil, []string{"new/path.go", "old/path.go"}, "report-R", testMaxChanges)
	if len(reasons) != 1 || reasons[0].ArtifactID != aid {
		t.Fatalf("expected the rename-tracked artifact staled, got %+v", reasons)
	}
}

func TestMarkStaleForImpact_NoEvidence_FallsBackToBlanket(t *testing.T) {
	s := NewMemStore()
	aid := seedReadyArtifact(t, s, "repo-1", ArtifactCliffNotes, nil) // no sections/evidence

	reasons := MarkStaleForImpact(s, "repo-1", []string{"sym-X"}, nil, "report-C", testMaxChanges)
	if len(reasons) != 1 || reasons[0].ArtifactID != aid {
		t.Fatalf("expected blanket stale for no-evidence artifact, got %+v", reasons)
	}
	if !reasons[0].Blanket {
		t.Fatalf("expected Blanket=true")
	}
	if !s.GetKnowledgeArtifact(aid).Stale {
		t.Fatalf("expected artifact to be stale")
	}
}

func TestMarkStaleForImpact_RepositoryEvidenceOnly_FallsBackToBlanket(t *testing.T) {
	s := NewMemStore()
	aid := seedReadyArtifact(t, s, "repo-1", ArtifactArchitectureDiagram, []Section{
		{Title: "Whole-repo", Evidence: []Evidence{
			{SourceType: EvidenceRepository, SourceID: "repo-1"},
			{SourceType: EvidenceCommit, SourceID: "abc123"},
		}},
	})

	reasons := MarkStaleForImpact(s, "repo-1", []string{"sym-unrelated"}, nil, "report-D", testMaxChanges)
	if len(reasons) != 1 || reasons[0].ArtifactID != aid {
		t.Fatalf("expected whole-repo artifact blanket-staled, got %+v", reasons)
	}
	if !reasons[0].Blanket {
		t.Fatalf("expected Blanket=true for repository-only evidence")
	}
}

func TestMarkStaleForImpact_ChangeSetExceedsMax_FallsBackToBlanket(t *testing.T) {
	s := NewMemStore()
	hit := seedReadyArtifact(t, s, "repo-1", ArtifactCliffNotes, []Section{
		{Title: "Sec", Evidence: []Evidence{{SourceType: EvidenceSymbol, SourceID: "sym-target"}}},
	})
	unrelated := seedReadyArtifact(t, s, "repo-1", ArtifactLearningPath, []Section{
		{Title: "Sec", Evidence: []Evidence{{SourceType: EvidenceSymbol, SourceID: "sym-elsewhere"}}},
	})

	// 201 changed symbols with cap=200 triggers blanket fallback.
	changed := make([]string, 201)
	for i := range changed {
		changed[i] = "sym-" + itoa(i)
	}

	reasons := MarkStaleForImpact(s, "repo-1", changed, nil, "report-E", 200)

	// Both ready artifacts should be in the stale list with Blanket=true.
	if len(reasons) != 2 {
		t.Fatalf("expected 2 blanket reasons, got %d: %+v", len(reasons), reasons)
	}
	for _, r := range reasons {
		if !r.Blanket {
			t.Fatalf("expected Blanket=true after fallback, got %+v", r)
		}
	}
	if !s.GetKnowledgeArtifact(hit).Stale || !s.GetKnowledgeArtifact(unrelated).Stale {
		t.Fatalf("expected both artifacts staled")
	}
}

func TestMarkStaleForImpact_IgnoresPendingAndAlreadyStale(t *testing.T) {
	s := NewMemStore()

	// Pending artifact — has matching evidence but should be skipped.
	pending, err := s.StoreKnowledgeArtifact(&Artifact{
		RepositoryID: "repo-1",
		Type:         ArtifactCliffNotes,
		Audience:     AudienceDeveloper,
		Depth:        DepthMedium,
		Status:       StatusPending,
	})
	if err != nil {
		t.Fatalf("StoreKnowledgeArtifact: %v", err)
	}
	_ = s.StoreKnowledgeSections(pending.ID, []Section{{Title: "S", Evidence: nil}})

	// Already-stale ready artifact — should be skipped.
	stale := seedReadyArtifact(t, s, "repo-1", ArtifactLearningPath, []Section{
		{Title: "Sec", Evidence: []Evidence{{SourceType: EvidenceSymbol, SourceID: "sym-X"}}},
	})
	if err := s.MarkKnowledgeArtifactStale(stale, true); err != nil {
		t.Fatalf("pre-stale: %v", err)
	}

	fresh := seedReadyArtifact(t, s, "repo-1", ArtifactCodeTour, []Section{
		{Title: "Sec", Evidence: []Evidence{{SourceType: EvidenceSymbol, SourceID: "sym-X"}}},
	})

	reasons := MarkStaleForImpact(s, "repo-1", []string{"sym-X"}, nil, "report-F", testMaxChanges)
	if len(reasons) != 1 || reasons[0].ArtifactID != fresh {
		t.Fatalf("expected only the fresh-ready artifact to be staled, got %+v", reasons)
	}
	if got := s.GetKnowledgeArtifact(pending.ID); got.Stale {
		t.Fatalf("expected pending artifact to remain non-stale")
	}
}

func TestMarkStaleForImpact_MultipleSourcesDedupe(t *testing.T) {
	s := NewMemStore()
	aid := seedReadyArtifact(t, s, "repo-1", ArtifactCliffNotes, []Section{
		{Title: "S1", Evidence: []Evidence{
			{SourceType: EvidenceSymbol, SourceID: "sym-A"},
			{SourceType: EvidenceSymbol, SourceID: "sym-B"},
		}},
	})

	reasons := MarkStaleForImpact(s, "repo-1", []string{"sym-A", "sym-B", "sym-A"}, nil, "report-G", testMaxChanges)
	if len(reasons) != 1 || reasons[0].ArtifactID != aid {
		t.Fatalf("expected exactly one reason, got %+v", reasons)
	}
	if len(reasons[0].Symbols) != 2 {
		t.Fatalf("expected 2 unique matched symbols, got %+v", reasons[0].Symbols)
	}
}

func TestMarkStaleForImpact_UnderstandingAlwaysRefreshed(t *testing.T) {
	s := NewMemStore()

	_, err := s.StoreRepositoryUnderstanding(&RepositoryUnderstanding{
		RepositoryID: "repo-1",
		Scope:        (&ArtifactScope{ScopeType: ScopeRepository}).NormalizePtr(),
		Stage:        UnderstandingReady,
	})
	if err != nil {
		t.Fatalf("StoreRepositoryUnderstanding: %v", err)
	}

	// Even with no changes, understanding still gets refresh-marked.
	_ = MarkStaleForImpact(s, "repo-1", nil, nil, "report-H", testMaxChanges)

	u := s.GetRepositoryUnderstanding("repo-1", ArtifactScope{ScopeType: ScopeRepository})
	if u == nil || u.Stage != UnderstandingNeedsRefresh {
		t.Fatalf("expected understanding to be marked needs_refresh, got %v", u)
	}
}

func TestSymbolIDFormatMatchesEvidence(t *testing.T) {
	// Guard against a future SurrealDB record-ID format change silently
	// breaking the selective-invalidation lookup. The contract: whatever
	// format the indexer stores as StoredSymbol.ID must be the same format
	// evidence rows carry as source_id, and GetArtifactsForSources must find
	// the artifact round-trip.
	s := NewMemStore()
	const symID = "sym-abc-123"
	aid := seedReadyArtifact(t, s, "repo-1", ArtifactCliffNotes, []Section{
		{Title: "S", Evidence: []Evidence{{SourceType: EvidenceSymbol, SourceID: symID}}},
	})

	got := s.GetArtifactsForSources("repo-1", []SourceRef{{SourceType: EvidenceSymbol, SourceID: symID}})
	if len(got) != 1 || got[0].ID != aid {
		t.Fatalf("expected symbol-ID round trip to hit the artifact, got %+v", got)
	}
}

// itoa is a tiny helper to avoid dragging strconv in for a few tests.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}
