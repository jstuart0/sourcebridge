// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package knowledge

import "testing"

func TestKnowledgeArtifactCRUD(t *testing.T) {
	s := NewMemStore()

	artifact := &Artifact{
		RepositoryID: "repo-1",
		Type:         ArtifactCliffNotes,
		Audience:     AudienceDeveloper,
		Depth:        DepthMedium,
		Status:       StatusPending,
		SourceRevision: SourceRevision{
			CommitSHA: "abc123",
			Branch:    "main",
		},
	}
	stored, err := s.StoreKnowledgeArtifact(artifact)
	if err != nil {
		t.Fatalf("StoreKnowledgeArtifact: %v", err)
	}
	if stored.ID == "" {
		t.Fatal("expected ID to be assigned")
	}
	if stored.Status != StatusPending {
		t.Fatalf("expected status pending, got %s", stored.Status)
	}

	fetched := s.GetKnowledgeArtifact(stored.ID)
	if fetched == nil {
		t.Fatal("expected to find artifact")
	}
	if fetched.RepositoryID != "repo-1" {
		t.Fatalf("expected repo-1, got %s", fetched.RepositoryID)
	}
	if fetched.SourceRevision.CommitSHA != "abc123" {
		t.Fatalf("expected commit abc123, got %s", fetched.SourceRevision.CommitSHA)
	}

	artifacts := s.GetKnowledgeArtifacts("repo-1")
	if len(artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(artifacts))
	}

	err = s.UpdateKnowledgeArtifactStatus(stored.ID, StatusReady)
	if err != nil {
		t.Fatalf("UpdateKnowledgeArtifactStatus: %v", err)
	}
	fetched = s.GetKnowledgeArtifact(stored.ID)
	if fetched.Status != StatusReady {
		t.Fatalf("expected status ready, got %s", fetched.Status)
	}
	if fetched.GeneratedAt.IsZero() {
		t.Fatal("expected generated_at to be set when status is ready")
	}

	err = s.MarkKnowledgeArtifactStale(stored.ID, true)
	if err != nil {
		t.Fatalf("MarkKnowledgeArtifactStale: %v", err)
	}
	fetched = s.GetKnowledgeArtifact(stored.ID)
	if !fetched.Stale {
		t.Fatal("expected artifact to be stale")
	}

	err = s.DeleteKnowledgeArtifact(stored.ID)
	if err != nil {
		t.Fatalf("DeleteKnowledgeArtifact: %v", err)
	}
	if s.GetKnowledgeArtifact(stored.ID) != nil {
		t.Fatal("expected artifact to be deleted")
	}
}

func TestKnowledgeSectionsAndEvidence(t *testing.T) {
	s := NewMemStore()

	stored, _ := s.StoreKnowledgeArtifact(&Artifact{
		RepositoryID: "repo-1",
		Type:         ArtifactCliffNotes,
		Audience:     AudienceBeginner,
		Depth:        DepthSummary,
		Status:       StatusReady,
	})

	sections := []Section{
		{Title: "System Purpose", Content: "This system does X.", Summary: "Does X.", Confidence: ConfidenceHigh},
		{Title: "Architecture", Content: "Layered architecture.", Summary: "Layers.", Confidence: ConfidenceMedium, Inferred: true},
	}
	err := s.StoreKnowledgeSections(stored.ID, sections)
	if err != nil {
		t.Fatalf("StoreKnowledgeSections: %v", err)
	}

	fetchedSections := s.GetKnowledgeSections(stored.ID)
	if len(fetchedSections) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(fetchedSections))
	}
	if fetchedSections[0].Title != "System Purpose" {
		t.Fatalf("expected first section 'System Purpose', got %q", fetchedSections[0].Title)
	}

	evidence := []Evidence{
		{SourceType: EvidenceFile, SourceID: "file-1", FilePath: "main.go", LineStart: 1, LineEnd: 10, Rationale: "Entry point"},
		{SourceType: EvidenceSymbol, SourceID: "sym-1", FilePath: "main.go", LineStart: 5, LineEnd: 8, Metadata: map[string]string{"kind": "function"}},
	}
	err = s.StoreKnowledgeEvidence(fetchedSections[0].ID, evidence)
	if err != nil {
		t.Fatalf("StoreKnowledgeEvidence: %v", err)
	}

	fetchedEvidence := s.GetKnowledgeEvidence(fetchedSections[0].ID)
	if len(fetchedEvidence) != 2 {
		t.Fatalf("expected 2 evidence, got %d", len(fetchedEvidence))
	}
	if fetchedEvidence[1].Metadata["kind"] != "function" {
		t.Fatalf("expected metadata kind=function, got %v", fetchedEvidence[1].Metadata)
	}

	full := s.GetKnowledgeArtifact(stored.ID)
	if len(full.Sections) != 2 {
		t.Fatalf("expected 2 nested sections, got %d", len(full.Sections))
	}
	if len(full.Sections[0].Evidence) != 2 {
		t.Fatalf("expected 2 nested evidence on first section, got %d", len(full.Sections[0].Evidence))
	}
}

func TestKnowledgeArtifactNotFound(t *testing.T) {
	s := NewMemStore()

	if s.GetKnowledgeArtifact("nonexistent") != nil {
		t.Fatal("expected nil for nonexistent artifact")
	}
	if err := s.UpdateKnowledgeArtifactStatus("nonexistent", StatusReady); err == nil {
		t.Fatal("expected error for nonexistent artifact")
	}
	if err := s.MarkKnowledgeArtifactStale("nonexistent", true); err == nil {
		t.Fatal("expected error for nonexistent artifact")
	}
	if err := s.DeleteKnowledgeArtifact("nonexistent"); err == nil {
		t.Fatal("expected error for nonexistent artifact")
	}
}

func TestDeleteArtifactCleansUpSectionsAndEvidence(t *testing.T) {
	s := NewMemStore()

	artifact, _ := s.StoreKnowledgeArtifact(&Artifact{
		RepositoryID: "repo-1",
		Type:         ArtifactLearningPath,
		Audience:     AudienceDeveloper,
		Depth:        DepthDeep,
		Status:       StatusReady,
	})

	_ = s.StoreKnowledgeSections(artifact.ID, []Section{{Title: "Step 1", Content: "Do this.", Confidence: ConfidenceHigh}})
	fetchedSections := s.GetKnowledgeSections(artifact.ID)
	_ = s.StoreKnowledgeEvidence(fetchedSections[0].ID, []Evidence{
		{SourceType: EvidenceFile, SourceID: "f1", FilePath: "a.go"},
	})

	_ = s.DeleteKnowledgeArtifact(artifact.ID)

	if len(s.GetKnowledgeSections(artifact.ID)) != 0 {
		t.Fatal("expected sections to be cleaned up")
	}
	if len(s.GetKnowledgeEvidence(fetchedSections[0].ID)) != 0 {
		t.Fatal("expected evidence to be cleaned up")
	}
}
